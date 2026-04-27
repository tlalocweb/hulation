package chat

// Service stitches together the components /chat/start needs:
//
//   * Captcha verifier (Turnstile / reCAPTCHA / TestBypass)
//   * Email verifier (AfterShip, optional)
//   * OpenAI moderation (optional)
//   * Rate limiter (per server_id, peer IP)
//   * Store (ClickHouse persistence)
//   * Token issuer
//
// The HTTP handler in server/chat_start.go is a thin shell over
// Service.Start().

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/pkg/chat/captcha"
	"github.com/tlalocweb/hulation/pkg/chat/emailverify"
	"github.com/tlalocweb/hulation/pkg/chat/moderate"
)

// VisitorWS is the small dependency bundle the visitor-side WS
// handler in server/chat_ws_visitor.go pulls from. Kept separate
// from Service so the WS handler can be wired without inheriting
// the start-flow's captcha/email/moderate machinery.
type VisitorWS struct {
	Store  *Store
	Hub    *Hub
	JWTKey string
}

// RouterEnqueuer is the small interface Service needs from the
// router. The concrete *Router implements it. Wired at boot.
type RouterEnqueuer interface {
	Enqueue(serverID string, sessionID uuid.UUID) (assignedTo string, queueDepth int)
}

// Service is the dependency bundle. Passed by pointer.
type Service struct {
	Store     *Store
	Captcha   captcha.Verifier
	Email     *emailverify.Verifier
	Moderate  *moderate.Moderator
	RateLimit *RateLimiter
	JWTKey    string
	TokenTTL  time.Duration
	// Router is the next-available enqueuer. Nil → no routing
	// (sessions stay status=queued and an admin must explicitly
	// take them).
	Router RouterEnqueuer

	// EmailTimeout / ModerateTimeout / CaptchaTimeout cap each
	// upstream call so a slow vendor doesn't hold /chat/start.
	EmailTimeout    time.Duration
	ModerateTimeout time.Duration
	CaptchaTimeout  time.Duration

	// DisableNewSessions is the operator kill-switch. When true,
	// Start() returns ErrDisabled.
	DisableNewSessions bool
}

// Errors returned by Service.Start. The HTTP handler maps these
// to specific status codes so the widget can render the right
// copy.
var (
	ErrDisabled       = errors.New("chat: new sessions disabled")
	ErrRateLimited    = errors.New("chat: rate limited")
	ErrCaptchaInvalid = errors.New("chat: captcha invalid")
	ErrCaptchaUnavail = errors.New("chat: captcha unavailable")
	ErrEmailInvalid   = errors.New("chat: email invalid")
	ErrModerated      = errors.New("chat: moderated")
	ErrServerUnknown  = errors.New("chat: unknown server_id")
)

// StartParams is what the HTTP handler hands to Start().
type StartParams struct {
	ServerID     string
	VisitorID    string
	Email        string
	FirstMessage string
	Turnstile    string
	PeerIP       string
	UserAgent    string
	Country      string
	Device       string
	Meta         map[string]any // arbitrary fields for chat_sessions.meta
}

// StartResult is what Start returns on success.
type StartResult struct {
	SessionID    uuid.UUID
	ChatToken    string
	ExpiresAt    time.Time
	FirstMessage Message // the persisted first-message row
}

// IsServerKnown should be set by the wiring layer to check that
// ServerID is configured. Defaults to "always known" — but the
// HTTP handler is expected to wire the real check (see
// server/chat_start.go) so a typo doesn't generate a session row
// for a server that doesn't exist.
type IsServerKnown func(serverID string) bool

// Start runs the full /chat/start pipeline:
//   1. operator kill-switch
//   2. server_id known?
//   3. rate limit by (server_id, peer ip)
//   4. captcha verify
//   5. email verify
//   6. optional OpenAI moderation
//   7. persist chat_session + first message
//   8. issue chat token
//
// The order matters: cheap checks first so a hostile actor pays
// the least to be rejected.
func (s *Service) Start(ctx context.Context, p StartParams, isKnown IsServerKnown) (*StartResult, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("chat: service not initialised")
	}
	if s.DisableNewSessions {
		return nil, ErrDisabled
	}
	if strings.TrimSpace(p.ServerID) == "" {
		return nil, fmt.Errorf("chat: server_id required")
	}
	if isKnown != nil && !isKnown(p.ServerID) {
		return nil, fmt.Errorf("%w: %q", ErrServerUnknown, p.ServerID)
	}
	if strings.TrimSpace(p.Email) == "" {
		return nil, fmt.Errorf("%w: email required", ErrEmailInvalid)
	}

	if !s.RateLimit.Allow(p.ServerID, p.PeerIP) {
		return nil, ErrRateLimited
	}

	// Captcha. Bounded by CaptchaTimeout (default 30s — same as
	// upstream client default; we keep the explicit cap so a slow
	// upstream can't pin a goroutine forever).
	captchaCtx := ctx
	if s.CaptchaTimeout > 0 {
		var cancel context.CancelFunc
		captchaCtx, cancel = context.WithTimeout(ctx, s.CaptchaTimeout)
		defer cancel()
	}
	if s.Captcha == nil {
		return nil, fmt.Errorf("%w: no verifier configured", ErrCaptchaUnavail)
	}
	if err := s.Captcha.Verify(captchaCtx, p.Turnstile, p.PeerIP); err != nil {
		if errors.Is(err, captcha.ErrInvalidToken) {
			return nil, fmt.Errorf("%w: %v", ErrCaptchaInvalid, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrCaptchaUnavail, err)
	}

	// Email verify. emailverify.Verifier is cheap (offline checks);
	// EmailTimeout caps DNS resolution.
	if s.Email != nil {
		emailCtx := ctx
		if s.EmailTimeout > 0 {
			var cancel context.CancelFunc
			emailCtx, cancel = context.WithTimeout(ctx, s.EmailTimeout)
			defer cancel()
		}
		if _, err := s.Email.Verify(emailCtx, p.Email); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEmailInvalid, err)
		}
	}

	// OpenAI moderation. Optional; nil moderator = skip.
	if s.Moderate != nil && strings.TrimSpace(p.FirstMessage) != "" {
		modCtx := ctx
		if s.ModerateTimeout > 0 {
			var cancel context.CancelFunc
			modCtx, cancel = context.WithTimeout(ctx, s.ModerateTimeout)
			defer cancel()
		}
		_, err := s.Moderate.Classify(modCtx, p.FirstMessage)
		switch {
		case errors.Is(err, moderate.ErrBlocked):
			return nil, fmt.Errorf("%w: %v", ErrModerated, err)
		case errors.Is(err, moderate.ErrUpstream):
			if s.Moderate.OnError() == "deny" {
				return nil, fmt.Errorf("%w: upstream %v", ErrModerated, err)
			}
			// "allow" — fall through; this is the documented
			// trade-off in PLAN_4B.md §5.4.
		case err != nil:
			// Should not happen, but treat unknown errors as
			// upstream-style and respect OnError.
			if s.Moderate.OnError() == "deny" {
				return nil, fmt.Errorf("%w: %v", ErrModerated, err)
			}
		}
	}

	// Persist session + first message.
	now := time.Now().UTC()
	sid := uuid.New()
	visitorID := strings.TrimSpace(p.VisitorID)
	if visitorID == "" {
		visitorID = uuid.New().String() // fall back to a one-off id
	}
	metaJSON := ""
	if len(p.Meta) > 0 {
		if buf, err := json.Marshal(p.Meta); err == nil {
			metaJSON = string(buf)
		}
	}
	sess := Session{
		ID:             sid,
		ServerID:       p.ServerID,
		VisitorID:      visitorID,
		VisitorEmail:   p.Email,
		VisitorCountry: p.Country,
		VisitorDevice:  p.Device,
		VisitorIP:      p.PeerIP,
		UserAgent:      p.UserAgent,
		StartedAt:      now,
		LastMessageAt:  now,
		Status:         StatusQueued,
		Meta:           metaJSON,
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("chat: persist session: %w", err)
	}
	var firstMsg Message
	if strings.TrimSpace(p.FirstMessage) != "" {
		firstMsg = Message{
			ID:        uuid.New(),
			SessionID: sid,
			ServerID:  p.ServerID,
			VisitorID: visitorID,
			Direction: DirVisitor,
			Content:   p.FirstMessage,
			When:      now,
		}
		if err := s.Store.AppendMessage(ctx, firstMsg); err != nil {
			return nil, fmt.Errorf("chat: persist first message: %w", err)
		}
	}

	// Issue token.
	tok, exp, err := IssueToken(s.JWTKey, sid, visitorID, p.ServerID, p.Email, s.TokenTTL)
	if err != nil {
		return nil, fmt.Errorf("chat: issue token: %w", err)
	}

	// Hand the new session to the router. If a ready agent picks
	// it up immediately, also flip the status to "assigned" + set
	// assigned_agent_id; the agent's control WS receives the
	// session_assigned frame from inside Enqueue.
	if s.Router != nil {
		if assignedTo, _ := s.Router.Enqueue(p.ServerID, sid); assignedTo != "" {
			if _, err := s.Store.AssignSession(ctx, p.ServerID, sid, assignedTo); err != nil {
				// Best-effort — the session is already
				// persisted; failure to update assignment
				// metadata is non-fatal.
				_ = err
			}
		}
	}

	return &StartResult{
		SessionID:    sid,
		ChatToken:    tok,
		ExpiresAt:    exp,
		FirstMessage: firstMsg,
	}, nil
}
