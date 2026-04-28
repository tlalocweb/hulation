// Package notifier is hula's cross-channel delivery engine. The
// Phase-4 alert evaluator and any future source (scheduled reports
// on a push-only channel, operator-alerts, etc.) hand it an
// Envelope and let the notifier fan out across email + APNs + FCM.
//
// Design notes:
//   * Backends are pluggable — an implementation registers itself
//     with a Composite and the evaluator never reaches past the
//     interface.
//   * Dead-token handling is a first-class concern: when APNs or
//     FCM rejects a token permanently, the backend returns
//     ErrDeadToken wrapped with the offending device ID so the
//     caller can mark the device inactive and skip it next fire.
//   * No retries live inside the notifier — retry policy is the
//     caller's job (evaluator already implements cooldown +
//     throttle). Transient errors surface unwrapped so the caller
//     can decide whether to retry or record "retrying".
//
// Thread-safety: Composite is safe for concurrent Deliver calls.
// Individual backends are expected to be safe too; every backend in
// this package uses stdlib/http.Client which is goroutine-safe.

package notifier

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNotConfigured is returned when a backend can't deliver because
// operator config is missing. Surfaces as DELIVERY_STATUS_MAILER_-
// UNCONFIGURED (or the push equivalent) in alert-event rows.
var ErrNotConfigured = errors.New("notifier: backend not configured")

// ErrDeadToken signals that the transport rejected a push token
// permanently — caller should mark the device inactive. Wrapped in
// DeadTokenError so the caller can identify which device.
var ErrDeadToken = errors.New("notifier: push token rejected permanently")

// DeadTokenError wraps ErrDeadToken with the offending device ID.
type DeadTokenError struct {
	DeviceID string
	Wrapped  error
}

func (e *DeadTokenError) Error() string {
	return fmt.Sprintf("dead token on device %q: %v", e.DeviceID, e.Wrapped)
}
func (e *DeadTokenError) Unwrap() error { return ErrDeadToken }

// Channel is the transport name recorded in StoredNotificationSend
// rows and in per-channel delivery results.
type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelAPNS  Channel = "apns"
	ChannelFCM   Channel = "fcm"
)

// DeviceAddr describes one recipient endpoint. A single user who
// registered an iPhone and a work email becomes two DeviceAddr
// entries in the Envelope's Recipients slice.
type DeviceAddr struct {
	Channel   Channel
	UserID    string
	DeviceID  string // empty for email
	Email     string // empty for push
	PushToken string // opened plaintext (tokenbox.Open has run)
}

// Envelope is what the caller hands to a Notifier. Per-channel
// payloads are separate because email wants HTML + a long subject
// while push wants a 180-ish-char short-text body.
type Envelope struct {
	ID        string // caller-supplied; correlates with the source event
	Subject   string
	HTMLBody  string
	ShortText string
	Recipients []DeviceAddr
}

// ChannelResult is the per-recipient outcome.
type ChannelResult struct {
	Channel  Channel
	DeviceID string
	UserID   string
	OK       bool
	Err      error
}

// DeliveryReport summarises a Deliver call. `AnyOK` is useful for
// the alert-event DeliveryStatus roll-up: success when any channel
// delivered, otherwise derive from the error set.
type DeliveryReport struct {
	Results []ChannelResult
}

// AnyOK is true when at least one channel delivered.
func (r DeliveryReport) AnyOK() bool {
	for _, c := range r.Results {
		if c.OK {
			return true
		}
	}
	return false
}

// AllConfigured is true when no channel returned ErrNotConfigured.
// Lets the caller distinguish "all failed" from "nothing configured".
func (r DeliveryReport) AllConfigured() bool {
	for _, c := range r.Results {
		if errors.Is(c.Err, ErrNotConfigured) {
			return false
		}
	}
	return true
}

// Notifier is the abstract backend contract. Implementations live
// under pkg/notifier/{email,apns,fcm}.
type Notifier interface {
	// Channel reports which transport this backend implements.
	Channel() Channel
	// Deliver attempts delivery to every recipient whose Channel
	// matches this backend. Recipients for other channels are
	// ignored. Returns per-recipient results; the outer error is
	// non-nil only for catastrophic failures (bad config structure,
	// etc.) — per-recipient errors go in the ChannelResult entries.
	Deliver(ctx context.Context, env Envelope) ([]ChannelResult, error)
}

// Composite fans out to every registered backend, concatenates the
// per-channel results, and returns a single DeliveryReport.
type Composite struct {
	mu       sync.RWMutex
	backends []Notifier
}

// NewComposite constructs a Composite. Zero backends → every
// Deliver returns an empty report.
func NewComposite(backends ...Notifier) *Composite {
	return &Composite{backends: append([]Notifier(nil), backends...)}
}

// Add registers an additional backend at runtime. Safe to call
// concurrently with Deliver.
func (c *Composite) Add(b Notifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backends = append(c.backends, b)
}

// Deliver fans out to every backend. Order of results within the
// report matches the order backends were registered — keeps tests
// deterministic.
func (c *Composite) Deliver(ctx context.Context, env Envelope) (DeliveryReport, error) {
	c.mu.RLock()
	bs := append([]Notifier(nil), c.backends...)
	c.mu.RUnlock()

	var all []ChannelResult
	for _, b := range bs {
		res, err := b.Deliver(ctx, env)
		if err != nil {
			// Backend-wide failure (e.g. bad config). Synthesize a
			// single result so the caller sees which channel broke.
			all = append(all, ChannelResult{
				Channel: b.Channel(),
				OK:      false,
				Err:     err,
			})
			continue
		}
		all = append(all, res...)
	}
	return DeliveryReport{Results: all}, nil
}

// Global holds the process-wide default Composite. Set by
// server.RunUnified at boot; consumed by the alert evaluator and
// the TestNotification RPC.
var (
	globalMu sync.RWMutex
	global   *Composite
)

// SetGlobal installs the process-wide Composite. Safe to call once
// per process; subsequent calls replace.
func SetGlobal(c *Composite) {
	globalMu.Lock()
	global = c
	globalMu.Unlock()
}

// Global returns the process-wide Composite or nil when unset.
// Callers should tolerate nil (evaluator logs-and-skips when it's
// nil — same "log only" degradation the pre-5a mailer showed).
func Global() *Composite {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
