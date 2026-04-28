// Package forwarder fans out completed visitor events to outbound
// ad-platform / analytics endpoints (Meta Conversions API, GA4
// Measurement Protocol, etc.). Hula owns the customer's first-party
// data; the forwarder layer is what lets customers continue feeding
// their downstream ad-platform attribution without depending on the
// browser-side pixel that ad blockers shred.
//
// Phase 4c.2.
//
// Design notes:
//   * Per-server registry: each hula virtual server has its own
//     Composite (adapters configured under server.forwarders:).
//     Servers without forwarders see Composite=nil and the dispatch
//     short-circuits.
//   * Async dispatch: events ship to a bounded per-server queue
//     and a worker pool drains it on best-effort. Backpressure:
//     queue full → drop with a metric tick. NEVER blocks the
//     visitor request path.
//   * Consent-aware: each adapter declares which consent purpose
//     gates its dispatch (Meta CAPI gates on marketing; GA4 MP gates
//     on analytics). Composite reads the event's
//     ConsentAnalytics / ConsentMarketing flags and skips adapters
//     whose required purpose is false.
//   * Deletion API: adapters expose Delete(ctx, visitorID) so the
//     ForgetVisitor RPC can fan out the right-to-be-forgotten to
//     every downstream that supports it.
package forwarder

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ConsentPurpose is the consent flag an adapter needs set to true
// before it'll dispatch an event.
type ConsentPurpose string

const (
	// PurposeAnalytics gates analytics-only adapters (GA4 MP).
	PurposeAnalytics ConsentPurpose = "analytics"
	// PurposeMarketing gates marketing/advertising adapters (Meta CAPI).
	PurposeMarketing ConsentPurpose = "marketing"
	// PurposeAlways means "no gate" — useful for first-party CRM
	// forwarders where consent is implicit. None of the v1 adapters
	// use this; reserved for future use.
	PurposeAlways ConsentPurpose = "always"
)

// Event is the small, adapter-agnostic projection of model.Event the
// forwarder layer needs. We don't pass the full ClickHouse-bound
// struct around — the adapter package shouldn't import the analytics
// model (and circular-import would result if it did).
type Event struct {
	// EventID — uuid v7 from the events table.
	EventID string
	// EventCode — model.EventCode* (page-view, form-submit, etc.).
	EventCode uint64
	// VisitorID — model.Event.BelongsTo. Used as the GA4 client_id
	// and as the Meta external_id (hashed).
	VisitorID string
	// ServerID — hula virtual-server id this event belongs to.
	ServerID string
	// When — time the event occurred.
	When time.Time
	// URL — full URL of the page-view (or empty for non-page events).
	URL string
	// Referer — referrer URL.
	Referer string
	// IP — client IP at ingest. Adapters that hash PII (Meta CAPI)
	// SHA256 this before transmission.
	IP string
	// UserAgent — client UA. Same hashing rule as IP.
	UserAgent string
	// Email — visitor email when known (from chat sign-up, lander
	// submit, etc.). Empty when anonymous. Adapters hash before send.
	Email string
	// ConsentAnalytics — copy of model.Event.ConsentAnalytics.
	ConsentAnalytics bool
	// ConsentMarketing — copy of model.Event.ConsentMarketing.
	ConsentMarketing bool
}

// Adapter is the contract every forwarder backend implements.
type Adapter interface {
	// Name identifies the adapter for logging/metrics.
	Name() string
	// Purpose is the consent flag that gates this adapter.
	Purpose() ConsentPurpose
	// Send dispatches one event to the upstream. Returns nil on
	// success; transport errors surface for the worker to log.
	Send(ctx context.Context, ev Event) error
	// Delete fans out the GDPR right-to-be-forgotten to the
	// upstream. Returns nil if the upstream doesn't support
	// deletion (we treat that as "best-effort done").
	Delete(ctx context.Context, serverID, visitorID string) error
}

// Composite is the per-server fan-out container. Each adapter runs
// concurrently when an event lands. Backpressure protection lives in
// the surrounding Worker, not here.
type Composite struct {
	mu       sync.RWMutex
	adapters []Adapter
}

// NewComposite constructs a Composite from zero or more adapters.
func NewComposite(adapters ...Adapter) *Composite {
	return &Composite{adapters: append([]Adapter(nil), adapters...)}
}

// Add registers an adapter at runtime. Safe to call concurrently
// with Send / Delete.
func (c *Composite) Add(a Adapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adapters = append(c.adapters, a)
}

// Adapters returns a snapshot of the registered adapters. Useful for
// metrics + introspection.
func (c *Composite) Adapters() []Adapter {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Adapter(nil), c.adapters...)
}

// Send dispatches the event to every adapter whose consent purpose
// is satisfied. Adapters run concurrently; per-adapter errors are
// returned as a slice. The outer error is reserved for catastrophic
// failures (none currently).
func (c *Composite) Send(ctx context.Context, ev Event) []error {
	c.mu.RLock()
	bs := append([]Adapter(nil), c.adapters...)
	c.mu.RUnlock()
	if len(bs) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, len(bs))
	for i, a := range bs {
		if !consentAllows(a.Purpose(), ev) {
			continue
		}
		wg.Add(1)
		go func(i int, a Adapter) {
			defer wg.Done()
			errs[i] = a.Send(ctx, ev)
		}(i, a)
	}
	wg.Wait()

	out := make([]error, 0, len(bs))
	for _, e := range errs {
		if e != nil {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Delete fans out the deletion to every adapter, regardless of
// consent state at deletion time (consent doesn't gate erasure —
// that's a privacy-floor not a privacy-gate).
func (c *Composite) Delete(ctx context.Context, serverID, visitorID string) []error {
	c.mu.RLock()
	bs := append([]Adapter(nil), c.adapters...)
	c.mu.RUnlock()
	if len(bs) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, len(bs))
	for i, a := range bs {
		wg.Add(1)
		go func(i int, a Adapter) {
			defer wg.Done()
			errs[i] = a.Delete(ctx, serverID, visitorID)
		}(i, a)
	}
	wg.Wait()

	out := make([]error, 0, len(bs))
	for _, e := range errs {
		if e != nil {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func consentAllows(p ConsentPurpose, ev Event) bool {
	switch p {
	case PurposeAnalytics:
		return ev.ConsentAnalytics
	case PurposeMarketing:
		return ev.ConsentMarketing
	case PurposeAlways:
		return true
	default:
		return false
	}
}

// --- Per-server registry --------------------------------------------

var (
	registryMu sync.RWMutex
	registry   = make(map[string]*Composite)
)

// SetForServer installs the Composite for the given server ID.
// Replaces any prior registration.
func SetForServer(serverID string, c *Composite) {
	registryMu.Lock()
	registry[serverID] = c
	registryMu.Unlock()
}

// GetForServer returns the Composite for the given server ID or nil
// when none is configured.
func GetForServer(serverID string) *Composite {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[serverID]
}

// Reset wipes the registry. Tests only.
func Reset() {
	registryMu.Lock()
	registry = make(map[string]*Composite)
	registryMu.Unlock()
}

// ErrSkipped indicates an adapter intentionally skipped an event
// (e.g. unsupported event_code, missing required field). Worker
// treats it as success — distinct from a transport error.
var ErrSkipped = errors.New("forwarder: event skipped by adapter")
