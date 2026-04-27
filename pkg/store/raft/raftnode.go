package raft

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb"
	"go.etcd.io/bbolt"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/common"
	"github.com/tlalocweb/hulation/pkg/tune"
)

// ErrCASConflict is returned when a compare-and-swap operation fails due to concurrent modification.
// This is an expected error that callers should handle with retry logic.
var ErrCASConflict = errors.New("CAS conflict")

var (
	// verboseLevel controls whether to show BoltDB rollback errors
	// Set to 0 by default, can be changed via SetVerboseLevel
	verboseLevel int
)

// truncateBytes returns a string representation of bytes, truncated if too long
func truncateBytes(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return fmt.Sprintf("%v", b)
	}
	return fmt.Sprintf("%v... +%d bytes", b[:maxLen], len(b)-maxLen)
}

// SetVerboseLevel sets the verbose level for Raft logging
// If verboseLevel > 1, BoltDB rollback errors will be logged as debug messages
func SetVerboseLevel(level int) {
	verboseLevel = level
}

func init() {
	// Filter BoltDB's standard logger to suppress benign "Rollback failed: tx closed" errors
	// These are internal errors from raft-boltdb's transaction management that don't affect functionality
	// We still allow other log messages through
	stdlog.SetOutput(&filteredWriter{})
}

// filteredWriter filters out benign BoltDB rollback error messages
type filteredWriter struct{}

func (w *filteredWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	// If verbose level > 1, log rollback errors as debug messages
	if strings.Contains(msg, "Rollback failed") {
		if verboseLevel > 1 {
			raftLogger.Debugf("BoltDB (benign): %s", strings.TrimSpace(msg))
		}
		// Pretend we wrote it successfully (discard from stderr)
		return len(p), nil
	}
	// Pass through other messages to stderr
	return os.Stderr.Write(p)
}

var raftLogger = log.GetTaggedLogger("raft", "Raft consensus")

// RaftNode manages the Raft consensus and state machine
type RaftNode struct {
	raft           *raft.Raft
	fsm            *FSM
	config         *RaftConfig
	logger         *log.TaggedLogger
	shutdown       chan struct{}
	barrierOnApply bool // If true, Apply() waits for FSM application via Barrier()
}

// FSM implements the Raft finite state machine
type FSM struct {
	backend   *BoltBackend
	mu        sync.RWMutex
	logger    *log.TaggedLogger
	observers []chan<- *common.Update
}

// NewRaftNode creates a new Raft node
func NewRaftNode(config *RaftConfig, backend *BoltBackend) (*RaftNode, error) {
	raftLogger.Infof("Creating Raft node with ID: %s", config.NodeID)

	// Create FSM
	fsm := &FSM{
		backend:   backend,
		logger:    raftLogger,
		observers: make([]chan<- *common.Update, 0),
	}

	// Setup Raft configuration
	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(config.NodeID)

	// For single-node clusters, use faster timeouts to enable quick leader election
	if config.Bootstrap && config.JoinAddr == "" {
		raftConfig.HeartbeatTimeout = 1000 * time.Millisecond
		raftConfig.ElectionTimeout = 1000 * time.Millisecond
		raftConfig.LeaderLeaseTimeout = 500 * time.Millisecond
		raftConfig.CommitTimeout = 500 * time.Millisecond
		raftLogger.Debugf("Using fast timeouts for single-node bootstrap")
	}

	// Set snapshot interval if configured
	if config.SnapshotInterval > 0 {
		raftConfig.SnapshotInterval = time.Duration(config.SnapshotInterval) * time.Second
	}

	// Create the snapshot store
	snapshotStore, err := raft.NewFileSnapshotStore(config.RaftDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	// Create the log store using raft-boltdb
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(config.RaftDir, "raft-log.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create log store: %w", err)
	}

	// Create the stable store using raft-boltdb
	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(config.RaftDir, "raft-stable.db"))
	if err != nil {
		logStore.Close()
		return nil, fmt.Errorf("failed to create stable store: %w", err)
	}

	// Setup Raft transport
	addr, err := net.ResolveTCPAddr("tcp", config.BindAddr)
	if err != nil {
		logStore.Close()
		stableStore.Close()
		return nil, fmt.Errorf("failed to resolve bind address: %w", err)
	}

	transport, err := raft.NewTCPTransport(config.BindAddr, addr, tune.GetRaftTransportMaxPool(), tune.GetRaftTransportTimeout(), os.Stderr)
	if err != nil {
		logStore.Close()
		stableStore.Close()
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	// Create the Raft system
	ra, err := raft.NewRaft(raftConfig, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		transport.Close()
		logStore.Close()
		stableStore.Close()
		return nil, fmt.Errorf("failed to create raft: %w", err)
	}

	node := &RaftNode{
		raft:           ra,
		fsm:            fsm,
		config:         config,
		logger:         raftLogger,
		shutdown:       make(chan struct{}),
		barrierOnApply: config.BarrierOnApply,
	}

	// Bootstrap cluster if configured
	if config.Bootstrap {
		raftLogger.Infof("Bootstrapping new Raft cluster")
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      raft.ServerID(config.NodeID),
					Address: transport.LocalAddr(),
					Suffrage: raft.Voter,
				},
			},
		}
		future := ra.BootstrapCluster(configuration)
		if err := future.Error(); err != nil {
			// If already bootstrapped, that's ok - just log it
			if err != raft.ErrCantBootstrap {
				raftLogger.Warnf("Bootstrap returned error (may be already bootstrapped): %v", err)
			}
		} else {
			raftLogger.Infof("Cluster bootstrap initiated successfully")
		}
	}

	// Join existing cluster if configured
	if config.JoinAddr != "" {
		raftLogger.Infof("Attempting to join cluster at: %s", config.JoinAddr)
		// Note: In a production system, you would implement a join RPC
		// to have the leader add this node to the cluster
	}

	raftLogger.Infof("Raft node created successfully")
	return node, nil
}

// Apply applies a command to the Raft log
func (r *RaftNode) Apply(cmd *Command) error {
	// Only leader can apply commands
	if r.raft.State() != raft.Leader {
		return fmt.Errorf("not the leader")
	}

	// Serialize command
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Apply to Raft log
	future := r.raft.Apply(data, tune.GetRaftApplyTimeout())
	if err := future.Error(); err != nil {
		return fmt.Errorf("failed to apply command: %w", err)
	}

	// Wait for FSM to apply all previous log entries if configured
	// This ensures read-after-write consistency by guaranteeing the FSM
	// has applied the command before we return to the caller
	if r.barrierOnApply {
		barrierFuture := r.raft.Barrier(tune.GetRaftBarrierTimeout())
		if err := barrierFuture.Error(); err != nil {
			return fmt.Errorf("failed to wait for FSM apply: %w", err)
		}
	}

	// Check if FSM returned an error
	if resp := future.Response(); resp != nil {
		if err, ok := resp.(error); ok {
			return err
		}
	}

	return nil
}

// IsLeader returns true if this node is the leader
func (r *RaftNode) IsLeader() bool {
	return r.raft.State() == raft.Leader
}

// Leader returns the address of the current leader
func (r *RaftNode) Leader() string {
	addr, _ := r.raft.LeaderWithID()
	return string(addr)
}

// AddObserver adds a channel to receive state change notifications
func (r *RaftNode) AddObserver(ch chan<- *common.Update) {
	r.fsm.mu.Lock()
	defer r.fsm.mu.Unlock()
	r.fsm.observers = append(r.fsm.observers, ch)
}

// RemoveObserver removes a notification channel
func (r *RaftNode) RemoveObserver(ch chan<- *common.Update) {
	r.fsm.mu.Lock()
	defer r.fsm.mu.Unlock()

	for i, observer := range r.fsm.observers {
		if observer == ch {
			r.fsm.observers = append(r.fsm.observers[:i], r.fsm.observers[i+1:]...)
			break
		}
	}
}

// Close shuts down the Raft node
func (r *RaftNode) Close() error {
	r.logger.Infof("Shutting down Raft node")
	close(r.shutdown)

	future := r.raft.Shutdown()
	if err := future.Error(); err != nil {
		return fmt.Errorf("failed to shutdown raft: %w", err)
	}

	return nil
}

// FSM Apply applies a log entry to the FSM
func (f *FSM) Apply(l *raft.Log) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		f.logger.Errorf("Failed to unmarshal command: %v", err)
		return err
	}

	var err error
	switch cmd.Op {
	case OpPut:
		err = f.backend.Put(cmd.Key, cmd.Value)
		if err == nil {
			f.notifyObservers(cmd.Key, cmd.Value)
		}

	case OpDelete:
		err = f.backend.Delete(cmd.Key)
		if err == nil {
			f.notifyObservers(cmd.Key, nil)
		}

	case OpPutBatch:
		// Apply all puts in a single transaction (ACID)
		err = f.applyPutBatch(&cmd)

	case OpDeletePrefix:
		err = f.backend.DeletePrefix(cmd.Key)
		if err == nil {
			f.notifyObservers(cmd.Key, nil)
		}

	case OpMutateObjects:
		// Apply all mutations in a single transaction (ACID)
		err = f.applyMutations(cmd.Mutations)

	case OpCompareAndSwap:
		// Apply compare-and-swap operation
		err = f.applyCompareAndSwap(&cmd)

	case OpBatchUpdate:
		// Apply batch update with index management
		err = f.applyBatchUpdate(cmd.BatchUpdates)

	default:
		err = fmt.Errorf("unknown command operation: %d", cmd.Op)
	}

	if err != nil {
		// CAS conflicts are expected during concurrent updates and will be retried by the driver
		// Log at DEBUG level to avoid noisy ERR logs
		if errors.Is(err, ErrCASConflict) {
			f.logger.Debugf("CAS conflict (will retry): %v", err)
		} else {
			f.logger.Errorf("Failed to apply command: %v", err)
		}
		return err
	}

	return nil
}

// applyMutations applies multiple mutations in a single ACID transaction
func (f *FSM) applyMutations(mutations []common.MutateTuple) error {
	// Track notifications to send after successful commit
	type notification struct {
		key   string
		value []byte
	}
	notifications := make([]notification, 0, len(mutations))

	// Execute all mutations in a single BoltDB transaction
	err := f.backend.ExecuteInTransaction(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		for _, mut := range mutations {
			// Get current value from transaction
			currentData := bucket.Get([]byte(mut.Key))

			// Copy data since it's only valid during transaction
			var dataCopy []byte
			if currentData != nil {
				dataCopy = make([]byte, len(currentData))
				copy(dataCopy, currentData)
			}

			// Create unwrappable
			unwrappable := common.NewUnwrappable(dataCopy)

			// Apply mutator
			err, doDelete := mut.Mutate(mut.Key, unwrappable)
			if err != nil {
				return fmt.Errorf("mutation failed for key %s: %w", mut.Key, err)
			}

			if doDelete {
				if err := bucket.Delete([]byte(mut.Key)); err != nil {
					return fmt.Errorf("failed to delete key %s: %w", mut.Key, err)
				}
				notifications = append(notifications, notification{key: mut.Key, value: nil})
			} else {
				// Get serialized data and store
				newData, err := unwrappable.GetSerialData()
				if err != nil {
					return fmt.Errorf("failed to serialize data for key %s: %w", mut.Key, err)
				}
				if err := bucket.Put([]byte(mut.Key), newData); err != nil {
					return fmt.Errorf("failed to put key %s: %w", mut.Key, err)
				}
				notifications = append(notifications, notification{key: mut.Key, value: newData})
			}
		}

		return nil
	})

	// Only notify observers if transaction succeeded
	if err == nil {
		for _, notif := range notifications {
			f.notifyObservers(notif.key, notif.value)
		}
		f.logger.Debugf("Successfully applied %d mutations atomically", len(mutations))
	}

	return err
}

// applyCompareAndSwap performs an atomic compare-and-swap operation
func (f *FSM) applyCompareAndSwap(cmd *Command) error {
	return f.backend.ExecuteInTransaction(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		// Get current value from BoltDB
		currentValue := bucket.Get([]byte(cmd.Key))

		// Copy currentValue since it's only valid during the transaction
		var currentCopy []byte
		if currentValue != nil {
			currentCopy = make([]byte, len(currentValue))
			copy(currentCopy, currentValue)
		}

		// Check if we expect nil (key doesn't exist)
		if cmd.ExpectedNil {
			if currentCopy != nil {
				f.logger.Debugf("CAS failed for key %s: key exists (expected nil)", cmd.Key)
				return fmt.Errorf("CAS failed: key exists (expected nil)")
			}
			// Key doesn't exist as expected, proceed with put
			if err := bucket.Put([]byte(cmd.Key), cmd.Value); err != nil {
				return fmt.Errorf("failed to put key %s: %w", cmd.Key, err)
			}
			f.logger.Debugf("CAS succeeded: created new key %s", cmd.Key)
			return nil
		}

		// Check if current value matches expected value
		if currentCopy == nil {
			f.logger.Debugf("CAS failed for key %s: key does not exist (expected value)", cmd.Key)
			return fmt.Errorf("%w: key does not exist (expected value)", ErrCASConflict)
		}

		if !bytesEqual(currentCopy, cmd.ExpectedValue) {
			f.logger.Debugf("CAS failed for key %s: value mismatch (current=%s, expected=%s)",
				cmd.Key, truncateBytes(currentCopy, 32), truncateBytes(cmd.ExpectedValue, 32))
			return fmt.Errorf("%w: value mismatch", ErrCASConflict)
		}

		// Value matches, proceed with update
		if err := bucket.Put([]byte(cmd.Key), cmd.Value); err != nil {
			return fmt.Errorf("failed to put key %s: %w", cmd.Key, err)
		}

		f.logger.Debugf("CAS succeeded: updated key %s from %s to %s", cmd.Key, truncateBytes(currentCopy, 32), truncateBytes(cmd.Value, 32))
		return nil
	})
}

// bytesEqual compares two byte slices
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// applyPutBatch applies a batch of puts in a single ACID transaction
func (f *FSM) applyPutBatch(cmd *Command) error {
	// Track notifications to send after successful commit
	type notification struct {
		key   string
		value []byte
	}
	notifications := make([]notification, 0, len(cmd.Batch))

	// Execute all puts in a single BoltDB transaction
	err := f.backend.ExecuteInTransaction(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		for key, value := range cmd.Batch {
			fullKey := cmd.Key + "/" + key
			if err := bucket.Put([]byte(fullKey), value); err != nil {
				return fmt.Errorf("failed to put key %s: %w", fullKey, err)
			}
			notifications = append(notifications, notification{key: fullKey, value: value})
		}

		return nil
	})

	// Only notify observers if transaction succeeded
	if err == nil {
		for _, notif := range notifications {
			f.notifyObservers(notif.key, notif.value)
		}
		f.logger.Debugf("Successfully applied batch put with %d keys atomically", len(cmd.Batch))
	}

	return err
}

// applyBatchUpdate applies a batch of object updates with index management in a single ACID transaction
func (f *FSM) applyBatchUpdate(updates []BatchObjectUpdate) error {
	// Track notifications to send after successful commit
	type notification struct {
		key   string
		value []byte
	}
	notifications := make([]notification, 0, len(updates))

	// Execute all updates in a single BoltDB transaction
	err := f.backend.ExecuteInTransaction(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(DefaultBucket))
		if bucket == nil {
			return fmt.Errorf("bucket not found")
		}

		for _, update := range updates {
			if update.Delete {
				// Delete the object
				if err := bucket.Delete([]byte(update.Key)); err != nil {
					return fmt.Errorf("failed to delete key %s: %w", update.Key, err)
				}
				notifications = append(notifications, notification{key: update.Key, value: nil})
			} else {
				// Put the object
				if err := bucket.Put([]byte(update.Key), update.Value); err != nil {
					return fmt.Errorf("failed to put key %s: %w", update.Key, err)
				}
				notifications = append(notifications, notification{key: update.Key, value: update.Value})
			}

			// Handle index additions
			for i, indexPath := range update.IndexesToAdd {
				if i >= len(update.IndexVals) {
					return fmt.Errorf("mismatch between IndexesToAdd and IndexVals for key %s", update.Key)
				}
				indexVal := update.IndexVals[i]
				if err := bucket.Put([]byte(indexPath), []byte(indexVal)); err != nil {
					return fmt.Errorf("failed to add index %s: %w", indexPath, err)
				}
			}

			// Handle index deletions
			for _, indexPath := range update.IndexesToDel {
				if err := bucket.Delete([]byte(indexPath)); err != nil {
					// Log but don't fail if index doesn't exist
					f.logger.Debugf("Index %s not found during deletion (may be expected)", indexPath)
				}
			}
		}

		return nil
	})

	// Only notify observers if transaction succeeded
	if err == nil {
		for _, notif := range notifications {
			f.notifyObservers(notif.key, notif.value)
		}
		f.logger.Debugf("Successfully applied batch update with %d objects atomically", len(updates))
	}

	return err
}

// notifyObservers sends update notifications to all observers
func (f *FSM) notifyObservers(key string, value []byte) {
	update := &common.Update{
		Key:   key,
		Value: value,
	}

	for _, observer := range f.observers {
		select {
		case observer <- update:
		default:
			// Don't block if observer is slow
			f.logger.Warnf("Observer channel full, dropping update for key: %s", key)
		}
	}
}

// Snapshot returns a snapshot of the FSM state
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	f.logger.Debugf("Creating FSM snapshot")

	// Create snapshot from BoltDB
	data, err := f.backend.Snapshot()
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	return &fsmSnapshot{data: data, logger: f.logger}, nil
}

// Restore restores the FSM from a snapshot
func (f *FSM) Restore(rc io.ReadCloser) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.logger.Infof("Restoring FSM from snapshot")

	// Read all snapshot data
	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("failed to read snapshot: %w", err)
	}

	// Restore to BoltDB
	if err := f.backend.Restore(data); err != nil {
		return fmt.Errorf("failed to restore snapshot: %w", err)
	}

	f.logger.Infof("FSM restored successfully")
	return nil
}

// fsmSnapshot implements the raft.FSMSnapshot interface
type fsmSnapshot struct {
	data   []byte
	logger *log.TaggedLogger
}

// Persist writes the snapshot to the given sink
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	s.logger.Debugf("Persisting snapshot (%d bytes)", len(s.data))

	// Write snapshot data
	if _, err := sink.Write(s.data); err != nil {
		sink.Cancel()
		return fmt.Errorf("failed to write snapshot: %w", err)
	}

	// Close the sink
	if err := sink.Close(); err != nil {
		return fmt.Errorf("failed to close snapshot sink: %w", err)
	}

	s.logger.Debugf("Snapshot persisted successfully")
	return nil
}

// Release is called when we're finished with the snapshot
func (s *fsmSnapshot) Release() {
	// Nothing to release in our case
}

// Join adds a new node to the Raft cluster
func (r *RaftNode) Join(nodeID, addr string) error {
	r.logger.Infof("Adding node %s at %s to cluster", nodeID, addr)

	if r.raft.State() != raft.Leader {
		return fmt.Errorf("not the leader")
	}

	configFuture := r.raft.GetConfiguration()
	if err := configFuture.Error(); err != nil {
		return fmt.Errorf("failed to get raft configuration: %w", err)
	}

	// Check if node already exists
	for _, srv := range configFuture.Configuration().Servers {
		if srv.ID == raft.ServerID(nodeID) {
			if srv.Address == raft.ServerAddress(addr) {
				r.logger.Warnf("Node %s already in cluster with same address", nodeID)
				return nil
			}
			// Remove old node first
			removeFuture := r.raft.RemoveServer(srv.ID, 0, 0)
			if err := removeFuture.Error(); err != nil {
				return fmt.Errorf("failed to remove old node: %w", err)
			}
		}
	}

	// Add the new node
	addFuture := r.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, 0)
	if err := addFuture.Error(); err != nil {
		return fmt.Errorf("failed to add voter: %w", err)
	}

	r.logger.Infof("Node %s added successfully", nodeID)
	return nil
}

// Remove removes a node from the Raft cluster
func (r *RaftNode) Remove(nodeID string) error {
	r.logger.Infof("Removing node %s from cluster", nodeID)

	if r.raft.State() != raft.Leader {
		return fmt.Errorf("not the leader")
	}

	removeFuture := r.raft.RemoveServer(raft.ServerID(nodeID), 0, 0)
	if err := removeFuture.Error(); err != nil {
		return fmt.Errorf("failed to remove server: %w", err)
	}

	r.logger.Infof("Node %s removed successfully", nodeID)
	return nil
}

// Stats returns Raft statistics
func (r *RaftNode) Stats() map[string]string {
	return r.raft.Stats()
}
