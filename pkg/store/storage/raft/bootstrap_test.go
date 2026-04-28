package raftbackend_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hulaconfig "github.com/tlalocweb/hulation/config"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

// TestAutoConfig_NilGeneratesIDs covers the "no team: block at
// all" code path — the most common deployment shape. AutoConfig
// must invent a team_id + node_id, persist them, and produce a
// runnable Config.
func TestAutoConfig_NilGeneratesIDs(t *testing.T) {
	dir := t.TempDir()
	tc := &hulaconfig.TeamConfig{DataDir: dir}

	cfg, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig: %v", err)
	}
	if cfg.NodeID == "" {
		t.Fatal("NodeID should be auto-generated")
	}
	if cfg.DataDir != dir {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, dir)
	}
	if !cfg.Bootstrap {
		t.Fatal("Bootstrap should default to true for solo mode")
	}
	if cfg.BindAddr != "127.0.0.1:0" {
		t.Fatalf("BindAddr = %q, want loopback ephemeral", cfg.BindAddr)
	}

	// The team-id file should now exist with a UUID-looking value.
	idBytes, err := os.ReadFile(filepath.Join(dir, "team-id"))
	if err != nil {
		t.Fatalf("team-id file: %v", err)
	}
	teamID := strings.TrimSpace(string(idBytes))
	if len(teamID) < 8 {
		t.Fatalf("team-id %q looks too short", teamID)
	}

	// Second call should return the same persisted team-id.
	cfg2, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig second: %v", err)
	}
	if cfg2.NodeID != cfg.NodeID {
		t.Fatalf("NodeID drifted: %q vs %q", cfg.NodeID, cfg2.NodeID)
	}
	idBytes2, _ := os.ReadFile(filepath.Join(dir, "team-id"))
	if strings.TrimSpace(string(idBytes2)) != teamID {
		t.Fatal("team-id rotated on second AutoConfig call")
	}
}

// TestAutoConfig_OperatorPinIDs verifies that an operator's
// explicit team_id / node_id wins over the persisted file.
func TestAutoConfig_OperatorPinIDs(t *testing.T) {
	dir := t.TempDir()

	// Pre-seed a different team-id on disk so we can assert it
	// gets overwritten by the operator's pin.
	if err := os.WriteFile(filepath.Join(dir, "team-id"), []byte("stale-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tc := &hulaconfig.TeamConfig{
		TeamID:  "pinned-team",
		NodeID:  "pinned-node",
		DataDir: dir,
	}
	cfg, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig: %v", err)
	}
	if cfg.NodeID != "pinned-node" {
		t.Fatalf("NodeID = %q, want pinned-node", cfg.NodeID)
	}
	teamID, _ := os.ReadFile(filepath.Join(dir, "team-id"))
	if strings.TrimSpace(string(teamID)) != "pinned-team" {
		t.Fatalf("team-id on disk = %q, want pinned-team", string(teamID))
	}
}

// TestAutoConfig_MultiNodeFallsBackToSolo verifies Stage-2's
// "parse but warn + fall back to solo" behaviour for
// non-solo bootstrap modes — operators can write multi-node
// config ahead of Stage 3 without breaking Stage-2 boot.
func TestAutoConfig_MultiNodeFallsBackToSolo(t *testing.T) {
	dir := t.TempDir()
	tc := &hulaconfig.TeamConfig{
		DataDir:   dir,
		Bootstrap: "first-of-team",
		Peers: []hulaconfig.TeamPeer{
			{ID: "node-a", RaftAddr: "10.0.0.1:8300"},
			{ID: "node-b", RaftAddr: "10.0.0.2:8300"},
		},
	}
	cfg, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig: %v", err)
	}
	if cfg.BindAddr != "127.0.0.1:0" {
		t.Fatalf("BindAddr = %q, want fallback loopback", cfg.BindAddr)
	}
}

// TestAutoConfig_FullBootRoundTrip uses AutoConfig output to
// boot a real RaftStorage and exercises a write → re-open →
// read cycle that mimics the production server boot path.
func TestAutoConfig_FullBootRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tc := &hulaconfig.TeamConfig{DataDir: dir}

	cfg, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig: %v", err)
	}
	rs, err := raftbackend.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := rs.WaitLeader(ctx); err != nil {
		cancel()
		_ = rs.Close()
		t.Fatalf("WaitLeader: %v", err)
	}
	cancel()
	if err := rs.Put(context.Background(), "goals/persist-me", []byte("alive")); err != nil {
		_ = rs.Close()
		t.Fatalf("Put: %v", err)
	}
	if err := rs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-boot through AutoConfig + New again (no override IDs).
	cfg2, err := raftbackend.AutoConfig(tc)
	if err != nil {
		t.Fatalf("AutoConfig second: %v", err)
	}
	rs2, err := raftbackend.New(cfg2)
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	defer rs2.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rs2.WaitLeader(ctx); err != nil {
		t.Fatalf("WaitLeader second: %v", err)
	}
	got, err := rs2.Get(context.Background(), "goals/persist-me")
	if err != nil {
		t.Fatalf("Get after re-boot: %v", err)
	}
	if string(got) != "alive" {
		t.Fatalf("post-reboot value = %q, want alive", got)
	}
}
