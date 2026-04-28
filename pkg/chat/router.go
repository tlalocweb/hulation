package chat

// Router holds the per-server "ready agent" pool + the FIFO queue
// of sessions waiting for an agent. One Router per process; the
// agent-control WS handler in server/chat_ws_control.go drives
// register/unregister, and Service.Start fans new sessions into
// Enqueue() to either assign immediately or queue.
//
// Fairness: round-robin via a ring (head pops, push tail). Equal
// distribution across agents without bookkeeping.
//
// Re-routing: 30 s ack timer on every assignment. If the agent
// hasn't opened the per-session WS by then, we clear the
// assignment, move them to the back of the ring, and re-enqueue
// the session at the HEAD of the queue (preserving customer wait
// order).
//
// Restart: in-memory only. Phase 4b's hub is in-process anyway,
// so the cluster-restart story is "single hula process" and the
// Router rebuilds itself from chat_sessions WHERE status IN
// ('queued','assigned') at boot via RecoverFromStore.

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AssignAckTimeout is how long the router waits for an assigned
// agent to call Ack() (i.e. open their per-session WS) before
// re-routing the session to the next ready agent.
const AssignAckTimeout = 30 * time.Second

// AgentSlot is one connected agent. Out is the WS-write channel
// the control WS handler drains; the router pushes
// session_assigned / session_released / queue_snapshot frames to
// it.
type AgentSlot struct {
	Username string
	Out      chan []byte
	JoinedAt time.Time
}

// queuedSession is a session sitting in the FIFO. Assignment
// timers are cancelled when the agent acks or declines.
type queuedSession struct {
	SessionID uuid.UUID
	QueuedAt  time.Time
}

type assignmentPending struct {
	SessionID uuid.UUID
	Agent     string
	Cancel    context.CancelFunc
}

// Router state. Per server_id maps so two-tenant operators get
// independent routing.
type Router struct {
	mu       sync.Mutex
	ready    map[string][]*AgentSlot     // server_id → ready ring (head = next pick)
	queued   map[string][]*queuedSession // server_id → FIFO queue
	pending  map[uuid.UUID]*assignmentPending
	notifyFn func(*AgentSlot, []byte) // optional override for tests; default = non-blocking send
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{
		ready:   make(map[string][]*AgentSlot),
		queued:  make(map[string][]*queuedSession),
		pending: make(map[uuid.UUID]*assignmentPending),
	}
}

// AgentReady adds slot to the ready pool for server_id. Returns
// the (queue, pending-assignments) snapshot the SPA paints on
// first frame. If sessions are already queued, drains them in
// FIFO order onto this agent (default 1; the agent gets one
// session, gets reshuffled to the tail; remaining queued sessions
// wait for the next AgentReady).
func (r *Router) AgentReady(serverID string, slot *AgentSlot) Snapshot {
	if r == nil || slot == nil || serverID == "" {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if slot.JoinedAt.IsZero() {
		slot.JoinedAt = time.Now().UTC()
	}
	// Append to tail. Ready ring is ordered by JoinedAt — the
	// freshest agent is at the back.
	r.ready[serverID] = append(r.ready[serverID], slot)

	// Drain at most one session to this agent so a single
	// reconnecting agent isn't slammed with every backlog. The
	// remaining queued sessions wait for the NEXT AgentReady or
	// for an explicit `take` from a different agent.
	if q := r.queued[serverID]; len(q) > 0 {
		head := q[0]
		r.queued[serverID] = q[1:]
		r.assignLocked(serverID, head.SessionID, slot)
	}
	return r.snapshotLocked(serverID, slot.Username)
}

// AgentGone removes the agent from the ready pool. Any sessions
// they had assigned-but-not-yet-ack-ed get re-queued at the head;
// actively-chatting sessions are unaffected (the per-session WS
// is the source of truth for "actively chatting").
func (r *Router) AgentGone(serverID, username string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pool := r.ready[serverID]
	keep := pool[:0]
	for _, s := range pool {
		if s.Username == username {
			continue
		}
		keep = append(keep, s)
	}
	r.ready[serverID] = keep

	// Walk pending assignments belonging to this agent.
	for sid, p := range r.pending {
		if p.Agent != username {
			continue
		}
		// Cancel the ack timer.
		if p.Cancel != nil {
			p.Cancel()
		}
		delete(r.pending, sid)
		// Re-queue at the head — preserve customer wait order.
		r.queued[serverID] = append([]*queuedSession{{SessionID: sid, QueuedAt: time.Now().UTC()}}, r.queued[serverID]...)
	}
}

// Enqueue pushes sessionID at the tail of the queue. If a ready
// agent is available, assign immediately and return their
// username. Otherwise returns ("", queueDepth).
func (r *Router) Enqueue(serverID string, sessionID uuid.UUID) (assignedTo string, queueDepth int) {
	if r == nil || serverID == "" || sessionID == uuid.Nil {
		return "", 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pool := r.ready[serverID]
	if len(pool) == 0 {
		r.queued[serverID] = append(r.queued[serverID], &queuedSession{
			SessionID: sessionID,
			QueuedAt:  time.Now().UTC(),
		})
		return "", len(r.queued[serverID])
	}
	head := pool[0]
	r.assignLocked(serverID, sessionID, head)
	return head.Username, len(r.queued[serverID])
}

// Ack marks the assignment as picked up — typically called when
// the agent opens the per-session WS. Cancels the re-route timer.
func (r *Router) Ack(serverID, username string, sessionID uuid.UUID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[sessionID]
	if !ok {
		return
	}
	if p.Agent != username {
		return
	}
	if p.Cancel != nil {
		p.Cancel()
	}
	delete(r.pending, sessionID)
}

// Decline re-routes the session to the next ready agent. Caller-
// initiated — the agent clicked "Decline" on the assignment
// banner. The declined-from agent stays in the ready pool but
// gets shuffled to the tail.
func (r *Router) Decline(serverID, username string, sessionID uuid.UUID) (newAssignee string, ok bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, has := r.pending[sessionID]
	if !has || p.Agent != username {
		return "", false
	}
	if p.Cancel != nil {
		p.Cancel()
	}
	delete(r.pending, sessionID)
	// Move declining agent to the tail.
	r.shuffleToTailLocked(serverID, username)
	// Try to find another agent.
	pool := r.ready[serverID]
	if len(pool) == 0 {
		// Re-queue at the head.
		r.queued[serverID] = append([]*queuedSession{{SessionID: sessionID, QueuedAt: time.Now().UTC()}}, r.queued[serverID]...)
		return "", false
	}
	head := pool[0]
	if head.Username == username {
		// Only the declining agent is in the pool — no one
		// else to route to. Re-queue.
		r.queued[serverID] = append([]*queuedSession{{SessionID: sessionID, QueuedAt: time.Now().UTC()}}, r.queued[serverID]...)
		return "", false
	}
	r.assignLocked(serverID, sessionID, head)
	return head.Username, true
}

// QueueSnapshot returns the queued / pending-assignment view for
// serverID. Used by /admin/queue.
func (r *Router) QueueSnapshot(serverID string) Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(serverID, "")
}

// Snapshot is what AgentReady returns to the new agent + what
// /admin/queue surfaces.
type Snapshot struct {
	// Queued is the FIFO; oldest first.
	Queued []QueueEntry
	// Assigned is the set of (session, agent) pairs not yet acked.
	Assigned []AssignmentEntry
	// ReadyAgents is the round-robin order, head first.
	ReadyAgents []string
}

type QueueEntry struct {
	SessionID  uuid.UUID
	QueuedAt   time.Time
	QueuedFor  time.Duration // computed at snapshot time
}

type AssignmentEntry struct {
	SessionID uuid.UUID
	Agent     string
}

func (r *Router) snapshotLocked(serverID, excludeAgent string) Snapshot {
	snap := Snapshot{}
	now := time.Now().UTC()
	for _, q := range r.queued[serverID] {
		snap.Queued = append(snap.Queued, QueueEntry{
			SessionID: q.SessionID,
			QueuedAt:  q.QueuedAt,
			QueuedFor: now.Sub(q.QueuedAt),
		})
	}
	// pending is keyed by session_id; we need to filter to this
	// server. We only have server_id when constructing pending
	// entries from the queue — since assignments are always made
	// from the per-server pool, pending sessions implicitly
	// belong to the same server. But we don't track that
	// directly; the SPA will see assignments matching its own
	// server_id when routing through the per-server snapshot.
	// For safety, we include only assignments whose pending
	// agent is in the per-server ready pool.
	known := make(map[string]struct{}, len(r.ready[serverID]))
	for _, s := range r.ready[serverID] {
		known[s.Username] = struct{}{}
	}
	keys := make([]string, 0, len(r.pending))
	pendingByID := make(map[string]*assignmentPending)
	for sid, p := range r.pending {
		if _, ok := known[p.Agent]; !ok {
			continue
		}
		k := sid.String()
		keys = append(keys, k)
		pendingByID[k] = p
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := pendingByID[k]
		snap.Assigned = append(snap.Assigned, AssignmentEntry{
			SessionID: p.SessionID,
			Agent:     p.Agent,
		})
	}
	for _, s := range r.ready[serverID] {
		if s.Username == excludeAgent {
			continue
		}
		snap.ReadyAgents = append(snap.ReadyAgents, s.Username)
	}
	return snap
}

func (r *Router) assignLocked(serverID string, sessionID uuid.UUID, slot *AgentSlot) {
	// Push slot to the tail of the ring (round-robin).
	r.shuffleToTailLocked(serverID, slot.Username)
	// Cancel any pre-existing assignment for this session.
	if old, ok := r.pending[sessionID]; ok {
		if old.Cancel != nil {
			old.Cancel()
		}
	}
	// Set up the ack timer.
	ctx, cancel := context.WithCancel(context.Background())
	r.pending[sessionID] = &assignmentPending{
		SessionID: sessionID,
		Agent:     slot.Username,
		Cancel:    cancel,
	}
	go r.ackTimer(ctx, serverID, sessionID, slot.Username)

	// Push session_assigned to the agent's control WS. Best-
	// effort — full buffer drops, the WS will close.
	frame, _ := jsonMarshal(map[string]any{
		"type":               "session_assigned",
		"session_id":         sessionID.String(),
		"queued_for_seconds": int(time.Since(slot.JoinedAt).Seconds()),
	})
	r.sendToAgent(slot, frame)
}

func (r *Router) ackTimer(ctx context.Context, serverID string, sessionID uuid.UUID, agent string) {
	t := time.NewTimer(AssignAckTimeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return
	case <-t.C:
		r.mu.Lock()
		defer r.mu.Unlock()
		// Race: the agent may have ack'd between the timer
		// firing and acquiring the lock.
		p, ok := r.pending[sessionID]
		if !ok || p.Agent != agent {
			return
		}
		delete(r.pending, sessionID)
		r.shuffleToTailLocked(serverID, agent)
		// Re-queue at the head.
		r.queued[serverID] = append([]*queuedSession{{SessionID: sessionID, QueuedAt: time.Now().UTC()}}, r.queued[serverID]...)
		// Fire the next round.
		r.tryDrainLocked(serverID)
	}
}

// tryDrainLocked attempts to assign one queued session to an
// available agent. Called when an agent connects or after a
// reroute.
func (r *Router) tryDrainLocked(serverID string) {
	pool := r.ready[serverID]
	queue := r.queued[serverID]
	if len(pool) == 0 || len(queue) == 0 {
		return
	}
	head := queue[0]
	r.queued[serverID] = queue[1:]
	slot := pool[0]
	r.assignLocked(serverID, head.SessionID, slot)
}

func (r *Router) shuffleToTailLocked(serverID, username string) {
	pool := r.ready[serverID]
	idx := -1
	for i, s := range pool {
		if s.Username == username {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	slot := pool[idx]
	pool = append(pool[:idx], pool[idx+1:]...)
	pool = append(pool, slot)
	r.ready[serverID] = pool
}

func (r *Router) sendToAgent(slot *AgentSlot, frame []byte) {
	if r.notifyFn != nil {
		r.notifyFn(slot, frame)
		return
	}
	if slot.Out == nil {
		return
	}
	select {
	case slot.Out <- frame:
	default:
		// Slow / full agent control WS. Drop. The control WS
		// reader will notice and close the connection.
	}
}

// jsonMarshal is a tiny indirection so the test file can stub
// json failures without depending on encoding/json semantics.
var jsonMarshal = func(v any) ([]byte, error) {
	return defaultJSONMarshal(v)
}

// errAlreadyAssigned signals an internal race: the caller's
// assumption that no agent currently holds an assignment was
// wrong. Surfaced through Decline / Ack with the boolean return.
var errAlreadyAssigned = errors.New("chat: already assigned")
