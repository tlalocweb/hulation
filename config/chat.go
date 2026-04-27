package config

// ChatConfig holds Phase-4b visitor-chat tunables. All fields
// optional. Sensible defaults are applied at boot:
//
//   retention_days: 365
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
