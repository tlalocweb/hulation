package config

// Tests for chat-widget theming.
//
// Two properties matter here beyond "the value comes through":
//
//  1. The fallback chain (vhost → installation → built-in default) must be
//     nil-safe at every level, because an install that themes nothing must
//     render exactly as it did before theming existed.
//  2. Colours are interpolated into a stylesheet the browser executes, so a
//     malformed value must be REJECTED rather than emitted. A value that can
//     close the declaration could append arbitrary rules.

import (
	"strings"
	"testing"
)

func TestChatThemeDefaultsWhenUnset(t *testing.T) {
	var nilTheme *ChatThemeConfig
	if got := nilTheme.ResolveAccent(nil); got != DefaultChatAccent {
		t.Errorf("nil accent = %q, want %q", got, DefaultChatAccent)
	}
	if got := nilTheme.ResolveHeaderBackground(nil); got != DefaultChatHeaderBG {
		t.Errorf("nil header bg = %q, want %q", got, DefaultChatHeaderBG)
	}
	if got := nilTheme.ResolveHeaderText(nil); got != DefaultChatHeaderText {
		t.Errorf("nil header text = %q, want %q", got, DefaultChatHeaderText)
	}
	// An empty (materialised) struct behaves like nil.
	empty := &ChatThemeConfig{}
	if got := empty.ResolveAccent(nil); got != DefaultChatAccent {
		t.Errorf("empty accent = %q, want default", got)
	}
	// And a nil *ChatConfig yields a nil global theme without panicking.
	var nilChat *ChatConfig
	if nilChat.ChatTheme() != nil {
		t.Error("nil ChatConfig should yield a nil theme")
	}
}

func TestChatThemeVhostOverridesGlobal(t *testing.T) {
	global := &ChatThemeConfig{Accent: "#00ff00", HeaderBackground: "#010101", HeaderText: "#020202"}
	vhost := &ChatThemeConfig{Accent: "#0f6fff"}

	// The vhost sets only accent, so it wins there and inherits the rest —
	// this is what a multi-brand install depends on.
	if got := vhost.ResolveAccent(global); got != "#0f6fff" {
		t.Errorf("accent = %q, want the vhost value #0f6fff", got)
	}
	if got := vhost.ResolveHeaderBackground(global); got != "#010101" {
		t.Errorf("header bg = %q, want the inherited global #010101", got)
	}
	if got := vhost.ResolveHeaderText(global); got != "#020202" {
		t.Errorf("header text = %q, want the inherited global #020202", got)
	}

	// With no vhost theme at all, the global applies wholesale.
	var noVhost *ChatThemeConfig
	if got := noVhost.ResolveAccent(global); got != "#00ff00" {
		t.Errorf("accent with no vhost theme = %q, want global #00ff00", got)
	}
}

func TestChatThemeAcceptsValidColorSyntaxes(t *testing.T) {
	for _, v := range []string{
		"#abc", "#AABBCC", "#aabbccdd", "rebeccapurple", "red",
		"rgb(15, 111, 255)", "rgba(15,111,255,0.5)", "hsl(210, 100%, 53%)",
	} {
		got := (&ChatThemeConfig{Accent: v}).ResolveAccent(nil)
		if got != v {
			t.Errorf("valid colour %q was rejected (got %q)", v, got)
		}
	}
}

// The important one: anything that could escape the CSS declaration must fall
// back to the default rather than reach the stylesheet.
func TestChatThemeRejectsCSSInjection(t *testing.T) {
	for _, v := range []string{
		"red; } body { display: none",         // closes the rule, appends another
		"#fff; background-image: url(x)",      // extra declaration
		"url(javascript:alert(1))",            // not a colour at all
		"expression(alert(1))",                // legacy IE expression
		"#12",                                 // too short to be a hex colour
		"#gggggg",                             // non-hex characters
		"</style><script>alert(1)</script>",   // tries to break out of the element
		"var(--x)",                            // indirection we don't intend to allow
	} {
		if got := (&ChatThemeConfig{Accent: v}).ResolveAccent(nil); got != DefaultChatAccent {
			t.Errorf("injection %q was NOT rejected — emitted %q", v, got)
		}
		if got := (&ChatThemeConfig{HeaderBackground: v}).ResolveHeaderBackground(nil); got != DefaultChatHeaderBG {
			t.Errorf("injection %q via header_background was NOT rejected — emitted %q", v, got)
		}
		if got := (&ChatThemeConfig{HeaderText: v}).ResolveHeaderText(nil); got != DefaultChatHeaderText {
			t.Errorf("injection %q via header_text was NOT rejected — emitted %q", v, got)
		}
	}
}

func TestChatThemeTrimsWhitespace(t *testing.T) {
	if got := (&ChatThemeConfig{Accent: "  #0f6fff  "}).ResolveAccent(nil); got != "#0f6fff" {
		t.Errorf("whitespace not trimmed: %q", got)
	}
	// Whitespace-only is treated as unset, not as an invalid colour.
	if got := (&ChatThemeConfig{Accent: "   "}).ResolveAccent(nil); got != DefaultChatAccent {
		t.Errorf("blank accent = %q, want default", got)
	}
}

// --- panel surfaces -------------------------------------------------------

func TestChatSurfacesDefaultsIncludeDarkPalette(t *testing.T) {
	var nilTheme *ChatThemeConfig
	s := nilTheme.ResolveSurfaces(nil)

	if s.Background != DefaultChatBackground || s.Text != DefaultChatText {
		t.Errorf("light defaults wrong: bg=%q text=%q", s.Background, s.Text)
	}
	// Nothing configured, so dark mode must still get hula's dark palette —
	// otherwise an unthemed widget would render white for dark-mode visitors.
	if s.BackgroundDark != DefaultChatBackgroundDark || s.TextDark != DefaultChatTextDark {
		t.Errorf("dark defaults wrong: bg=%q text=%q", s.BackgroundDark, s.TextDark)
	}
	if s.ThreadBackgroundDark != DefaultChatThreadBackgroundDark || s.AgentBubbleDark != DefaultChatAgentBubbleDark {
		t.Errorf("dark thread/bubble wrong: %q / %q", s.ThreadBackgroundDark, s.AgentBubbleDark)
	}
}

// The behaviour chosen for this feature: a configured colour applies in BOTH
// colour schemes, so a themed widget keeps matching its site for visitors
// whose OS prefers dark.
func TestChatSurfacesExplicitConfigWinsOverDarkMode(t *testing.T) {
	theme := &ChatThemeConfig{
		Background:       "#fbf7f0", // gravhl cream
		Text:             "#1f2a26", // gravhl ink
		ThreadBackground: "#f5efe3",
		AgentBubble:      "#ede8f5",
	}
	s := theme.ResolveSurfaces(nil)

	if s.Background != "#fbf7f0" || s.BackgroundDark != "#fbf7f0" {
		t.Errorf("configured background must apply in both schemes: light=%q dark=%q", s.Background, s.BackgroundDark)
	}
	if s.Text != "#1f2a26" || s.TextDark != "#1f2a26" {
		t.Errorf("configured text must apply in both schemes: light=%q dark=%q", s.Text, s.TextDark)
	}
	if s.ThreadBackground != "#f5efe3" || s.ThreadBackgroundDark != "#f5efe3" {
		t.Error("configured thread background must apply in both schemes")
	}
	if s.AgentBubble != "#ede8f5" || s.AgentBubbleDark != "#ede8f5" {
		t.Error("configured agent bubble must apply in both schemes")
	}
}

// Partial configuration must not drag unrelated colours out of dark mode.
func TestChatSurfacesPartialConfigKeepsDarkDefaultsForUnsetFields(t *testing.T) {
	s := (&ChatThemeConfig{Background: "#fbf7f0"}).ResolveSurfaces(nil)

	if s.BackgroundDark != "#fbf7f0" {
		t.Errorf("set field: dark = %q, want the configured value", s.BackgroundDark)
	}
	if s.TextDark != DefaultChatTextDark {
		t.Errorf("unset field: dark text = %q, want hula's dark default", s.TextDark)
	}
	if s.ThreadBackgroundDark != DefaultChatThreadBackgroundDark {
		t.Errorf("unset field: dark thread = %q, want hula's dark default", s.ThreadBackgroundDark)
	}
}

// Inputs follow the panel background rather than being a separate knob.
func TestChatSurfacesInputFollowsBackground(t *testing.T) {
	s := (&ChatThemeConfig{Background: "#fbf7f0"}).ResolveSurfaces(nil)
	if s.InputBackground != "#fbf7f0" || s.InputBackgroundDark != "#fbf7f0" {
		t.Errorf("input should follow a configured background: %q / %q", s.InputBackground, s.InputBackgroundDark)
	}
	// Unconfigured, inputs keep their own light/dark defaults.
	d := (&ChatThemeConfig{}).ResolveSurfaces(nil)
	if d.InputBackground != DefaultChatInputBackground || d.InputBackgroundDark != DefaultChatInputBackgroundDark {
		t.Errorf("unset input defaults wrong: %q / %q", d.InputBackground, d.InputBackgroundDark)
	}
}

func TestChatSurfacesVhostOverridesGlobal(t *testing.T) {
	global := &ChatThemeConfig{Background: "#ffffff", Text: "#000000"}
	vhost := &ChatThemeConfig{Background: "#fbf7f0"}
	s := vhost.ResolveSurfaces(global)

	if s.Background != "#fbf7f0" {
		t.Errorf("vhost background should win: %q", s.Background)
	}
	if s.Text != "#000000" {
		t.Errorf("unset vhost field should inherit global: %q", s.Text)
	}
	// Inherited-from-global still counts as configured for dark mode.
	if s.TextDark != "#000000" {
		t.Errorf("globally-configured text should apply in dark too: %q", s.TextDark)
	}
}

func TestChatSurfacesRejectInjection(t *testing.T) {
	bad := "#fff; } body { display: none"
	s := (&ChatThemeConfig{Background: bad, Text: bad, ThreadBackground: bad, AgentBubble: bad}).ResolveSurfaces(nil)
	for name, got := range map[string]string{
		"Background": s.Background, "Text": s.Text,
		"ThreadBackground": s.ThreadBackground, "AgentBubble": s.AgentBubble,
		"BackgroundDark": s.BackgroundDark, "TextDark": s.TextDark,
	} {
		if strings.Contains(got, "body") {
			t.Errorf("%s emitted an injected value: %q", name, got)
		}
	}
}
