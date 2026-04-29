// Package storagetest exposes a contract test suite that any
// storage.Storage implementation can run against itself. The same
// suite is invoked by:
//
//   - pkg/store/storage/local — wraps bbolt, runs in pure-Go
//     tests with a t.TempDir() backing file.
//   - pkg/store/storage/raft — wraps a single-node Raft cluster
//     with raft-boltdb, runs the same contract.
//
// The tests assert behaviour, not implementation. A failing test
// here means the impl is divergent from the contract laid out in
// pkg/store/storage/storage.go.
package storagetest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// Factory constructs a fresh, empty Storage for one test. The
// returned cleanup is invoked at test end; it should close the
// Storage and remove any temp files.
type Factory func(t *testing.T) (s storage.Storage, cleanup func())

// Run runs the entire contract suite. Implementations call this
// from their own _test.go.
func Run(t *testing.T, f Factory) {
	t.Helper()
	t.Run("PutGet", func(t *testing.T) { testPutGet(t, f) })
	t.Run("DeleteIdempotent", func(t *testing.T) { testDeleteIdempotent(t, f) })
	t.Run("OverwritePut", func(t *testing.T) { testOverwritePut(t, f) })
	t.Run("ListPrefix", func(t *testing.T) { testListPrefix(t, f) })
	t.Run("KeysPrefix", func(t *testing.T) { testKeysPrefix(t, f) })
	t.Run("CompareAndSwap", func(t *testing.T) { testCompareAndSwap(t, f) })
	t.Run("CompareAndCreate", func(t *testing.T) { testCompareAndCreate(t, f) })
	t.Run("MutateWrite", func(t *testing.T) { testMutateWrite(t, f) })
	t.Run("MutateSkip", func(t *testing.T) { testMutateSkip(t, f) })
	t.Run("MutateDelete", func(t *testing.T) { testMutateDelete(t, f) })
	t.Run("BatchAtomicity", func(t *testing.T) { testBatchAtomicity(t, f) })
	t.Run("WatchPrefix", func(t *testing.T) { testWatchPrefix(t, f) })
	t.Run("WatchOutsidePrefix", func(t *testing.T) { testWatchOutsidePrefix(t, f) })
	t.Run("WatchCancel", func(t *testing.T) { testWatchCancel(t, f) })
	t.Run("CloseIsIdempotent", func(t *testing.T) { testCloseIdempotent(t, f) })
	t.Run("OpsAfterClose", func(t *testing.T) { testOpsAfterClose(t, f) })
	t.Run("ConcurrentPuts", func(t *testing.T) { testConcurrentPuts(t, f) })
}

func ctx() context.Context { return context.Background() }

func testPutGet(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Put(ctx(), "consent_log/v1", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx(), "consent_log/v1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q want hello", got)
	}
	// Missing key.
	_, err = s.Get(ctx(), "consent_log/missing")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing key err: got %v want ErrNotFound", err)
	}
}

func testDeleteIdempotent(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	// Delete on missing key — no error.
	if err := s.Delete(ctx(), "goals/missing"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
	// Delete on present key — no error, key gone.
	if err := s.Put(ctx(), "goals/g1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx(), "goals/g1"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get(ctx(), "goals/g1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after delete: got %v want ErrNotFound", err)
	}
	// Second delete still no error.
	if err := s.Delete(ctx(), "goals/g1"); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func testOverwritePut(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Put(ctx(), "alerts/a1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx(), "alerts/a1", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx(), "alerts/a1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2" {
		t.Errorf("overwrite: got %q want v2", got)
	}
}

func testListPrefix(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	for _, kv := range []struct{ k, v string }{
		{"reports/r1", "1"},
		{"reports/r2", "2"},
		{"alerts/a1", "3"},
	} {
		if err := s.Put(ctx(), kv.k, []byte(kv.v)); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := s.List(ctx(), "reports/")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep) != 2 {
		t.Errorf("reports prefix: got %d want 2", len(rep))
	}
	if string(rep["reports/r1"]) != "1" || string(rep["reports/r2"]) != "2" {
		t.Errorf("reports values: %v", rep)
	}

	// Prefix that matches the entire bucket boundary.
	rep2, err := s.List(ctx(), "reports/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2) != 2 {
		t.Errorf("reports/r prefix: got %d want 2", len(rep2))
	}
	rep3, err := s.List(ctx(), "reports/r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep3) != 1 {
		t.Errorf("reports/r1 prefix: got %d want 1", len(rep3))
	}
}

func testKeysPrefix(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	for _, kv := range []struct{ k, v string }{
		{"report_runs/run-c", "3"},
		{"report_runs/run-a", "1"},
		{"report_runs/run-b", "2"},
	} {
		if err := s.Put(ctx(), kv.k, []byte(kv.v)); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := s.Keys(ctx(), "report_runs/")
	if err != nil {
		t.Fatal(err)
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("keys not sorted: %v", keys)
	}
	if len(keys) != 3 {
		t.Errorf("keys count: got %d want 3", len(keys))
	}
}

func testCompareAndSwap(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	// CAS on missing key with non-nil expected → fails.
	err := s.CompareAndSwap(ctx(), "opaque_records/missing", []byte("a"), []byte("b"))
	if !errors.Is(err, storage.ErrCASFailed) {
		t.Errorf("CAS missing key: got %v want ErrCASFailed", err)
	}
	// Seed.
	if err := s.Put(ctx(), "opaque_records/k", []byte("v0")); err != nil {
		t.Fatal(err)
	}
	// Wrong expected.
	err = s.CompareAndSwap(ctx(), "opaque_records/k", []byte("wrong"), []byte("v1"))
	if !errors.Is(err, storage.ErrCASFailed) {
		t.Errorf("CAS wrong expected: got %v want ErrCASFailed", err)
	}
	// Right expected.
	if err := s.CompareAndSwap(ctx(), "opaque_records/k", []byte("v0"), []byte("v1")); err != nil {
		t.Fatalf("CAS right expected: %v", err)
	}
	got, err := s.Get(ctx(), "opaque_records/k")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Errorf("after CAS: got %q want v1", got)
	}
}

func testCompareAndCreate(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.CompareAndCreate(ctx(), "chat_acl/c1", []byte("v")); err != nil {
		t.Fatal(err)
	}
	// Second create fails.
	err := s.CompareAndCreate(ctx(), "chat_acl/c1", []byte("v"))
	if !errors.Is(err, storage.ErrCASFailed) {
		t.Errorf("second create: got %v want ErrCASFailed", err)
	}
}

func testMutateWrite(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Put(ctx(), "goals/g1", []byte("0")); err != nil {
		t.Fatal(err)
	}
	err := s.Mutate(ctx(), "goals/g1", func(cur []byte) ([]byte, error) {
		if string(cur) != "0" {
			t.Errorf("mutate fn saw current %q want 0", cur)
		}
		return []byte("1"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx(), "goals/g1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1" {
		t.Errorf("after mutate: got %q want 1", got)
	}
}

func testMutateSkip(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Put(ctx(), "goals/g1", []byte("seed")); err != nil {
		t.Fatal(err)
	}
	err := s.Mutate(ctx(), "goals/g1", func(cur []byte) ([]byte, error) {
		return nil, storage.ErrSkip
	})
	if err != nil {
		t.Errorf("mutate skip: got err %v want nil", err)
	}
	got, err := s.Get(ctx(), "goals/g1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "seed" {
		t.Errorf("after skip: got %q want seed (unchanged)", got)
	}
}

func testMutateDelete(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Put(ctx(), "goals/g1", []byte("seed")); err != nil {
		t.Fatal(err)
	}
	err := s.Mutate(ctx(), "goals/g1", func(cur []byte) ([]byte, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx(), "goals/g1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after mutate-delete: got %v want ErrNotFound", err)
	}
}

func testBatchAtomicity(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	ops := []storage.BatchOp{
		{Op: storage.OpPut, Key: "alerts/a1", Value: []byte("1")},
		{Op: storage.OpPut, Key: "alerts/a2", Value: []byte("2")},
		{Op: storage.OpPut, Key: "alerts/a3", Value: []byte("3")},
	}
	if err := s.Batch(ctx(), ops); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		got, err := s.Get(ctx(), "alerts/a"+string(rune('0'+i)))
		if err != nil {
			t.Errorf("batch entry a%d missing", i)
		}
		if len(got) == 0 {
			t.Errorf("batch entry a%d empty", i)
		}
	}
	// Mixed put+delete batch.
	mixed := []storage.BatchOp{
		{Op: storage.OpPut, Key: "alerts/a4", Value: []byte("4")},
		{Op: storage.OpDelete, Key: "alerts/a1"},
	}
	if err := s.Batch(ctx(), mixed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx(), "alerts/a4"); err != nil {
		t.Errorf("batch put a4 missing")
	}
	if _, err := s.Get(ctx(), "alerts/a1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("batch delete a1: %v", err)
	}
}

func testWatchPrefix(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	wctx, cancel := context.WithCancel(ctx())
	defer cancel()

	ch, err := s.Watch(wctx, "alerts/")
	if err != nil {
		t.Fatal(err)
	}

	// Put two matching, one non-matching. t.Errorf (not Fatal)
	// because we're in a goroutine and Fatal must run on the test
	// goroutine.
	go func() {
		time.Sleep(10 * time.Millisecond)
		if err := s.Put(ctx(), "alerts/a1", []byte("1")); err != nil {
			t.Errorf("put alerts/a1: %v", err)
		}
		if err := s.Put(ctx(), "goals/g1", []byte("ignored")); err != nil {
			t.Errorf("put goals/g1: %v", err)
		}
		if err := s.Put(ctx(), "alerts/a2", []byte("2")); err != nil {
			t.Errorf("put alerts/a2: %v", err)
		}
	}()

	got := collectEvents(ch, 2, 1*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(got), got)
	}
	for _, ev := range got {
		if ev.Op != storage.OpPut {
			t.Errorf("event op: got %v want put", ev.Op)
		}
	}
}

func testWatchOutsidePrefix(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	wctx, cancel := context.WithCancel(ctx())
	defer cancel()

	ch, err := s.Watch(wctx, "goals/")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		if err := s.Put(ctx(), "alerts/a1", []byte("1")); err != nil {
			t.Errorf("put alerts/a1: %v", err)
		}
	}()

	// Should NOT see any event.
	got := collectEvents(ch, 1, 200*time.Millisecond)
	if len(got) != 0 {
		t.Errorf("expected 0 events, got %d: %+v", len(got), got)
	}
}

func testWatchCancel(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	wctx, cancel := context.WithCancel(ctx())
	ch, err := s.Watch(wctx, "alerts/")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// After cancel, channel should close within a reasonable timeout.
	select {
	case _, ok := <-ch:
		if ok {
			// drained one residual; that's OK. Wait for close.
			select {
			case _, ok2 := <-ch:
				if ok2 {
					t.Errorf("expected closed channel after cancel")
				}
			case <-time.After(500 * time.Millisecond):
				t.Errorf("watch channel did not close after cancel")
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("watch channel did not close after cancel")
	}
}

func testCloseIdempotent(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Second close — no error.
	if err := s.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func testOpsAfterClose(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	_ = s.Close()
	err := s.Put(ctx(), "alerts/a1", []byte("x"))
	if !errors.Is(err, storage.ErrClosed) {
		t.Errorf("put after close: got %v want ErrClosed", err)
	}
	_, err = s.Get(ctx(), "alerts/a1")
	if !errors.Is(err, storage.ErrClosed) {
		t.Errorf("get after close: got %v want ErrClosed", err)
	}
}

func testConcurrentPuts(t *testing.T, f Factory) {
	s, cleanup := f(t)
	defer cleanup()
	var wg sync.WaitGroup
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "alerts/" + string(rune('a'+(i%26))) + string(rune('0'+(i/26)))
			if err := s.Put(ctx(), key, []byte("v")); err != nil {
				t.Errorf("concurrent put %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	keys, err := s.Keys(ctx(), "alerts/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 {
		t.Errorf("expected some keys after concurrent puts; got 0")
	}
}

func collectEvents(ch <-chan storage.Event, want int, timeout time.Duration) []storage.Event {
	var got []storage.Event
	deadline := time.After(timeout)
	for len(got) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			return got
		}
	}
	return got
}
