package chat

// Idle-session sweeper.
//
// Persisting the visitor's session across a page refresh (see resume.go) means
// sessions now outlive the socket by design, so something has to close the
// ones the visitor simply walked away from. Without this, every abandoned chat
// stays queued/assigned/open forever and the agent's live list fills with
// chats nobody is on the other end of.
//
// The sweep closes any non-terminal session whose last_message_at is older
// than the configured idle timeout, reusing CloseSession so a swept session is
// indistinguishable from an agent-closed one: same persisted terminal status,
// same authoritative session_closed broadcast, same idempotency.
//
// HA note: CloseSession is idempotent and the hub broadcast is per-process, so
// running a sweeper on every node is safe — whichever node wins the transition
// notifies the sockets it hosts, and the others no-op. A visitor whose session
// was swept by a different node learns of it on their next send or reconnect,
// where the reload-from-source-of-truth gate already applies.

import (
	"context"
	"time"

	"github.com/tlalocweb/hulation/log"
)

// SweeperConfig tunes the idle sweeper.
type SweeperConfig struct {
	// IdleTimeout is how long a session may sit with no new messages before
	// being closed. Non-positive disables the sweeper entirely.
	IdleTimeout time.Duration
	// Interval is how often to scan. Non-positive falls back to 5m.
	Interval time.Duration
	// BatchLimit caps sessions closed per pass. Zero uses the store default.
	BatchLimit uint32
	// CloseReason is carried on the broadcast session_closed frame.
	CloseReason string
}

// sweeperStore is the narrow store surface the sweeper needs: find the stale
// rows, then close them through the shared CloseSession path. *Store satisfies
// it; keeping it an interface lets the sweep logic be tested without a live
// ClickHouse.
type sweeperStore interface {
	closableStore
	ListStaleSessions(ctx context.Context, cutoff time.Time, limit uint32) ([]Session, error)
}

// Sweeper periodically closes idle chat sessions.
type Sweeper struct {
	Store sweeperStore
	Hub   *Hub
	Cfg   SweeperConfig
}

// NewSweeper builds a Sweeper. Returns nil when the config disables sweeping,
// so callers can `if sw := NewSweeper(...); sw != nil { go sw.Run(ctx) }`.
func NewSweeper(store sweeperStore, hub *Hub, cfg SweeperConfig) *Sweeper {
	if store == nil || cfg.IdleTimeout <= 0 {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.CloseReason == "" {
		cfg.CloseReason = "idle_timeout"
	}
	return &Sweeper{Store: store, Hub: hub, Cfg: cfg}
}

// Run blocks, sweeping every Interval until ctx is cancelled. Intended to be
// started on its own goroutine at boot. A sweep failure is logged and retried
// on the next tick — never fatal, since chat must keep working even if the
// janitor can't reach ClickHouse.
func (s *Sweeper) Run(ctx context.Context) {
	if s == nil {
		return
	}
	log.Infof("chat: idle sweeper started (idle_timeout=%s, interval=%s)",
		s.Cfg.IdleTimeout, s.Cfg.Interval)
	t := time.NewTicker(s.Cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.SweepOnce(ctx); err != nil {
				log.Warnf("chat: idle sweep: %s", err)
			} else if n > 0 {
				log.Infof("chat: idle sweep closed %d abandoned session(s)", n)
			}
		}
	}
}

// SweepOnce runs a single pass and returns how many sessions it closed.
// Exported so tests (and any future admin "sweep now" action) can drive it
// deterministically without waiting on the ticker.
func (s *Sweeper) SweepOnce(ctx context.Context) (int, error) {
	if s == nil || s.Store == nil || s.Cfg.IdleTimeout <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-s.Cfg.IdleTimeout)
	stale, err := s.Store.ListStaleSessions(ctx, cutoff, s.Cfg.BatchLimit)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, sess := range stale {
		// Re-check cancellation between rows: a large backlog shouldn't
		// outlive shutdown.
		if ctx.Err() != nil {
			return closed, ctx.Err()
		}
		_, transitioned, err := CloseSession(ctx, s.Store, s.Hub, sess.ServerID, sess.ID, s.Cfg.CloseReason)
		if err != nil {
			// One bad row must not abort the pass — keep going and let the
			// next tick retry it.
			log.Warnf("chat: idle sweep close %s: %s", sess.ID, err)
			continue
		}
		if transitioned {
			closed++
		}
	}
	return closed, nil
}
