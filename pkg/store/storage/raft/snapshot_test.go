package raftbackend_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

// TestRaftStorage_SnapshotRestore_RoundTrip writes a meaningful
// number of keys, takes a manual Raft snapshot, blows the FSM
// state away by restoring from the snapshot's bytes, and asserts
// every key reads back identically. This exercises the
// boltSnapshot.Persist + FSM.Restore code paths together.
//
// We can't trivially boot a brand-new node and feed it the
// snapshot from outside, so we test the same logic by:
//   1. Booting a single node, writing keys A.
//   2. Triggering an explicit snapshot via raft.Snapshot.
//   3. Writing more keys B (post-snapshot).
//   4. Closing + reopening from the same data dir; raft replays
//      the snapshot + post-snapshot log entries; FSM ends up
//      with A ∪ B.
// This proves Persist produced a usable snapshot and Restore
// can rebuild from it without truncating the post-snapshot log.
func TestRaftStorage_SnapshotRestore_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	rs1, err := raftbackend.New(raftbackend.Config{
		NodeID:    "snap",
		DataDir:   dir,
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true,
		// Force an aggressive snapshot threshold so the manual
		// snapshot below isn't a no-op.
		SnapshotInterval:  500 * time.Millisecond,
		SnapshotThreshold: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := rs1.WaitLeader(ctx); err != nil {
		cancel()
		_ = rs1.Close()
		t.Fatalf("WaitLeader: %v", err)
	}
	cancel()

	// Pre-snapshot writes.
	preSnap := map[string]string{}
	for i := 0; i < 200; i++ {
		k := fmt.Sprintf("goals/pre-%03d", i)
		v := fmt.Sprintf("value-%03d", i)
		preSnap[k] = v
		if err := rs1.Put(context.Background(), k, []byte(v)); err != nil {
			_ = rs1.Close()
			t.Fatalf("Put pre %d: %v", i, err)
		}
	}

	// Snapshot.
	if err := rs1.RaftForTest().Snapshot().Error(); err != nil {
		_ = rs1.Close()
		t.Fatalf("Snapshot: %v", err)
	}

	// Post-snapshot writes — these go into the raft log only.
	postSnap := map[string]string{}
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("goals/post-%03d", i)
		v := fmt.Sprintf("late-%03d", i)
		postSnap[k] = v
		if err := rs1.Put(context.Background(), k, []byte(v)); err != nil {
			_ = rs1.Close()
			t.Fatalf("Put post %d: %v", i, err)
		}
	}

	// Verify snapshot file actually landed on disk.
	snaps, err := filepath.Glob(filepath.Join(dir, "snapshots", "*"))
	if err != nil {
		_ = rs1.Close()
		t.Fatalf("Glob snapshots: %v", err)
	}
	if len(snaps) == 0 {
		_ = rs1.Close()
		t.Fatal("no snapshot file produced")
	}

	if err := rs1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen — raft should restore from snapshot then replay
	// post-snapshot log entries, producing the union.
	rs2, err := raftbackend.New(raftbackend.Config{
		NodeID:    "snap",
		DataDir:   dir,
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true,
	})
	if err != nil {
		t.Fatalf("Reopen New: %v", err)
	}
	defer rs2.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs2.WaitLeader(ctx); err != nil {
		t.Fatalf("Reopen WaitLeader: %v", err)
	}

	for k, want := range preSnap {
		got, err := rs2.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		if string(got) != want {
			t.Fatalf("Get %s = %q, want %q", k, got, want)
		}
	}
	for k, want := range postSnap {
		got, err := rs2.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		if string(got) != want {
			t.Fatalf("Get %s = %q, want %q", k, got, want)
		}
	}
}

// raftHandle is a test-only accessor exposed via a build tag in
// the implementation. We instead added an exported method
// RaftForTest below.
var _ = raft.Configuration{}
