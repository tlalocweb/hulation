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

	"github.com/tlalocweb/hulation/log"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
)

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
			Meta           map[string]any `json:"meta"`
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

		res, err := svc.Start(r.Context(), chatpkg.StartParams{
			ServerID:     body.ServerID,
			VisitorID:    body.VisitorID,
			Email:        body.Email,
			FirstMessage: body.FirstMessage,
			Turnstile:    body.TurnstileToken,
			PeerIP:       peerIP,
			UserAgent:    r.Header.Get("User-Agent"),
			Country:      body.Country,
			Device:       body.Device,
			Meta:         body.Meta,
		}, isKnown)

		if err != nil {
			handleStartErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": res.SessionID.String(),
			"chat_token": res.ChatToken,
			"expires_at": res.ExpiresAt,
			"message_id": idOrEmpty(res.FirstMessage),
		})
	}
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

// quiet vet's "context not used" lint — context is reserved for
// per-request cancellation that the handler doesn't strictly need
// here but should pass through (the underlying svc.Start does).
var _ = context.Background
