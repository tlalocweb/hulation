package server

// Leader-priority loop boot wiring (HA Stage 3.8). Runs only on
// nodes that have the team PKI configured (i.e., multi-node teams).
// Solo deployments don't need it — the single node IS the leader,
// and there's nothing to transfer leadership to.

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/pkg/team/priority"
)

// startPriorityLoop launches the per-node CH-connected refresher
// + the leader-only priority transfer loop. Returns a stoppable
// handle that callers can hold for graceful shutdown.
func startPriorityLoop(ctx context.Context, cfg *config.Config) *priority.Loop {
	if cfg == nil || cfg.Team == nil {
		return nil
	}
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return nil
	}
	loop, err := priority.Start(ctx, priority.Config{
		NodeID:        cfg.Team.NodeID,
		IsCHConnected: cfg.Team.CHConnected,
		Storage:       rs,
		Cluster:       priorityClusterAdapter{rs: rs},
	})
	if err != nil {
		unifiedLog.Warnf("priority loop disabled: %v", err)
		return nil
	}
	unifiedLog.Infof("leader-priority loop online (node=%s ch_connected=%t)",
		cfg.Team.NodeID, cfg.Team.CHConnected)
	return loop
}

// priorityClusterAdapter satisfies priority.Cluster against a live
// *raftbackend.RaftStorage. Mirrors the membership adapter pattern.
type priorityClusterAdapter struct{ rs *raftbackend.RaftStorage }

func (a priorityClusterAdapter) IsLeader() bool                { return a.rs.IsLeader() }
func (a priorityClusterAdapter) LeaderInfo() (string, string)  { return a.rs.LeaderInfo() }
func (a priorityClusterAdapter) Members() []priority.ClusterMember {
	src := a.rs.Members()
	out := make([]priority.ClusterMember, 0, len(src))
	for _, m := range src {
		out = append(out, priority.ClusterMember{
			NodeID:   m.NodeID,
			RaftAddr: m.RaftAddr,
			IsLeader: m.IsLeader,
		})
	}
	return out
}
func (a priorityClusterAdapter) LeadershipTransfer(targetID, targetAddr string) error {
	return a.rs.LeadershipTransfer(targetID, targetAddr)
}
