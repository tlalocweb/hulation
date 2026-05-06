package gossip

import (
	"sync"
	"testing"
	"time"
)

func TestClock_NowMonotonic(t *testing.T) {
	c := NewClock("nodeA")
	first := c.Now()
	second := c.Now()
	if !second.After(first) {
		t.Errorf("second %v should be after first %v", second, first)
	}
}

func TestClock_NowSameMillisecond_BumpsCounter(t *testing.T) {
	c := NewClock("nodeA")
	frozen := time.UnixMilli(1_700_000_000_000)
	c.SetNowFunc(func() time.Time { return frozen })
	a := c.Now()
	b := c.Now()
	if a.WallMs != b.WallMs {
		t.Fatalf("wall ticked despite frozen now: %d vs %d", a.WallMs, b.WallMs)
	}
	if b.Counter != a.Counter+1 {
		t.Errorf("counter not bumped: a.counter=%d b.counter=%d", a.Counter, b.Counter)
	}
}

func TestClock_Update_PullsForward(t *testing.T) {
	c := NewClock("local")
	c.SetNowFunc(func() time.Time { return time.UnixMilli(100) })
	local := c.Now() // wall=100 counter=0
	if local.WallMs != 100 {
		t.Fatalf("local.WallMs = %d, want 100", local.WallMs)
	}
	remote := HLC{WallMs: 500, Counter: 7, NodeID: "remote"}
	merged := c.Update(remote)
	if merged.WallMs < 500 {
		t.Errorf("after Update wall=%d, want >= 500", merged.WallMs)
	}
	if merged.NodeID != "local" {
		t.Errorf("after Update node_id=%q, want local", merged.NodeID)
	}
}

func TestClock_Update_LocalIsAhead(t *testing.T) {
	c := NewClock("local")
	c.SetNowFunc(func() time.Time { return time.UnixMilli(1000) })
	a := c.Now() // wall=1000 c=0
	remote := HLC{WallMs: 500, Counter: 0, NodeID: "remote"}
	b := c.Update(remote)
	if !b.After(a) {
		t.Errorf("b (%v) should be after a (%v) — local was ahead", b, a)
	}
	if b.WallMs != a.WallMs {
		t.Errorf("walls diverged unnecessarily: a=%d b=%d", a.WallMs, b.WallMs)
	}
}

func TestHLC_AfterTiebreak(t *testing.T) {
	a := HLC{WallMs: 1, Counter: 0, NodeID: "alpha"}
	b := HLC{WallMs: 1, Counter: 0, NodeID: "beta"}
	if !b.After(a) {
		t.Errorf("beta should outrank alpha on tiebreak")
	}
	if a.After(b) {
		t.Errorf("alpha should NOT outrank beta on tiebreak")
	}
}

func TestHLC_AfterCounter(t *testing.T) {
	a := HLC{WallMs: 1, Counter: 5, NodeID: "n"}
	b := HLC{WallMs: 1, Counter: 6, NodeID: "n"}
	if !b.After(a) {
		t.Errorf("higher counter should be later")
	}
}

func TestClock_Concurrency(t *testing.T) {
	c := NewClock("local")
	const N = 1000
	var wg sync.WaitGroup
	seen := make(chan HLC, N*4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < N; j++ {
				seen <- c.Now()
			}
		}()
	}
	wg.Wait()
	close(seen)

	all := make([]HLC, 0, N*4)
	for h := range seen {
		all = append(all, h)
	}
	// Every HLC must be unique — duplicates would mean Now() raced.
	uniq := map[string]bool{}
	for _, h := range all {
		k := h.String()
		if uniq[k] {
			t.Fatalf("duplicate HLC: %s", k)
		}
		uniq[k] = true
	}
}

func TestHLC_IsZero(t *testing.T) {
	if !(HLC{}).IsZero() {
		t.Error("zero value should report IsZero")
	}
	if (HLC{WallMs: 1}).IsZero() {
		t.Error("populated HLC reported IsZero")
	}
}
