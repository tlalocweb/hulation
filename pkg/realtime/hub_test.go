package realtime

import (
	"testing"
	"time"
)

func TestSubscribeAndBroadcast(t *testing.T) {
	h := New()
	a := h.Subscribe("server-1")
	b := h.Subscribe("") // firehose
	defer h.Unsubscribe(a)
	defer h.Unsubscribe(b)

	h.Broadcast(Event{ServerID: "server-1", Ts: time.Now(), VisitorID: "v1"})
	// subscriber b is firehose; should also receive.
	h.Broadcast(Event{ServerID: "server-2", Ts: time.Now(), VisitorID: "v2"})

	select {
	case got := <-a.Ch:
		if got.VisitorID != "v1" {
			t.Fatalf("a got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("a timed out waiting for event")
	}
	if len(b.Ch) != 2 {
		t.Fatalf("b firehose expected 2 events, got %d", len(b.Ch))
	}
}

func TestSlowClientGetsDropped(t *testing.T) {
	h := New()
	s := h.Subscribe("")
	defer h.Unsubscribe(s)
	// Over-fill the buffer — Broadcast drops rather than blocks.
	for i := 0; i < subscriberBufferSize*2; i++ {
		h.Broadcast(Event{ServerID: "x"})
	}
	if len(s.Ch) > subscriberBufferSize {
		t.Fatalf("buffer over-filled: %d > %d", len(s.Ch), subscriberBufferSize)
	}
}

func TestUnsubscribeIdempotent(t *testing.T) {
	h := New()
	s := h.Subscribe("")
	h.Unsubscribe(s)
	// Second unsubscribe should not panic.
	h.Unsubscribe(s)
}
