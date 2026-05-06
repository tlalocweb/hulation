// Package gossip implements the Hybrid Logical Clock + the gossip
// protocol that replicates HA Stage 3's CRDT layer (HA_PLAN3 §7).
// Visitor records, session continuity, and bad-actor flags all live
// here — not in Raft — because Raft commit latency over public-
// internet WAN (100–300ms p99) is too slow for the visitor cookie
// hot path.
package gossip

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// HLC is a hybrid logical clock timestamp. Wall-millisecond
// resolution + a 16-bit counter to handle same-ms write bursts +
// the node id for deterministic tiebreak when two nodes generate
// timestamps with identical (wall, counter).
type HLC struct {
	WallMs  int64
	Counter uint32
	NodeID  string
}

// Clock owns the per-process HLC state. Now() advances the clock
// monotonically; Update() pulls the clock forward when a peer
// reports a higher value.
type Clock struct {
	nodeID  string
	nowFunc func() time.Time

	mu   sync.Mutex
	last HLC
}

// NewClock builds a Clock keyed to nodeID. nowFunc is overridable
// for tests that want a fake wall clock.
func NewClock(nodeID string) *Clock {
	if nodeID == "" {
		nodeID = "unknown"
	}
	return &Clock{nodeID: nodeID, nowFunc: time.Now}
}

// SetNowFunc replaces the wall-clock source. Tests only.
func (c *Clock) SetNowFunc(f func() time.Time) {
	c.mu.Lock()
	c.nowFunc = f
	c.mu.Unlock()
}

// Now produces the next HLC. Standard HLC algorithm:
//   pt = max(last.wall, now())
//   if pt > last.wall: counter = 0
//   else:              counter = last.counter + 1
func (c *Clock) Now() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := c.nowFunc().UnixMilli()
	pt := wall
	if c.last.WallMs > pt {
		pt = c.last.WallMs
	}
	var counter uint32
	if pt > c.last.WallMs {
		counter = 0
	} else {
		counter = c.last.Counter + 1
	}
	c.last = HLC{WallMs: pt, Counter: counter, NodeID: c.nodeID}
	return c.last
}

// Update pulls the clock forward when a remote HLC is newer.
// Returns the next-emitted local HLC after the merge so callers can
// stamp their reply.
func (c *Clock) Update(remote HLC) HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := c.nowFunc().UnixMilli()
	pt := wall
	if c.last.WallMs > pt {
		pt = c.last.WallMs
	}
	if remote.WallMs > pt {
		pt = remote.WallMs
	}
	var counter uint32
	switch {
	case pt > c.last.WallMs && pt > remote.WallMs:
		counter = 0
	case pt == c.last.WallMs && pt == remote.WallMs:
		if c.last.Counter > remote.Counter {
			counter = c.last.Counter + 1
		} else {
			counter = remote.Counter + 1
		}
	case pt == c.last.WallMs:
		counter = c.last.Counter + 1
	default:
		counter = remote.Counter + 1
	}
	c.last = HLC{WallMs: pt, Counter: counter, NodeID: c.nodeID}
	return c.last
}

// After reports whether this HLC is strictly later than other.
// Tiebreak on equal (wall, counter) is by node_id (lexicographic).
// "Equal" returns false here — use Equal for that.
func (a HLC) After(b HLC) bool {
	if a.WallMs != b.WallMs {
		return a.WallMs > b.WallMs
	}
	if a.Counter != b.Counter {
		return a.Counter > b.Counter
	}
	return a.NodeID > b.NodeID
}

// Equal compares both HLCs across all three components.
func (a HLC) Equal(b HLC) bool {
	return a.WallMs == b.WallMs && a.Counter == b.Counter && a.NodeID == b.NodeID
}

// IsZero reports whether the HLC has never been set.
func (a HLC) IsZero() bool { return a.WallMs == 0 && a.Counter == 0 && a.NodeID == "" }

// String renders the HLC in a debug-friendly format.
func (a HLC) String() string {
	if a.IsZero() {
		return "<hlc-zero>"
	}
	return fmt.Sprintf("%d.%d@%s", a.WallMs, a.Counter, a.NodeID)
}

// ErrInvalidHLC is returned by codecs that can't decode a value.
var ErrInvalidHLC = errors.New("gossip: invalid HLC bytes")
