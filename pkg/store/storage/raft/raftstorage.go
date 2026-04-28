// Package raftbackend implements storage.Storage on top of
// hashicorp/raft. Single-node clusters are the most common
// production shape (= "solo" mode); multi-node clusters share
// the same code path with peers added via raft.AddVoter.
//
// Design notes live in HA_PLAN2.md. Reference impl: ../izcr.
//
// Reads (Get / List / Keys / Watch) hit the FSM directly without
// going through the Raft log — they're cheap and a stale read on
// a follower is no worse than a stale read on a single-node
// cluster's main goroutine. Writes (Put / Delete / CAS / Mutate
// / Batch) are proposed to the Raft log and applied by every
// node's FSM in the same order.
package raftbackend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

var raftLog = log.GetTaggedLogger("storage-raft", "Raft-backed storage")

// Default tunables. Operators can override via Config.
const (
	defaultApplyTimeout    = 10 * time.Second
	defaultBarrierTimeout  = 10 * time.Second
	defaultMutateRetries   = 50
	defaultLeaderWaitDelay = 50 * time.Millisecond
)

// Config bundles every knob the Raft backend exposes. Most
// operators will only set NodeID and DataDir; the rest have
// sensible defaults for solo mode.
type Config struct {
	// NodeID is unique within the Team. Default = hostname.
	NodeID string

	// DataDir is the root for FSM bolt + raft log + raft stable
	// + snapshots. Sub-paths:
	//   <DataDir>/data.db          — FSM state (bbolt)
	//   <DataDir>/raft-log.db      — raft log
	//   <DataDir>/raft-stable.db   — raft stable store
	//   <DataDir>/snapshots/       — raft snapshots
	DataDir string

	// BindAddr is the TCP address the raft transport listens
	// on. Solo mode uses "127.0.0.1:0" so the kernel picks an
	// ephemeral port and remote peers can't reach us. Stage 3
	// flips this to a routable address.
	BindAddr string

	// AdvertiseAddr is what other peers see in raft
	// configuration. Solo mode leaves this empty so
	// transport.LocalAddr() is used. Stage 3 surfaces it.
	AdvertiseAddr string

	// Bootstrap, if true, calls raft.BootstrapCluster on first
	// run with a single-server config. Idempotent — subsequent
	// runs detect existing state and skip.
	Bootstrap bool

	// SnapshotInterval / SnapshotThreshold are passed straight
	// through to raft.Config. Solo defaults: 5 minutes / 8192
	// log entries.
	SnapshotInterval  time.Duration
	SnapshotThreshold uint64

	// ApplyTimeout / BarrierTimeout cap how long Apply waits.
	// Defaults: 10s / 10s.
	ApplyTimeout   time.Duration
	BarrierTimeout time.Duration

	// MaxMutateRetries caps the optimistic CAS retry count for
	// Storage.Mutate. Default: 50 (matches izcr).
	MaxMutateRetries int

	// Logger lets callers plug in their own hclog. Defaults to
	// a wrapper around the hula tagged logger.
	Logger hclog.Logger
}

// applyDefaults fills in sensible defaults for unset fields.
func (c *Config) applyDefaults() {
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 5 * time.Minute
	}
	if c.SnapshotThreshold == 0 {
		c.SnapshotThreshold = 8192
	}
	if c.ApplyTimeout == 0 {
		c.ApplyTimeout = defaultApplyTimeout
	}
	if c.BarrierTimeout == 0 {
		c.BarrierTimeout = defaultBarrierTimeout
	}
	if c.MaxMutateRetries == 0 {
		c.MaxMutateRetries = defaultMutateRetries
	}
	if c.NodeID == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			c.NodeID = h
		} else {
			c.NodeID = "node"
		}
	}
	if c.BindAddr == "" {
		c.BindAddr = "127.0.0.1:0"
	}
	if c.Logger == nil {
		c.Logger = hclog.New(&hclog.LoggerOptions{
			Name:   "raft",
			Level:  hclog.Warn,
			Output: hclogShim{},
		})
	}
}

// RaftStorage implements storage.Storage on top of hashicorp/raft.
type RaftStorage struct {
	cfg Config

	mu     sync.RWMutex
	closed bool

	raft      *raft.Raft
	fsm       *FSM
	transport *raft.NetworkTransport
	logStore  *raftboltdb.BoltStore
	stable    *raftboltdb.BoltStore
	snaps     raft.SnapshotStore
}

// New constructs and starts a RaftStorage. Returns once the
// raft.Raft instance is constructed; callers wanting to wait for
// leadership should follow up with WaitLeader.
func New(cfg Config) (*RaftStorage, error) {
	cfg.applyDefaults()

	if cfg.DataDir == "" {
		return nil, errors.New("raftbackend: Config.DataDir required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("raftbackend: mkdir %s: %w", cfg.DataDir, err)
	}

	fsm, err := newFSM(filepath.Join(cfg.DataDir, "data.db"))
	if err != nil {
		return nil, err
	}

	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-log.db"))
	if err != nil {
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: log store: %w", err)
	}
	stable, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-stable.db"))
	if err != nil {
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: stable store: %w", err)
	}

	snapsDir := filepath.Join(cfg.DataDir, "snapshots")
	if err := os.MkdirAll(snapsDir, 0o755); err != nil {
		_ = stable.Close()
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: snapshot dir: %w", err)
	}
	snaps, err := raft.NewFileSnapshotStoreWithLogger(snapsDir, 3, cfg.Logger.Named("snapshot"))
	if err != nil {
		_ = stable.Close()
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: snapshot store: %w", err)
	}

	bindTCP, err := net.ResolveTCPAddr("tcp", cfg.BindAddr)
	if err != nil {
		_ = stable.Close()
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: resolve %s: %w", cfg.BindAddr, err)
	}
	advertise := bindTCP
	if cfg.AdvertiseAddr != "" {
		adv, err := net.ResolveTCPAddr("tcp", cfg.AdvertiseAddr)
		if err != nil {
			_ = stable.Close()
			_ = logStore.Close()
			_ = fsm.close()
			return nil, fmt.Errorf("raftbackend: resolve advertise %s: %w", cfg.AdvertiseAddr, err)
		}
		advertise = adv
	}
	transport, err := raft.NewTCPTransportWithLogger(cfg.BindAddr, advertise, 3, 10*time.Second, cfg.Logger.Named("transport"))
	if err != nil {
		_ = stable.Close()
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: transport: %w", err)
	}

	rcfg := raft.DefaultConfig()
	rcfg.LocalID = raft.ServerID(cfg.NodeID)
	rcfg.SnapshotInterval = cfg.SnapshotInterval
	rcfg.SnapshotThreshold = cfg.SnapshotThreshold
	rcfg.Logger = cfg.Logger

	r, err := raft.NewRaft(rcfg, fsm, logStore, stable, snaps, transport)
	if err != nil {
		_ = transport.Close()
		_ = stable.Close()
		_ = logStore.Close()
		_ = fsm.close()
		return nil, fmt.Errorf("raftbackend: raft.NewRaft: %w", err)
	}

	if cfg.Bootstrap {
		hasState, err := raft.HasExistingState(logStore, stable, snaps)
		if err != nil {
			_ = r.Shutdown().Error()
			_ = transport.Close()
			_ = stable.Close()
			_ = logStore.Close()
			_ = fsm.close()
			return nil, fmt.Errorf("raftbackend: HasExistingState: %w", err)
		}
		if !hasState {
			conf := raft.Configuration{
				Servers: []raft.Server{{
					ID:       rcfg.LocalID,
					Address:  transport.LocalAddr(),
					Suffrage: raft.Voter,
				}},
			}
			if err := r.BootstrapCluster(conf).Error(); err != nil {
				if !errors.Is(err, raft.ErrCantBootstrap) {
					_ = r.Shutdown().Error()
					_ = transport.Close()
					_ = stable.Close()
					_ = logStore.Close()
					_ = fsm.close()
					return nil, fmt.Errorf("raftbackend: bootstrap: %w", err)
				}
			}
		}
	}

	rs := &RaftStorage{
		cfg:       cfg,
		raft:      r,
		fsm:       fsm,
		transport: transport,
		logStore:  logStore,
		stable:    stable,
		snaps:     snaps,
	}
	raftLog.Infof("raft storage online (node=%s, addr=%s, bootstrap=%t)", cfg.NodeID, transport.LocalAddr(), cfg.Bootstrap)
	return rs, nil
}

// WaitLeader polls until this node sees itself as the leader (or
// observes a leader exists) — solo bootstrap usually completes in
// well under a second. Returns ctx.Err() on timeout.
func (s *RaftStorage) WaitLeader(ctx context.Context) error {
	for {
		state := s.raft.State()
		if state == raft.Leader {
			return nil
		}
		if leader, _ := s.raft.LeaderWithID(); leader != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("raftbackend: wait for leader: %w", ctx.Err())
		case <-time.After(defaultLeaderWaitDelay):
		}
	}
}

// IsLeader is a thin wrapper around raft.State == Leader. Used
// by Mutate / Apply paths to short-circuit the leader check.
func (s *RaftStorage) IsLeader() bool {
	return s.raft.State() == raft.Leader
}

// RaftForTest exposes the underlying *raft.Raft so tests can
// trigger snapshots, observe leadership, etc. Not intended for
// production use; the production code talks through the
// Storage interface only.
func (s *RaftStorage) RaftForTest() *raft.Raft { return s.raft }

// Close shuts the Raft node down cleanly. Safe to call multiple
// times.
func (s *RaftStorage) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if err := s.raft.Shutdown().Error(); err != nil {
		raftLog.Warnf("raft shutdown: %v", err)
	}
	if s.transport != nil {
		_ = s.transport.Close()
	}
	if s.logStore != nil {
		_ = s.logStore.Close()
	}
	if s.stable != nil {
		_ = s.stable.Close()
	}
	return s.fsm.close()
}

// --- storage.Storage --------------------------------------------

func (s *RaftStorage) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return storage.ErrClosed
	}
	return nil
}

func (s *RaftStorage) Get(_ context.Context, key string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.fsm.localGet(key)
}

func (s *RaftStorage) Put(ctx context.Context, key string, value []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.applyAsLeader(ctx, newPutCmd(s.cfg.NodeID, key, value))
}

func (s *RaftStorage) Delete(ctx context.Context, key string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.applyAsLeader(ctx, newDeleteCmd(s.cfg.NodeID, key))
}

func (s *RaftStorage) List(_ context.Context, prefix string) (map[string][]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.fsm.localList(prefix)
}

func (s *RaftStorage) Keys(_ context.Context, prefix string) ([]string, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.fsm.localKeys(prefix)
}

func (s *RaftStorage) CompareAndSwap(ctx context.Context, key string, expected, fresh []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.applyAsLeader(ctx, newCASCmd(s.cfg.NodeID, key, expected, fresh))
}

func (s *RaftStorage) CompareAndCreate(ctx context.Context, key string, value []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	return s.applyAsLeader(ctx, newCASCreateCmd(s.cfg.NodeID, key, value))
}

func (s *RaftStorage) Mutate(ctx context.Context, key string, fn storage.MutateFn) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if !s.IsLeader() {
		return s.forwardToLeader(ctx, "mutate", key)
	}
	for retry := 0; retry < s.cfg.MaxMutateRetries; retry++ {
		current, err := s.fsm.localGet(key)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		var currentForFn []byte
		if !errors.Is(err, storage.ErrNotFound) {
			currentForFn = current
		}
		next, mErr := fn(currentForFn)
		if errors.Is(mErr, storage.ErrSkip) {
			return nil
		}
		if mErr != nil {
			return mErr
		}

		var cmd *Command
		if next == nil {
			// Delete-via-CAS.
			if currentForFn == nil {
				// Mutator wants to delete a missing key — already gone.
				return nil
			}
			cmd = newCASDeleteCmd(s.cfg.NodeID, key, current)
		} else if currentForFn == nil {
			cmd = newCASCreateCmd(s.cfg.NodeID, key, next)
		} else {
			cmd = newCASCmd(s.cfg.NodeID, key, current, next)
		}

		if err := s.applyAsLeader(ctx, cmd); err == nil {
			return nil
		} else if !errors.Is(err, storage.ErrCASFailed) {
			return err
		}
		// CAS lost a race; another writer mutated between our
		// read and our apply. Retry.
	}
	return fmt.Errorf("raftbackend: mutate %q: too many retries", key)
}

func (s *RaftStorage) Batch(ctx context.Context, ops []storage.BatchOp) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	return s.applyAsLeader(ctx, newBatchCmd(s.cfg.NodeID, ops))
}

func (s *RaftStorage) Watch(ctx context.Context, prefix string) (<-chan storage.Event, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.fsm.informer.subscribe(ctx, prefix), nil
}

// --- internals --------------------------------------------------

// applyAsLeader marshals cmd and submits it to raft.Apply. Only
// callable from the leader (Stage 2 has only solo so this is
// always the leader; Stage 3 will route through forwardToLeader
// for follower nodes).
func (s *RaftStorage) applyAsLeader(ctx context.Context, cmd *Command) error {
	if !s.IsLeader() {
		return s.forwardToLeader(ctx, "apply", cmd.GetKey())
	}
	data, err := encodeCommand(cmd)
	if err != nil {
		return err
	}
	timeout := s.cfg.ApplyTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain < timeout && remain > 0 {
			timeout = remain
		}
	}
	future := s.raft.Apply(data, timeout)
	if err := future.Error(); err != nil {
		return fmt.Errorf("raftbackend: apply: %w", err)
	}
	resp := future.Response()
	if ar, ok := resp.(*ApplyResult); ok {
		return applyResultErr(ar)
	}
	if resp != nil {
		return fmt.Errorf("raftbackend: unexpected apply response %T", resp)
	}
	return nil
}

// forwardToLeader is a Stage 3 placeholder. In solo mode every
// node IS the leader, so this is unreachable; we return a clear
// error so the call site is obvious if it does fire.
func (s *RaftStorage) forwardToLeader(_ context.Context, op, key string) error {
	leader, _ := s.raft.LeaderWithID()
	return fmt.Errorf("raftbackend: %s on key %q must run on leader (current leader: %q) — multi-node forwarding lands in Stage 3", op, key, leader)
}

// --- hclog ↔ tagged-log shim ------------------------------------

// hclogShim translates hclog output to the hula tagged logger so
// raft's verbose logs honor hula's log level configuration.
type hclogShim struct{}

func (hclogShim) Write(p []byte) (int, error) {
	raftLog.Debugf("%s", string(p))
	return len(p), nil
}
