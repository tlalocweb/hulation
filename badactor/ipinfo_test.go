package badactor

import (
	"sync"
	"testing"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// withTestCache swaps in a fresh LRU for the duration of a test. Avoids
// stomping on whatever a previous test or production init left behind.
func withTestCache(t *testing.T, capacity int, ttl time.Duration, fn func()) {
	t.Helper()
	ipInfoCacheMu.Lock()
	prev := ipInfoCache
	ipInfoCache = lru.NewLRU[string, *IPInfo](capacity, nil, ttl)
	ipInfoCacheMu.Unlock()
	defer func() {
		ipInfoCacheMu.Lock()
		ipInfoCache = prev
		ipInfoCacheMu.Unlock()
	}()
	fn()
}

func TestGetIPInfo_AddRoundTrip(t *testing.T) {
	withTestCache(t, 16, time.Hour, func() {
		ipInfoCache.Add("1.2.3.4", &IPInfo{
			CountryCode: "US",
			Region:      "CA",
			City:        "SF",
			LookedUpAt:  time.Now(),
		})
		got := GetIPInfo("1.2.3.4")
		if got == nil {
			t.Fatalf("expected hit, got nil")
		}
		if got.CountryCode != "US" || got.City != "SF" {
			t.Fatalf("unexpected payload: %+v", got)
		}
	})
}

func TestGetIPInfo_EvictsOldestAtCapacity(t *testing.T) {
	withTestCache(t, 2, time.Hour, func() {
		now := time.Now()
		ipInfoCache.Add("a", &IPInfo{CountryCode: "AA", LookedUpAt: now})
		ipInfoCache.Add("b", &IPInfo{CountryCode: "BB", LookedUpAt: now})
		// "c" pushes "a" out (capacity = 2).
		ipInfoCache.Add("c", &IPInfo{CountryCode: "CC", LookedUpAt: now})
		if got := GetIPInfo("a"); got != nil {
			t.Fatalf("expected 'a' evicted, got %+v", got)
		}
		if got := GetIPInfo("b"); got == nil || got.CountryCode != "BB" {
			t.Fatalf("expected 'b' resident, got %+v", got)
		}
		if got := GetIPInfo("c"); got == nil || got.CountryCode != "CC" {
			t.Fatalf("expected 'c' resident, got %+v", got)
		}
	})
}

func TestGetIPInfo_NilCacheIsSafe(t *testing.T) {
	ipInfoCacheMu.Lock()
	prev := ipInfoCache
	ipInfoCache = nil
	ipInfoCacheMu.Unlock()
	defer func() {
		ipInfoCacheMu.Lock()
		ipInfoCache = prev
		ipInfoCacheMu.Unlock()
	}()
	if got := GetIPInfo("anything"); got != nil {
		t.Fatalf("nil-cache GetIPInfo must return nil, got %+v", got)
	}
}

func TestPendingLookups_DedupesConcurrentCallers(t *testing.T) {
	pendingLookups = sync.Map{}
	if _, busy := pendingLookups.LoadOrStore("1.2.3.4", struct{}{}); busy {
		t.Fatalf("first call must not see busy")
	}
	if _, busy := pendingLookups.LoadOrStore("1.2.3.4", struct{}{}); !busy {
		t.Fatalf("second call must see busy (dedupe failed)")
	}
	pendingLookups.Delete("1.2.3.4")
	if _, busy := pendingLookups.LoadOrStore("1.2.3.4", struct{}{}); busy {
		t.Fatalf("after Delete, dedupe slot must be free")
	}
}
