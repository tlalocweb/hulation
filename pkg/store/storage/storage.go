// Package storage is hula's unified persistence interface. Everything
// that needs durable state goes through this seam: ACL grants, OPAQUE
// records, goals, scheduled reports, alerts, mobile-device
// registrations, notification prefs, chat ACL, consent log, cookieless
// salts.
//
// Two implementations:
//
//   - pkg/store/storage/local — wraps bbolt directly. Used by tests
//     and offline CLI tools that need direct file access.
//   - pkg/store/storage/raft — wraps hashicorp/raft + raft-boltdb.
//     The production default once HA Plan 2 lands.
//
// The interface is modeled on ../izcr/pkg/store/common/types.go::Storage,
// trimmed to the core key/value primitives that map cleanly onto a
// Raft FSM. See HA_PLAN1.md for the design rationale.
package storage

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. Callers check via errors.Is.
var (
	// ErrNotFound is returned by Get for a missing key.
	ErrNotFound = errors.New("storage: key not found")

	// ErrCASFailed is returned by CompareAndSwap / CompareAndCreate
	// when the precondition isn't met.
	ErrCASFailed = errors.New("storage: compare-and-swap failed")

	// ErrSkip is returned by a MutateFn that decided not to write.
	// Storage.Mutate translates this to a successful no-op.
	ErrSkip = errors.New("storage: mutate skipped")

	// ErrClosed is returned by every method after Close has been
	// called on the Storage handle.
	ErrClosed = errors.New("storage: closed")
)

// Storage is the unified persistence contract.
//
// Keys are slash-delimited strings ("server_access/<userID>|<serverID>").
// The first slash-delimited segment is the "bucket" — implementations
// may use that to route to an underlying bolt bucket, raft FSM map
// shard, or anything else. Callers don't need to manage buckets.
//
// All methods are safe for concurrent use. Multiple goroutines may
// call Get / Put / Delete / etc. simultaneously.
type Storage interface {
	// Get returns the value for the key. Returns ErrNotFound if
	// the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)

	// Put writes the key. Overwrites any existing value.
	Put(ctx context.Context, key string, value []byte) error

	// Delete removes the key. Idempotent — no error if absent.
	Delete(ctx context.Context, key string) error

	// List returns every (key, value) pair under the prefix.
	// The map returned is owned by the caller (safe to mutate).
	List(ctx context.Context, prefix string) (map[string][]byte, error)

	// Keys returns just the keys under the prefix, sorted
	// lexicographically. Cheaper than List when callers only need
	// the index.
	Keys(ctx context.Context, prefix string) ([]string, error)

	// CompareAndSwap atomically replaces the value at key from
	// expected to fresh. Returns ErrCASFailed if the current value
	// doesn't match expected (including missing-vs-nil mismatch).
	CompareAndSwap(ctx context.Context, key string, expected, fresh []byte) error

	// CompareAndCreate atomically creates the key. Returns
	// ErrCASFailed if the key already exists.
	CompareAndCreate(ctx context.Context, key string, value []byte) error

	// Mutate runs the user-supplied mutator inside a single
	// transaction. The mutator receives the current value (or nil
	// if absent) and returns:
	//
	//   - (newValue, nil)         → write newValue
	//   - (nil, nil)              → delete the key
	//   - (nil, ErrSkip)          → leave unchanged
	//   - (nil, otherErr)         → abort, propagate error
	//
	// Implementations may serialise this through consensus (Raft)
	// using compare-and-swap with retry; LocalStorage runs it
	// inside a single bolt transaction.
	Mutate(ctx context.Context, key string, fn MutateFn) error

	// Batch runs multiple Put/Delete operations atomically in one
	// transaction. RaftStorage coalesces the whole batch into a
	// single Raft Apply.
	Batch(ctx context.Context, ops []BatchOp) error

	// Watch streams every change matching the prefix. The returned
	// channel is buffered (size 64 by default); consumers that
	// fall behind miss intermediate states, by design — Watch is
	// a change-notification primitive, not a durable log.
	//
	// Cancel the subscription via ctx.
	Watch(ctx context.Context, prefix string) (<-chan Event, error)

	// Close releases resources. Safe to call multiple times;
	// subsequent calls are no-ops.
	Close() error
}

// MutateFn is the signature for Storage.Mutate. See the Mutate
// docstring for the return-value semantics.
type MutateFn func(current []byte) (next []byte, err error)

// BatchOp is one operation inside a Storage.Batch transaction.
type BatchOp struct {
	Op    OpKind
	Key   string
	Value []byte // ignored for OpDelete
}

// OpKind identifies the kind of mutation in a BatchOp or Event.
type OpKind int

const (
	// OpPut writes a key. The Value field carries the new bytes.
	OpPut OpKind = iota
	// OpDelete removes a key. The Value field is ignored / nil.
	OpDelete
)

// String renders OpKind as a debug-friendly token.
func (o OpKind) String() string {
	switch o {
	case OpPut:
		return "put"
	case OpDelete:
		return "delete"
	default:
		return "?"
	}
}

// Event is the payload streamed via Watch.
type Event struct {
	Key       string
	Value     []byte // nil for OpDelete
	Op        OpKind
	Timestamp time.Time
}
