// Package priority drives leadership preference toward CH-connected
// voters (HA_PLAN3 §10). Each node refreshes its own
// `_team/ch_connected/<id>` flag on a 60s ticker; a separate loop
// on the LEADER only watches the cluster's flag distribution and,
// when leadership lives on a non-CH node while a CH-connected
// voter is healthy, calls LeadershipTransfer to nudge it.
//
// Best-effort. A non-CH leader works fine — analytics relay still
// routes events to whichever node has CH (Stage 3.6); the loop is
// a "nudge to preferred topology", never a hard constraint.
package priority

import (
	"context"
	"errors"
	"time"

	"github.com/tlalocweb/hulation/log"
	gossippkg "github.com/tlalocweb/hulation/pkg/gossip"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
)

var priorityLog = log.GetTaggedLogger("priority", "Leader priority loop")

// Cluster captures the slice of *raftbackend.RaftStorage methods
// the priority loop needs. Defining it here keeps the package
// import-cycle-free and trivial to mock.
type Cluster interface {
	IsLeader() bool
	LeaderInfo() (id, addr string)
	Members() []ClusterMember
	LeadershipTransfer(targetID, targetAddr string) error
}

// ClusterMember mirrors raftbackend.MemberInfo.
type ClusterMember struct {
	NodeID   string
	RaftAddr string
	IsLeader bool
}

const (
	// CHConnectedKeyPrefix mirrors the membership package's key
	// space so the FSM and the loop agree on where the flag lives.
	CHConnectedKeyPrefix = "_team/ch_connected/"

	// CHFlagTTL is how long a flag is considered fresh after the
	// owning node last refreshed it. HA_PLAN3 §10.1: 90s catches a
	// missed 60s refresh tick + jitter.
	CHFlagTTL = 90 * time.Second

	// DefaultRefreshInterval is the per-node CH-connected flag
	// refresh cadence.
	DefaultRefreshInterval = 60 * time.Second

	// DefaultLoopInterval is the cadence at which the leader
	// re-evaluates whether a transfer is warranted.
	DefaultLoopInterval = 60 * time.Second
)

// Config tunes the loop. Zero values pick the documented defaults.
type Config struct {
	NodeID            string
	IsCHConnected     bool
	RefreshInterval   time.Duration
	LoopInterval      time.Duration
	Storage           storagepkg.Storage
	Cluster           Cluster
}

// Loop is the running ticker handle. Stop() returns once both
// goroutines exit.
type Loop struct {
	cfg       Config
	stopRefresh chan struct{}
	stopLoop    chan struct{}
	cancel    context.CancelFunc
}

// Start launches the refresh + priority goroutines. Call Stop to
// terminate both cleanly.
func Start(ctx context.Context, cfg Config) (*Loop, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("priority: NodeID required")
	}
	if cfg.Storage == nil {
		return nil, errors.New("priority: Storage required")
	}
	if cfg.Cluster == nil {
		return nil, errors.New("priority: Cluster required")
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	if cfg.LoopInterval <= 0 {
		cfg.LoopInterval = DefaultLoopInterval
	}
	cctx, cancel := context.WithCancel(ctx)
	l := &Loop{
		cfg:         cfg,
		stopRefresh: make(chan struct{}),
		stopLoop:    make(chan struct{}),
		cancel:      cancel,
	}
	go l.refreshLoop(cctx)
	go l.priorityLoop(cctx)
	return l, nil
}

// Stop tears down both goroutines and waits for exit.
func (l *Loop) Stop() {
	l.cancel()
	<-l.stopRefresh
	<-l.stopLoop
}

// refreshLoop periodically writes the local node's CH-connected
// flag into the FSM. Writes timestamp + node id (the value's gob
// shape lets future loops read it back without scanning); the key
// existing at all is the convention for "this node is ch-connected".
//
// Stage 3.5's forwardToLeader makes this work even when this node
// isn't the leader — the Put forwards to the leader transparently.
func (l *Loop) refreshLoop(ctx context.Context) {
	defer close(l.stopRefresh)
	t := time.NewTicker(l.cfg.RefreshInterval)
	defer t.Stop()
	l.refreshOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.refreshOnce(ctx)
		}
	}
}

func (l *Loop) refreshOnce(ctx context.Context) {
	key := CHConnectedKeyPrefix + l.cfg.NodeID
	if !l.cfg.IsCHConnected {
		// Erase the flag if we previously had it. Best-effort;
		// missed deletes resolve when the next CH-connected node
		// refreshes its own flag and a future loop sees the ttl-
		// expired one.
		if err := l.cfg.Storage.Delete(ctx, key); err != nil {
			priorityLog.Debugf("ch_connected delete (%s): %v", l.cfg.NodeID, err)
		}
		return
	}
	value := encodeFlag(time.Now().UTC())
	if err := l.cfg.Storage.Put(ctx, key, value); err != nil {
		priorityLog.Warnf("ch_connected refresh failed: %v", err)
	}
}

// priorityLoop runs only when this node is the leader. Each tick
// it checks whether the current leader (us) is CH-connected; if
// not, it scans members for a CH-connected voter and asks Raft to
// transfer leadership.
func (l *Loop) priorityLoop(ctx context.Context) {
	defer close(l.stopLoop)
	t := time.NewTicker(l.cfg.LoopInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !l.cfg.Cluster.IsLeader() {
				continue
			}
			l.evaluateTransfer(ctx)
		}
	}
}

func (l *Loop) evaluateTransfer(ctx context.Context) {
	if l.cfg.IsCHConnected {
		return // we ARE CH-connected; stay leader.
	}
	candidate := l.findPreferredCandidate(ctx)
	if candidate.NodeID == "" {
		return // no CH-connected voter is healthy — keep current leadership.
	}
	priorityLog.Infof("transferring leadership to CH-connected node: %s (%s)", candidate.NodeID, candidate.RaftAddr)
	if err := l.cfg.Cluster.LeadershipTransfer(candidate.NodeID, candidate.RaftAddr); err != nil {
		priorityLog.Warnf("LeadershipTransfer to %s failed (will retry next tick): %v", candidate.NodeID, err)
	}
}

// findPreferredCandidate scans cluster members and returns the
// first CH-connected voter (other than the local node) whose flag
// is fresh. Returns a zero-value member when none qualify.
func (l *Loop) findPreferredCandidate(ctx context.Context) ClusterMember {
	for _, m := range l.cfg.Cluster.Members() {
		if m.NodeID == l.cfg.NodeID {
			continue
		}
		if m.RaftAddr == "" {
			continue
		}
		v, err := l.cfg.Storage.Get(ctx, CHConnectedKeyPrefix+m.NodeID)
		if err != nil {
			continue
		}
		if !flagFresh(v) {
			continue
		}
		return m
	}
	return ClusterMember{}
}

// encodeFlag serialises a timestamp as the bbolt flag value. The
// on-disk form is an RFC3339Nano string (e.g.
// "2026-05-08T06:41:05.123456789Z") with no trailing newline —
// flagFresh round-trips it via time.Parse. We intentionally avoid
// gob/proto here: this key is read thousands of times per cluster
// lifetime and a textual format keeps it human-inspectable in
// bbolt dumps.
func encodeFlag(t time.Time) []byte {
	return []byte(t.Format(time.RFC3339Nano))
}

func flagFresh(v []byte) bool {
	if len(v) == 0 {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, string(v))
	if err != nil {
		return false
	}
	return time.Since(t) < CHFlagTTL
}

// PreferenceFromGossip is a convenience the read path uses to
// derive a node's CH-connected status from a gossip store-read.
// Today the priority loop persists into the Raft FSM; future
// stages may switch this onto the gossip layer. Documenting the
// seam here so the move is a one-liner.
func PreferenceFromGossip(ctx context.Context, store *gossippkg.Store, nodeID string) bool {
	_ = ctx
	_ = store
	_ = nodeID
	return false
}
