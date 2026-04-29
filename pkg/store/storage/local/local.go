// Package local is the bbolt-backed implementation of
// storage.Storage. It's used by tests and offline CLI tools
// that need direct bbolt-file access (notably the OPAQUE
// recovery flow). Production hula uses
// pkg/store/storage/raft/RaftStorage instead, but RaftStorage
// internally embeds a similar bbolt-backed FSM, so the two
// implementations share the same on-disk shape per bucket.
//
// Watch fires synchronously from the same goroutine that
// performed the Put / Delete / Batch. The publish step happens
// AFTER the bbolt transaction commits, so subscribers see only
// changes that durably landed.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

var localLog = log.GetTaggedLogger("storage-local", "Local bbolt-backed storage")

// LocalStorage implements storage.Storage on top of a single
// bbolt.DB handle.
type LocalStorage struct {
	db       *bolt.DB
	informer *informer

	mu     sync.RWMutex
	closed bool

	// ownDB indicates whether we own the db and should close it
	// in Close(). False when constructed via FromBolt (caller
	// retains ownership).
	ownDB bool

	// reportedUnrouted tracks bucket-unrouted keys we've already
	// warn-logged so a single typo doesn't spam the log.
	reportedMu       sync.Mutex
	reportedUnrouted map[string]bool
}

// Options for Open.
type Options struct {
	// Path is the bbolt file location. Required.
	Path string
	// Timeout for the file lock. Default 2s.
	Timeout time.Duration
}

// Open opens (or creates) a bbolt file at the given path and
// pre-creates the canonical bucket set.
func Open(opts Options) (*LocalStorage, error) {
	if opts.Path == "" {
		return nil, errors.New("local storage: Path is required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, fmt.Errorf("local storage: mkdir %s: %w", filepath.Dir(opts.Path), err)
	}
	db, err := bolt.Open(opts.Path, 0o600, &bolt.Options{Timeout: opts.Timeout})
	if err != nil {
		return nil, fmt.Errorf("local storage: open %s: %w", opts.Path, err)
	}

	if err := ensureBuckets(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &LocalStorage{
		db:               db,
		informer:         newInformer(),
		ownDB:            true,
		reportedUnrouted: map[string]bool{},
	}, nil
}

// FromBolt wraps an existing bbolt.DB handle. Caller retains
// ownership of the underlying handle (Close on the returned
// LocalStorage will not close the underlying db). Useful when
// other code paths still hold a reference to the same file.
func FromBolt(db *bolt.DB) (*LocalStorage, error) {
	if err := ensureBuckets(db); err != nil {
		return nil, err
	}
	return &LocalStorage{
		db:               db,
		informer:         newInformer(),
		ownDB:            false,
		reportedUnrouted: map[string]bool{},
	}, nil
}

func ensureBuckets(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		for _, b := range Buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(b)); err != nil {
				return fmt.Errorf("create bucket %s: %w", b, err)
			}
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(BucketUnrouted)); err != nil {
			return fmt.Errorf("create bucket %s: %w", BucketUnrouted, err)
		}
		return nil
	})
}

// --- storage.Storage methods --------------------------------------

func (s *LocalStorage) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	bucket, subKey := s.routeWithReport(key)
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
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

func (s *LocalStorage) Put(ctx context.Context, key string, value []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	bucket, subKey := s.routeWithReport(key)
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Put([]byte(subKey), value)
	})
	if err != nil {
		return err
	}
	s.informer.publish(key, value, storage.OpPut)
	return nil
}

func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	bucket, subKey := s.routeWithReport(key)
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(subKey))
	})
	if err != nil {
		return err
	}
	s.informer.publish(key, nil, storage.OpDelete)
	return nil
}

func (s *LocalStorage) List(ctx context.Context, prefix string) (map[string][]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.scanPrefix(tx, prefix, func(key string, v []byte) bool {
			out[key] = append([]byte(nil), v...)
			return true
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *LocalStorage) Keys(ctx context.Context, prefix string) ([]string, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	var keys []string
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.scanPrefix(tx, prefix, func(key string, _ []byte) bool {
			keys = append(keys, key)
			return true
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *LocalStorage) CompareAndSwap(ctx context.Context, key string, expected, fresh []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	bucket, subKey := s.routeWithReport(key)
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		cur := b.Get([]byte(subKey))
		if !bytes.Equal(cur, expected) {
			return storage.ErrCASFailed
		}
		return b.Put([]byte(subKey), fresh)
	})
	if err != nil {
		return err
	}
	s.informer.publish(key, fresh, storage.OpPut)
	return nil
}

func (s *LocalStorage) CompareAndCreate(ctx context.Context, key string, value []byte) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	bucket, subKey := s.routeWithReport(key)
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b.Get([]byte(subKey)) != nil {
			return storage.ErrCASFailed
		}
		return b.Put([]byte(subKey), value)
	})
	if err != nil {
		return err
	}
	s.informer.publish(key, value, storage.OpPut)
	return nil
}

func (s *LocalStorage) Mutate(ctx context.Context, key string, fn storage.MutateFn) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	bucket, subKey := s.routeWithReport(key)

	var (
		publishOp  storage.OpKind
		publishVal []byte
		didWrite   bool
	)
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		var current []byte
		if v := b.Get([]byte(subKey)); v != nil {
			current = append([]byte(nil), v...)
		}
		next, err := fn(current)
		if err != nil {
			if errors.Is(err, storage.ErrSkip) {
				return nil
			}
			return err
		}
		if next == nil {
			// Delete.
			if err := b.Delete([]byte(subKey)); err != nil {
				return err
			}
			publishOp = storage.OpDelete
			didWrite = true
			return nil
		}
		if err := b.Put([]byte(subKey), next); err != nil {
			return err
		}
		publishOp = storage.OpPut
		publishVal = append([]byte(nil), next...)
		didWrite = true
		return nil
	})
	if err != nil {
		return err
	}
	if didWrite {
		s.informer.publish(key, publishVal, publishOp)
	}
	return nil
}

func (s *LocalStorage) Batch(ctx context.Context, ops []storage.BatchOp) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	type pub struct {
		key   string
		value []byte
		op    storage.OpKind
	}
	var pubs []pub

	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, op := range ops {
			bucket, subKey := s.routeWithReport(op.Key)
			b := tx.Bucket([]byte(bucket))
			switch op.Op {
			case storage.OpPut:
				if err := b.Put([]byte(subKey), op.Value); err != nil {
					return err
				}
				pubs = append(pubs, pub{key: op.Key, value: append([]byte(nil), op.Value...), op: storage.OpPut})
			case storage.OpDelete:
				if err := b.Delete([]byte(subKey)); err != nil {
					return err
				}
				pubs = append(pubs, pub{key: op.Key, op: storage.OpDelete})
			default:
				return fmt.Errorf("local storage: unknown batch op %v", op.Op)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range pubs {
		s.informer.publish(p.key, p.value, p.op)
	}
	return nil
}

func (s *LocalStorage) Watch(ctx context.Context, prefix string) (<-chan storage.Event, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.informer.subscribe(ctx, prefix), nil
}

func (s *LocalStorage) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.informer.shutdown()
	if s.ownDB && s.db != nil {
		return s.db.Close()
	}
	return nil
}

// --- internal -----------------------------------------------------

func (s *LocalStorage) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return storage.ErrClosed
	}
	return nil
}

func (s *LocalStorage) routeWithReport(key string) (string, string) {
	bucket, subKey := routeKey(key)
	if bucket == BucketUnrouted {
		s.reportedMu.Lock()
		if !s.reportedUnrouted[key] {
			s.reportedUnrouted[key] = true
			localLog.Warnf("key routed to unrouted bucket: %q (typo or new bucket needs to be added to local.Buckets)", key)
		}
		s.reportedMu.Unlock()
	}
	return bucket, subKey
}

// scanPrefix walks the matching subset of keys for the given Storage
// prefix. The prefix can:
//   - exactly equal a bucket name + "/" → walk that bucket entirely
//   - exactly equal a bucket name (no trailing slash) → walk that bucket
//   - extend into a sub-prefix (e.g., "alerts/foo|") → walk just those
//   - be empty → walk every bucket (slow but correct)
//
// fn is invoked with the FULL Storage key (bucket + "/" + subKey)
// and the value bytes. Returning false stops iteration.
func (s *LocalStorage) scanPrefix(tx *bolt.Tx, prefix string, fn func(key string, v []byte) bool) error {
	// Decompose the prefix into (bucketHint, subPrefix).
	var (
		bucketHint string
		subPrefix  string
	)
	slash := strings.IndexByte(prefix, '/')
	switch {
	case prefix == "":
		// Walk everything.
		bucketHint = ""
	case slash < 0:
		// Prefix is a single segment with no slash. If it matches a
		// known bucket name exactly, walk that whole bucket;
		// otherwise treat it as a partial bucket-name match — walk
		// every bucket whose name starts with the segment.
		if _, ok := bucketSet[prefix]; ok {
			bucketHint = prefix
		} else {
			bucketHint = ""
			subPrefix = prefix
		}
	default:
		// "<bucket>/<rest>"
		head := prefix[:slash]
		if _, ok := bucketSet[head]; ok {
			bucketHint = head
			subPrefix = prefix[slash+1:]
		} else {
			// Unknown bucket prefix. Look up in fallback only.
			bucketHint = BucketUnrouted
			subPrefix = prefix
		}
	}

	walkBucket := func(name string) error {
		b := tx.Bucket([]byte(name))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		var k, v []byte
		if subPrefix != "" {
			// Seek to the first key >= subPrefix.
			k, v = c.Seek([]byte(subPrefix))
		} else {
			k, v = c.First()
		}
		for ; k != nil; k, v = c.Next() {
			if subPrefix != "" && !bytes.HasPrefix(k, []byte(subPrefix)) {
				break
			}
			fullKey := recompose(name, string(k))
			// When iterating "everything", filter against the
			// caller's prefix as well (for "" we accept all).
			if prefix != "" && !strings.HasPrefix(fullKey, prefix) {
				continue
			}
			if !fn(fullKey, v) {
				return nil
			}
		}
		return nil
	}

	if bucketHint != "" {
		return walkBucket(bucketHint)
	}
	for _, name := range Buckets {
		if err := walkBucket(name); err != nil {
			return err
		}
	}
	// Also walk the fallback bucket.
	return walkBucket(BucketUnrouted)
}
