// Package captcha verifies Turnstile / reCAPTCHA tokens server-side
// before hula will issue a chat-session JWT in /chat/start.
//
// Two providers ship in 4b.3:
//
//   - "turnstile" (Cloudflare). POSTs the token to
//     https://challenges.cloudflare.com/turnstile/v0/siteverify
//     with `secret` + `response` + `remoteip`.
//   - "recaptcha" (Google v2/v3). POSTs to
//     https://www.google.com/recaptcha/api/siteverify with the same
//     fields. v3 also returns a score; we accept any score above
//     RecaptchaV3Threshold.
//
// A third "test" provider treats any non-empty token as valid;
// e2e suites and `pnpm dev` against a local hula opt in via the
// HULA_CHAT_CAPTCHA_TEST_BYPASS=1 env var or
// config.chat.captcha.test_bypass: true. Production should leave
// both off — boot logs a WARN if either is set.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Verifier is what /chat/start calls. Implementations are picked
// by the chat-config provider field at boot.
type Verifier interface {
	// Verify returns nil on success, an error otherwise. token is
	// what the widget sent over the wire; remoteIP is the visitor's
	// peer address (passed to the upstream verifier so it can do
	// its own correlation).
	Verify(ctx context.Context, token, remoteIP string) error
	// Name returns a short string for logs/metrics.
	Name() string
}

// ErrInvalidToken is returned when the upstream service rejects the
// token. Wrapped with context where useful.
var ErrInvalidToken = errors.New("captcha: invalid token")

// ErrUnavailable is returned when the upstream service is unreachable
// or returns a non-2xx the verifier doesn't know how to handle.
// Callers can decide whether to fail-open or fail-closed.
var ErrUnavailable = errors.New("captcha: upstream unavailable")

// RecaptchaV3Threshold is the minimum score (0–1) an reCAPTCHA-v3
// verification must report to be accepted. 0.5 is Google's default
// recommendation. Configurable via the recaptcha provider's
// MinScore field if operators tune.
const RecaptchaV3Threshold = 0.5

// --- Turnstile --------------------------------------------------

const turnstileURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Turnstile is the Cloudflare verifier.
type Turnstile struct {
	SecretKey string
	// URL overrides the upstream siteverify endpoint. Empty means
	// the production Cloudflare URL. Tests stub this; production
	// code never sets it.
	URL  string
	HTTP *http.Client // optional; defaults to a 30s-timeout client
}

func (t Turnstile) Name() string { return "turnstile" }

type turnstileResp struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"`
}

func (t Turnstile) Verify(ctx context.Context, token, remoteIP string) error {
	if t.SecretKey == "" {
		return fmt.Errorf("turnstile: secret_key not configured")
	}
	if token == "" {
		return ErrInvalidToken
	}
	form := url.Values{}
	form.Set("secret", t.SecretKey)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	c := t.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := t.URL
	if endpoint == "" {
		endpoint = turnstileURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%w: status %d", ErrUnavailable, res.StatusCode)
	}
	var body turnstileResp
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: decode body: %v", ErrUnavailable, err)
	}
	if !body.Success {
		return fmt.Errorf("%w: %s", ErrInvalidToken, strings.Join(body.ErrorCodes, ","))
	}
	return nil
}

// --- reCAPTCHA --------------------------------------------------

const recaptchaURL = "https://www.google.com/recaptcha/api/siteverify"

// Recaptcha is the Google reCAPTCHA v2/v3 verifier. v3 is detected
// by a non-zero Score in the response; v2 always reports score=0.
type Recaptcha struct {
	SecretKey string
	// MinScore overrides the default (0.5) when set. Ignored for
	// v2 responses (which omit the score field).
	MinScore float64
	URL      string // override for tests; empty = production Google URL
	HTTP     *http.Client
}

func (r Recaptcha) Name() string { return "recaptcha" }

type recaptchaResp struct {
	Success     bool     `json:"success"`
	Score       float64  `json:"score"`
	Action      string   `json:"action"`
	ChallengeTs string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	ErrorCodes  []string `json:"error-codes"`
}

func (r Recaptcha) Verify(ctx context.Context, token, remoteIP string) error {
	if r.SecretKey == "" {
		return fmt.Errorf("recaptcha: secret_key not configured")
	}
	if token == "" {
		return ErrInvalidToken
	}
	form := url.Values{}
	form.Set("secret", r.SecretKey)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	c := r.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := r.URL
	if endpoint == "" {
		endpoint = recaptchaURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("recaptcha: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%w: status %d", ErrUnavailable, res.StatusCode)
	}
	var body recaptchaResp
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: decode body: %v", ErrUnavailable, err)
	}
	if !body.Success {
		return fmt.Errorf("%w: %s", ErrInvalidToken, strings.Join(body.ErrorCodes, ","))
	}
	// v3: score present + above threshold; v2: score is 0 and
	// success=true is enough.
	threshold := r.MinScore
	if threshold == 0 {
		threshold = RecaptchaV3Threshold
	}
	if body.Score > 0 && body.Score < threshold {
		return fmt.Errorf("%w: score %.2f below threshold %.2f", ErrInvalidToken, body.Score, threshold)
	}
	return nil
}

// --- Test (bypass) ----------------------------------------------

// TestBypass accepts any non-empty token. Use only in dev / e2e.
type TestBypass struct{}

func (TestBypass) Name() string { return "test-bypass" }

func (TestBypass) Verify(_ context.Context, token, _ string) error {
	if token == "" {
		return ErrInvalidToken
	}
	return nil
}

// EnvTestBypassEnabled is the env-var escape hatch e2e + dev set
// to short-circuit captcha. Tested separately so production code
// can fail loud if it's set in a non-dev build.
const EnvTestBypassEnabled = "HULA_CHAT_CAPTCHA_TEST_BYPASS"

// IsTestBypassEnv returns true if the operator opted in via env.
func IsTestBypassEnv() bool {
	return strings.TrimSpace(os.Getenv(EnvTestBypassEnabled)) != ""
}
