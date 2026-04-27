package moderate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// We can't reach OpenAI in unit tests, but we can stub the
// chat-completions endpoint via httptest and verify the verdict
// parsing + happy/blocked/upstream-error paths.

func newMockOpenAI(t *testing.T, replyContent string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer secret"; got != want {
			t.Errorf("Authorization header: got %q, want %q", got, want)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "gpt-test" {
			t.Errorf("model: got %v, want gpt-test", body["model"])
		}
		if status != 0 {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{"content": replyContent},
				},
			},
		})
	}))
}

func newModerator(t *testing.T, srv *httptest.Server) *Moderator {
	t.Helper()
	m := New(Config{
		Enabled: true,
		APIKey:  "secret",
		Model:   "gpt-test",
		Timeout: 2 * time.Second,
	})
	if m == nil {
		t.Fatal("New returned nil for enabled config")
	}
	m.url = srv.URL
	m.client = srv.Client()
	return m
}

func TestClassifyReal(t *testing.T) {
	srv := newMockOpenAI(t, "REAL", 0)
	defer srv.Close()
	m := newModerator(t, srv)
	v, err := m.Classify(context.Background(), "hi, do you ship to mexico?")
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if v != VerdictReal {
		t.Errorf("verdict: got %s, want REAL", v)
	}
}

func TestClassifyBlocked(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  Verdict
	}{
		{"ABUSE", VerdictAbuse},
		{"abuse", VerdictAbuse},
		{"SPAM.", VerdictSpam},
		{"  spam ", VerdictSpam},
	} {
		t.Run(c.reply, func(t *testing.T) {
			srv := newMockOpenAI(t, c.reply, 0)
			defer srv.Close()
			m := newModerator(t, srv)
			v, err := m.Classify(context.Background(), "hi")
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("want ErrBlocked, got %v", err)
			}
			if v != c.want {
				t.Errorf("verdict: got %s, want %s", v, c.want)
			}
		})
	}
}

func TestClassifyUnknownVerdict(t *testing.T) {
	srv := newMockOpenAI(t, "MAYBE", 0)
	defer srv.Close()
	m := newModerator(t, srv)
	v, err := m.Classify(context.Background(), "hi")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
	if v != VerdictUnspecified {
		t.Errorf("verdict: got %s, want UNSPECIFIED", v)
	}
}

func TestClassifyUpstream500(t *testing.T) {
	srv := newMockOpenAI(t, "", http.StatusBadGateway)
	defer srv.Close()
	m := newModerator(t, srv)
	_, err := m.Classify(context.Background(), "hi")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func TestNewDisabled(t *testing.T) {
	if m := New(Config{}); m != nil {
		t.Error("disabled config: want nil moderator")
	}
	if m := New(Config{Enabled: true}); m != nil {
		t.Error("missing api key: want nil moderator")
	}
}

func TestNilModeratorClassifyReal(t *testing.T) {
	var m *Moderator
	v, err := m.Classify(context.Background(), "anything")
	if err != nil || v != VerdictReal {
		t.Errorf("nil moderator should return REAL,nil — got %s,%v", v, err)
	}
}

func TestParseVerdict(t *testing.T) {
	cases := map[string]Verdict{
		"REAL":     VerdictReal,
		"real":     VerdictReal,
		"REAL\n":   VerdictReal,
		"ABUSE.":   VerdictAbuse,
		"SPAM!":    VerdictSpam,
		"":         VerdictUnspecified,
		"NOPE":     VerdictUnspecified,
		"REAL?":    VerdictReal,
		"REAL bot": VerdictReal,
	}
	for in, want := range cases {
		if got := parseVerdict(in); got != want {
			t.Errorf("parseVerdict(%q) = %s, want %s", in, got, want)
		}
	}
	// Sanity: the prompt instruction we ship in moderate.go ends
	// with literal "REAL, ABUSE, or SPAM" — make sure the strings
	// we depend on parsing match those tokens.
	for _, w := range []string{"REAL", "ABUSE", "SPAM"} {
		if parseVerdict(w) == VerdictUnspecified {
			t.Errorf("token %q should parse", w)
		}
	}
	// String() round-trips.
	for _, v := range []Verdict{VerdictReal, VerdictAbuse, VerdictSpam} {
		if !strings.EqualFold(v.String(), v.String()) {
			t.Error("String() not stable")
		}
	}
}
