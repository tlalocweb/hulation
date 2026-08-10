package chat

// Session resume. The visitor widget persists {session_id, chat_token} in the
// browser so a page refresh — or closing the tab and coming back — rejoins the
// SAME chat instead of stranding it and starting a new one.
//
// The server side of that is this file. The problem it solves: the chat token
// is deliberately short-lived (30 min) while a resumable session is long-lived
// (24h by default), so a returning visitor almost always presents an expired
// token. Re-running /chat/start is not an option — it is captcha-gated and
// mints a NEW session, which is exactly the bug we're fixing. Resume instead
// re-issues a token for the session the visitor already holds.
//
// Authority model — the token proves *which* session, the database decides
// *whether*:
//
//   - The token's signature, subject and sid/srv are fully validated, so only
//     the visitor who was issued this session can resume it. Expiry alone is
//     ignored (see ParseTokenAllowExpired).
//   - The session row is then reloaded from ClickHouse and is the sole
//     authority on liveness: a terminal (closed/expired) session is refused,
//     and the resume window is measured against the stored last_message_at,
//     never against a claim the client could hold a stale copy of.
//
// So a stolen token buys no more than the configured window on one session
// that is still open, and an operator shortening resume_window takes effect
// immediately for tokens already in the wild.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Errors returned by Service.Resume. The HTTP handler maps each to a status +
// machine-readable code so the widget can tell "start fresh" apart from "show
// the read-only transcript".
var (
	// ErrResumeExpired means the session is still open but its last activity
	// is older than the configured resume window.
	ErrResumeExpired = errors.New("chat: resume window elapsed")
	// ErrResumeUnavailable means resume is not possible for a reason the
	// visitor can't fix by retrying (bad token, unknown session).
	ErrResumeUnavailable = errors.New("chat: cannot resume")
)

// ResumeResult is what Resume returns on success.
type ResumeResult struct {
	SessionID uuid.UUID
	ChatToken string
	ExpiresAt time.Time
	// Status is the session's current status. Always non-terminal here —
	// a terminal session returns ErrSessionClosed instead.
	Status string
	// VisitorEmail is echoed back so the widget can render the session
	// header without a second round-trip.
	VisitorEmail string
}

// ResumeWindow is the maximum age (since last activity) of a session the
// visitor may still resume. Zero falls back to the 24h package default so a
// mis-wired boot can't accidentally grant an unbounded window.
const DefaultResumeWindow = 24 * time.Hour

// Resume validates a (possibly expired) chat token and, when the session is
// still live and within the resume window, issues a fresh token for it.
//
// Returns:
//   - ErrSessionClosed when the session exists but is terminal. This is NOT a
//     failure for the widget: it re-connects read-only and shows the ended
//     transcript, which is why it stays distinct from the errors below.
//   - ErrResumeExpired when the session is open but too old to resume.
//   - ErrResumeUnavailable for a malformed/forged token or a missing row.
func (s *Service) Resume(ctx context.Context, token string, window time.Duration) (*ResumeResult, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("chat: service not initialised")
	}
	return resumeSession(ctx, s.Store, s.JWTKey, s.TokenTTL, token, window)
}

// resumeSession is Resume's logic over the narrow read surface it actually
// needs, so the rules that matter (terminal refused, window enforced from the
// stored last_message_at, expiry waived but signature not) are unit-testable
// against a fake instead of requiring a live ClickHouse.
func resumeSession(
	ctx context.Context,
	store sessionGetter,
	jwtKey string,
	ttl time.Duration,
	token string,
	window time.Duration,
) (*ResumeResult, error) {
	if window <= 0 {
		window = DefaultResumeWindow
	}

	// Signature/subject/shape are enforced; only `exp` is waived. A returning
	// visitor's token is expected to be expired — that is the normal case.
	claims, err := ParseTokenAllowExpired(jwtKey, token)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResumeUnavailable, err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: bad sid", ErrResumeUnavailable)
	}

	// Source of truth. Never trust the token's view of the session's state.
	sess, err := store.GetSession(ctx, claims.ServerID, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: no such session", ErrResumeUnavailable)
		}
		return nil, fmt.Errorf("chat: resume load session: %w", err)
	}
	if IsTerminalStatus(sess.Status) {
		return nil, ErrSessionClosed
	}

	// Resume window measured from the session's last activity, so an active
	// chat stays resumable indefinitely while an abandoned one ages out. (The
	// idle sweeper normally closes an abandoned session first; this check is
	// the backstop for when the sweeper is disabled or hasn't run yet.)
	if last := sess.LastMessageAt; !last.IsZero() {
		if age := time.Since(last); age > window {
			return nil, fmt.Errorf("%w: idle %s > %s", ErrResumeExpired, age.Truncate(time.Second), window)
		}
	}

	// Re-issue. Same session, same visitor identity, fresh TTL. Prefer the
	// email recorded on the session row over the token's copy so a token
	// minted before an email correction doesn't resurrect the stale value.
	email := sess.VisitorEmail
	if email == "" {
		email = claims.VisitorEmail
	}
	tok, exp, err := IssueToken(jwtKey, sessionID, sess.VisitorID, sess.ServerID, email, ttl)
	if err != nil {
		return nil, fmt.Errorf("chat: resume issue token: %w", err)
	}
	return &ResumeResult{
		SessionID:    sessionID,
		ChatToken:    tok,
		ExpiresAt:    exp,
		Status:       sess.Status,
		VisitorEmail: email,
	}, nil
}
