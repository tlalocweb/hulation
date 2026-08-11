package handler

// End-to-end test for chat-widget theming: config → template vars → the CSS
// bytes a browser actually receives.
//
// The unit tests in config/ cover the resolution and validation rules. What
// they can't catch is the wiring: a var named in the stylesheet but absent
// from the map renders as EMPTY, producing `--hc-accent: ;` — a silently
// broken declaration that still returns HTTP 200. So this renders the real
// embedded asset and asserts on the output.

import (
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

const chatCSSAsset = "styles/hula-chat.css"

func renderChatCSS(t *testing.T, srv *config.Server, cfg *config.Config) string {
	t.Helper()
	out, err := renderBuiltinAsset(chatCSSAsset, buildBuiltinVarsFromConfig(srv, cfg))
	if err != nil {
		t.Fatalf("render %s: %v", chatCSSAsset, err)
	}
	return string(out)
}

func TestChatCSSRendersDefaultThemeWhenUnconfigured(t *testing.T) {
	css := renderChatCSS(t, &config.Server{ID: "s1"}, &config.Config{})

	// An install that themes nothing must look exactly as it did before
	// theming existed.
	if !strings.Contains(css, "--hc-accent: "+config.DefaultChatAccent+";") {
		t.Errorf("default accent missing; got:\n%s", firstLines(css, 40))
	}
	if !strings.Contains(css, "--hc-header-bg: "+config.DefaultChatHeaderBG+";") {
		t.Error("default header background missing")
	}
	if !strings.Contains(css, "--hc-header-fg: "+config.DefaultChatHeaderText+";") {
		t.Error("default header text missing")
	}
}

func TestChatCSSAppliesPerVhostTheme(t *testing.T) {
	srv := &config.Server{
		ID:        "tlaloc",
		ChatTheme: &config.ChatThemeConfig{Accent: "#0f6fff"},
	}
	cfg := &config.Config{
		Chat: &config.ChatConfig{
			Theme: &config.ChatThemeConfig{
				Accent:           "#00ff00",
				HeaderBackground: "#101010",
			},
		},
	}
	css := renderChatCSS(t, srv, cfg)

	if !strings.Contains(css, "--hc-accent: #0f6fff;") {
		t.Error("per-vhost accent did not win over the installation-wide theme")
	}
	if !strings.Contains(css, "--hc-header-bg: #101010;") {
		t.Error("unset vhost field did not inherit the installation-wide value")
	}
	if !strings.Contains(css, "--hc-header-fg: "+config.DefaultChatHeaderText+";") {
		t.Error("field unset at both levels did not fall back to the built-in default")
	}
}

// The wiring failure this file exists for: an unsubstituted or empty variable
// yields `--hc-accent: ;`, which is invalid CSS that no status code reveals.
func TestChatCSSLeavesNoUnrenderedOrEmptyVars(t *testing.T) {
	css := renderChatCSS(t, &config.Server{ID: "s1"}, &config.Config{})

	if strings.Contains(css, "{{") || strings.Contains(css, "}}") {
		t.Error("stylesheet contains unrendered mustache tags")
	}
	for _, prop := range []string{"--hc-accent", "--hc-header-bg", "--hc-header-fg"} {
		if strings.Contains(css, prop+": ;") {
			t.Errorf("%s rendered empty — the template var is not wired into the vars map", prop)
		}
	}
}

// A rejected colour must never reach the stylesheet, even end-to-end.
func TestChatCSSDropsInvalidColorToDefault(t *testing.T) {
	srv := &config.Server{
		ID:        "s1",
		ChatTheme: &config.ChatThemeConfig{Accent: "red; } body { display: none"},
	}
	css := renderChatCSS(t, srv, &config.Config{})

	// Assert on markers unique to the injection. NOT on "display: none" —
	// the stylesheet legitimately contains that for its [hidden] rules, so
	// matching it just finds our own CSS and fails for the wrong reason.
	if strings.Contains(css, "body {") || strings.Contains(css, "body{") {
		t.Error("injected `body` selector reached the rendered stylesheet")
	}
	if strings.Contains(css, "--hc-accent: red") {
		t.Error("injected value was emitted into the custom property")
	}
	if !strings.Contains(css, "--hc-accent: "+config.DefaultChatAccent+";") {
		t.Error("invalid colour did not fall back to the default")
	}
}

// Dark-mode rules use #f9fafb/#111827 as panel colours, NOT header colours.
// Binding them to the header variables would make a dark header colour render
// unreadable text across the whole panel.
func TestChatCSSDarkModeIsNotBoundToHeaderVars(t *testing.T) {
	srv := &config.Server{
		ID: "s1",
		ChatTheme: &config.ChatThemeConfig{
			HeaderBackground: "#0f6fff",
			HeaderText:       "#123456",
		},
	}
	css := renderChatCSS(t, srv, &config.Config{})

	i := strings.Index(css, "@media (prefers-color-scheme: dark)")
	if i < 0 {
		t.Fatal("dark-mode block not found in the stylesheet")
	}
	if dark := css[i:]; strings.Contains(dark, "var(--hc-header-fg") || strings.Contains(dark, "var(--hc-header-bg") {
		t.Error("dark-mode rules reference header theme vars; they should use their own literals")
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
