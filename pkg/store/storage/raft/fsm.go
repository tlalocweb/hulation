package raftbackend

// FSM — the deterministic state machine that hashicorp/raft
// drives. It owns a bbolt file at <DataDir>/data.db and a
// dedicated informer for Watch fan-out. Every Raft log entry
// flows through Apply; followers re-evaluate CAS commands
// against their local FSM state and produce the same result as
// the leader because the log is total-ordered.
//
// On Snapshot we stream the entire bbolt file (raw bytes) into
// the snapshot store; Restore writes those bytes to a temp file
// and atomically renames over data.db.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/raft"
	bolt "go.etcd.io/bbolt"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
)

var fsmLog = log.GetTaggedLogger("storage-raft-fsm", "Raft FSM")

// FSM implements raft.FSM on top of a bbolt file. Owned by the
// containing RaftStorage; not safe to use directly.
type FSM struct {
	// dbPath is the absolute path to data.db. Held so Restore
	// can atomically replace the file.
	dbPath string

	// mu protects the db pointer itself — Restore swaps it out.
	// Reads (Get/List/Keys) take RLock; writes (Apply) take
	// Lock; Restore takes Lock.
	mu sync.RWMutex
	db *bolt.DB

	// informer fans Apply mutations out to Watch subscribers.
	informer *informer

	// reportedMu / reportedUnrouted matches LocalStorage's
	// behaviour: warn once per unrouted key, never spam.
	reportedMu       sync.Mutex
	reportedUnrouted map[string]bool
}

// newFSM opens (or creates) the FSM bbolt file at dbPath and
// pre-creates the canonical bucket set.
func newFSM(dbPath string) (*FSM, error) {
	if dbPath == "" {
		return nil, errors.New("raftbackend: FSM dbPath required")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("raftbackend: mkdir %s: %w", filepath.Dir(dbPath), err)
	}
	db, err := bolt.Open(dbPath, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("raftbackend: open %s: %w", dbPath, err)
	}
	if err := local.EnsureBuckets(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &FSM{
		dbPath:           dbPath,
		db:               db,
		informer:         newInformer(),
		reportedUnrouted: map[string]bool{},
	}, nil
}

// close releases the bbolt handle and tears down the informer.
// Idempotent.
func (f *FSM) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.informer.shutdown()
	if f.db != nil {
		err := f.db.Close()
		f.db = nil
		return err
	}
	return nil
}

// route is the FSM's wrapper around local.RouteKey. Logs once
// per unrouted key, exactly like LocalStorage.
func (f *FSM) route(key string) (string, string) {
	bucket, subKey := local.RouteKey(key)
	if bucket == local.BucketUnrouted {
		f.reportedMu.Lock()
		if !f.reportedUnrouted[key] {
			f.reportedUnrouted[key] = true
			fsmLog.Warnf("key routed to unrouted bucket: %q (typo or new bucket needs to be added to local.Buckets)", key)
		}
		f.reportedMu.Unlock()
	}
	return bucket, subKey
}

// --- raft.FSM ----------------------------------------------------

// Apply runs a single Raft log entry. The returned interface{}
// is whatever raft.Apply's future will receive — we wrap
// everything in *ApplyResult so callers can check CasFailed.
func (f *FSM) Apply(l *raft.Log) interface{} {
	cmd, err := decodeCommand(l.Data)
	if err != nil {
		return &ApplyResult{Error: err.Error()}
	}
	switch cmd.GetOp() {
	case Op_OP_PUT:
		return f.applyPut(cmd)
	case Op_OP_DELETE:
		return f.applyDelete(cmd)
	case Op_OP_CAS:
		return f.applyCAS(cmd)
	case Op_OP_CAS_CREATE:
		return f.applyCASCreate(cmd)
	case Op_OP_BATCH:
		return f.applyBatch(cmd)
	default:
		return &ApplyResult{Error: fmt.Sprintf("unknown op: %s", cmd.GetOp())}
	}
}

func (f *FSM) applyPut(cmd *Command) *ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, subKey := f.route(cmd.GetKey())
	value := append([]byte(nil), cmd.GetValue()...)
	err := f.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Put([]byte(subKey), value)
	})
	if err != nil {
		return &ApplyResult{Error: err.Error()}
	}
	f.informer.publish(cmd.GetKey(), value, storage.OpPut)
	return &ApplyResult{}
}

func (f *FSM) applyDelete(cmd *Command) *ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, subKey := f.route(cmd.GetKey())
	err := f.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(subKey))
	})
	if err != nil {
		return &ApplyResult{Error: err.Error()}
	}
	f.informer.publish(cmd.GetKey(), nil, storage.OpDelete)
	return &ApplyResult{}
}

// applyCAS handles both CAS-update and CAS-delete (when
// DeleteOnApply is set on the command). Followers run the same
// equality check against their copy and reach the same outcome
// as the leader because the log is deterministic.
func (f *FSM) applyCAS(cmd *Command) *ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, subKey := f.route(cmd.GetKey())
	deleteOnApply := cmd.GetDeleteOnApply()
	freshCopy := append([]byte(nil), cmd.GetValue()...)
	var publishOp storage.OpKind
	var publishVal []byte

	err := f.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		cur := b.Get([]byte(subKey))
		// Empty bbolt slice is non-nil but len==0; CAS callers
		// pass nil for "absent". Treat nil and zero-length the
		// same when the proposer's expected was nil.
		if !bytesEqualNilSensitive(cur, cmd.GetExpected()) {
			return storage.ErrCASFailed
		}
		if deleteOnApply {
			return b.Delete([]byte(subKey))
		}
		return b.Put([]byte(subKey), freshCopy)
	})
	if err != nil {
		if errors.Is(err, storage.ErrCASFailed) {
			return &ApplyResult{Error: storage.ErrCASFailed.Error(), CasFailed: true}
		}
		return &ApplyResult{Error: err.Error()}
	}
	if deleteOnApply {
		publishOp = storage.OpDelete
	} else {
		publishOp = storage.OpPut
		publishVal = freshCopy
	}
	f.informer.publish(cmd.GetKey(), publishVal, publishOp)
	return &ApplyResult{}
}

func (f *FSM) applyCASCreate(cmd *Command) *ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, subKey := f.route(cmd.GetKey())
	freshCopy := append([]byte(nil), cmd.GetValue()...)
	err := f.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b.Get([]byte(subKey)) != nil {
			return storage.ErrCASFailed
		}
		return b.Put([]byte(subKey), freshCopy)
	})
	if err != nil {
		if errors.Is(err, storage.ErrCASFailed) {
			return &ApplyResult{Error: storage.ErrCASFailed.Error(), CasFailed: true}
		}
		return &ApplyResult{Error: err.Error()}
	}
	f.informer.publish(cmd.GetKey(), freshCopy, storage.OpPut)
	return &ApplyResult{}
}

func (f *FSM) applyBatch(cmd *Command) *ApplyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	type pub struct {
		key   string
		value []byte
		op    storage.OpKind
	}
	pubs := make([]pub, 0, len(cmd.GetBatch()))
	err := f.db.Update(func(tx *bolt.Tx) error {
		for _, e := range cmd.GetBatch() {
			bucket, subKey := f.route(e.GetKey())
			b := tx.Bucket([]byte(bucket))
			switch e.GetOp() {
			case Op_OP_PUT:
				v := append([]byte(nil), e.GetValue()...)
				if err := b.Put([]byte(subKey), v); err != nil {
					return err
				}
				pubs = append(pubs, pub{key: e.GetKey(), value: v, op: storage.OpPut})
			case Op_OP_DELETE:
				if err := b.Delete([]byte(subKey)); err != nil {
					return err
				}
				pubs = append(pubs, pub{key: e.GetKey(), op: storage.OpDelete})
			default:
				return fmt.Errorf("raftbackend: invalid batch op %s", e.GetOp())
			}
		}
		return nil
	})
	if err != nil {
		return &ApplyResult{Error: err.Error()}
	}
	for _, p := range pubs {
		f.informer.publish(p.key, p.value, p.op)
	}
	return &ApplyResult{}
}

// --- read-side helpers (used by RaftStorage Get/List/Keys) ------

// localGet is a direct read from the FSM's bbolt. Bypasses Raft
// — used for proposer-side pre-reads in Mutate as well as for
// the public Storage.Get implementation.
func (f *FSM) localGet(key string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	bucket, subKey := f.route(key)
	var out []byte
	err := f.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return storage.ErrNotFound
		}
		v := b.Get([]byte(subKey))
		if v == nil {
			return storage.ErrNotFound
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}

// localList walks the prefix and returns full (key, value)
// pairs. Same iteration logic as LocalStorage.scanPrefix —
// duplicated here so we don't expose the local package's
// per-instance helper as an exported API.
func (f *FSM) localList(prefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := f.scanPrefix(prefix, func(key string, v []byte) bool {
		out[key] = append([]byte(nil), v...)
		return true
	})
	return out, err
}

// localKeys walks the prefix and returns just the keys, sorted.
func (f *FSM) localKeys(prefix string) ([]string, error) {
	var keys []string
	err := f.scanPrefix(prefix, func(key string, _ []byte) bool {
		keys = append(keys, key)
		return true
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// scanPrefix walks the matching subset of keys for the given
// Storage prefix. Same decomposition rules LocalStorage uses
// (see local.LocalStorage.scanPrefix for full table).
func (f *FSM) scanPrefix(prefix string, fn func(key string, v []byte) bool) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var (
		bucketHint string
		subPrefix  string
	)
	slash := strings.IndexByte(prefix, '/')
	switch {
	case prefix == "":
		bucketHint = ""
	case slash < 0:
		if local.IsKnownBucket(prefix) {
			bucketHint = prefix
		} else {
			subPrefix = prefix
		}
	default:
		head := prefix[:slash]
		if local.IsKnownBucket(head) {
			bucketHint = head
			subPrefix = prefix[slash+1:]
		} else {
			bucketHint = local.BucketUnrouted
			subPrefix = prefix
		}
	}

	walkBucket := func(tx *bolt.Tx, name string) error {
		b := tx.Bucket([]byte(name))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		var k, v []byte
		if subPrefix != "" {
			k, v = c.Seek([]byte(subPrefix))
		} else {
			k, v = c.First()
		}
		for ; k != nil; k, v = c.Next() {
			if subPrefix != "" && !bytes.HasPrefix(k, []byte(subPrefix)) {
				break
			}
			fullKey := local.Recompose(name, string(k))
			if prefix != "" && !strings.HasPrefix(fullKey, prefix) {
				continue
			}
			if !fn(fullKey, v) {
				return nil
			}
		}
		return nil
	}

	return f.db.View(func(tx *bolt.Tx) error {
		if bucketHint != "" {
			return walkBucket(tx, bucketHint)
		}
		for _, name := range local.Buckets {
			if err := walkBucket(tx, name); err != nil {
				return err
			}
		}
		return walkBucket(tx, local.BucketUnrouted)
	})
}

// --- raft.FSMSnapshot --------------------------------------------

// Snapshot returns a raft.FSMSnapshot that streams the entire
// FSM bbolt file. We hold a read tx during Persist so the bytes
// are a consistent point-in-time copy.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	return &boltSnapshot{fsm: f}, nil
}

// Restore replaces the FSM's bbolt file atomically. The supplied
// reader streams the snapshot bytes; we write them to a temp
// file in the same directory, fsync, close the live db, rename,
// reopen.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Dir(f.dbPath)
	tmp, err := os.CreateTemp(dir, ".restore-*.db")
	if err != nil {
		return fmt.Errorf("raftbackend: restore tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if rename succeeded

	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("raftbackend: restore copy: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("raftbackend: restore sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("raftbackend: restore close tmp: %w", err)
	}

	// Close the live db so we can rename over it. On Linux,
	// rename works while a file is open, but bbolt holds a
	// flock — close-then-rename is the safe order.
	if err := f.db.Close(); err != nil {
		return fmt.Errorf("raftbackend: restore close live: %w", err)
	}
	if err := os.Rename(tmpPath, f.dbPath); err != nil {
		// We've already closed live db; reopen original to keep
		// the FSM responsive even if rename failed.
		if reopened, rerr := bolt.Open(f.dbPath, 0o600, nil); rerr == nil {
			f.db = reopened
		}
		return fmt.Errorf("raftbackend: restore rename: %w", err)
	}
	db, err := bolt.Open(f.dbPath, 0o600, nil)
	if err != nil {
		return fmt.Errorf("raftbackend: restore reopen: %w", err)
	}
	if err := local.EnsureBuckets(db); err != nil {
		_ = db.Close()
		return fmt.Errorf("raftbackend: restore buckets: %w", err)
	}
	f.db = db
	return nil
}

// boltSnapshot streams a consistent copy of the FSM's bbolt
// file. Persist holds a read tx for the duration of the copy so
// concurrent Apply writes don't observe a torn snapshot.
type boltSnapshot struct {
	fsm *FSM
}

func (s *boltSnapshot) Persist(sink raft.SnapshotSink) error {
	s.fsm.mu.RLock()
	db := s.fsm.db
	s.fsm.mu.RUnlock()

	err := db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(sink)
		return err
	})
	if err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("raftbackend: snapshot persist: %w", err)
	}
	return sink.Close()
}

func (s *boltSnapshot) Release() {}

// --- helpers -----------------------------------------------------

// bytesEqualNilSensitive treats both nil and zero-length slices
// as "absent" for CAS comparison purposes. bbolt returns nil for
// missing keys; callers that pass nil for "expected = absent"
// must match against either.
func bytesEqualNilSensitive(a, b []byte) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return bytes.Equal(a, b)
}

// ensureClosed is exposed for tests.
func ensureClosed(_ context.Context, _ *FSM) {}
