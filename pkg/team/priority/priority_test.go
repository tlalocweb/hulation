package priority

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	localstorage "github.com/tlalocweb/hulation/pkg/store/storage/local"
)

type fakeCluster struct {
	mu          sync.Mutex
	leader      atomic.Bool
	leaderID    string
	leaderAddr  string
	members     []ClusterMember
	transfers   []string
	transferErr error
}

func (f *fakeCluster) IsLeader() bool                  { return f.leader.Load() }
func (f *fakeCluster) LeaderInfo() (string, string)    { return f.leaderID, f.leaderAddr }
func (f *fakeCluster) Members() []ClusterMember {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ClusterMember(nil), f.members...)
}
func (f *fakeCluster) LeadershipTransfer(targetID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transferErr != nil {
		return f.transferErr
	}
	f.transfers = append(f.transfers, targetID)
	return nil
}

func openStore(t *testing.T) storagepkg.Storage {
	t.Helper()
	s, err := localstorage.Open(localstorage.Options{Path: filepath.Join(t.TempDir(), "data.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRefresh_WritesFlagWhenCHConnected(t *testing.T) {
	store := openStore(t)
	cluster := &fakeCluster{}
	loop, err := Start(context.Background(), Config{
		NodeID: "node-east", IsCHConnected: true,
		RefreshInterval: 20 * time.Millisecond, LoopInterval: time.Hour,
		Storage: store, Cluster: cluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Stop)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := store.Get(context.Background(), CHConnectedKeyPrefix+"node-east")
		if len(v) > 0 && flagFresh(v) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("flag never written")
}

func TestRefresh_DeletesFlagWhenNotCHConnected(t *testing.T) {
	store := openStore(t)
	// Pre-populate the key so we can observe deletion.
	_ = store.Put(context.Background(), CHConnectedKeyPrefix+"node-west", encodeFlag(time.Now()))

	loop, err := Start(context.Background(), Config{
		NodeID: "node-west", IsCHConnected: false,
		RefreshInterval: 10 * time.Millisecond, LoopInterval: time.Hour,
		Storage: store, Cluster: &fakeCluster{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Stop)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		_, err := store.Get(context.Background(), CHConnectedKeyPrefix+"node-west")
		if errors.Is(err, storagepkg.ErrNotFound) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("flag never deleted")
}

func TestPriorityLoop_TransfersToCHConnectedPeer(t *testing.T) {
	store := openStore(t)
	// Mark node-west (our peer) as CH-connected via a fresh flag.
	_ = store.Put(context.Background(), CHConnectedKeyPrefix+"node-west", encodeFlag(time.Now()))

	cluster := &fakeCluster{
		leaderID: "node-east",
		members: []ClusterMember{
			{NodeID: "node-east", RaftAddr: "east:443", IsLeader: true},
			{NodeID: "node-west", RaftAddr: "west:443"},
		},
	}
	cluster.leader.Store(true)

	loop, err := Start(context.Background(), Config{
		NodeID: "node-east", IsCHConnected: false, // east is leader but NOT ch-connected
		RefreshInterval: time.Hour, LoopInterval: 20 * time.Millisecond,
		Storage: store, Cluster: cluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Stop)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		cluster.mu.Lock()
		got := append([]string(nil), cluster.transfers...)
		cluster.mu.Unlock()
		for _, id := range got {
			if id == "node-west" {
				return
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Error("LeadershipTransfer to node-west never fired")
}

func TestPriorityLoop_NoTransferWhenWeAreCH(t *testing.T) {
	cluster := &fakeCluster{leaderID: "node-east"}
	cluster.leader.Store(true)
	cluster.members = []ClusterMember{
		{NodeID: "node-east", RaftAddr: "east:443", IsLeader: true},
		{NodeID: "node-west", RaftAddr: "west:443"},
	}
	loop, err := Start(context.Background(), Config{
		NodeID: "node-east", IsCHConnected: true,
		RefreshInterval: time.Hour, LoopInterval: 20 * time.Millisecond,
		Storage: openStore(t), Cluster: cluster,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Stop)
	time.Sleep(100 * time.Millisecond)
	cluster.mu.Lock()
	defer cluster.mu.Unlock()
	if len(cluster.transfers) != 0 {
		t.Errorf("unexpected transfers when CH leader: %v", cluster.transfers)
	}
}

func TestPriorityLoop_NoTransferWhenNotLeader(t *testing.T) {
	cluster := &fakeCluster{leaderID: "node-west"}
	// IsLeader() returns false → priority loop skips.
	loop, _ := Start(context.Background(), Config{
		NodeID: "node-east", IsCHConnected: false,
		RefreshInterval: time.Hour, LoopInterval: 20 * time.Millisecond,
		Storage: openStore(t), Cluster: cluster,
	})
	t.Cleanup(loop.Stop)
	time.Sleep(100 * time.Millisecond)
	cluster.mu.Lock()
	defer cluster.mu.Unlock()
	if len(cluster.transfers) != 0 {
		t.Errorf("non-leader should not transfer; got %v", cluster.transfers)
	}
}

func TestFlagFresh_TTLBoundary(t *testing.T) {
	now := encodeFlag(time.Now())
	if !flagFresh(now) {
		t.Error("just-emitted flag should be fresh")
	}
	stale := encodeFlag(time.Now().Add(-2 * CHFlagTTL))
	if flagFresh(stale) {
		t.Error("stale flag should not be fresh")
	}
	if flagFresh(nil) {
		t.Error("empty flag should not be fresh")
	}
	if flagFresh([]byte("garbage")) {
		t.Error("garbage flag should not be fresh")
	}
}

func TestStart_RejectsMissingDeps(t *testing.T) {
	if _, err := Start(context.Background(), Config{NodeID: "x"}); err == nil {
		t.Error("expected error when Storage missing")
	}
	if _, err := Start(context.Background(), Config{NodeID: "x", Storage: openStore(t)}); err == nil {
		t.Error("expected error when Cluster missing")
	}
	if _, err := Start(context.Background(), Config{Storage: openStore(t), Cluster: &fakeCluster{}}); err == nil {
		t.Error("expected error when NodeID missing")
	}
}
