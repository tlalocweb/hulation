package server

// HTTP handler for POST /api/v1/chat/start.
//
// Public surface — no admin JWT. Auth is via the token returned to
// the visitor here, used at /api/v1/chat/ws (stage 4b.4).
//
// Request body (JSON):
//
//	{
//	  "server_id":       "tlaloc",
//	  "visitor_id":      "<bounce-id from hello.js cookie>",
//	  "email":           "alice@example.com",
//	  "turnstile_token": "0.…",
//	  "first_message":   "Hi, do you ship to Mexico?"
//	}
//
// Response on success (200):
//
//	{
//	  "session_id":  "<uuid>",
//	  "chat_token":  "<jwt>",
//	  "expires_at":  "<rfc3339>",
//	  "message_id":  "<uuid|empty>"
//	}
//
// Failure: 400/403/429/503 with a machine-readable code so the
// widget can render the right copy. Codes:
//
//	server_unknown    — server_id not in hula config
//	rate_limited      — too many starts from this peer
//	captcha_invalid   — Turnstile/reCAPTCHA rejected the token
//	captcha_unavail   — upstream verifier unreachable / 5xx
//	email_invalid     — AfterShip flagged syntax/dns/disposable/role
//	moderated         — OpenAI moderation flagged ABUSE/SPAM
//	disabled          — operator kill-switch on
//	bad_request       — validation failure (missing field, body…)
//	internal          — unexpected server-side error

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/analytics/enrich"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
)

// chatPushTimeout bounds the lifetime of the /chat/start push fan-out
// goroutine. APNs HTTP/2 and FCM OAuth refresh are typically sub-
// second; this cap kills goroutines stuck on a wedged upstream so they
// don't accumulate across many chat-starts or live past process exit.
const chatPushTimeout = 30 * time.Second

// chatStartHandler returns the handler bound to svc + isKnown.
// isKnown is the per-config server-id check (server/chat_acl.go's
// allConfiguredServerIDs slice, hoisted into a closure for the
// handler's exclusive use).
func chatStartHandler(svc *chatpkg.Service, isKnown chatpkg.IsServerKnown) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeStartError(w, http.StatusMethodNotAllowed, "bad_request", "method not allowed")
			return
		}
		var body struct {
			ServerID       string         `json:"server_id"`
			VisitorID      string         `json:"visitor_id"`
			Email          string         `json:"email"`
			TurnstileToken string         `json:"turnstile_token"`
			FirstMessage   string         `json:"first_message"`
			Country        string         `json:"country"`
			Device         string         `json:"device"`
			// Latitude / Longitude come from visitor widgets that
			// successfully obtained browser geolocation. Pointers so
			// "field absent" is distinguishable from "zero coords".
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
			Meta      map[string]any `json:"meta"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
			writeStartError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
		if strings.TrimSpace(body.ServerID) == "" || strings.TrimSpace(body.Email) == "" {
			writeStartError(w, http.StatusBadRequest, "bad_request", "server_id and email required")
			return
		}
		peerIP := extractPeerIP(r)
		ua := r.Header.Get("User-Agent")

		// Server-side enrichment: fill country / device / page when the
		// widget hasn't bothered, so every operator gets a usable session
		// header in the mobile app regardless of which visitor widget the
		// customer ships. Customer-provided values always win, followed
		// by browser-geolocation coords, then IP-based lookup.
		country, device, meta := enrichChatStart(body.Country, body.Device, body.Meta, peerIP, ua, r.Header.Get("Referer"), body.Latitude, body.Longitude)

		res, err := svc.Start(r.Context(), chatpkg.StartParams{
			ServerID:     body.ServerID,
			VisitorID:    body.VisitorID,
			Email:        body.Email,
			FirstMessage: body.FirstMessage,
			Turnstile:    body.TurnstileToken,
			PeerIP:       peerIP,
			UserAgent:    ua,
			Country:      country,
			Device:       device,
			Meta:         meta,
		}, isKnown)

		if err != nil {
			handleStartErr(w, err)
			return
		}

		// Fan out an APNs/FCM push to every operator that has push
		// notifications enabled. Runs on its own goroutine so the
		// /chat/start response isn't held up by the (potentially slow)
		// APNs HTTP/2 hop and the FCM OAuth token refresh. We
		// deliberately don't derive from r.Context() — that's
		// cancelled the moment the response flushes, defeating the
		// detach. chatPushTimeout caps stuck goroutines instead.
		pushCtx, pushCancel := context.WithTimeout(context.Background(), chatPushTimeout)
		go func(in chatpkg.ChatPushInput) {
			defer pushCancel()
			chatpkg.FireNewChatPush(pushCtx, in)
		}(chatpkg.ChatPushInput{
			SessionID:      res.SessionID.String(),
			ServerID:       body.ServerID,
			VisitorID:      body.VisitorID,
			VisitorEmail:   body.Email,
			VisitorCountry: body.Country,
			FirstMessage:   body.FirstMessage,
		})

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		resp := map[string]any{
			"session_id": res.SessionID.String(),
			"chat_token": res.ChatToken,
			"expires_at": res.ExpiresAt,
			"message_id": idOrEmpty(res.FirstMessage),
		}
		// HA Stage 3.8: pin the chat WS to THIS node so the in-
		// process hub never has to fan out across the team. The
		// widget opens its WS to the per-node hostname rather than
		// the team-public hostname; round-robin / GeoDNS routing
		// keeps working for visitor /v/* traffic, only chat
		// stickiness is enforced.
		if pinURL := chatPinURL(r); pinURL != "" {
			resp["chat_url"] = pinURL
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// chatPinURL returns wss://<node-hostname>/api/v1/chat/ws when the
// current request was served by a team node with team.node_hostname
// set. Empty string disables pinning — the widget falls back to
// dialing the same host that served chat-start, which is correct
// for solo deployments.
func chatPinURL(r *http.Request) string {
	cfg := hulaapp.GetConfig()
	if cfg == nil || cfg.Team == nil || cfg.Team.NodeHostname == "" {
		return ""
	}
	scheme := "wss"
	// Mirror the chat-start request's scheme — handles staging
	// fixtures that run cleartext WS via a TLS-terminating proxy.
	if r.Header.Get("X-Forwarded-Proto") == "http" {
		scheme = "ws"
	}
	return scheme + "://" + cfg.Team.NodeHostname + "/api/v1/chat/ws"
}

func idOrEmpty(m chatpkg.Message) string {
	if m.ID == [16]byte{} {
		return ""
	}
	return m.ID.String()
}

// writeStartError emits a small JSON envelope the widget can parse
// uniformly: { code, message }.
func writeStartError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
	})
}

// handleStartErr maps the typed errors from chatpkg.Service.Start
// to HTTP status + machine-readable code.
func handleStartErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chatpkg.ErrDisabled):
		writeStartError(w, http.StatusServiceUnavailable, "disabled", "new chat sessions are disabled")
	case errors.Is(err, chatpkg.ErrServerUnknown):
		writeStartError(w, http.StatusNotFound, "server_unknown", err.Error())
	case errors.Is(err, chatpkg.ErrRateLimited):
		writeStartError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
	case errors.Is(err, chatpkg.ErrCaptchaInvalid):
		writeStartError(w, http.StatusForbidden, "captcha_invalid", err.Error())
	case errors.Is(err, chatpkg.ErrCaptchaUnavail):
		writeStartError(w, http.StatusServiceUnavailable, "captcha_unavail", err.Error())
	case errors.Is(err, chatpkg.ErrEmailInvalid):
		writeStartError(w, http.StatusBadRequest, "email_invalid", err.Error())
	case errors.Is(err, chatpkg.ErrModerated):
		writeStartError(w, http.StatusForbidden, "moderated", err.Error())
	default:
		log.Warnf("chat/start internal error: %s", err.Error())
		writeStartError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// extractPeerIP pulls the peer IP off r. Honours
// X-Forwarded-For first (Cloudflare + reverse-proxy hosts), falls
// back to RemoteAddr.
func extractPeerIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First IP in the chain is the originator.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return strings.TrimSpace(cf)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// enrichChatStart fills in country / device / meta.url when the widget
// posted them blank. The mobile app's session header reads these three
// fields directly, and most third-party visitor widgets won't populate
// them — so deriving from browser geolocation, peer IP, User-Agent, and
// Referer here gives every deployment a useful operator view for free.
//
// Country resolution order (each step only fires if the previous
// produced nothing):
//
//  1. Customer-provided body.Country (visitor widget's explicit value).
//  2. body.Latitude / body.Longitude — browser geolocation, reverse-
//     geocoded via Nominatim. High-confidence; no "via IP" tag.
//  3. peerIP via badactor.GetIPInfo (cache-only). Tagged
//     meta.country_source = "ip" so the operator UI can render
//     "via IP" in small text.
//
// Customer-provided values are never overwritten. IP cache lookups are
// cache-only on the hot path; an async fetch primes the cache for the
// next request from this IP.
func enrichChatStart(country, device string, meta map[string]any, peerIP, ua, referer string, lat, lon *float64) (string, string, map[string]any) {
	if country == "" && lat != nil && lon != nil {
		if cc := badactor.LookupCoordsCountry(*lat, *lon); cc != "" {
			country = cc
		}
	}
	if country == "" && peerIP != "" {
		if info := badactor.GetIPInfo(peerIP); info != nil && info.CountryCode != "" {
			country = info.CountryCode
			// Flag the source so the operator UI can surface the
			// lower-confidence nature of IP-based geolocation (e.g.
			// render "via IP" in small text under the country code).
			// Direct visitor-widget submissions never get this tag.
			if meta == nil {
				meta = map[string]any{}
			}
			meta["country_source"] = "ip"
		}
		badactor.LookupIPInfoAsync(peerIP)
	}
	if device == "" {
		device = formatDeviceFromUA(ua)
	}
	if referer != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		if _, ok := meta["url"]; !ok {
			meta["url"] = referer
		}
	}
	return country, device, meta
}

// formatDeviceFromUA renders a short "OS · Browser" label from the
// User-Agent header for the mobile app's session-header Device card.
// Returns empty for unparseable / bot UAs so the card falls back to "—".
func formatDeviceFromUA(ua string) string {
	uaf := enrich.ParseUA(ua)
	if uaf.IsBot {
		return ""
	}
	os := normalizeUAField(uaf.OS)
	br := normalizeUAField(uaf.Browser)
	switch {
	case os != "" && br != "":
		return os + " · " + br
	case os != "":
		return os
	case br != "":
		return br
	default:
		return ""
	}
}

// normalizeUAField strips uap-go's placeholder value ("Other") for
// unrecognised families and trims whitespace.
func normalizeUAField(s string) string {
	s = strings.TrimSpace(s)
	if s == "Other" {
		return ""
	}
	return s
}

// quiet vet's "context not used" lint — context is reserved for
// per-request cancellation that the handler doesn't strictly need
// here but should pass through (the underlying svc.Start does).
var _ = context.Background
