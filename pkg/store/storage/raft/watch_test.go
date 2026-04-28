package raftbackend_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// TestRaftStorage_Watch_FiresFromRaftApply asserts that Watch
// events fire via FSM.Apply (i.e., after the raft log commits
// the entry) — not just from a synchronous publish like in the
// LocalStorage path. This is the concrete proof that the
// Stage-3 "watch on the leader's stream" plan can rely on the
// informer's existing wiring.
func TestRaftStorage_Watch_FiresFromRaftApply(t *testing.T) {
	rs := newRaftStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	ch, err := rs.Watch(wctx, "alerts/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Burst of 10 writes — each goes through raft.Apply so the
	// only way Watch sees them is via FSM.Apply → informer.
	keys := []string{}
	for i := 0; i < 10; i++ {
		k := "alerts/burst" + string(rune('0'+i))
		keys = append(keys, k)
		if err := rs.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got := map[string]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < len(keys) && time.Now().Before(deadline) {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("watch channel closed early")
			}
			if ev.Op != storage.OpPut {
				t.Fatalf("event op = %v, want OpPut", ev.Op)
			}
			got[ev.Key] = true
		case <-time.After(200 * time.Millisecond):
		}
	}

	if len(got) != len(keys) {
		seen := []string{}
		for k := range got {
			seen = append(seen, k)
		}
		sort.Strings(seen)
		t.Fatalf("watch saw %d/%d events; got: %v", len(got), len(keys), seen)
	}
}

// TestRaftStorage_Watch_DeleteEvents covers OpDelete fan-out:
// Watch should fire on Delete + on Mutate-that-deletes, both of
// which run through different FSM apply paths.
func TestRaftStorage_Watch_DeleteEvents(t *testing.T) {
	rs := newRaftStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rs.Put(ctx, "goals/a", []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := rs.Put(ctx, "goals/b", []byte("2")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	ch, err := rs.Watch(wctx, "goals/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Direct Delete.
	if err := rs.Delete(ctx, "goals/a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Mutate-that-deletes.
	err = rs.Mutate(ctx, "goals/b", func(_ []byte) ([]byte, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Mutate-delete: %v", err)
	}

	deletes := map[string]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for len(deletes) < 2 && time.Now().Before(deadline) {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("watch channel closed early")
			}
			if ev.Op == storage.OpDelete {
				deletes[ev.Key] = true
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !deletes["goals/a"] || !deletes["goals/b"] {
		t.Fatalf("watch saw deletes: %v, want both goals/a and goals/b", deletes)
	}
}
