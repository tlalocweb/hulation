package local

// In-process pub/sub for Watch. Every Put / Delete / Batch op on
// LocalStorage publishes to this informer; subscribers whose
// prefix is a prefix of the changed key receive an Event on
// their channel.
//
// Slow consumers drop. Watch is for change notification, not a
// durable log — anyone who needs the canonical state should
// read it back via Get / List after they receive an event.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// watchChanCap is the buffer size for each Watch subscriber's
// channel. 64 is the same default izcr uses; large enough to
// absorb a small write burst, small enough that a runaway
// consumer doesn't pin unbounded memory.
const watchChanCap = 64

type subscription struct {
	prefix string
	ch     chan storage.Event
	cancel context.CancelFunc
}

// informer is a per-LocalStorage pub/sub. Methods are
// goroutine-safe.
type informer struct {
	mu   sync.RWMutex
	subs []*subscription
}

func newInformer() *informer { return &informer{} }

// subscribe registers a new watcher and returns its channel.
// Cancelling ctx releases the subscription and closes the
// channel.
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

// publish notifies every subscriber whose prefix is a prefix of
// the key. Drops on full channel — Watch is for notification,
// not a durable feed.
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
			// Slow consumer; drop.
		}
	}
}

// shutdown closes every subscription. Called by LocalStorage.Close.
func (i *informer) shutdown() {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, sub := range i.subs {
		sub.cancel()
	}
	i.subs = nil
}
