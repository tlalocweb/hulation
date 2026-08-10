package server

// Visitor-authenticated chat endpoints — the two operations a visitor needs
// that aren't messages on the socket. Both authenticate with the chat token
// issued by /chat/start (no admin JWT, no captcha):
//
//	POST /api/v1/chat/resume  — re-issue a token for a session the widget
//	                            persisted across a page refresh.
//	POST /api/v1/chat/close   — the visitor explicitly ends their own chat.
//
// Why resume is a separate endpoint rather than a flag on /chat/start:
// /chat/start is captcha-gated and always mints a NEW session. Reusing it on
// refresh is what stranded the old session and produced a duplicate in the
// agent's queue. Resume never creates anything — it only re-credentials a
// session the caller already provably owns.
//
// Error envelope matches /chat/start ({code, message}) so the widget parses
// one shape everywhere. Codes:
//
//	resume_expired    — session still open but idle beyond resume_window
//	session_closed    — session is terminal; widget should start fresh
//	cannot_resume     — bad/forged token, or no such session
//	bad_request       — malformed body
//	internal          — unexpected server-side error

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/log"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
)

// chatVisitorRateWindow / chatVisitorRateMax bound how often one peer IP may
// hit the visitor REST endpoints. These are public and each one costs a
// ClickHouse read, so an unthrottled flood is a cheap DoS.
//
// Sized well above legitimate use rather than near it: a widget resumes once
// per page load plus once per reconnect attempt, and reconnect backs off to
// 30s, so a normal visitor is nowhere near 30/min even while flapping. Several
// visitors behind one NAT still fit.
const (
	chatVisitorRateWindow = time.Minute
	chatVisitorRateMax    = 30
)

// chatVisitorAPI is the dependency bundle for the visitor REST endpoints.
type chatVisitorAPI struct {
	Service *chatpkg.Service
	Store   *chatpkg.Store
	Hub     *chatpkg.Hub
	JWTKey  string
	// ResumeWindow bounds how stale a session may be and still resume.
	ResumeWindow time.Duration
	// RateLimit throttles per (endpoint, peer IP). Nil disables throttling.
	RateLimit *chatpkg.RateLimiter
}

// allow reports whether this peer may proceed, writing the 429 itself when
// not. bucket separates resume from close so exhausting one can't lock the
// visitor out of the other — in particular, a resume flood must never stop
// someone from ending their chat.
func (api *chatVisitorAPI) allow(w http.ResponseWriter, r *http.Request, bucket string) bool {
	if api == nil || api.RateLimit == nil {
		return true
	}
	if api.RateLimit.Allow(bucket, extractPeerIP(r)) {
		return true
	}
	writeStartError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
	return false
}

// chatTokenFromRequest pulls the visitor's chat token from the JSON body
// ({"chat_token":"…"}).
//
// Body only — deliberately no ?token= fallback. The WebSocket endpoint has to
// put the token in the URL (browsers can't set headers on a WS handshake), but
// these are ordinary POSTs, and a token in a query string lands in access logs,
// proxy logs and Referer headers. Resume tokens are long-lived by design, so
// that's exactly the credential not to leak into logs.
func chatTokenFromRequest(w http.ResponseWriter, r *http.Request) (string, error) {
	var body struct {
		ChatToken string `json:"chat_token"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&body)
	}
	tok := strings.TrimSpace(body.ChatToken)
	if tok == "" {
		return "", errors.New("chat_token required")
	}
	return tok, nil
}

// chatResumeHandler re-issues a chat token for an existing, still-live
// session. This is what makes a browser refresh non-destructive.
func chatResumeHandler(api *chatVisitorAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeStartError(w, http.StatusMethodNotAllowed, "bad_request", "method not allowed")
			return
		}
		if !api.allow(w, r, "chat_resume") {
			return
		}
		tok, err := chatTokenFromRequest(w, r)
		if err != nil {
			writeStartError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		res, err := api.Service.Resume(r.Context(), tok, api.ResumeWindow)
		if err != nil {
			switch {
			case errors.Is(err, chatpkg.ErrSessionClosed):
				// Terminal, not an error condition per se: the widget drops
				// its stored session and offers a fresh chat.
				writeStartError(w, http.StatusConflict, "session_closed", "this chat has ended")
			case errors.Is(err, chatpkg.ErrResumeExpired):
				writeStartError(w, http.StatusConflict, "resume_expired", "this chat is no longer resumable")
			case errors.Is(err, chatpkg.ErrResumeUnavailable):
				// Deliberately vague to the client — a forged token and an
				// unknown session are indistinguishable from outside, so this
				// can't be used to probe for valid session ids.
				writeStartError(w, http.StatusUnauthorized, "cannot_resume", "could not resume this chat")
			default:
				log.Warnf("chat/resume internal error: %s", err)
				writeStartError(w, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		resp := map[string]any{
			"session_id": res.SessionID.String(),
			"chat_token": res.ChatToken,
			"expires_at": res.ExpiresAt,
			"status":     res.Status,
		}
		// Same node-pinning rule as /chat/start: the hub is per-process, so a
		// resumed socket must land on the node that owns the session.
		if pinURL := chatPinURL(r); pinURL != "" {
			resp["chat_url"] = pinURL
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// chatVisitorCloseHandler lets the visitor end their own chat.
//
// The widget's primary path is the WS "close" frame (see chat_ws_visitor.go);
// this REST fallback exists so "End chat" still works when the socket is down
// — otherwise the one moment a visitor most wants to leave (a broken
// connection) is the one moment they can't.
//
// An expired token is accepted here on purpose: ending a chat you already own
// is always safe and idempotent, and refusing would strand exactly the
// sessions we're trying to stop accumulating.
func chatVisitorCloseHandler(api *chatVisitorAPI) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeStartError(w, http.StatusMethodNotAllowed, "bad_request", "method not allowed")
			return
		}
		if !api.allow(w, r, "chat_close") {
			return
		}
		tok, err := chatTokenFromRequest(w, r)
		if err != nil {
			writeStartError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		claims, err := chatpkg.ParseTokenAllowExpired(api.JWTKey, tok)
		if err != nil {
			writeStartError(w, http.StatusUnauthorized, "cannot_resume", "invalid token")
			return
		}
		sessionID, err := uuid.Parse(claims.SessionID)
		if err != nil {
			writeStartError(w, http.StatusUnauthorized, "cannot_resume", "invalid session")
			return
		}
		_, _, err = chatpkg.CloseSession(r.Context(), api.Store, api.Hub,
			claims.ServerID, sessionID, chatCloseReasonVisitor)
		if err != nil {
			if errors.Is(err, chatpkg.ErrNotFound) {
				// Nothing to close. Report success: the caller's goal
				// ("this chat is over") already holds.
				writeChatOK(w)
				return
			}
			log.Warnf("chat/close internal error: %s", err)
			writeStartError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeChatOK(w)
	}
}

// chatCloseReasonVisitor is the reason string carried on the session_closed
// frame when the visitor themselves ended the chat, so the agent UI can
// distinguish it from an agent close or an idle sweep.
const chatCloseReasonVisitor = "visitor_ended"

func writeChatOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}
