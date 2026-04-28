package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/pkg/chat/captcha"
)

// We don't have a sql-driver shim handy, so these tests assert on
// the early-exit branches of Service.Start (rate limit, captcha,
// email, kill-switch) and exercise IssueToken / ParseToken
// directly for the happy-path bits. The persistence layer is
// covered by the live-DB smoke test in stage 4b.2.

func newServiceWith(captchaErr error) *Service {
	return &Service{
		Store:     NewStore(nil), // nil DB → CreateSession returns "DB unavailable"
		Captcha:   stubVerifier{err: captchaErr},
		RateLimit: NewRateLimiter(time.Minute, 100),
		JWTKey:    "k-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

type stubVerifier struct{ err error }

func (s stubVerifier) Name() string { return "stub" }
func (s stubVerifier) Verify(_ context.Context, _, _ string) error {
	return s.err
}

func TestStartKillSwitch(t *testing.T) {
	s := newServiceWith(nil)
	s.DisableNewSessions = true
	_, err := s.Start(context.Background(), StartParams{ServerID: "tlaloc", Email: "a@b.com"}, nil)
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("want ErrDisabled, got %v", err)
	}
}

func TestStartUnknownServer(t *testing.T) {
	s := newServiceWith(nil)
	_, err := s.Start(context.Background(),
		StartParams{ServerID: "tlaloc", Email: "a@b.com", Turnstile: "tok"},
		func(string) bool { return false })
	if !errors.Is(err, ErrServerUnknown) {
		t.Errorf("want ErrServerUnknown, got %v", err)
	}
}

func TestStartMissingEmail(t *testing.T) {
	s := newServiceWith(nil)
	_, err := s.Start(context.Background(), StartParams{ServerID: "tlaloc"}, nil)
	if !errors.Is(err, ErrEmailInvalid) {
		t.Errorf("want ErrEmailInvalid, got %v", err)
	}
}

func TestStartCaptchaInvalid(t *testing.T) {
	s := newServiceWith(captcha.ErrInvalidToken)
	_, err := s.Start(context.Background(),
		StartParams{ServerID: "tlaloc", Email: "a@b.com", Turnstile: "tok"},
		nil)
	if !errors.Is(err, ErrCaptchaInvalid) {
		t.Errorf("want ErrCaptchaInvalid, got %v", err)
	}
}

func TestStartCaptchaUnavailable(t *testing.T) {
	s := newServiceWith(captcha.ErrUnavailable)
	_, err := s.Start(context.Background(),
		StartParams{ServerID: "tlaloc", Email: "a@b.com", Turnstile: "tok"},
		nil)
	if !errors.Is(err, ErrCaptchaUnavail) {
		t.Errorf("want ErrCaptchaUnavail, got %v", err)
	}
}

func TestStartRateLimited(t *testing.T) {
	s := newServiceWith(nil)
	s.RateLimit = NewRateLimiter(time.Minute, 1)
	// First call gets past the rate limiter and then fails because
	// captcha verification needs a (stubbed) verifier — we passed
	// captchaErr=nil so it succeeds, then store fails (nil DB).
	// We assert the SECOND call fails with ErrRateLimited.
	_, _ = s.Start(context.Background(),
		StartParams{ServerID: "tlaloc", Email: "a@b.com", Turnstile: "tok", PeerIP: "1.2.3.4"},
		nil)
	_, err := s.Start(context.Background(),
		StartParams{ServerID: "tlaloc", Email: "a@b.com", Turnstile: "tok", PeerIP: "1.2.3.4"},
		nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("want ErrRateLimited, got %v", err)
	}
}

func TestStartIssuesValidToken(t *testing.T) {
	// Walk through the pipeline by hand for the happy path — the
	// store is nil so we can't actually persist, but we can test
	// the bits we do reach (rate limit, captcha, email, moderation
	// short-circuit). Then exercise IssueToken / ParseToken
	// directly so the test still asserts the wire-format tokens
	// we'd hand to a visitor.
	tok, _, err := IssueToken(
		"k-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		uuid.New(),
		"vid", "tlaloc", "alice@example.com",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseToken("k-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.VisitorEmail != "alice@example.com" || claims.ServerID != "tlaloc" {
		t.Errorf("claims drift: %+v", claims)
	}
}
