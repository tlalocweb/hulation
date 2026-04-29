package raftbackend

// Watch fan-out for RaftStorage. Mirrors local.informer (same
// publish/subscribe contract, same drop-on-slow-consumer
// semantics) but lives next to the FSM so Apply can call it
// directly without crossing a package boundary.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// watchChanCap matches local.watchChanCap so Watch consumers see
// the same back-pressure profile across implementations.
const watchChanCap = 64

type subscription struct {
	prefix string
	ch     chan storage.Event
	cancel context.CancelFunc
}

type informer struct {
	mu   sync.RWMutex
	subs []*subscription
}

func newInformer() *informer { return &informer{} }

func (i *informer) subscribe(ctx context.Context, prefix string) <-chan storage.Event {
	wctx, cancel := context.WithCancel(ctx)
	sub := &subscription{
		prefix: prefix,
		ch:     make(chan storage.Event, watchChanCap),
		cancel: cancel,
	}
	i.mu.Lock()
	i.subs = append(i.subs, sub)
	i.mu.Unlock()

	go func() {
		<-wctx.Done()
		i.unsubscribe(sub)
	}()
	return sub.ch
}

func (i *informer) unsubscribe(sub *subscription) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for k, s := range i.subs {
		if s == sub {
			i.subs = append(i.subs[:k], i.subs[k+1:]...)
			close(s.ch)
			return
		}
	}
}

func (i *informer) publish(key string, value []byte, op storage.OpKind) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(i.subs) == 0 {
		return
	}
	ev := storage.Event{
		Key:       key,
		Value:     append([]byte(nil), value...),
		Op:        op,
		Timestamp: time.Now(),
	}
	for _, sub := range i.subs {
		if !strings.HasPrefix(key, sub.prefix) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer; drop. Callers reconcile by re-listing.
		}
	}
}

func (i *informer) shutdown() {
	// Detach the subs slice under lock so concurrent publish/
	// unsubscribe see an empty informer immediately, then close
	// channels and cancel contexts outside the lock. Without this
	// detach-first pattern, the per-sub unsubscribe goroutine
	// (spawned in subscribe) wakes on ctx.Done, scans the empty
	// slice, and returns without ever calling close(sub.ch) — so
	// Watch consumers blocked on the channel never see EOF.
	i.mu.Lock()
	subs := i.subs
	i.subs = nil
	i.mu.Unlock()
	for _, sub := range subs {
		close(sub.ch)
		sub.cancel()
	}
}
