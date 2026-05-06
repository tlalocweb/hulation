package gossip

// The gossip protocol — push-on-write fan-out + periodic anti-
// entropy ticker. Both modes target the same set of peers (Raft
// membership) and use the same Sender callback to ship deltas;
// the Engine just orchestrates when each fires.

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Sender ships a list of deltas to one peer. Production wires a
// gRPC client to GossipService.Push; tests pass a fake.
type Sender interface {
	Push(ctx context.Context, peer Peer, deltas []Delta) error
	SyncDigest(ctx context.Context, peer Peer, digest map[string]HLC) ([]Delta, error)
}

// EngineConfig tunes the gossip engine.
type EngineConfig struct {
	AntiEntropyInterval time.Duration // default 10s
	PushTimeout         time.Duration // per-Push deadline. Default 3s
	SyncTimeout         time.Duration // per-SyncDigest deadline. Default 5s
	MaxDeltasPerSync    int           // bounded response size. Default 256
	PushFanOutTimeout   time.Duration // total budget across all push fan-outs. Default 5s
}

func applyEngineDefaults(c EngineConfig) EngineConfig {
	if c.AntiEntropyInterval <= 0 {
		c.AntiEntropyInterval = 10 * time.Second
	}
	if c.PushTimeout <= 0 {
		c.PushTimeout = 3 * time.Second
	}
	if c.SyncTimeout <= 0 {
		c.SyncTimeout = 5 * time.Second
	}
	if c.MaxDeltasPerSync <= 0 {
		c.MaxDeltasPerSync = 256
	}
	if c.PushFanOutTimeout <= 0 {
		c.PushFanOutTimeout = 5 * time.Second
	}
	return c
}

// Engine drives the gossip lifecycle. Hooked into a Store via
// SetOnWrite so every local CRDT write fans out to peers, and
// runs an anti-entropy ticker in the background to reconcile any
// missed pushes.
type Engine struct {
	store    *Store
	peers    PeerProvider
	sender   Sender
	cfg      EngineConfig

	stopped chan struct{}
	cancel  context.CancelFunc

	rng *rand.Rand
	mu  sync.Mutex

	pushCount     atomic.Uint64
	pushFailures  atomic.Uint64
	syncCount     atomic.Uint64
	syncFailures  atomic.Uint64
	deltasMerged  atomic.Uint64
}

// NewEngine builds an Engine with the given dependencies. Call
// Start to launch the background goroutines.
func NewEngine(store *Store, peers PeerProvider, sender Sender, cfg EngineConfig) (*Engine, error) {
	if store == nil {
		return nil, errors.New("gossip: store required")
	}
	if peers == nil {
		return nil, errors.New("gossip: peers provider required")
	}
	if sender == nil {
		return nil, errors.New("gossip: sender required")
	}
	return &Engine{
		store:   store,
		peers:   peers,
		sender:  sender,
		cfg:     applyEngineDefaults(cfg),
		stopped: make(chan struct{}),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// Start wires the push-on-write hook into the store and launches
// the anti-entropy goroutine. Returns immediately.
func (e *Engine) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.store.SetOnWrite(func(bucket, key string, payload []byte, hlc HLC) {
		e.onLocalWrite(bucket, key, payload, hlc)
	})
	go e.antiEntropyLoop(ctx)
}

// Stop tears down the background goroutine and waits for it to exit.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	<-e.stopped
}

// Stats returns counter snapshots for ops + tests.
func (e *Engine) Stats() Stats {
	return Stats{
		Pushes:        e.pushCount.Load(),
		PushFailures:  e.pushFailures.Load(),
		Syncs:         e.syncCount.Load(),
		SyncFailures:  e.syncFailures.Load(),
		DeltasMerged:  e.deltasMerged.Load(),
	}
}

// Stats is the public counter snapshot.
type Stats struct {
	Pushes        uint64
	PushFailures  uint64
	Syncs         uint64
	SyncFailures  uint64
	DeltasMerged  uint64
}

// onLocalWrite is the SetOnWrite callback. Fires Push to every
// peer in fire-and-forget mode; missed pushes heal at the next
// anti-entropy tick.
func (e *Engine) onLocalWrite(bucket, key string, payload []byte, hlc HLC) {
	if bucket == bucketMeta {
		return
	}
	delta := Delta{Bucket: bucket, Key: key, Payload: payload, HLC: hlc}
	go e.fanOutPush(context.Background(), []Delta{delta})
}

func (e *Engine) fanOutPush(parent context.Context, deltas []Delta) {
	ctx, cancel := context.WithTimeout(parent, e.cfg.PushFanOutTimeout)
	defer cancel()
	peers, err := e.peers.Peers(ctx)
	if err != nil {
		return
	}
	for _, p := range peers {
		p := p
		go func() {
			pctx, pcancel := context.WithTimeout(ctx, e.cfg.PushTimeout)
			defer pcancel()
			if err := e.sender.Push(pctx, p, deltas); err != nil {
				e.pushFailures.Add(1)
				return
			}
			e.pushCount.Add(1)
		}()
	}
}

func (e *Engine) antiEntropyLoop(ctx context.Context) {
	defer close(e.stopped)
	t := time.NewTicker(e.cfg.AntiEntropyInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.runAntiEntropyOnce(ctx)
		}
	}
}

// runAntiEntropyOnce picks one random peer (HA_PLAN3 §7.4) and
// exchanges per-bucket digests. Any deltas the peer returns get
// merged into the local store via ApplyDelta.
func (e *Engine) runAntiEntropyOnce(ctx context.Context) {
	peers, err := e.peers.Peers(ctx)
	if err != nil || len(peers) == 0 {
		return
	}
	e.mu.Lock()
	idx := e.rng.Intn(len(peers))
	e.mu.Unlock()
	peer := peers[idx]

	digest := make(map[string]HLC, len(dataBuckets))
	for _, bucket := range dataBuckets {
		hw, err := e.store.HighWater(ctx, bucket)
		if err != nil {
			continue
		}
		digest[bucket] = hw
	}

	syncCtx, cancel := context.WithTimeout(ctx, e.cfg.SyncTimeout)
	defer cancel()

	deltas, err := e.sender.SyncDigest(syncCtx, peer, digest)
	if err != nil {
		e.syncFailures.Add(1)
		return
	}
	e.syncCount.Add(1)
	for _, d := range deltas {
		changed, _, err := e.store.ApplyDelta(ctx, d)
		if err != nil {
			continue
		}
		if changed {
			e.deltasMerged.Add(1)
		}
	}
}

// SyncFromDigestRequest is the receiver-side helper used by the
// GossipService.SyncDigest implementation to compute the response.
// Returned deltas are everything in any of the named buckets whose
// HLC is After the requester's high-water for that bucket.
//
// Lives on Store but exposed here too so the server impl can call
// it without taking a *Store dep that exposes Open / Close (which
// it shouldn't need).
func (e *Engine) SyncFromDigest(ctx context.Context, digest map[string]HLC, max int) ([]Delta, error) {
	return e.store.SyncFromDigest(ctx, digest, max)
}

// SyncFromDigest on the Store walks the named buckets and returns
// every record whose HLC is After the requester's reported value.
func (s *Store) SyncFromDigest(ctx context.Context, digest map[string]HLC, max int) ([]Delta, error) {
	if max <= 0 {
		max = 256
	}
	out := make([]Delta, 0, max)
	for _, bucket := range dataBuckets {
		since, ok := digest[bucket]
		if !ok {
			since = HLC{}
		}
		more, err := s.DeltasSince(ctx, bucket, since, max-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
		if len(out) >= max {
			return out, nil
		}
	}
	return out, nil
}
