package config

// Tests for the chat resume/idle knobs. These are nil-safe by design — a
// deployment with no `chat:` block at all still gets working defaults — and
// "0" must mean "disabled" for idle_timeout while an unparseable value falls
// back rather than failing the boot.

import (
	"testing"
	"time"
)

func TestChatDurationDefaultsWhenUnset(t *testing.T) {
	// A nil *ChatConfig is the no-`chat:`-block case and must not panic.
	var nilCfg *ChatConfig
	if got := nilCfg.ResolveResumeWindow(); got != DefaultChatResumeWindow {
		t.Errorf("nil resume window = %s, want %s", got, DefaultChatResumeWindow)
	}
	if got := nilCfg.ResolveIdleTimeout(); got != DefaultChatIdleTimeout {
		t.Errorf("nil idle timeout = %s, want %s", got, DefaultChatIdleTimeout)
	}
	if got := nilCfg.ResolveSweepInterval(); got != DefaultChatSweepInterval {
		t.Errorf("nil sweep interval = %s, want %s", got, DefaultChatSweepInterval)
	}
	if got := nilCfg.ResolveHistoryLimit(); got != DefaultChatHistoryLimit {
		t.Errorf("nil history limit = %d, want %d", got, DefaultChatHistoryLimit)
	}

	// An empty block behaves identically.
	empty := &ChatConfig{}
	if got := empty.ResolveResumeWindow(); got != DefaultChatResumeWindow {
		t.Errorf("empty resume window = %s, want %s", got, DefaultChatResumeWindow)
	}
	if got := empty.ResolveIdleTimeout(); got != DefaultChatIdleTimeout {
		t.Errorf("empty idle timeout = %s, want %s", got, DefaultChatIdleTimeout)
	}
}

func TestChatDurationsHonourConfiguredValues(t *testing.T) {
	c := &ChatConfig{
		ResumeWindow:  "72h",
		IdleTimeout:   "30m",
		SweepInterval: "90s",
		HistoryLimit:  50,
	}
	if got := c.ResolveResumeWindow(); got != 72*time.Hour {
		t.Errorf("resume window = %s, want 72h", got)
	}
	if got := c.ResolveIdleTimeout(); got != 30*time.Minute {
		t.Errorf("idle timeout = %s, want 30m", got)
	}
	if got := c.ResolveSweepInterval(); got != 90*time.Second {
		t.Errorf("sweep interval = %s, want 90s", got)
	}
	if got := c.ResolveHistoryLimit(); got != 50 {
		t.Errorf("history limit = %d, want 50", got)
	}
}

func TestChatIdleTimeoutZeroDisablesSweeper(t *testing.T) {
	// "0" is the documented kill-switch for auto-close and must survive as 0,
	// NOT silently fall back to the 2h default — otherwise an operator who
	// turned sweeping off would still have their sessions closed.
	for _, v := range []string{"0", "0s", "-1s"} {
		if got := (&ChatConfig{IdleTimeout: v}).ResolveIdleTimeout(); got != 0 {
			t.Errorf("idle_timeout %q = %s, want 0 (disabled)", v, got)
		}
	}
}

func TestChatBadDurationsFallBackNotFatal(t *testing.T) {
	// A typo must degrade to the default rather than take the server down.
	c := &ChatConfig{ResumeWindow: "24 hours", IdleTimeout: "banana", SweepInterval: "?"}
	if got := c.ResolveResumeWindow(); got != DefaultChatResumeWindow {
		t.Errorf("bad resume window = %s, want default %s", got, DefaultChatResumeWindow)
	}
	if got := c.ResolveIdleTimeout(); got != DefaultChatIdleTimeout {
		t.Errorf("bad idle timeout = %s, want default %s", got, DefaultChatIdleTimeout)
	}
	if got := c.ResolveSweepInterval(); got != DefaultChatSweepInterval {
		t.Errorf("bad sweep interval = %s, want default %s", got, DefaultChatSweepInterval)
	}
}

func TestChatResumeWindowRejectsNonPositive(t *testing.T) {
	// Unlike idle_timeout, a zero resume window has no useful meaning (it
	// would make every stored session instantly unresumable, i.e. exactly the
	// bug this feature fixes), so it falls back to the default.
	if got := (&ChatConfig{ResumeWindow: "0"}).ResolveResumeWindow(); got != DefaultChatResumeWindow {
		t.Errorf("zero resume window = %s, want default %s", got, DefaultChatResumeWindow)
	}
}
