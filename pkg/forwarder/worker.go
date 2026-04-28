package forwarder

// Worker — bounded per-server async dispatch queue. Calling
// Enqueue from the visitor request path is non-blocking and
// allocation-cheap; the worker goroutine drains the queue and
// invokes Composite.Send for each event.
//
// Backpressure: when the queue is full the new event is DROPPED
// with a metric tick. Visitor requests must never block on a
// downstream forwarder being slow.

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tlalocweb/hulation/log"
)

const (
	// DefaultQueueSize is the per-server bounded buffer. 1024 events
	// at the typical 5 events/visitor amortizes ~200 visitors of
	// burst before dropping.
	DefaultQueueSize = 1024

	// DefaultSendTimeout caps each adapter Send call. Adapters that
	// hit this still get logged; the next event is unaffected.
	DefaultSendTimeout = 5 * time.Second
)

// Worker drains a queue and dispatches via the underlying Composite.
type Worker struct {
	composite *Composite
	queue     chan Event

	// Metrics — read-only counters for ops dashboards.
	enqueued atomic.Uint64
	dropped  atomic.Uint64
	sent     atomic.Uint64
	failed   atomic.Uint64

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewWorker constructs a worker with the given Composite and queue
// size. queueSize<=0 → DefaultQueueSize.
func NewWorker(c *Composite, queueSize int) *Worker {
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	return &Worker{
		composite: c,
		queue:     make(chan Event, queueSize),
		stopCh:    make(chan struct{}),
	}
}

// Start launches the drain goroutine. Idempotent — calling twice is
// harmless if the prior worker is still draining.
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.drain()
}

// Enqueue offers an event to the queue. Returns true on accept,
// false on drop (queue full). Non-blocking.
func (w *Worker) Enqueue(ev Event) bool {
	select {
	case w.queue <- ev:
		w.enqueued.Add(1)
		return true
	default:
		w.dropped.Add(1)
		return false
	}
}

// Stop signals the drain goroutine to exit and waits for it. Pending
// queued events are discarded — operators should drain via the
// queue's natural rate before shutdown.
func (w *Worker) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

// Stats returns the running counters as a snapshot.
type Stats struct {
	Enqueued, Dropped, Sent, Failed uint64
}

func (w *Worker) Stats() Stats {
	return Stats{
		Enqueued: w.enqueued.Load(),
		Dropped:  w.dropped.Load(),
		Sent:     w.sent.Load(),
		Failed:   w.failed.Load(),
	}
}

func (w *Worker) drain() {
	defer w.wg.Done()
	for {
		select {
		case <-w.stopCh:
			return
		case ev := <-w.queue:
			w.dispatch(ev)
		}
	}
}

func (w *Worker) dispatch(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultSendTimeout)
	defer cancel()

	errs := w.composite.Send(ctx, ev)
	if len(errs) == 0 {
		w.sent.Add(1)
		return
	}
	w.failed.Add(1)
	for _, e := range errs {
		log.Debugf("forwarder: send failed (server=%s ev=%s): %s", ev.ServerID, ev.EventID, e.Error())
	}
}

// --- Per-server worker registry -----------------------------------

var (
	workerMu sync.RWMutex
	workers  = make(map[string]*Worker)
)

// SetWorkerForServer installs and Start()s a worker for the given
// server. Replaces and Stops any prior worker.
func SetWorkerForServer(serverID string, w *Worker) {
	workerMu.Lock()
	if old, ok := workers[serverID]; ok {
		go old.Stop()
	}
	workers[serverID] = w
	workerMu.Unlock()
	w.Start()
}

// GetWorkerForServer returns the worker for serverID or nil.
func GetWorkerForServer(serverID string) *Worker {
	workerMu.RLock()
	defer workerMu.RUnlock()
	return workers[serverID]
}

// EnqueueForServer is a convenience shortcut callers in
// handler/visitor.go use without poking at the registry directly.
// Returns true if accepted, false if the server has no worker
// configured (caller treats as "no forwarders enabled — silent").
func EnqueueForServer(serverID string, ev Event) bool {
	w := GetWorkerForServer(serverID)
	if w == nil {
		return false
	}
	return w.Enqueue(ev)
}

// ResetWorkers wipes the worker registry. Tests only.
func ResetWorkers() {
	workerMu.Lock()
	for _, w := range workers {
		go w.Stop()
	}
	workers = make(map[string]*Worker)
	workerMu.Unlock()
}
