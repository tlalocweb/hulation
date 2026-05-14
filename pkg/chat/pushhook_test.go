package chat

import (
	"testing"
)

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
