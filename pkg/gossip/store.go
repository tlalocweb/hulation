package gossip

// Store is the bbolt-backed CRDT state. Lives in a SEPARATE file
// from the Raft FSM (HA_PLAN3 §7.2) so the FSM snapshot/restore
// lifecycle never touches CRDT state and operators can tar / size /
// rotate them independently.
//
// Bucket layout (all top-level inside crdt.db):
//
//   visitors/<server_id>/<visitor_id>     LWW record
//   sessions/<server_id>/<visitor_id>     LWW record
//   badactor_flags/<ip>                   Monotone record
//
// Plus one bookkeeping bucket:
//
//   _meta — high-water HLC per data bucket, used by the
//           anti-entropy ticker to compute digest deltas without
//           scanning the whole bucket.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// Reserved bucket names. Adding a new CRDT family means: add it
	// to this list, decide whether it's LWW or Monotone, and route
	// callers through PutLWW / PutMonotone.
	BucketVisitors      = "visitors"
	BucketSessions      = "sessions"
	BucketBadactorFlags = "badactor_flags"

	bucketMeta       = "_meta"
	metaHighWaterKey = "high_water"
)

// dataBuckets is the canonical set of CRDT-replicated buckets.
// Anti-entropy + iteration helpers walk these (skipping _meta).
var dataBuckets = []string{
	BucketVisitors,
	BucketSessions,
	BucketBadactorFlags,
}

// IsKnownBucket reports whether name is a CRDT data bucket.
func IsKnownBucket(name string) bool {
	for _, b := range dataBuckets {
		if b == name {
			return true
		}
	}
	return false
}

// Store owns the CRDT bbolt file + the local HLC clock. Methods
// are concurrency-safe.
type Store struct {
	db    *bolt.DB
	clock *Clock

	// onWrite optionally fires after every successful Put / Delete
	// so the gossip layer can fan out push-on-write deltas.
	mu      sync.RWMutex
	onWrite func(bucket, key string, payload []byte, hlc HLC)
}

// OpenStore opens a Store at path (creating the file if needed).
// The clock is keyed to the supplied nodeID — every locally-emitted
// HLC carries it.
func OpenStore(path, nodeID string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("gossip store: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, bk := range dataBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(bk)); err != nil {
				return err
			}
		}
		_, err := tx.CreateBucketIfNotExists([]byte(bucketMeta))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gossip store: init buckets: %w", err)
	}
	return &Store{
		db:    db,
		clock: NewClock(nodeID),
	}, nil
}

// Close flushes + closes the underlying bbolt file.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Clock exposes the underlying HLC clock for callers that need it
// (e.g. the gossip protocol bumps it on every received Push).
func (s *Store) Clock() *Clock { return s.clock }

// SetOnWrite installs a callback that fires after every successful
// write (Put / Delete / FlagBad / ApplyDelta). The callback is
// invoked synchronously while the Store mutex is NOT held — peer
// Push calls happen on a separate goroutine.
func (s *Store) SetOnWrite(f func(bucket, key string, payload []byte, hlc HLC)) {
	s.mu.Lock()
	s.onWrite = f
	s.mu.Unlock()
}

// PutLWW writes (or overwrites) an LWW record. Stamps a fresh HLC
// from the local clock, encodes the record, persists it, advances
// the bucket high-water, and fires onWrite for gossip fan-out.
func (s *Store) PutLWW(_ context.Context, bucket, key string, payload []byte) (HLC, error) {
	if !IsKnownBucket(bucket) {
		return HLC{}, fmt.Errorf("gossip store: unknown bucket %q", bucket)
	}
	hlc := s.clock.Now()
	rec := LWWRecord{HLC: hlc, Payload: payload}
	encoded, err := EncodeLWW(rec)
	if err != nil {
		return HLC{}, err
	}
	if err := s.put(bucket, key, encoded, hlc); err != nil {
		return HLC{}, err
	}
	s.fireOnWrite(bucket, key, encoded, hlc)
	return hlc, nil
}

// DeleteLWW writes a tombstone for the key. Tombstones survive
// merge against any value with HLC ≤ tombstone.HLC.
func (s *Store) DeleteLWW(_ context.Context, bucket, key string) (HLC, error) {
	if !IsKnownBucket(bucket) {
		return HLC{}, fmt.Errorf("gossip store: unknown bucket %q", bucket)
	}
	hlc := s.clock.Now()
	rec := LWWRecord{HLC: hlc, Tombstone: true}
	encoded, err := EncodeLWW(rec)
	if err != nil {
		return HLC{}, err
	}
	if err := s.put(bucket, key, encoded, hlc); err != nil {
		return HLC{}, err
	}
	s.fireOnWrite(bucket, key, encoded, hlc)
	return hlc, nil
}

// GetLWW returns the current record for a key. Returns
// ErrKeyNotFound when no value (live or tombstoned) exists.
func (s *Store) GetLWW(_ context.Context, bucket, key string) (LWWRecord, error) {
	var raw []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
		if bk == nil {
			return ErrKeyNotFound
		}
		v := bk.Get([]byte(key))
		if v == nil {
			return ErrKeyNotFound
		}
		raw = append(raw, v...)
		return nil
	})
	if err != nil {
		return LWWRecord{}, err
	}
	return DecodeLWW(raw)
}

// FlagBad raises the monotone bad-actor flag for the given key
// (typically a stringified IP). HA_PLAN3 §7.5: cross-node
// convergence happens via gossip; admin-side clearing happens via
// Raft and the read-time predicate prefers Raft-clear over the
// gossip flag.
func (s *Store) FlagBad(_ context.Context, bucket, key, reason, byNodeID string) (HLC, error) {
	if !IsKnownBucket(bucket) {
		return HLC{}, fmt.Errorf("gossip store: unknown bucket %q", bucket)
	}
	hlc := s.clock.Now()
	rec := MonotoneRecord{HLC: hlc, Flagged: true, Reason: reason, ByNodeID: byNodeID}
	encoded, err := EncodeMonotone(rec)
	if err != nil {
		return HLC{}, err
	}
	if err := s.put(bucket, key, encoded, hlc); err != nil {
		return HLC{}, err
	}
	s.fireOnWrite(bucket, key, encoded, hlc)
	return hlc, nil
}

// GetMonotone returns the current monotone record. Empty record +
// ErrKeyNotFound when no flag has been set.
func (s *Store) GetMonotone(_ context.Context, bucket, key string) (MonotoneRecord, error) {
	var raw []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
		if bk == nil {
			return ErrKeyNotFound
		}
		v := bk.Get([]byte(key))
		if v == nil {
			return ErrKeyNotFound
		}
		raw = append(raw, v...)
		return nil
	})
	if err != nil {
		return MonotoneRecord{}, err
	}
	return DecodeMonotone(raw)
}

// Delta is the wire shape for one CRDT update. Used by both the
// push-on-write fan-out and the anti-entropy SyncDigest reply.
// Mirrors gossip.proto's Delta message verbatim; defined here too
// so packages above + below the proto layer don't have an import
// cycle.
type Delta struct {
	Bucket  string
	Key     string
	Payload []byte
	HLC     HLC
}

// ApplyDelta merges an incoming delta into the local store. Returns
// (changed, mergedHLC, error). When changed is false the local
// state was already at-or-after the incoming delta and the on-disk
// bytes were not touched.
func (s *Store) ApplyDelta(_ context.Context, d Delta) (bool, HLC, error) {
	if !IsKnownBucket(d.Bucket) {
		return false, HLC{}, fmt.Errorf("gossip store: unknown bucket %q", d.Bucket)
	}
	if len(d.Payload) == 0 {
		return false, HLC{}, errors.New("gossip store: empty payload")
	}

	// Pull the local clock forward to acknowledge the remote.
	s.clock.Update(d.HLC)

	var changed bool
	var mergedHLC HLC

	err := s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(d.Bucket))
		if bk == nil {
			return fmt.Errorf("gossip store: bucket %q vanished", d.Bucket)
		}
		existing := bk.Get([]byte(d.Key))
		if existing == nil {
			if err := bk.Put([]byte(d.Key), d.Payload); err != nil {
				return err
			}
			changed = true
			mergedHLC = d.HLC
			return s.bumpHighWater(tx, d.Bucket, d.HLC)
		}
		merged, err := MergeEncoded(existing, d.Payload)
		if err != nil {
			return err
		}
		if equalBytes(merged, existing) {
			return nil
		}
		if err := bk.Put([]byte(d.Key), merged); err != nil {
			return err
		}
		mergedHLC, _ = HLCFromPayload(merged)
		changed = true
		return s.bumpHighWater(tx, d.Bucket, mergedHLC)
	})
	if err != nil {
		return false, HLC{}, err
	}
	if changed {
		s.fireOnWrite(d.Bucket, d.Key, d.Payload, mergedHLC)
	}
	return changed, mergedHLC, nil
}

// HighWater returns the bucket's most recent HLC. Returns the zero
// HLC when the bucket is empty.
func (s *Store) HighWater(_ context.Context, bucket string) (HLC, error) {
	if !IsKnownBucket(bucket) {
		return HLC{}, fmt.Errorf("gossip store: unknown bucket %q", bucket)
	}
	var hlc HLC
	err := s.db.View(func(tx *bolt.Tx) error {
		hlc = s.readHighWater(tx, bucket)
		return nil
	})
	return hlc, err
}

// DeltasSince returns every record in `bucket` whose HLC is After
// `since`. Used by SyncDigest to ship missing entries to a peer.
// Caller passes max to bound the response size.
func (s *Store) DeltasSince(_ context.Context, bucket string, since HLC, max int) ([]Delta, error) {
	if !IsKnownBucket(bucket) {
		return nil, fmt.Errorf("gossip store: unknown bucket %q", bucket)
	}
	if max <= 0 {
		max = 1024
	}
	out := make([]Delta, 0, max)
	err := s.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
		if bk == nil {
			return nil
		}
		c := bk.Cursor()
		for k, v := c.First(); k != nil && len(out) < max; k, v = c.Next() {
			hlc, err := HLCFromPayload(v)
			if err != nil {
				continue
			}
			if !hlc.After(since) {
				continue
			}
			out = append(out, Delta{
				Bucket:  bucket,
				Key:     string(k),
				Payload: append([]byte(nil), v...),
				HLC:     hlc,
			})
		}
		return nil
	})
	return out, err
}

// IterateAll walks every CRDT bucket and calls fn for each entry.
// Used by tests + diagnostics; production hot paths shouldn't use
// this.
func (s *Store) IterateAll(fn func(d Delta) bool) error {
	return s.db.View(func(tx *bolt.Tx) error {
		for _, bucket := range dataBuckets {
			bk := tx.Bucket([]byte(bucket))
			if bk == nil {
				continue
			}
			c := bk.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				hlc, _ := HLCFromPayload(v)
				if !fn(Delta{Bucket: bucket, Key: string(k), Payload: v, HLC: hlc}) {
					return nil
				}
			}
		}
		return nil
	})
}

// SweepTombstones removes tombstoned LWW records older than ttl.
// HA_PLAN3 §7.6 — default ttl is 30 days, configurable. Caller
// runs this from a background sweeper. Return value: number of
// rows deleted.
func (s *Store) SweepTombstones(ttl time.Duration) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	cutoffMs := time.Now().Add(-ttl).UnixMilli()
	deleted := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{BucketVisitors, BucketSessions} {
			bk := tx.Bucket([]byte(bucket))
			if bk == nil {
				continue
			}
			toDel := make([][]byte, 0, 16)
			c := bk.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				rec, err := DecodeLWW(v)
				if err != nil || !rec.Tombstone {
					continue
				}
				if rec.HLC.WallMs < cutoffMs {
					toDel = append(toDel, append([]byte(nil), k...))
				}
			}
			for _, k := range toDel {
				if err := bk.Delete(k); err != nil {
					return err
				}
				deleted++
			}
		}
		return nil
	})
	return deleted, err
}

// --- internals -----------------------------------------------------

// ErrKeyNotFound is returned when a Get hits a missing key.
var ErrKeyNotFound = errors.New("gossip store: key not found")

func (s *Store) put(bucket, key string, encoded []byte, hlc HLC) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
		if bk == nil {
			return fmt.Errorf("bucket %q missing", bucket)
		}
		if err := bk.Put([]byte(key), encoded); err != nil {
			return err
		}
		return s.bumpHighWater(tx, bucket, hlc)
	})
}

func (s *Store) bumpHighWater(tx *bolt.Tx, bucket string, hlc HLC) error {
	mb := tx.Bucket([]byte(bucketMeta))
	if mb == nil {
		var err error
		mb, err = tx.CreateBucket([]byte(bucketMeta))
		if err != nil {
			return err
		}
	}
	cur := s.readHighWaterFromMeta(mb, bucket)
	if !hlc.After(cur) {
		return nil
	}
	encoded, err := EncodeHLC(hlc)
	if err != nil {
		return err
	}
	return mb.Put(highWaterKey(bucket), encoded)
}

func (s *Store) readHighWater(tx *bolt.Tx, bucket string) HLC {
	mb := tx.Bucket([]byte(bucketMeta))
	if mb == nil {
		return HLC{}
	}
	return s.readHighWaterFromMeta(mb, bucket)
}

func (s *Store) readHighWaterFromMeta(mb *bolt.Bucket, bucket string) HLC {
	v := mb.Get(highWaterKey(bucket))
	if len(v) == 0 {
		return HLC{}
	}
	hlc, err := DecodeHLC(v)
	if err != nil {
		return HLC{}
	}
	return hlc
}

func highWaterKey(bucket string) []byte {
	return []byte(metaHighWaterKey + "/" + bucket)
}

func (s *Store) fireOnWrite(bucket, key string, payload []byte, hlc HLC) {
	s.mu.RLock()
	cb := s.onWrite
	s.mu.RUnlock()
	if cb == nil {
		return
	}
	cb(bucket, key, payload, hlc)
}

func equalBytes(a, b []byte) bool {
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

// EncodeHLC / DecodeHLC are the on-disk format for the
// high-water bookkeeping. Plain gob — small + future-proof.
func EncodeHLC(h HLC) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString(h.NodeID)
	sb.WriteByte('|')
	sb.WriteString(fmt.Sprintf("%d.%d", h.WallMs, h.Counter))
	return []byte(sb.String()), nil
}

func DecodeHLC(b []byte) (HLC, error) {
	s := string(b)
	idx := strings.IndexByte(s, '|')
	if idx < 0 {
		return HLC{}, ErrInvalidHLC
	}
	nodeID := s[:idx]
	rest := s[idx+1:]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return HLC{}, ErrInvalidHLC
	}
	var wall int64
	var counter uint32
	if _, err := fmt.Sscanf(rest, "%d.%d", &wall, &counter); err != nil {
		return HLC{}, fmt.Errorf("decode hlc: %w", err)
	}
	return HLC{WallMs: wall, Counter: counter, NodeID: nodeID}, nil
}
