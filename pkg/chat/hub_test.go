package chat

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newSub(role Role, agentID string) *Subscriber {
	return &Subscriber{
		Out:     make(chan []byte, 4),
		Role:    role,
		AgentID: agentID,
	}
}

func TestHubSubscribePresenceSnapshot(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	visitor := newSub(RoleVisitor, "")
	a1 := newSub(RoleAgent, "alice")
	a2 := newSub(RoleAgent, "bob")
	a1.JoinedAt = time.Now().Add(-2 * time.Minute)
	a2.JoinedAt = time.Now().Add(-1 * time.Minute)

	h.Subscribe(sid, visitor)
	h.Subscribe(sid, a1)
	snap := h.Subscribe(sid, a2)
	if !snap.VisitorOnline {
		t.Error("snapshot should reflect visitor online")
	}
	if !reflect.DeepEqual(snap.Agents, []string{"alice"}) {
		t.Errorf("snapshot agents: want [alice] (excluding self), got %v", snap.Agents)
	}
}

func TestHubPublishFanout(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	v := newSub(RoleVisitor, "")
	a := newSub(RoleAgent, "alice")
	h.Subscribe(sid, v)
	h.Subscribe(sid, a)
	h.Publish(sid, []byte("hi"), nil)

	for _, s := range []*Subscriber{v, a} {
		select {
		case got := <-s.Out:
			if string(got) != "hi" {
				t.Errorf("unexpected frame: %q", got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber did not receive frame")
		}
	}
}

func TestHubPublishExcludesSender(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	v := newSub(RoleVisitor, "")
	a := newSub(RoleAgent, "alice")
	h.Subscribe(sid, v)
	h.Subscribe(sid, a)
	h.Publish(sid, []byte("typing"), v)

	// v should not see the frame.
	select {
	case got := <-v.Out:
		t.Errorf("excluded sender got frame: %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	// a should.
	select {
	case got := <-a.Out:
		if string(got) != "typing" {
			t.Errorf("unexpected: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not receive")
	}
}

func TestHubSlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	s := &Subscriber{Role: RoleAgent, AgentID: "slow", Out: make(chan []byte, 1)}
	h.Subscribe(sid, s)
	// First send fits.
	h.Publish(sid, []byte("a"), nil)
	// Second send overflows the buffer; hub drops, doesn't block.
	done := make(chan struct{})
	go func() {
		h.Publish(sid, []byte("b"), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Publish blocked on slow subscriber")
	}
	// Drain to clean up.
	<-s.Out
}

func TestHubUnsubscribeRemovesEmptyMaps(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	s := newSub(RoleVisitor, "")
	h.Subscribe(sid, s)
	h.Unsubscribe(sid, s)
	h.mu.RLock()
	defer h.mu.RUnlock()
	if _, ok := h.sessions[sid]; ok {
		t.Error("empty session map should be GC'd from h.sessions")
	}
}

func TestHubAgentsForOrdering(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	now := time.Now()
	a1 := &Subscriber{Out: make(chan []byte, 1), Role: RoleAgent, AgentID: "second", JoinedAt: now.Add(-time.Minute)}
	a2 := &Subscriber{Out: make(chan []byte, 1), Role: RoleAgent, AgentID: "first", JoinedAt: now.Add(-2 * time.Minute)}
	h.Subscribe(sid, a1)
	h.Subscribe(sid, a2)
	got := h.AgentsFor(sid)
	if !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Errorf("ordering: want [first second], got %v", got)
	}
}

func TestHubVisitorOnline(t *testing.T) {
	h := NewHub()
	sid := uuid.New()
	if h.VisitorOnline(sid) {
		t.Error("empty session: should be false")
	}
	v := newSub(RoleVisitor, "")
	h.Subscribe(sid, v)
	if !h.VisitorOnline(sid) {
		t.Error("after Subscribe: should be true")
	}
	h.Unsubscribe(sid, v)
	if h.VisitorOnline(sid) {
		t.Error("after Unsubscribe: should be false")
	}
}

func TestNilHubSafe(t *testing.T) {
	var h *Hub
	h.Publish(uuid.New(), []byte("x"), nil)
	h.Subscribe(uuid.New(), nil)
	h.Unsubscribe(uuid.New(), nil)
	if h.AgentsFor(uuid.New()) != nil {
		t.Error("nil hub AgentsFor should be nil")
	}
	if h.VisitorOnline(uuid.New()) {
		t.Error("nil hub VisitorOnline should be false")
	}
}
