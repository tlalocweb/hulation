package notifier

import (
	"context"
	"errors"
	"testing"
)

type stubBackend struct {
	channel Channel
	reply   []ChannelResult
	err     error
}

func (s *stubBackend) Channel() Channel { return s.channel }
func (s *stubBackend) Deliver(ctx context.Context, env Envelope) ([]ChannelResult, error) {
	return s.reply, s.err
}

func TestComposite_FansOut(t *testing.T) {
	emailOK := &stubBackend{channel: ChannelEmail, reply: []ChannelResult{{Channel: ChannelEmail, OK: true}}}
	apnsOK := &stubBackend{channel: ChannelAPNS, reply: []ChannelResult{{Channel: ChannelAPNS, OK: true}}}
	c := NewComposite(emailOK, apnsOK)

	rep, err := c.Deliver(context.Background(), Envelope{Subject: "t"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(rep.Results))
	}
	if !rep.AnyOK() {
		t.Fatal("AnyOK should be true")
	}
	if !rep.AllConfigured() {
		t.Fatal("AllConfigured should be true")
	}
}

func TestComposite_DeadTokenSurface(t *testing.T) {
	dead := &stubBackend{
		channel: ChannelAPNS,
		reply: []ChannelResult{{
			Channel:  ChannelAPNS,
			DeviceID: "dev-1",
			OK:       false,
			Err:      &DeadTokenError{DeviceID: "dev-1", Wrapped: errors.New("410")},
		}},
	}
	c := NewComposite(dead)
	rep, err := c.Deliver(context.Background(), Envelope{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(rep.Results))
	}
	if rep.AnyOK() {
		t.Fatal("AnyOK should be false when all failed")
	}
	if !errors.Is(rep.Results[0].Err, ErrDeadToken) {
		t.Fatalf("want ErrDeadToken, got %v", rep.Results[0].Err)
	}
}

func TestComposite_AllConfiguredFalse(t *testing.T) {
	nope := &stubBackend{
		channel: ChannelFCM,
		reply: []ChannelResult{{
			Channel: ChannelFCM,
			OK:      false,
			Err:     ErrNotConfigured,
		}},
	}
	c := NewComposite(nope)
	rep, _ := c.Deliver(context.Background(), Envelope{})
	if rep.AllConfigured() {
		t.Fatal("AllConfigured should be false when any result is ErrNotConfigured")
	}
}
