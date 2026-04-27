// Package moderate is the optional pre-issuance OpenAI moderation
// pass for /chat/start. When enabled (chat.openai.enabled = true),
// the visitor's first message is shown to a small classifier
// prompt; messages flagged ABUSE or SPAM block the session.
//
// The same vendor + similar shape to the tlalocwebsite backend's
// classifier (PLAN_4B.md §1.1). We inline a tiny REST client here
// rather than importing the official OpenAI SDK because (a) the
// SDK's transitive deps are heavy and (b) we use exactly one
// chat-completions endpoint with one prompt.
//
// Production knobs: timeout (default 3s), on-error policy
// ("allow"|"deny"; allow by default — blocking real customers when
// moderation is unavailable would be a worse outcome than the
// occasional spam getting through).
package moderate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Verdict is the classifier output.
type Verdict int

const (
	VerdictUnspecified Verdict = iota
	VerdictReal               // looks like a genuine customer query
	VerdictAbuse              // hostile / harassing / off-topic
	VerdictSpam               // bot-shaped or commercial promotion
)

// String surfaces the verdict in logs / error messages.
func (v Verdict) String() string {
	switch v {
	case VerdictReal:
		return "REAL"
	case VerdictAbuse:
		return "ABUSE"
	case VerdictSpam:
		return "SPAM"
	}
	return "UNSPECIFIED"
}

// ErrUpstream signals the OpenAI call failed (timeout, network,
// non-2xx). The /chat/start handler decides whether to fail-open
// or fail-closed based on Config.OnError.
var ErrUpstream = errors.New("moderate: upstream error")

// ErrBlocked indicates the verdict was ABUSE or SPAM.
var ErrBlocked = errors.New("moderate: blocked")

// Config controls the optional moderation step. Zero-valued config
// means "disabled" — the New() factory returns nil for those, so
// callers do `if m := moderate.New(cfg); m != nil { m.Classify(...) }`.
type Config struct {
	Enabled bool
	APIKey  string
	Model   string        // e.g. "gpt-5.4-nano"
	Timeout time.Duration // 0 → 3s default
	OnError string        // "allow" (default) | "deny"
}

// Moderator is the package handle. Construct with New().
type Moderator struct {
	apiKey  string
	model   string
	timeout time.Duration
	onErr   string
	url     string // overridable for tests; empty → production OpenAI
	client  *http.Client
}

// DefaultEndpoint is the OpenAI chat-completions URL.
const DefaultEndpoint = "https://api.openai.com/v1/chat/completions"

// New returns a Moderator, or nil when cfg.Enabled is false /
// APIKey is missing. The caller short-circuits the moderation step
// in that case.
func New(cfg Config) *Moderator {
	if !cfg.Enabled || cfg.APIKey == "" {
		return nil
	}
	t := cfg.Timeout
	if t <= 0 {
		t = 3 * time.Second
	}
	model := cfg.Model
	if model == "" {
		model = "gpt-5.4-nano" // matches tlaloc backend default
	}
	onErr := cfg.OnError
	if onErr != "deny" {
		onErr = "allow"
	}
	return &Moderator{
		apiKey:  cfg.APIKey,
		model:   model,
		timeout: t,
		onErr:   onErr,
		client:  &http.Client{Timeout: t},
	}
}

// Classify the visitor's first chat message. Returns:
//
//	VerdictReal, nil        — let the session through
//	VerdictAbuse, ErrBlocked — fail the request
//	VerdictSpam,  ErrBlocked — fail the request
//	VerdictUnspecified, ErrUpstream — caller checks OnError
//
// The /chat/start handler treats the last case via OnError:
// "allow" → log + proceed, "deny" → 503.
func (m *Moderator) Classify(ctx context.Context, message string) (Verdict, error) {
	if m == nil {
		return VerdictReal, nil // moderation disabled
	}
	message = strings.TrimSpace(message)
	if message == "" {
		// Empty message can't be classified — treat as REAL and
		// let the upstream "first_message required" check (if any)
		// surface. Our handler enforces non-empty before calling.
		return VerdictReal, nil
	}

	endpoint := m.url
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	body := map[string]any{
		"model": m.model,
		"messages": []any{
			map[string]string{
				"role": "system",
				"content": "You are a chat-intake moderation classifier. Reply with " +
					"exactly one token: REAL, ABUSE, or SPAM. " +
					"REAL = a genuine customer question or comment. " +
					"ABUSE = hostile, harassing, or off-topic content. " +
					"SPAM = bot-shaped, automated, or commercial promotion.",
			},
			map[string]string{"role": "user", "content": message},
		},
		"temperature": 0,
		"max_tokens":  4,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return VerdictUnspecified, fmt.Errorf("%w: marshal: %v", ErrUpstream, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return VerdictUnspecified, fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	res, err := m.client.Do(req)
	if err != nil {
		return VerdictUnspecified, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		return VerdictUnspecified, fmt.Errorf("%w: status %d: %s", ErrUpstream, res.StatusCode, string(raw))
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return VerdictUnspecified, fmt.Errorf("%w: decode: %v", ErrUpstream, err)
	}
	if len(decoded.Choices) == 0 {
		return VerdictUnspecified, fmt.Errorf("%w: empty choices", ErrUpstream)
	}
	verdict := parseVerdict(decoded.Choices[0].Message.Content)
	if verdict == VerdictAbuse || verdict == VerdictSpam {
		return verdict, fmt.Errorf("%w: %s", ErrBlocked, verdict)
	}
	if verdict == VerdictUnspecified {
		// Model returned something we don't recognise. Treat as
		// "couldn't classify" and let the on-error policy decide.
		return VerdictUnspecified, fmt.Errorf("%w: unrecognised verdict %q", ErrUpstream, decoded.Choices[0].Message.Content)
	}
	return verdict, nil
}

// OnError returns the configured fallback policy ("allow" / "deny").
func (m *Moderator) OnError() string {
	if m == nil {
		return "allow"
	}
	return m.onErr
}

func parseVerdict(s string) Verdict {
	s = strings.ToUpper(strings.TrimSpace(s))
	// Models often wrap the answer in punctuation/whitespace; pick
	// the first whitespace-delimited token.
	if i := strings.IndexAny(s, " \t\n,.;:!?"); i >= 0 {
		s = s[:i]
	}
	switch s {
	case "REAL":
		return VerdictReal
	case "ABUSE":
		return VerdictAbuse
	case "SPAM":
		return VerdictSpam
	}
	return VerdictUnspecified
}
