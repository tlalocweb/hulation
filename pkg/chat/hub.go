package chat

// In-process pub/sub for chat sessions. One Hub per hula process;
// each chat session has its own subscriber set.
//
// Visitor and agent both connect as Subscribers with different
// Roles (RoleVisitor / RoleAgent). The hub fans every Publish'd
// frame to every subscriber on that session, optionally excluding
// the originator (so a sender doesn't see their own typing
// indicator echoed). Slow subscribers (full Out channel) get the
// drop — same policy as pkg/realtime.Hub. The reader goroutine
// will notice the dropped frames and close the WS soon after.
//
// Multi-process broadcast is OUT OF SCOPE for 4b. The hub is
// in-memory; if hula scales horizontally without a Redis/NATS
// fan-out, sessions on process A won't see messages posted to
// process B. Boot fails loud (server/chat_boot.go) when
// HULA_CHAT_MULTIPROC=1 is set without a backend, so operators
// can't accidentally split sessions.

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Role is the subscriber kind.
type Role int

const (
	RoleVisitor Role = iota
	RoleAgent
)

func (r Role) String() string {
	switch r {
	case RoleVisitor:
		return "visitor"
	case RoleAgent:
		return "agent"
	}
	return "unknown"
}

// SubscriberOutBufferSize is the number of frames each subscriber
// can be behind before the hub drops further frames. 32 = a few
// kB at most given chat frames are tiny; large enough that a brief
// network hiccup doesn't immediately drop, small enough that a
// truly stuck reader gets disconnected.
const SubscriberOutBufferSize = 32

// Subscriber is one connected WS endpoint. The WS handler creates
// it, calls Hub.Subscribe, then drains Out in its writer goroutine.
type Subscriber struct {
	// Out is the per-subscriber outbound queue. The hub does a
	// non-blocking send; full → drop the frame.
	Out chan []byte
	// Role is RoleVisitor (1 per session, max) or RoleAgent (N).
	Role Role
	// AgentID is the admin username; empty for RoleVisitor.
	AgentID string
	// JoinedAt is set by Subscribe; used for presence snapshot
	// ordering.
	JoinedAt time.Time
}

// PresenceSnapshot is the state Hub.Subscribe returns to the
// caller so the new subscriber can render the current room before
// any presence frames arrive.
type PresenceSnapshot struct {
	VisitorOnline bool
	Agents        []string // sorted by JoinedAt ascending; sender's own id excluded
}

// Hub is the per-process session pub/sub. Construct with NewHub.
type Hub struct {
	mu       sync.RWMutex
	sessions map[uuid.UUID]map[*Subscriber]struct{}
}

// NewHub returns an empty Hub. Tests use it directly; the runtime
// holds one shared Hub per process.
func NewHub() *Hub {
	return &Hub{sessions: make(map[uuid.UUID]map[*Subscriber]struct{})}
}

// Subscribe registers s on sessionID. Returns a snapshot of who
// else is in the room so the caller can write a presence frame to
// the new subscriber before the first real frame arrives. Calling
// Subscribe twice on the same Subscriber is a no-op (the second
// add into the same map slot is idempotent).
func (h *Hub) Subscribe(sessionID uuid.UUID, s *Subscriber) PresenceSnapshot {
	if h == nil || s == nil {
		return PresenceSnapshot{}
	}
	if s.Out == nil {
		s.Out = make(chan []byte, SubscriberOutBufferSize)
	}
	if s.JoinedAt.IsZero() {
		s.JoinedAt = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.sessions[sessionID]
	if subs == nil {
		subs = make(map[*Subscriber]struct{})
		h.sessions[sessionID] = subs
	}
	subs[s] = struct{}{}
	// Build the snapshot WITHOUT including the new subscriber.
	snap := PresenceSnapshot{}
	type ag struct {
		id string
		t  time.Time
	}
	var agents []ag
	for other := range subs {
		if other == s {
			continue
		}
		switch other.Role {
		case RoleVisitor:
			snap.VisitorOnline = true
		case RoleAgent:
			agents = append(agents, ag{id: other.AgentID, t: other.JoinedAt})
		}
	}
	// Stable order by JoinedAt — handy for the SPA's "agents"
	// presence chip.
	for i := 1; i < len(agents); i++ {
		for j := i; j > 0 && agents[j-1].t.After(agents[j].t); j-- {
			agents[j-1], agents[j] = agents[j], agents[j-1]
		}
	}
	for _, a := range agents {
		snap.Agents = append(snap.Agents, a.id)
	}
	return snap
}

// Unsubscribe removes s from sessionID. Idempotent. The caller
// also closes s.Out — Unsubscribe doesn't, since closing a
// channel that another goroutine is sending on would panic, and
// the only other sender is the hub itself which holds the same
// lock the unregister takes.
func (h *Hub) Unsubscribe(sessionID uuid.UUID, s *Subscriber) {
	if h == nil || s == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.sessions[sessionID]
	if subs == nil {
		return
	}
	delete(subs, s)
	if len(subs) == 0 {
		delete(h.sessions, sessionID)
	}
}

// Publish fans frame to every subscriber on sessionID. exclude
// (may be nil) is skipped — typing indicators use this so a
// sender doesn't see their own frame echoed back, which would
// flicker the dots.
func (h *Hub) Publish(sessionID uuid.UUID, frame []byte, exclude *Subscriber) {
	if h == nil || len(frame) == 0 {
		return
	}
	h.mu.RLock()
	subs := make([]*Subscriber, 0, len(h.sessions[sessionID]))
	for s := range h.sessions[sessionID] {
		if s == exclude {
			continue
		}
		subs = append(subs, s)
	}
	h.mu.RUnlock()
	for _, s := range subs {
		select {
		case s.Out <- frame:
		default:
			// Slow subscriber. Drop the frame; the WS reader
			// goroutine will notice (via timeout / write
			// deadline / next ping) and close the connection.
		}
	}
}

// AgentsFor returns the sorted set of agent usernames currently
// subscribed to sessionID. Used by chatimpl's GetLiveSessions.
func (h *Hub) AgentsFor(sessionID uuid.UUID) []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs := h.sessions[sessionID]
	if subs == nil {
		return nil
	}
	type ag struct {
		id string
		t  time.Time
	}
	var agents []ag
	for s := range subs {
		if s.Role == RoleAgent {
			agents = append(agents, ag{s.AgentID, s.JoinedAt})
		}
	}
	for i := 1; i < len(agents); i++ {
		for j := i; j > 0 && agents[j-1].t.After(agents[j].t); j-- {
			agents[j-1], agents[j] = agents[j], agents[j-1]
		}
	}
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.id)
	}
	return out
}

// VisitorOnline reports whether at least one visitor WS is open
// against sessionID. Multi-visitor isn't allowed by the WS
// handler (re-opening the same chat token kicks the old socket),
// so this is effectively boolean.
func (h *Hub) VisitorOnline(sessionID uuid.UUID) bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	subs := h.sessions[sessionID]
	if subs == nil {
		return false
	}
	for s := range subs {
		if s.Role == RoleVisitor {
			return true
		}
	}
	return false
}

// VisitorSubscriber returns the active visitor subscriber for
// sessionID, or nil. Used by the visitor WS handler to kick a
// stale visitor connection when the same chat token is presented
// twice (re-open after disconnect).
func (h *Hub) VisitorSubscriber(sessionID uuid.UUID) *Subscriber {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.sessions[sessionID] {
		if s.Role == RoleVisitor {
			return s
		}
	}
	return nil
}
