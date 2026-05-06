// Package relay implements the analytics-event outbox + drainer
// (HA_PLAN3 §6). Non-CH nodes accept visitor traffic at the public
// edge, return immediately, and drop the enriched model.Event into
// an outbox here. A drainer goroutine pulls batches and ships them
// to the CH-connected peer via RelayService.RecordEventBatch.
//
// The hot path NEVER blocks. Outbox.Enqueue is non-blocking. The
// in-memory ring sheds to a bbolt-backed disk overflow once it
// fills; FIFO eviction kicks in past the disk cap.
package relay

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
)

var relayLog = log.GetTaggedLogger("relay", "Analytics relay outbox")

const (
	outboxBucket = "events"
	metaBucket   = "meta"
	headKey      = "head"
	tailKey      = "tail"
)

// Config tunes outbox behaviour. Zero values pick reasonable
// defaults documented per field.
type Config struct {
	// RingSize is the in-memory ring capacity (events). Defaults
	// to 4096. Sized so a steady ~100 events/s burst absorbs at
	// ~40s of CH-side stall before disk overflow kicks in.
	RingSize int

	// DiskOverflowPath is where the bbolt-backed FIFO overflow
	// queue lives. Empty = memory-only (events are lost across
	// restarts). HA_PLAN3 §6.5 recommends data_dir/outbox/events.queue.
	DiskOverflowPath string

	// DiskMaxBytes caps the on-disk queue size. Past this cap,
	// the oldest events are evicted FIFO and a 1-per-second
	// rate-limited WARN is logged. Default 256 MB.
	DiskMaxBytes int64

	// EvictionLogEvery throttles the WARN line emitted when FIFO
	// eviction fires. Default 1s.
	EvictionLogEvery time.Duration
}

// Outbox is the relay-side queue. Methods are concurrency-safe;
// Enqueue is non-blocking and the visitor hot path never observes
// queue work beyond a couple of mutex acquisitions + a serialise
// of the event row.
type Outbox struct {
	cfg Config

	mu      sync.Mutex
	ring    []*model.Event
	ringHead int
	ringSize int
	overflow *bolt.DB

	lastEvictWarn time.Time

	// Counters surfaced through Stats(). Operators wire these into
	// /metrics or whatever they're scraping.
	stats Stats
}

// Stats is a snapshot of outbox activity. All fields monotonically
// increase except RingDepth + DiskDepth.
type Stats struct {
	Enqueued       uint64 // total Enqueue calls that returned without error
	RingDepth      int    // current in-memory queue depth
	DiskDepth      uint64 // events currently parked in the overflow file
	DiskBytes      int64  // approximate disk space used by overflow
	Evictions      uint64 // events dropped because both ring + disk were full
	BatchesSent    uint64 // successful drain batches
	BatchesRetried uint64 // drain attempts that returned an error
}

// New builds an Outbox. Returns an error if DiskOverflowPath was
// supplied but bbolt couldn't open the file.
func New(cfg Config) (*Outbox, error) {
	cfg = applyDefaults(cfg)
	o := &Outbox{
		cfg:  cfg,
		ring: make([]*model.Event, cfg.RingSize),
	}
	if cfg.DiskOverflowPath != "" {
		db, err := bolt.Open(cfg.DiskOverflowPath, 0o600, &bolt.Options{Timeout: 5 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("relay: open overflow %s: %w", cfg.DiskOverflowPath, err)
		}
		if err := db.Update(func(tx *bolt.Tx) error {
			if _, err := tx.CreateBucketIfNotExists([]byte(outboxBucket)); err != nil {
				return err
			}
			if _, err := tx.CreateBucketIfNotExists([]byte(metaBucket)); err != nil {
				return err
			}
			return nil
		}); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("relay: init overflow buckets: %w", err)
		}
		o.overflow = db
	}
	return o, nil
}

func applyDefaults(c Config) Config {
	if c.RingSize <= 0 {
		c.RingSize = 4096
	}
	if c.DiskMaxBytes <= 0 {
		c.DiskMaxBytes = 256 * 1024 * 1024
	}
	if c.EvictionLogEvery <= 0 {
		c.EvictionLogEvery = time.Second
	}
	return c
}

// Close flushes the overflow file. Visit hot path doesn't call this.
func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.overflow != nil {
		err := o.overflow.Close()
		o.overflow = nil
		return err
	}
	return nil
}

// Enqueue accepts a pre-enriched event for relay. Never blocks the
// caller. Returns nil except on programmer-error inputs (nil event).
//
// Order of preference:
//  1. Push to the in-memory ring while it has slack.
//  2. If the ring is full and we have a disk overflow, push to disk.
//  3. If both are full, evict the oldest disk row (FIFO) and try
//     again. Logs a 1-per-second WARN.
func (o *Outbox) Enqueue(ev *model.Event) error {
	if ev == nil {
		return errors.New("relay: nil event")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.ringSize < o.cfg.RingSize {
		o.ringPush(ev)
		o.stats.Enqueued++
		o.stats.RingDepth = o.ringSize
		return nil
	}

	if o.overflow != nil {
		if err := o.diskPush(ev); err == nil {
			o.stats.Enqueued++
			return nil
		} else if !errors.Is(err, errOverflowFull) {
			return err
		}
		// disk full → evict oldest then retry
		if err := o.diskEvictOldest(); err == nil {
			if err := o.diskPush(ev); err == nil {
				o.stats.Enqueued++
				o.stats.Evictions++
				o.maybeWarnEviction()
				return nil
			}
		}
	}

	// Ring full, disk-overflow disabled or wedged. Evict the
	// in-memory tail (oldest in ring) to make room for the new event.
	o.ringEvictOldest()
	o.ringPush(ev)
	o.stats.Enqueued++
	o.stats.Evictions++
	o.stats.RingDepth = o.ringSize
	o.maybeWarnEviction()
	return nil
}

// Drain returns up to n events from the head of the queue (oldest
// first). Caller is expected to call Ack(seqs) after a successful
// remote send to advance the queue head; on failure, do nothing —
// the events stay in place and the next Drain re-issues them.
//
// The returned []DrainItem keeps the (seq, event) pairing the
// caller needs for Ack.
func (o *Outbox) Drain(n int) ([]DrainItem, error) {
	if n <= 0 {
		return nil, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]DrainItem, 0, n)
	// FIFO order: ring holds the OLDEST events (Enqueue pushes
	// there until full; only THEN spills to disk). Drain ring
	// first, then disk-overflow.
	for len(out) < n && o.ringSize > 0 {
		ev := o.ringPop()
		out = append(out, DrainItem{seq: ringSeq, ev: ev})
	}
	if len(out) < n && o.overflow != nil {
		items, err := o.diskPeek(n - len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	o.stats.RingDepth = o.ringSize
	return out, nil
}

// Ack confirms the caller successfully shipped a batch returned by
// Drain. Disk-backed entries are deleted; ring-backed entries were
// already popped by Drain so this is a no-op for them.
func (o *Outbox) Ack(items []DrainItem) error {
	if len(items) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.overflow == nil {
		return nil
	}
	return o.overflow.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(outboxBucket))
		for _, it := range items {
			if it.seq == ringSeq {
				continue
			}
			if err := bk.Delete(seqKey(it.seq)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Requeue undoes a Drain on failure: ring-backed events go back to
// the head of the ring; disk-backed events stay where they are
// (they were never deleted).
func (o *Outbox) Requeue(items []DrainItem) {
	if len(items) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.seq != ringSeq {
			continue
		}
		// Push back at the HEAD of the ring (oldest-first).
		o.ringPushFront(it.ev)
	}
	o.stats.RingDepth = o.ringSize
	_ = o.cfg // keep field referenced in case future logic uses it
}

// Stats returns a snapshot.
func (o *Outbox) Stats() Stats {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.stats
	if o.overflow != nil {
		_ = o.overflow.View(func(tx *bolt.Tx) error {
			bk := tx.Bucket([]byte(outboxBucket))
			st := bk.Stats()
			s.DiskDepth = uint64(st.KeyN)
			s.DiskBytes = int64(st.LeafAlloc + st.BranchAlloc)
			return nil
		})
	}
	s.RingDepth = o.ringSize
	return s
}

// DrainItem is the (seq, event) pairing returned by Drain. seq
// == ringSeq for in-memory entries; otherwise seq is the bbolt
// key under outboxBucket.
type DrainItem struct {
	seq uint64
	ev  *model.Event
}

// Event returns the underlying event row for the caller's batch.
func (d DrainItem) Event() *model.Event { return d.ev }

// ringSeq is the sentinel used in DrainItem.seq for events that
// came from the ring (no disk row to delete on Ack).
const ringSeq = ^uint64(0)

// --- ring internals -------------------------------------------------

func (o *Outbox) ringPush(ev *model.Event) {
	idx := (o.ringHead + o.ringSize) % o.cfg.RingSize
	o.ring[idx] = ev
	o.ringSize++
}

func (o *Outbox) ringPushFront(ev *model.Event) {
	if o.ringSize >= o.cfg.RingSize {
		// shouldn't happen in steady-state requeue, but if it does
		// drop the oldest from the back to make room.
		tailIdx := (o.ringHead + o.ringSize - 1) % o.cfg.RingSize
		o.ring[tailIdx] = nil
		o.ringSize--
	}
	o.ringHead = (o.ringHead - 1 + o.cfg.RingSize) % o.cfg.RingSize
	o.ring[o.ringHead] = ev
	o.ringSize++
}

func (o *Outbox) ringPop() *model.Event {
	ev := o.ring[o.ringHead]
	o.ring[o.ringHead] = nil
	o.ringHead = (o.ringHead + 1) % o.cfg.RingSize
	o.ringSize--
	return ev
}

func (o *Outbox) ringEvictOldest() {
	if o.ringSize == 0 {
		return
	}
	o.ring[o.ringHead] = nil
	o.ringHead = (o.ringHead + 1) % o.cfg.RingSize
	o.ringSize--
}

// --- disk overflow internals --------------------------------------

var errOverflowFull = errors.New("relay: disk overflow at capacity")

func (o *Outbox) diskPush(ev *model.Event) error {
	if o.overflow == nil {
		return errOverflowFull
	}
	encoded, err := encodeEvent(ev)
	if err != nil {
		return err
	}
	return o.overflow.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(outboxBucket))
		st := bk.Stats()
		if int64(st.LeafAlloc+st.BranchAlloc) >= o.cfg.DiskMaxBytes {
			return errOverflowFull
		}
		seq, err := bk.NextSequence()
		if err != nil {
			return err
		}
		return bk.Put(seqKey(seq), encoded)
	})
}

func (o *Outbox) diskEvictOldest() error {
	if o.overflow == nil {
		return errOverflowFull
	}
	return o.overflow.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(outboxBucket))
		c := bk.Cursor()
		k, _ := c.First()
		if k == nil {
			return nil
		}
		return bk.Delete(k)
	})
}

func (o *Outbox) diskPeek(n int) ([]DrainItem, error) {
	if o.overflow == nil {
		return nil, nil
	}
	out := make([]DrainItem, 0, n)
	err := o.overflow.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(outboxBucket))
		c := bk.Cursor()
		for k, v := c.First(); k != nil && len(out) < n; k, v = c.Next() {
			ev, err := decodeEvent(v)
			if err != nil {
				relayLog.Warnf("relay: decode disk event seq=%d: %v", binary.BigEndian.Uint64(k), err)
				continue
			}
			out = append(out, DrainItem{
				seq: binary.BigEndian.Uint64(k),
				ev:  ev,
			})
		}
		return nil
	})
	return out, err
}

func seqKey(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

func (o *Outbox) maybeWarnEviction() {
	if time.Since(o.lastEvictWarn) < o.cfg.EvictionLogEvery {
		return
	}
	o.lastEvictWarn = time.Now()
	relayLog.Warnf("relay: outbox eviction — events being dropped (total=%d)", o.stats.Evictions)
}

// --- gob codec -----------------------------------------------------

// EncodeEvent / DecodeEvent are exported because the drainer uses
// them to ship across the wire — same encoding both for disk and
// for the gRPC payload, so a relay-receiver decoding off the wire
// uses the same routine the local outbox used to write to disk.
func EncodeEvent(ev *model.Event) ([]byte, error) { return encodeEvent(ev) }
func DecodeEvent(b []byte) (*model.Event, error)  { return decodeEvent(b) }

func encodeEvent(ev *model.Event) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(ev); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeEvent(b []byte) (*model.Event, error) {
	ev := &model.Event{}
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(ev); err != nil {
		return nil, err
	}
	return ev, nil
}
