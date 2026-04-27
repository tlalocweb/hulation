// Package realtime is the in-process pub/sub for the Phase-5a
// mobile realtime WebSocket feed. The visitor-tracking ingest hook
// calls `Broadcast(evt)` whenever an event is accepted; every live
// WebSocket connection has a bounded buffered channel that
// Broadcast pushes into.
//
// Slow clients: when a subscriber's buffer is full, Broadcast drops
// the message for that subscriber (not the sender). A client that
// can't keep up loses messages rather than blocking the broadcaster.
// This is defensible because the mobile app reconnects and a
// missed-event is never load-bearing.

package realtime

import (
	"sync"
	"time"
)

// Event is the on-wire shape streamed to each subscriber. Matches
// the mobile app's expected VisitorEvent-ish fields; keep it small
// so a push doesn't blow through the 4 KB APNs/FCM limit if the
// mobile client forwards it via notification.
type Event struct {
	ServerID  string    `json:"server_id"`
	Ts        time.Time `json:"ts"`
	VisitorID string    `json:"visitor_id"`
	EventCode int64     `json:"event_code"`
	URL       string    `json:"url"`
	Country   string    `json:"country"`
	Device    string    `json:"device"`
}

// subscriberBufferSize is the per-connection channel depth. Bigger
// numbers tolerate burstier ingest but cost more memory per open
// connection.
const subscriberBufferSize = 32

// Subscriber represents one live WebSocket connection. The reader
// goroutine range-loops over Ch; when the hub decides to kick a slow
// subscriber, it closes Ch and the reader exits.
type Subscriber struct {
	Ch       chan Event
	ServerID string // optional — when set, only events for this server_id fan in
	closed   bool
}

// Hub is the fan-out. Call Subscribe → get a Subscriber → read from
// its channel. Call Broadcast (usually from the ingest hook) to push
// an event to every subscriber whose ServerID matches.
type Hub struct {
	mu     sync.RWMutex
	subs   map[*Subscriber]struct{}
}

// Global is the process-wide hub. Set by server.RunUnified so the
// ingest hook can reach it without a DI graph.
var (
	globalMu sync.RWMutex
	global   *Hub
)

// SetGlobal installs the process-wide hub. Safe to call once per
// process.
func SetGlobal(h *Hub) { globalMu.Lock(); global = h; globalMu.Unlock() }

// Global returns the process-wide hub or nil when unset.
func Global() *Hub { globalMu.RLock(); defer globalMu.RUnlock(); return global }

// New constructs an empty Hub.
func New() *Hub {
	return &Hub{subs: make(map[*Subscriber]struct{})}
}

// Subscribe registers a new subscriber. Pass an empty serverID to
// subscribe to every server's events (admin "firehose" mode).
func (h *Hub) Subscribe(serverID string) *Subscriber {
	s := &Subscriber{
		Ch:       make(chan Event, subscriberBufferSize),
		ServerID: serverID,
	}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel. Safe to
// call multiple times; subsequent closes are no-ops.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
	}
	h.mu.Unlock()
	if !s.closed {
		close(s.Ch)
		s.closed = true
	}
}

// Broadcast fans an event out to every matching subscriber. When a
// subscriber's channel is full, Broadcast drops the message for
// that subscriber (non-blocking send via select with default).
// Returns the count of subscribers that actually received.
func (h *Hub) Broadcast(evt Event) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	delivered := 0
	for s := range h.subs {
		if s.ServerID != "" && s.ServerID != evt.ServerID {
			continue
		}
		select {
		case s.Ch <- evt:
			delivered++
		default:
			// Drop — slow client. The reader goroutine will
			// eventually notice the absence via the keepalive
			// ping/pong timeout in server/mobile_ws.go.
		}
	}
	return delivered
}

// ActiveSubscribers returns the current subscriber count. Useful
// for the /hulastatus gauge.
func (h *Hub) ActiveSubscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
