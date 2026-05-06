package gossip_test

// End-to-end gossip integration test (no Docker, no gRPC). Wires
// two Stores together via an in-process Sender that calls
// ApplyDelta directly on the peer; tests that:
//   - push-on-write fans a local write out to peers
//   - anti-entropy ticks reconcile missed pushes
//   - bad-actor monotone "bad wins" replicates
//   - tombstones propagate

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/gossip"
)

type inProcSender struct {
	mu     sync.Mutex
	stores map[string]*gossip.Store // peer NodeID → Store
	dropPushes bool                 // when true, Push returns nil but does NOT apply
}

func (s *inProcSender) Push(ctx context.Context, peer gossip.Peer, deltas []gossip.Delta) error {
	s.mu.Lock()
	store := s.stores[peer.NodeID]
	drop := s.dropPushes
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	if drop {
		return nil
	}
	for _, d := range deltas {
		if _, _, err := store.ApplyDelta(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

func (s *inProcSender) SyncDigest(ctx context.Context, peer gossip.Peer, digest map[string]gossip.HLC) ([]gossip.Delta, error) {
	s.mu.Lock()
	store := s.stores[peer.NodeID]
	s.mu.Unlock()
	if store == nil {
		return nil, nil
	}
	return store.SyncFromDigest(ctx, digest, 256)
}

func openTestStore(t *testing.T, nodeID string) *gossip.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := gossip.OpenStore(filepath.Join(dir, "crdt.db"), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEngine_PushOnWriteReplicates(t *testing.T) {
	storeA := openTestStore(t, "A")
	storeB := openTestStore(t, "B")
	sender := &inProcSender{stores: map[string]*gossip.Store{
		"A": storeA, "B": storeB,
	}}
	peersA := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "B"}})
	peersB := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "A"}})

	engA, _ := gossip.NewEngine(storeA, peersA, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	engB, _ := gossip.NewEngine(storeB, peersB, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engA.Start(ctx)
	engB.Start(ctx)
	t.Cleanup(engA.Stop)
	t.Cleanup(engB.Stop)

	// Write on A; B should see it via push-on-write.
	if _, err := storeA.PutLWW(ctx, gossip.BucketVisitors, "srv/v1", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := storeB.GetLWW(ctx, gossip.BucketVisitors, "srv/v1"); err == nil && string(got.Payload) == "hello" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("B never saw the push")
}

func TestEngine_AntiEntropyHealsMissedPushes(t *testing.T) {
	storeA := openTestStore(t, "A")
	storeB := openTestStore(t, "B")
	sender := &inProcSender{
		stores: map[string]*gossip.Store{"A": storeA, "B": storeB},
		dropPushes: true, // pushes are silently dropped
	}
	peersA := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "B"}})
	peersB := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "A"}})

	engA, _ := gossip.NewEngine(storeA, peersA, sender, gossip.EngineConfig{AntiEntropyInterval: 50 * time.Millisecond})
	engB, _ := gossip.NewEngine(storeB, peersB, sender, gossip.EngineConfig{AntiEntropyInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engA.Start(ctx)
	engB.Start(ctx)
	t.Cleanup(engA.Stop)
	t.Cleanup(engB.Stop)

	// Write on A; pushes are dropped, so only the next anti-
	// entropy tick on B reconciles.
	if _, err := storeA.PutLWW(ctx, gossip.BucketVisitors, "k", []byte("anti-entropy")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := storeB.GetLWW(ctx, gossip.BucketVisitors, "k"); err == nil && string(got.Payload) == "anti-entropy" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("anti-entropy never reconciled the missed push")
}

func TestEngine_BadActorReplicates(t *testing.T) {
	storeA := openTestStore(t, "A")
	storeB := openTestStore(t, "B")
	sender := &inProcSender{stores: map[string]*gossip.Store{"A": storeA, "B": storeB}}
	peersA := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "B"}})
	peersB := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "A"}})

	engA, _ := gossip.NewEngine(storeA, peersA, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	engB, _ := gossip.NewEngine(storeB, peersB, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engA.Start(ctx)
	engB.Start(ctx)
	t.Cleanup(engA.Stop)
	t.Cleanup(engB.Stop)

	if _, err := storeA.FlagBad(ctx, gossip.BucketBadactorFlags, "1.2.3.4", "scoring", "A"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := storeB.GetMonotone(ctx, gossip.BucketBadactorFlags, "1.2.3.4")
		if err == nil && got.Flagged && got.ByNodeID == "A" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("bad-actor flag never reached B")
}

func TestEngine_TombstoneReplicates(t *testing.T) {
	storeA := openTestStore(t, "A")
	storeB := openTestStore(t, "B")
	sender := &inProcSender{stores: map[string]*gossip.Store{"A": storeA, "B": storeB}}
	peersA := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "B"}})
	peersB := gossip.NewStaticPeers([]gossip.Peer{{NodeID: "A"}})

	engA, _ := gossip.NewEngine(storeA, peersA, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	engB, _ := gossip.NewEngine(storeB, peersB, sender, gossip.EngineConfig{AntiEntropyInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engA.Start(ctx)
	engB.Start(ctx)
	t.Cleanup(engA.Stop)
	t.Cleanup(engB.Stop)

	if _, err := storeA.PutLWW(ctx, gossip.BucketVisitors, "srv/v1", []byte("alive")); err != nil {
		t.Fatal(err)
	}
	// give B a moment to pick up the live value.
	time.Sleep(100 * time.Millisecond)
	if _, err := storeA.DeleteLWW(ctx, gossip.BucketVisitors, "srv/v1"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := storeB.GetLWW(ctx, gossip.BucketVisitors, "srv/v1")
		if err == nil && got.Tombstone {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("tombstone never reached B")
}
