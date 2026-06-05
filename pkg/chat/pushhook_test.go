package chat

import (
	"encoding/json"
	"testing"
)

func TestBuildNewChatPreview_HappyPath(t *testing.T) {
	p := buildNewChatPreview(ChatPushInput{
		SessionID:      "abc-123",
		ServerID:       "gravhl.com",
		VisitorID:      "guest-xyz",
		VisitorEmail:   "ed@tlaloc.us",
		VisitorCountry: "Austin, TX",
		FirstMessage:   "Hi — I met you all at the startup event yesterday",
	})
	if p.Title != "ed@tlaloc.us" {
		t.Errorf("title: want ed@tlaloc.us, got %q", p.Title)
	}
	if p.Subtitle != "New chat · Austin, TX" {
		t.Errorf("subtitle: want %q, got %q", "New chat · Austin, TX", p.Subtitle)
	}
	if p.Body != "Hi — I met you all at the startup event yesterday" {
		t.Errorf("body: got %q", p.Body)
	}
	if p.Kind != "chat.new" {
		t.Errorf("kind: got %q", p.Kind)
	}
	if p.SrvID != "gravhl.com" || p.SesID != "abc-123" {
		t.Errorf("ids: got %+v", p)
	}
}

func TestBuildNewChatPreview_TruncatesByRune(t *testing.T) {
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'é' // multi-byte rune; truncation must not split it.
	}
	p := buildNewChatPreview(ChatPushInput{FirstMessage: string(long)})
	if got := []rune(p.Body); len(got) != 178 || got[177] != '…' {
		t.Fatalf("expected 177-rune prefix + ellipsis, got %d runes", len(got))
	}
}

func TestBuildNewChatPreview_JSONShape(t *testing.T) {
	// The on-device NSE / FBMS parses this exact JSON shape; pin the field
	// names so accidental renames are caught.
	b, err := json.Marshal(buildNewChatPreview(ChatPushInput{
		SessionID: "s", ServerID: "srv", VisitorEmail: "v@x", FirstMessage: "hi",
	}))
	if err != nil {
		t.Fatalf("marshal: %s", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %s", err)
	}
	for _, k := range []string{"title", "subtitle", "body", "kind", "server_id", "session_id"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing field %q in marshaled payload: %s", k, b)
		}
	}
}

func TestBuildNewChatEnvelope_HappyPath(t *testing.T) {
	env := BuildNewChatEnvelope(ChatPushInput{
		SessionID:      "abc-123",
		ServerID:       "gravhl.com",
		VisitorID:      "guest-xyz",
		VisitorEmail:   "ed@tlaloc.us",
		VisitorCountry: "Austin, TX",
		FirstMessage:   "Hi — I met you all at the startup event yesterday",
	})

	if env.Subject != "ed@tlaloc.us" {
		t.Errorf("subject: want ed@tlaloc.us, got %q", env.Subject)
	}
	if env.ShortText != "Hi — I met you all at the startup event yesterday" {
		t.Errorf("shorttext: got %q", env.ShortText)
	}

	hula, ok := env.CustomData["hula"].(map[string]any)
	if !ok {
		t.Fatalf("CustomData[hula] missing or wrong type: %T", env.CustomData["hula"])
	}
	checks := map[string]string{
		"kind":            "chat.new",
		"server_id":       "gravhl.com",
		"session_id":      "abc-123",
		"visitor_email":   "ed@tlaloc.us",
		"visitor_country": "Austin, TX",
		"subtitle":        "New chat · Austin, TX",
	}
	for k, want := range checks {
		got, _ := hula[k].(string)
		if got != want {
			t.Errorf("hula[%q]: want %q, got %q", k, want, got)
		}
	}
}

func TestBuildNewChatEnvelope_FallbackTitle(t *testing.T) {
	env := BuildNewChatEnvelope(ChatPushInput{
		SessionID:    "abc",
		ServerID:     "x",
		VisitorID:    "guest-123",
		VisitorEmail: "",
	})
	if env.Subject != "guest-123" {
		t.Errorf("subject: want guest-123, got %q", env.Subject)
	}
}

func TestBuildNewChatEnvelope_UnknownVisitor(t *testing.T) {
	env := BuildNewChatEnvelope(ChatPushInput{
		SessionID: "abc",
		ServerID:  "x",
	})
	if env.Subject != "Anonymous visitor" {
		t.Errorf("subject: want fallback, got %q", env.Subject)
	}
}

func TestBuildNewChatEnvelope_CountryDefault(t *testing.T) {
	env := BuildNewChatEnvelope(ChatPushInput{
		SessionID: "abc",
		ServerID:  "x",
	})
	hula := env.CustomData["hula"].(map[string]any)
	if hula["visitor_country"] != "Unknown" {
		t.Errorf("country fallback: got %q", hula["visitor_country"])
	}
	if hula["subtitle"] != "New chat · Unknown" {
		t.Errorf("subtitle fallback: got %q", hula["subtitle"])
	}
}

func TestBuildNewChatEnvelope_TruncatesLongFirstMessage(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	env := BuildNewChatEnvelope(ChatPushInput{
		SessionID:    "abc",
		ServerID:     "x",
		FirstMessage: string(long),
	})
	if len(env.ShortText) > 180 {
		t.Errorf("shorttext should be capped at 180 chars, got %d", len(env.ShortText))
	}
}
