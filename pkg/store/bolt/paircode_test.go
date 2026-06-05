package bolt_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

func TestBoltPairCodeStore_PutConsumeRoundTrip(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewPairCodeStore(s)
	now := time.Now().UTC()

	entry := authware.PairCode{
		Code:      "HULA-PAIR-ABCD-EFGH-JKLM",
		UserID:    "marcus",
		ServerID:  "site-a",
		Label:     "Marcus iPhone",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	}
	store.Put(entry)
	got, err := store.Consume(entry.Code, now)
	if err != nil {
		t.Fatalf("consume: %s", err)
	}
	if got.UserID != "marcus" || got.ServerID != "site-a" || got.Label != "Marcus iPhone" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Single-use: a second consume must return not-found.
	if _, err := store.Consume(entry.Code, now); !errors.Is(err, authware.ErrPairCodeNotFound) {
		t.Fatalf("second consume: want ErrPairCodeNotFound, got %v", err)
	}
}

func TestBoltPairCodeStore_RejectsExpiredAndDeletes(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewPairCodeStore(s)
	t0 := time.Now().UTC()
	store.Put(authware.PairCode{
		Code:      "HULA-PAIR-XXXX-YYYY-ZZZZ",
		ExpiresAt: t0.Add(time.Second),
	})
	if _, err := store.Consume("HULA-PAIR-XXXX-YYYY-ZZZZ", t0.Add(2*time.Second)); !errors.Is(err, authware.ErrPairCodeNotFound) {
		t.Fatalf("expired consume: %v", err)
	}
	// Even a second consume (with a future `now`) sees not-found because the
	// first Consume deleted the corrupt/expired row.
	if _, err := store.Consume("HULA-PAIR-XXXX-YYYY-ZZZZ", t0); !errors.Is(err, authware.ErrPairCodeNotFound) {
		t.Fatalf("re-consume after expiry: %v", err)
	}
}

func TestBoltPairCodeStore_NilStorageDegradesSafely(t *testing.T) {
	store := hulabolt.NewPairCodeStore(nil)
	store.Put(authware.PairCode{Code: "x"}) // must not panic
	if _, err := store.Consume("x", time.Now()); !errors.Is(err, authware.ErrPairCodeNotFound) {
		t.Fatalf("nil store consume: want ErrPairCodeNotFound, got %v", err)
	}
	if dropped := store.PurgeExpired(time.Now()); dropped != 0 {
		t.Fatalf("nil store purge: want 0, got %d", dropped)
	}
}

func TestBoltPairCodeStore_PurgeExpiredDropsOnlyOld(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewPairCodeStore(s)
	t0 := time.Now().UTC()
	store.Put(authware.PairCode{Code: "alive", ExpiresAt: t0.Add(time.Hour)})
	store.Put(authware.PairCode{Code: "dead", ExpiresAt: t0.Add(-time.Hour)})
	if dropped := store.PurgeExpired(t0); dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if _, err := store.Consume("alive", t0); err != nil {
		t.Fatalf("alive entry should still be there: %v", err)
	}
}

func TestBoltPairCodeStore_SurvivesReopen(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewPairCodeStore(s)
	now := time.Now().UTC()
	store.Put(authware.PairCode{
		Code:      "HULA-PAIR-PERS-ISTS-AAAA",
		UserID:    "u",
		ExpiresAt: now.Add(time.Hour),
	})
	// Spin up a fresh store handle against the same bolt — proves the entry
	// is on disk, not just in some in-memory cache.
	store2 := hulabolt.NewPairCodeStore(s)
	got, err := store2.Consume("HULA-PAIR-PERS-ISTS-AAAA", now)
	if err != nil {
		t.Fatalf("consume from fresh handle: %s", err)
	}
	if got.UserID != "u" {
		t.Fatalf("survived round-trip but got wrong row: %+v", got)
	}
}
