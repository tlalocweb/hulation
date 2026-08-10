package chat

// Pure, in-memory tests for session resume and the idle sweeper — the two
// server-side halves of "a browser refresh must not lose the chat". No
// ClickHouse required; they run against the same fakeStore close_test.go uses,
// so the rules that actually protect the feature always execute in CI.
//
// What each case pins down:
//   - an expired token still resumes                → TestResumeAcceptsExpiredToken
//   - a forged/foreign-signed token never does      → TestResumeRejectsBadSignature
//   - a closed session is terminal, not resumable   → TestResumeRefusesTerminalSession
//   - the window is measured from stored activity   → TestResumeEnforcesWindowFromLastMessage
//   - an active chat stays resumable indefinitely   → TestResumeActiveSessionWithinWindow
//   - resume issues a usable, unexpired token       → TestResumeIssuesFreshUsableToken
//   - resume never mints a second session           → TestResumeKeepsSameSessionID
//   - the sweeper closes only genuinely idle chats  → TestSweeperClosesOnlyIdleSessions
//   - sweeping is idempotent across passes          → TestSweeperIsIdempotent
//   - idle_timeout=0 disables sweeping entirely     → TestNewSweeperDisabled

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testJWTKey = "resume-test-key"

// ListStaleSessions extends the shared fakeStore (close_test.go) with the one
// read the sweeper needs. Methods may live in any file of the package.
func (f *fakeStore) ListStaleSessions(_ context.Context, cutoff time.Time, limit uint32) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Session
	for _, s := range f.sessions {
		if IsTerminalStatus(s.Status) {
			continue
		}
		if s.LastMessageAt.Before(cutoff) {
			out = append(out, s)
		}
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

// seedSession puts an open session whose last activity was `idle` ago.
func seedSession(t *testing.T, store *fakeStore, idle time.Duration) Session {
	t.Helper()
	sess := Session{
		ID:            uuid.New(),
		ServerID:      "srv1",
		VisitorID:     "vis1",
		VisitorEmail:  "visitor@example.com",
		Status:        StatusOpen,
		StartedAt:     time.Now().UTC().Add(-idle),
		LastMessageAt: time.Now().UTC().Add(-idle),
	}
	store.put(sess)
	return sess
}

// expiredTokenFor mints a token for sess that expired an hour ago — the normal
// state of a token a visitor left sitting in localStorage.
//
// Signed here rather than via IssueToken because that helper deliberately
// normalises ttl <= 0 up to DefaultTokenTTL, so it cannot produce an
// already-expired token (and a test built on it would silently assert nothing).
func expiredTokenFor(t *testing.T, sess Session) string {
	t.Helper()
	return signChatTokenAt(t, sess, time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-time.Hour))
}

func signChatTokenAt(t *testing.T, sess Session, issued, expires time.Time) string {
	t.Helper()
	claims := &ChatClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   TokenSubject,
			IssuedAt:  jwt.NewNumericDate(issued),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
		SessionID:    sess.ID.String(),
		VisitorID:    sess.VisitorID,
		ServerID:     sess.ServerID,
		VisitorEmail: sess.VisitorEmail,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestResumeAcceptsExpiredToken(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, time.Minute)
	tok := expiredTokenFor(t, sess)

	// Sanity: the token really is expired for normal use, so this test would
	// be vacuous if ParseToken accepted it.
	if _, err := ParseToken(testJWTKey, tok); err == nil {
		t.Fatal("expected ParseToken to reject the expired token")
	}

	res, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL, tok, 24*time.Hour)
	if err != nil {
		t.Fatalf("resume with expired token: %v", err)
	}
	if res.SessionID != sess.ID {
		t.Fatalf("resumed the wrong session: got %s want %s", res.SessionID, sess.ID)
	}
}

func TestResumeRejectsBadSignature(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, time.Minute)
	// Same claims, signed with a different key — i.e. forged.
	tok, _, err := IssueToken("some-other-key", sess.ID, sess.VisitorID, sess.ServerID, sess.VisitorEmail, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	_, err = resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL, tok, 24*time.Hour)
	if !errors.Is(err, ErrResumeUnavailable) {
		t.Fatalf("expected ErrResumeUnavailable for a foreign-signed token, got %v", err)
	}
}

func TestResumeRefusesTerminalSession(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, time.Minute)
	sess.Status = StatusClosed
	store.put(sess)

	_, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 24*time.Hour)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed for a closed session, got %v", err)
	}
}

func TestResumeEnforcesWindowFromLastMessage(t *testing.T) {
	store := newFakeStore()
	// Open, but untouched for 25h — past a 24h window.
	sess := seedSession(t, store, 25*time.Hour)

	_, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 24*time.Hour)
	if !errors.Is(err, ErrResumeExpired) {
		t.Fatalf("expected ErrResumeExpired past the window, got %v", err)
	}

	// The same session resumes under a window wide enough to cover it, which
	// proves the bound comes from config rather than anything baked into the
	// token — so shortening resume_window takes effect on tokens already out.
	if _, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 48*time.Hour); err != nil {
		t.Fatalf("expected resume to succeed inside a 48h window, got %v", err)
	}
}

func TestResumeActiveSessionWithinWindow(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, 5*time.Minute)
	if _, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 24*time.Hour); err != nil {
		t.Fatalf("recently-active session should resume: %v", err)
	}
}

func TestResumeIssuesFreshUsableToken(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, time.Minute)

	res, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 24*time.Hour)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// The whole point: the reissued token must pass the STRICT parse the
	// WebSocket handler runs, otherwise the widget still can't reconnect.
	claims, err := ParseToken(testJWTKey, res.ChatToken)
	if err != nil {
		t.Fatalf("reissued token failed strict parse: %v", err)
	}
	if claims.SessionID != sess.ID.String() {
		t.Fatalf("reissued token points at %s, want %s", claims.SessionID, sess.ID)
	}
	if claims.ServerID != sess.ServerID {
		t.Fatalf("reissued token server %q, want %q", claims.ServerID, sess.ServerID)
	}
	if !res.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("reissued token already expired at %s", res.ExpiresAt)
	}
}

func TestResumeKeepsSameSessionID(t *testing.T) {
	store := newFakeStore()
	sess := seedSession(t, store, time.Minute)

	// Resuming twice must keep returning the SAME session. A resume that
	// minted a new one would reintroduce exactly the duplicate-session bug
	// this feature exists to fix.
	first, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		expiredTokenFor(t, sess), 24*time.Hour)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	second, err := resumeSession(context.Background(), store, testJWTKey, DefaultTokenTTL,
		first.ChatToken, 24*time.Hour)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if first.SessionID != sess.ID || second.SessionID != sess.ID {
		t.Fatalf("session id drifted across resumes: %s / %s (want %s)",
			first.SessionID, second.SessionID, sess.ID)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("resume created extra session rows: %d", len(store.sessions))
	}
}

func TestSweeperClosesOnlyIdleSessions(t *testing.T) {
	store := newFakeStore()
	idle := seedSession(t, store, 3*time.Hour)   // abandoned
	active := seedSession(t, store, 5*time.Minute) // still in use

	sw := NewSweeper(store, NewHub(), SweeperConfig{IdleTimeout: 2 * time.Hour})
	if sw == nil {
		t.Fatal("expected a sweeper for a positive idle timeout")
	}
	n, err := sw.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d sessions, want 1", n)
	}

	got, _ := store.GetSession(context.Background(), "srv1", idle.ID)
	if !IsTerminalStatus(got.Status) {
		t.Fatalf("idle session left in status %q, want terminal", got.Status)
	}
	got, _ = store.GetSession(context.Background(), "srv1", active.ID)
	if IsTerminalStatus(got.Status) {
		t.Fatal("sweeper closed a session that was still active")
	}
}

func TestSweeperIsIdempotent(t *testing.T) {
	store := newFakeStore()
	seedSession(t, store, 3*time.Hour)
	sw := NewSweeper(store, NewHub(), SweeperConfig{IdleTimeout: 2 * time.Hour})

	if n, err := sw.SweepOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first sweep: n=%d err=%v", n, err)
	}
	// Second pass must report no transitions — a session already closed is
	// not re-closed, so agents don't get a duplicate session_closed every tick.
	if n, err := sw.SweepOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("second sweep should be a no-op: n=%d err=%v", n, err)
	}
}

func TestNewSweeperDisabled(t *testing.T) {
	if sw := NewSweeper(newFakeStore(), NewHub(), SweeperConfig{IdleTimeout: 0}); sw != nil {
		t.Fatal("idle_timeout=0 must disable the sweeper entirely")
	}
}
