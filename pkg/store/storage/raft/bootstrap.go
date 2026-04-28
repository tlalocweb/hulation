package raftbackend

// Boot-time helpers that bridge config.TeamConfig (the YAML
// surface) into a runnable Config (the raft-package surface).
//
// In solo mode (the default — and the most common deployment
// shape) we generate a TeamID UUID v7 on first run, derive a
// NodeID from the hostname, and bind the raft transport to
// loopback. Stage 3 expands this for multi-node clusters; Stage 2
// silently downgrades any non-solo bootstrap mode to solo with a
// warning so multi-node config can be written ahead of time.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/log"
	hulaconfig "github.com/tlalocweb/hulation/config"
)

var bootLog = log.GetTaggedLogger("storage-raft-boot", "Raft bootstrap")

// AutoConfig converts a (possibly nil) *hulaconfig.TeamConfig
// into a runnable raft Config, persisting any auto-generated
// values (TeamID, NodeID) under DataDir so subsequent boots
// pick them up unchanged. Idempotent — calling twice on the
// same DataDir yields the same Config.
func AutoConfig(tc *hulaconfig.TeamConfig) (Config, error) {
	if tc == nil {
		tc = &hulaconfig.TeamConfig{}
	}

	dataDir := tc.DataDir
	if dataDir == "" {
		dataDir = hulaconfig.DefaultTeamDataDir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("raftbackend: mkdir data_dir %s: %w", dataDir, err)
	}

	teamID, err := persistOrGenerate(filepath.Join(dataDir, "team-id"), tc.TeamID, generateTeamID)
	if err != nil {
		return Config{}, fmt.Errorf("raftbackend: team-id: %w", err)
	}
	nodeID, err := persistOrGenerate(filepath.Join(dataDir, "node-id"), tc.NodeID, defaultNodeID)
	if err != nil {
		return Config{}, fmt.Errorf("raftbackend: node-id: %w", err)
	}

	bindAddr := tc.BindAddr
	bootstrapMode := strings.ToLower(strings.TrimSpace(tc.Bootstrap))
	switch bootstrapMode {
	case "", "solo":
		// Pure solo. Force loopback if operator didn't set one.
		if bindAddr == "" {
			bindAddr = "127.0.0.1:0"
		}
	case "first-of-team", "join":
		bootLog.Warnf("team.bootstrap = %q parsed but ignored — multi-node lands in HA Stage 3; falling back to solo", bootstrapMode)
		if bindAddr == "" {
			bindAddr = "127.0.0.1:0"
		}
	default:
		return Config{}, fmt.Errorf("raftbackend: unknown team.bootstrap %q", bootstrapMode)
	}

	if len(tc.Peers) > 0 {
		bootLog.Warnf("team.peers (%d entry/entries) parsed but ignored — multi-node lands in HA Stage 3", len(tc.Peers))
	}

	cfg := Config{
		NodeID:    nodeID,
		DataDir:   dataDir,
		BindAddr:  bindAddr,
		Bootstrap: true,
	}
	if tc.SnapshotInterval != "" {
		d, err := time.ParseDuration(tc.SnapshotInterval)
		if err != nil {
			return Config{}, fmt.Errorf("raftbackend: snapshot_interval %q: %w", tc.SnapshotInterval, err)
		}
		cfg.SnapshotInterval = d
	}
	if tc.SnapshotThreshold > 0 {
		cfg.SnapshotThreshold = tc.SnapshotThreshold
	}

	bootLog.Infof("team config resolved (team_id=%s, node_id=%s, data_dir=%s, mode=solo)", teamID, nodeID, dataDir)
	return cfg, nil
}

// persistOrGenerate reads the value at path. If the file is
// missing AND override is empty it calls gen() and writes the
// result. If override is set it always wins (operator pinned
// the value via YAML) and is persisted. Returns the resolved
// value.
func persistOrGenerate(path, override string, gen func() (string, error)) (string, error) {
	if override != "" {
		if err := os.WriteFile(path, []byte(override+"\n"), 0o600); err != nil {
			return "", err
		}
		return override, nil
	}
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	v, err := gen()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(v+"\n"), 0o600); err != nil {
		return "", err
	}
	return v, nil
}

func generateTeamID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		// Fall back to crypto/rand-derived hex if uuid v7 path
		// fails (very unusual; implies wall clock is broken).
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}
	return id.String(), nil
}

func defaultNodeID() (string, error) {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "node-" + hex.EncodeToString(b[:]), nil
}
