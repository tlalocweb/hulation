package bolt

// Persistent PairCodeStore. Implements `authware.PairCodeStore` against a
// `storage.Storage` (bolt-backed in production). The keyspace is flat under
// `pair_codes/<code>`; entries store the full PairCode envelope as JSON so a
// future schema change can read old rows by ignoring unknown fields.
//
// Single-use semantics mirror the in-memory store: Consume reads-then-deletes
// inside a Mutate transaction, returning the prior value once. Expired
// entries are deleted on lookup so the issue-but-never-redeem path can't grow
// the bucket unbounded.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

const pairCodePrefix = "pair_codes/"

func pairCodeKey(code string) string { return pairCodePrefix + code }

// PairCodeStore wraps a `storage.Storage` and satisfies
// `authware.PairCodeStore`. Construct once at boot and pass to
// `wirePairHandlers`.
type PairCodeStore struct {
	store storage.Storage
}

// NewPairCodeStore returns a store backed by `s`. Passing nil produces a
// store whose methods all degrade to no-ops — callers that haven't initialised
// bolt yet can still call Put without nil-checking.
func NewPairCodeStore(s storage.Storage) *PairCodeStore {
	return &PairCodeStore{store: s}
}

// Put implements authware.PairCodeStore. Overwrites any existing entry.
// Errors are swallowed (logged on the caller's side) so the surrounding
// handler keeps its "always returns 200 on issue" contract — operators
// retrying on transient storage failures will see the error then.
func (s *PairCodeStore) Put(code authware.PairCode) {
	if s == nil || s.store == nil {
		return
	}
	bytes, err := json.Marshal(&code)
	if err != nil {
		return
	}
	_ = s.store.Put(context.Background(), pairCodeKey(code.Code), bytes)
}

// Consume implements authware.PairCodeStore. Reads + deletes the entry
// atomically via Mutate. Returns ErrPairCodeNotFound for any of {unknown,
// expired, json-corrupt} so the caller emits the same 410 Gone in all three
// cases (single-use semantics — a corrupt row is unrecoverable).
func (s *PairCodeStore) Consume(code string, now time.Time) (authware.PairCode, error) {
	if s == nil || s.store == nil {
		return authware.PairCode{}, authware.ErrPairCodeNotFound
	}
	var consumed authware.PairCode
	var found bool
	err := s.store.Mutate(context.Background(), pairCodeKey(code), func(current []byte) ([]byte, error) {
		if len(current) == 0 {
			return nil, nil // leave absent
		}
		var entry authware.PairCode
		if uerr := json.Unmarshal(current, &entry); uerr != nil {
			// Drop unreadable entries. Returning nil from a Mutator deletes
			// the key — exactly the "kick the corrupt row" behaviour we want.
			return nil, nil
		}
		// Always delete on read (single-use + bound the bucket size). The
		// caller decides expired vs. happy-path via the in-window check below.
		if !entry.IsExpired(now) {
			consumed = entry
			found = true
		}
		return nil, nil
	})
	if err != nil {
		return authware.PairCode{}, fmt.Errorf("paircode: consume: %w", err)
	}
	if !found {
		return authware.PairCode{}, authware.ErrPairCodeNotFound
	}
	return consumed, nil
}

// PurgeExpired scans every entry under the prefix and drops the ones whose
// ExpiresAt is past `now`. Returns the count dropped. Cheap given the bucket
// stays small (15-min TTL × low issue rate).
func (s *PairCodeStore) PurgeExpired(now time.Time) int {
	if s == nil || s.store == nil {
		return 0
	}
	ctx := context.Background()
	rows, err := s.store.List(ctx, pairCodePrefix)
	if err != nil {
		return 0
	}
	dropped := 0
	for key, raw := range rows {
		var entry authware.PairCode
		if uerr := json.Unmarshal(raw, &entry); uerr != nil {
			// Corrupt row — purge.
			_ = s.store.Delete(ctx, key)
			dropped++
			continue
		}
		if entry.IsExpired(now) {
			_ = s.store.Delete(ctx, key)
			dropped++
		}
	}
	return dropped
}

// stripPrefix returns the key sans the bucket prefix. Kept private because
// callers should treat the PairCodeStore as opaque — they only see codes,
// not raw storage keys.
func stripPrefix(key string) string {
	return strings.TrimPrefix(key, pairCodePrefix)
}

// Unused helper kept for future "list pending codes" admin surface; silenced
// to keep lints quiet until that endpoint lands.
var _ = stripPrefix
