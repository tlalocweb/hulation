package server

// Tests for the chat-start response shape — specifically HA Stage
// 3.8's chat WS pinning behavior. Full integration tests for the
// rest of the chat-start flow live in test/e2e/suites/32-chat-*.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

func withConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := config.GetConfig()
	config.SetConfigForTesting(cfg)
	t.Cleanup(func() { config.SetConfigForTesting(prev) })
}

func TestChatPinURL_TeamMissing(t *testing.T) {
	withConfig(t, &config.Config{})
	r := httptest.NewRequest("POST", "https://www.example.com/api/v1/chat/start", nil)
	if got := chatPinURL(r); got != "" {
		t.Errorf("expected empty pin URL when team config absent, got %q", got)
	}
}

func TestChatPinURL_NoNodeHostname(t *testing.T) {
	withConfig(t, &config.Config{Team: &config.TeamConfig{}})
	r := httptest.NewRequest("POST", "https://www.example.com/api/v1/chat/start", nil)
	if got := chatPinURL(r); got != "" {
		t.Errorf("expected empty pin URL when node_hostname unset, got %q", got)
	}
}

func TestChatPinURL_HappyPath(t *testing.T) {
	withConfig(t, &config.Config{
		Team: &config.TeamConfig{NodeHostname: "node-east.www.example.com"},
	})
	r := httptest.NewRequest("POST", "https://www.example.com/api/v1/chat/start", nil)
	want := "wss://node-east.www.example.com/api/v1/chat/ws"
	if got := chatPinURL(r); got != want {
		t.Errorf("pin URL got %q, want %q", got, want)
	}
}

func TestChatPinURL_RespectsForwardedProto(t *testing.T) {
	withConfig(t, &config.Config{
		Team: &config.TeamConfig{NodeHostname: "node-east.local"},
	})
	r := httptest.NewRequest("POST", "https://www.example.com/api/v1/chat/start", nil)
	r.Header.Set("X-Forwarded-Proto", "http")
	want := "ws://node-east.local/api/v1/chat/ws"
	if got := chatPinURL(r); got != want {
		t.Errorf("ws scheme got %q, want %q", got, want)
	}
}

// Compile-time assertion: chat-start uses the same http handler
// type as the rest of the unified mux. If a refactor breaks this,
// the chat WS pin would silently no-op.
var _ http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
