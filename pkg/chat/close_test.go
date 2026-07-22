package chat

// Pure, in-memory tests for terminal-session enforcement. No ClickHouse
// required — a fakeStore stands in for the real *Store so the closure
// logic (which is what actually enforces "a closed chat is terminal")
// always runs in CI.
//
// Coverage maps to the feature plan:
//   - agent closes while a visitor is connected      → TestCloseSessionBroadcastsToConnectedVisitor
//   - visitor cannot send afterward / nothing stored → TestVisitorCannotSendAfterClose
//   - reconnecting to a closed session is read-only  → TestReconnectToClosedIsReadOnly
//   - starting a new chat is a different live session→ TestStartNewChatIsDistinctSession
//   - idempotent close (no duplicate side effects)   → TestCloseSessionIdempotent
//   - explicit close is distinct from a disconnect   → TestCloseIsDistinctFromDisconnect
//   - close/send race — closure always wins          → TestCloseSendRaceClosureWins

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore is an in-memory stand-in satisfying both closableStore and
// sessionGetter (and AppendMessage for the persist tests). Its
// AppendMessage refuses a terminal session so "nothing post-close is
// persisted" is enforced at the store boundary too — mirroring the
// reload gate the WS handler runs before persisting.
type fakeStore struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]Session
	msgs     []Message
}

func newFakeStore() *fakeStore {
	return &fakeStore{sessions: make(map[uuid.UUID]Session)}
}

func (f *fakeStore) put(s Session) {
	f.mu.Lock()
	f.sessions[s.ID] = s
	f.mu.Unlock()
}

func (f *fakeStore) GetSession(_ context.Context, serverID string, id uuid.UUID) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok || (serverID != "" && s.ServerID != serverID) {
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) MarkSessionClosed(_ context.Context, _ string, id uuid.UUID, _ string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	if IsTerminalStatus(s.Status) {
		return s, nil // idempotent
	}
	now := time.Now().UTC()
	s.Status = StatusClosed
	s.ClosedAt = &now
	f.sessions[id] = s
	return s, nil
}

func (f *fakeStore) AppendMessage(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[m.SessionID]
	if IsTerminalStatus(s.Status) {
		return ErrSessionClosed // never persist to a terminal session
	}
	f.msgs = append(f.msgs, m)
	return nil
}

func (f *fakeStore) msgCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// gatedVisitorSend models exactly what server/chat_ws_visitor.go does
// on an inbound visitor "msg": consult the in-process hub flag, reload
// the source of truth, and only then persist. Returns true iff the
// message was accepted + stored.
func gatedVisitorSend(ctx context.Context, store *fakeStore, hub *Hub, serverID string, id uuid.UUID) bool {
	if hub.IsClosed(id) {
		return false
	}
	if _, err := ReloadOpenSession(ctx, store, serverID, id); err != nil {
		return false
	}
	msg := Message{ID: uuid.New(), SessionID: id, ServerID: serverID, Direction: DirVisitor, Content: "hi", When: time.Now().UTC()}
	return store.AppendMessage(ctx, msg) == nil
}

func openSession(serverID string) Session {
	return Session{ID: uuid.New(), ServerID: serverID, Status: StatusOpen, StartedAt: time.Now().UTC()}
}

func TestSessionClosedFrame(t *testing.T) {
	if got := string(SessionClosedFrame("")); got != `{"type":"session_closed"}` {
		t.Fatalf("no-reason frame = %s", got)
	}
	got := string(SessionClosedFrame("agent done"))
	// Field order in encoding/json map output is sorted by key: reason < type.
	if got != `{"reason":"agent done","type":"session_closed"}` {
		t.Fatalf("reason frame = %s", got)
	}
}

func TestIsTerminalStatus(t *testing.T) {
	terminal := map[string]bool{
		StatusQueued:   false,
		StatusAssigned: false,
		StatusOpen:     false,
		StatusClosed:   true,
		StatusExpired:  true,
		"":             false,
	}
	for st, want := range terminal {
		if got := IsTerminalStatus(st); got != want {
			t.Errorf("IsTerminalStatus(%q) = %v, want %v", st, got, want)
		}
	}
}

func TestReloadOpenSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	open := openSession("acme")
	store.put(open)
	closed := openSession("acme")
	closed.Status = StatusClosed
	store.put(closed)
	expired := openSession("acme")
	expired.Status = StatusExpired
	store.put(expired)

	if _, err := ReloadOpenSession(ctx, store, "acme", open.ID); err != nil {
		t.Errorf("open session should reload cleanly: %v", err)
	}
	if _, err := ReloadOpenSession(ctx, store, "acme", closed.ID); err != ErrSessionClosed {
		t.Errorf("closed → ErrSessionClosed, got %v", err)
	}
	if _, err := ReloadOpenSession(ctx, store, "acme", expired.ID); err != ErrSessionClosed {
		t.Errorf("expired → ErrSessionClosed, got %v", err)
	}
	if _, err := ReloadOpenSession(ctx, store, "acme", uuid.New()); err != ErrNotFound {
		t.Errorf("missing → ErrNotFound, got %v", err)
	}
}

// Agent closes while the visitor is connected: the visitor's hub
// subscriber must receive the authoritative session_closed frame, and
// the session must be marked terminal + flagged closed in-process.
func TestCloseSessionBroadcastsToConnectedVisitor(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)

	visitor := &Subscriber{Out: make(chan []byte, 4), Role: RoleVisitor}
	agent := &Subscriber{Out: make(chan []byte, 4), Role: RoleAgent, AgentID: "alice"}
	hub.Subscribe(s.ID, visitor)
	hub.Subscribe(s.ID, agent)

	sess, transitioned, err := CloseSession(ctx, store, hub, "acme", s.ID, "wrapping up")
	if err != nil || !transitioned {
		t.Fatalf("close: transitioned=%v err=%v", transitioned, err)
	}
	if sess.Status != StatusClosed {
		t.Fatalf("status after close = %q", sess.Status)
	}
	if !hub.IsClosed(s.ID) {
		t.Fatal("hub should flag session closed")
	}
	want := `{"reason":"wrapping up","type":"session_closed"}`
	select {
	case got := <-visitor.Out:
		if string(got) != want {
			t.Fatalf("visitor frame = %s, want %s", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("visitor never received session_closed")
	}
	// The agent that was subscribed also learns the room closed.
	select {
	case got := <-agent.Out:
		if string(got) != want {
			t.Fatalf("agent frame = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("agent never received session_closed")
	}
}

// After a close, the visitor cannot send: the gate rejects and nothing
// is persisted.
func TestVisitorCannotSendAfterClose(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)

	// Before close: a send is accepted.
	if !gatedVisitorSend(ctx, store, hub, "acme", s.ID) {
		t.Fatal("pre-close send should be accepted")
	}
	if store.msgCount() != 1 {
		t.Fatalf("pre-close msg count = %d", store.msgCount())
	}

	if _, _, err := CloseSession(ctx, store, hub, "acme", s.ID, ""); err != nil {
		t.Fatalf("close: %v", err)
	}

	// After close: every send is rejected, transcript frozen.
	for i := 0; i < 5; i++ {
		if gatedVisitorSend(ctx, store, hub, "acme", s.ID) {
			t.Fatal("post-close send must be rejected")
		}
	}
	if store.msgCount() != 1 {
		t.Fatalf("post-close msg count = %d (nothing new should persist)", store.msgCount())
	}
}

// A visitor reconnecting to a closed session is read-only: the
// connect-time terminal check trips and the reload gate rejects sends.
func TestReconnectToClosedIsReadOnly(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)
	if _, _, err := CloseSession(ctx, store, hub, "acme", s.ID, ""); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reconnect: the handler recomputes terminalAtConnect from the row
	// status OR the in-process flag; both say terminal here.
	reloaded, _ := store.GetSession(ctx, "acme", s.ID)
	terminalAtConnect := IsTerminalStatus(reloaded.Status) || hub.IsClosed(s.ID)
	if !terminalAtConnect {
		t.Fatal("reconnect should be terminal-at-connect (read-only)")
	}
	if gatedVisitorSend(ctx, store, hub, "acme", s.ID) {
		t.Fatal("read-only reconnect must not be able to send")
	}
}

// Closing one chat leaves a freshly started chat fully live.
func TestStartNewChatIsDistinctSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	a := openSession("acme")
	store.put(a)
	if _, _, err := CloseSession(ctx, store, hub, "acme", a.ID, ""); err != nil {
		t.Fatalf("close A: %v", err)
	}

	b := openSession("acme") // "Start new chat" → brand-new session id
	store.put(b)
	if a.ID == b.ID {
		t.Fatal("new chat must have a different session id")
	}
	if hub.IsClosed(b.ID) {
		t.Fatal("new session must not inherit the closed flag")
	}
	if _, err := ReloadOpenSession(ctx, store, "acme", b.ID); err != nil {
		t.Fatalf("new session should be live: %v", err)
	}
	if !gatedVisitorSend(ctx, store, hub, "acme", b.ID) {
		t.Fatal("new session should accept messages")
	}
}

// Re-closing a closed session is a no-op success: no transition, and no
// duplicate session_closed frame is broadcast.
func TestCloseSessionIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)
	visitor := &Subscriber{Out: make(chan []byte, 4), Role: RoleVisitor}
	hub.Subscribe(s.ID, visitor)

	if _, transitioned, err := CloseSession(ctx, store, hub, "acme", s.ID, ""); err != nil || !transitioned {
		t.Fatalf("first close: transitioned=%v err=%v", transitioned, err)
	}
	// Drain the single expected frame.
	<-visitor.Out

	_, transitioned, err := CloseSession(ctx, store, hub, "acme", s.ID, "")
	if err != nil {
		t.Fatalf("second close err: %v", err)
	}
	if transitioned {
		t.Fatal("re-closing a closed session must report transitioned=false")
	}
	select {
	case got := <-visitor.Out:
		t.Fatalf("idempotent close must not broadcast again, got %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	if !hub.IsClosed(s.ID) {
		t.Fatal("session should remain flagged closed")
	}
}

// An agent disconnect is NOT a close: dropping a subscriber leaves the
// session live so the visitor can keep chatting with another agent.
func TestCloseIsDistinctFromDisconnect(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)
	agent := &Subscriber{Out: make(chan []byte, 4), Role: RoleAgent, AgentID: "alice"}
	hub.Subscribe(s.ID, agent)

	// Simulate the agent's WS dropping: Unsubscribe (what the handler's
	// stop-branch does) — NOT CloseSession.
	hub.Unsubscribe(s.ID, agent)

	if hub.IsClosed(s.ID) {
		t.Fatal("a disconnect must not flag the session closed")
	}
	reloaded, _ := store.GetSession(ctx, "acme", s.ID)
	if IsTerminalStatus(reloaded.Status) {
		t.Fatalf("a disconnect must not change status, got %q", reloaded.Status)
	}
	if !gatedVisitorSend(ctx, store, hub, "acme", s.ID) {
		t.Fatal("visitor should still be able to send after a mere disconnect")
	}
}

// Close vs. send racing: once the close commits, closure always wins —
// no message persists to the closed session and the transcript is
// frozen at the moment of close. Run with -race for data-race coverage
// of the hub's closed-set.
func TestCloseSendRaceClosureWins(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()
	s := openSession("acme")
	store.put(s)

	// A visitor is connected for the duration of the race — its rapid
	// sends are modelled by the gatedVisitorSend goroutines below. This is
	// what makes the in-process closed-flag apply (it only guards a live
	// subscriber's send race). Its Out is buffered; we never read it since
	// we only assert that closure wins.
	visitor := &Subscriber{Out: make(chan []byte, 8), Role: RoleVisitor}
	hub.Subscribe(s.ID, visitor)

	const senders = 8
	var wg sync.WaitGroup
	var countAtClose int64 = -1

	// Senders hammer the gate concurrently with the close.
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				gatedVisitorSend(ctx, store, hub, "acme", s.ID)
				if atomic.LoadInt64(&countAtClose) >= 0 {
					return // close already committed; stop racing
				}
			}
		}()
	}

	// Closer fires partway through.
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(200 * time.Microsecond)
		if _, _, err := CloseSession(ctx, store, hub, "acme", s.ID, ""); err != nil {
			t.Errorf("close: %v", err)
		}
		// The moment MarkSessionClosed committed, AppendMessage refuses
		// every further send — so the count is frozen here.
		atomic.StoreInt64(&countAtClose, int64(store.msgCount()))
	}()

	wg.Wait()

	frozen := atomic.LoadInt64(&countAtClose)
	if frozen < 0 {
		t.Fatal("closer never ran")
	}
	if final := int64(store.msgCount()); final != frozen {
		t.Fatalf("transcript grew after close: frozen=%d final=%d (closure did not win)", frozen, final)
	}
	if !hub.IsClosed(s.ID) {
		t.Fatal("session must be flagged closed after the race")
	}
	if gatedVisitorSend(ctx, store, hub, "acme", s.ID) {
		t.Fatal("a send after the race resolved must be rejected")
	}
}

// The in-process closed-flag is bounded: it is only held while a
// subscriber is live, and is dropped when the last one leaves, so the hub
// can't accumulate a flag for every session ever closed over the life of
// the process. Correctness never depends on the flag — the durable store
// stays authoritative — so eviction only trades a fast-path for one DB
// read on a later reconnect.
func TestClosedFlagBoundedToLiveSubscribers(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	hub := NewHub()

	// Closed while a subscriber is connected: the flag is set...
	s := openSession("acme")
	store.put(s)
	v := &Subscriber{Out: make(chan []byte, 4), Role: RoleVisitor}
	hub.Subscribe(s.ID, v)
	if _, _, err := CloseSession(ctx, store, hub, "acme", s.ID, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hub.IsClosed(s.ID) {
		t.Fatal("flag should be set while the subscriber is connected")
	}
	// ...and evicted once the last subscriber leaves.
	hub.Unsubscribe(s.ID, v)
	if hub.IsClosed(s.ID) {
		t.Fatal("flag must be evicted after the last subscriber unsubscribes")
	}
	// The durable store still reports terminal, so a reconnect is still
	// read-only even without the flag.
	if _, err := ReloadOpenSession(ctx, store, "acme", s.ID); err != ErrSessionClosed {
		t.Fatalf("store must still be terminal after eviction, got %v", err)
	}

	// Closed with NO subscriber connected: no flag is retained at all.
	s2 := openSession("acme")
	store.put(s2)
	if _, _, err := CloseSession(ctx, store, hub, "acme", s2.ID, ""); err != nil {
		t.Fatalf("close idle: %v", err)
	}
	if hub.IsClosed(s2.ID) {
		t.Fatal("closing with no subscriber must not retain an in-process flag")
	}
	if _, err := ReloadOpenSession(ctx, store, "acme", s2.ID); err != ErrSessionClosed {
		t.Fatalf("idle-closed session must still be terminal in the store, got %v", err)
	}
}
