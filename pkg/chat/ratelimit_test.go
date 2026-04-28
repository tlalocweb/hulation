package chat

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiterBasic(t *testing.T) {
	r := NewRateLimiter(time.Minute, 3)
	for i := 0; i < 3; i++ {
		if !r.Allow("srv", "1.2.3.4") {
			t.Errorf("attempt %d: should allow", i+1)
		}
	}
	// 4th attempt within window: deny.
	if r.Allow("srv", "1.2.3.4") {
		t.Error("4th attempt should be denied")
	}
	// Different ip / server gets its own bucket.
	if !r.Allow("srv", "5.6.7.8") {
		t.Error("different ip should not share bucket")
	}
	if !r.Allow("other", "1.2.3.4") {
		t.Error("different server_id should not share bucket")
	}
}

func TestRateLimiterExpiry(t *testing.T) {
	r := NewRateLimiter(50*time.Millisecond, 1)
	if !r.Allow("srv", "ip") {
		t.Fatal("first should allow")
	}
	if r.Allow("srv", "ip") {
		t.Error("second-immediate should deny")
	}
	time.Sleep(60 * time.Millisecond)
	if !r.Allow("srv", "ip") {
		t.Error("after expiry, should allow again")
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	r := NewRateLimiter(time.Second, 100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Allow("srv", "ip-shared")
		}()
	}
	wg.Wait()
	// 50 hits, max 100 — should still allow more.
	if !r.Allow("srv", "ip-shared") {
		t.Error("under cap; should still allow")
	}
}

func TestNilLimiterAllows(t *testing.T) {
	var r *RateLimiter
	if !r.Allow("a", "b") {
		t.Error("nil receiver should fail-open")
	}
}
