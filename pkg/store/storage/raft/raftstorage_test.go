package raftbackend_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/pkg/store/storage/storagetest"
)

// newRaftStorage spins up a single-node Raft cluster on a temp
// data dir and waits for it to become leader. Returns the
// Storage handle plus a Cleanup that closes it.
func newRaftStorage(t *testing.T) storage.Storage {
	t.Helper()
	rs, err := raftbackend.New(raftbackend.Config{
		NodeID:    "test-" + t.Name(),
		DataDir:   t.TempDir(),
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true,
	})
	if err != nil {
		t.Fatalf("raftbackend.New: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs.WaitLeader(ctx); err != nil {
		t.Fatalf("WaitLeader: %v", err)
	}
	return rs
}

// TestRaftStorage_StorageContract runs the same 17-case contract
// suite that LocalStorage passes. Every Storage interface
// guarantee is exercised against a single-node Raft cluster.
func TestRaftStorage_StorageContract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) (storage.Storage, func()) {
		t.Helper()
		rs := newRaftStorage(t)
		return rs, func() {}
	})
}

// TestRaftStorage_BootstrapIdempotent verifies that re-opening a
// raft storage on an existing data dir doesn't try to bootstrap
// again — the persisted log carries the cluster configuration.
func TestRaftStorage_BootstrapIdempotent(t *testing.T) {
	dir := t.TempDir()

	rs1, err := raftbackend.New(raftbackend.Config{
		NodeID:    "boot-test",
		DataDir:   dir,
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true,
	})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := rs1.WaitLeader(ctx); err != nil {
		cancel()
		_ = rs1.Close()
		t.Fatalf("first WaitLeader: %v", err)
	}
	cancel()

	// Write something we can verify after restart.
	if err := rs1.Put(context.Background(), "goals/keep-me", []byte("v1")); err != nil {
		_ = rs1.Close()
		t.Fatalf("Put: %v", err)
	}
	if err := rs1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	rs2, err := raftbackend.New(raftbackend.Config{
		NodeID:    "boot-test",
		DataDir:   dir,
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true, // intentionally still true — should be a no-op
	})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer rs2.Close()

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs2.WaitLeader(ctx); err != nil {
		t.Fatalf("second WaitLeader: %v", err)
	}

	got, err := rs2.Get(context.Background(), "goals/keep-me")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("post-restart value = %q, want %q", got, "v1")
	}
}

// TestRaftStorage_DataDirLayout asserts the on-disk file layout
// — the e2e suite + ops tooling depend on these names.
func TestRaftStorage_DataDirLayout(t *testing.T) {
	dir := t.TempDir()
	rs, err := raftbackend.New(raftbackend.Config{
		NodeID:    "layout",
		DataDir:   dir,
		BindAddr:  "127.0.0.1:0",
		Bootstrap: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rs.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs.WaitLeader(ctx); err != nil {
		t.Fatalf("WaitLeader: %v", err)
	}

	wantFiles := []string{
		"data.db",
		"raft-log.db",
		"raft-stable.db",
	}
	for _, name := range wantFiles {
		path := filepath.Join(dir, name)
		// Force a write so files are flushed.
		if err := rs.Put(context.Background(), "goals/touch", []byte("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshots")); err != nil {
		t.Fatalf("expected snapshots dir to exist: %v", err)
	}
}
