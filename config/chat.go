package config

import "time"

const (
	// DefaultChatResumeWindow is how long after a session's last activity a
	// visitor may still resume it (POST /chat/resume re-issues a chat token
	// without re-running captcha). Bounds how long the token persisted in the
	// browser stays useful.
	DefaultChatResumeWindow = 24 * time.Hour
	// DefaultChatIdleTimeout is how long a non-terminal session may sit idle
	// before the sweeper closes it. Keeps abandoned chats out of the agent's
	// live queue. Zero disables auto-close.
	DefaultChatIdleTimeout = 2 * time.Hour
	// DefaultChatSweepInterval is how often the idle sweeper runs.
	DefaultChatSweepInterval = 5 * time.Minute
	// DefaultChatHistoryLimit is how many past messages the visitor WS
	// replays on connect so a reconnecting widget can rebuild its transcript.
	DefaultChatHistoryLimit = 200
)

// ChatConfig holds Phase-4b visitor-chat tunables. All fields
// optional. Sensible defaults are applied at boot:
//
//   retention_days: 365
//   resume_window:  24h
//   idle_timeout:   2h
//   sweep_interval: 5m
//   history_limit:  200
//   captcha:        { provider: "turnstile", test_bypass: false }
//   email_verifier: { smtp_check: false, disposable_check: true,
//                     role_check: true, misspell_check: true }
//   openai:         { enabled: false }
//
// Stage 4b.3 fills in the Captcha / EmailVerifier / OpenAI sub-
// configs with their concrete fields. Stage 4b.1 only needs the
// retention knob; the rest is declared here so the YAML round-trip
// stays compatible across the phase.
type ChatConfig struct {
	// RetentionDays sets TTL for chat_sessions and chat_messages.
	// Default 365. Operators can shorten for cost or lengthen for
	// compliance reasons; the migration runner picks up the value
	// at boot and the TTL is rewritten if it changes.
	RetentionDays int `yaml:"retention_days,omitempty"`

	// Captcha provider config (Turnstile / reCAPTCHA / test).
	// Filled in stage 4b.3. Nil = Turnstile with no test bypass.
	Captcha *ChatCaptchaConfig `yaml:"captcha,omitempty"`

	// EmailVerifier knobs for github.com/AfterShip/email-verifier.
	// Filled in stage 4b.3.
	EmailVerifier *ChatEmailVerifierConfig `yaml:"email_verifier,omitempty"`

	// OpenAI moderation pass over the visitor's first message.
	// Disabled by default (no quota / latency cost). Filled in 4b.3.
	OpenAI *ChatOpenAIConfig `yaml:"openai,omitempty"`

	// DisableNewSessions is the operator kill-switch. When true,
	// POST /chat/start returns 503 with a machine-readable error
	// code; existing sessions stay live. Useful during spam waves.
	DisableNewSessions bool `yaml:"disable_new_sessions,omitempty"`

	// ResumeWindow is how long after a session's last activity the visitor
	// may still resume it, as a Go duration string ("24h", "30m"). The chat
	// token itself is short-lived (30m); resuming re-issues one without
	// re-running captcha, so THIS is the knob that governs how long a
	// browser-persisted session stays usable. Default 24h.
	//
	// Note this interacts with IdleTimeout: a session the sweeper has already
	// closed is terminal, so in practice an idle chat stops being resumable
	// after IdleTimeout regardless of this value. Resuming a closed session
	// isn't an error — the widget gets a read-only transcript.
	ResumeWindow string `yaml:"resume_window,omitempty" default:"24h"`

	// IdleTimeout is how long a non-terminal session may sit with no new
	// messages before the sweeper closes it, as a Go duration string.
	// Prevents abandoned chats accumulating in the agent's live queue.
	// "0" (or a negative duration) disables auto-close entirely. Default 2h.
	IdleTimeout string `yaml:"idle_timeout,omitempty" default:"2h"`

	// SweepInterval is how often the idle sweeper scans for stale sessions.
	// Only meaningful when IdleTimeout is enabled. Default 5m.
	SweepInterval string `yaml:"sweep_interval,omitempty" default:"5m"`

	// HistoryLimit caps how many past messages the visitor WebSocket
	// replays on connect so a reconnecting widget can rebuild its
	// transcript. 0 = default (200); the store clamps at 1000.
	HistoryLimit int `yaml:"history_limit,omitempty"`
}

// parseDurationOr returns the parsed Go duration string, or def when the
// string is empty or unparseable. Shared by the Resolve* helpers below so a
// typo in the config degrades to the documented default rather than a boot
// failure — matching how ddns.ResolveInterval treats its interval.
func parseDurationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// ResolveResumeWindow returns the configured resume window, or the 24h
// default. Nil-safe: a missing `chat:` block resolves to the default.
func (c *ChatConfig) ResolveResumeWindow() time.Duration {
	if c == nil {
		return DefaultChatResumeWindow
	}
	d := parseDurationOr(c.ResumeWindow, DefaultChatResumeWindow)
	if d <= 0 {
		return DefaultChatResumeWindow
	}
	return d
}

// ResolveIdleTimeout returns the configured idle auto-close timeout, or the
// 2h default. A configured non-positive duration ("0", "0s") is honoured as
// "disabled" and returns 0 — distinct from an absent value, which defaults.
func (c *ChatConfig) ResolveIdleTimeout() time.Duration {
	if c == nil {
		return DefaultChatIdleTimeout
	}
	if c.IdleTimeout == "" {
		return DefaultChatIdleTimeout
	}
	d, err := time.ParseDuration(c.IdleTimeout)
	if err != nil {
		return DefaultChatIdleTimeout
	}
	if d <= 0 {
		return 0 // explicitly disabled
	}
	return d
}

// ResolveSweepInterval returns how often the idle sweeper runs, or the 5m
// default.
func (c *ChatConfig) ResolveSweepInterval() time.Duration {
	if c == nil {
		return DefaultChatSweepInterval
	}
	d := parseDurationOr(c.SweepInterval, DefaultChatSweepInterval)
	if d <= 0 {
		return DefaultChatSweepInterval
	}
	return d
}

// ResolveHistoryLimit returns how many messages to replay on WS connect.
func (c *ChatConfig) ResolveHistoryLimit() int {
	if c == nil || c.HistoryLimit <= 0 {
		return DefaultChatHistoryLimit
	}
	return c.HistoryLimit
}

// ChatCaptchaConfig — populated in stage 4b.3.
type ChatCaptchaConfig struct {
	// Provider selects the verifier. "turnstile" (default) or
	// "recaptcha". Only one provider is active per server.
	Provider string `yaml:"provider,omitempty"`
	// SiteKey + SecretKey are the per-deployment credentials issued
	// by the provider. SecretKey supports the {{env:NAME}} pattern
	// the rest of the config uses.
	SiteKey   string `yaml:"site_key,omitempty"`
	SecretKey string `yaml:"secret_key,omitempty"`
	// TestBypass: when true, /chat/start treats any captcha token
	// as valid. Combined with HULA_CHAT_CAPTCHA_TEST_BYPASS=1, this
	// gives e2e + dev a deterministic path. Should never be true in
	// production; boot logs a warning if it is.
	TestBypass bool `yaml:"test_bypass,omitempty"`
}

// ChatEmailVerifierConfig — populated in stage 4b.3.
type ChatEmailVerifierConfig struct {
	// SMTPCheck enables an outbound SMTP probe (slow, often
	// greylisted). Default false: rely on offline checks.
	SMTPCheck bool `yaml:"smtp_check,omitempty"`
	// DisposableCheck blocks 10minutemail-style domains. Default true.
	DisposableCheck *bool `yaml:"disposable_check,omitempty"`
	// RoleCheck blocks postmaster@, info@, etc. Default true.
	RoleCheck *bool `yaml:"role_check,omitempty"`
	// MisspellCheck surfaces "did you mean gmail.com?" failures.
	// Default true.
	MisspellCheck *bool `yaml:"misspell_check,omitempty"`
	// DNSCheck requires the address's domain to publish MX records.
	// Default true. Set false where outbound DNS isn't available or
	// resolvable — a sandboxed test environment, or an intranet
	// deployment whose mail domain has no public MX. Without an opt-out
	// this check alone makes chat unusable in those environments, since
	// every address fails before any other rule is consulted.
	DNSCheck *bool `yaml:"dns_check,omitempty"`
}

// ChatOpenAIConfig — populated in stage 4b.3.
type ChatOpenAIConfig struct {
	// Enabled gates the OpenAI moderation step entirely. Default false.
	Enabled bool `yaml:"enabled,omitempty"`
	// APIKey is the OpenAI key. Supports {{env:NAME}} substitution.
	APIKey string `yaml:"api_key,omitempty"`
	// Model: chat-completions model id; default "gpt-5.4-nano" to
	// match the existing tlaloc backend classifier.
	Model string `yaml:"model,omitempty"`
	// TimeoutMS bounds the OpenAI call so a slow API doesn't block
	// /chat/start. Default 3000ms.
	TimeoutMS int `yaml:"timeout_ms,omitempty"`
	// OnError selects behaviour when the OpenAI call fails or
	// times out: "allow" (default) or "deny". Allowed errors don't
	// block real customers when moderation is unavailable.
	OnError string `yaml:"on_error,omitempty"`
}
