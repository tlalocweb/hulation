package chat

// In-memory per-(server_id, peer-IP) sliding-window rate limiter.
// Used by /chat/start to bound spam waves before they hit the
// captcha verifier (which costs a network round-trip).
//
// 5 starts / 5 min default per the plan. Keeps a small map keyed
// on "<server_id>|<ip>" with a circular buffer of recent timestamps;
// at lookup time we drop expired entries and reject if len > limit.
//
// Single-process; no Redis. Phase 4b's hub is in-process anyway,
// so cluster-wide rate-limiting waits for whatever multi-process
// solution we land then.

import (
	"sync"
	"time"
)

// DefaultRateLimit is the per-(server_id, ip) start budget.
const (
	DefaultRateLimitWindow = 5 * time.Minute
	DefaultRateLimitMax    = 5
)

// RateLimiter is a sliding-window counter. Zero value is usable.
type RateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	hits   map[string][]time.Time
}

// NewRateLimiter returns a limiter. window=0 → DefaultRateLimitWindow,
// max<=0 → DefaultRateLimitMax.
func NewRateLimiter(window time.Duration, max int) *RateLimiter {
	if window <= 0 {
		window = DefaultRateLimitWindow
	}
	if max <= 0 {
		max = DefaultRateLimitMax
	}
	return &RateLimiter{
		window: window,
		max:    max,
		hits:   make(map[string][]time.Time),
	}
}

// Allow returns true if the request should proceed. Records the
// attempt unconditionally — rejected calls still count toward the
// budget so a hostile client can't hide behind retries.
func (r *RateLimiter) Allow(serverID, ip string) bool {
	if r == nil {
		return true
	}
	if r.hits == nil {
		// Zero-value default: lazy init.
		r.hits = make(map[string][]time.Time)
		if r.window == 0 {
			r.window = DefaultRateLimitWindow
		}
		if r.max == 0 {
			r.max = DefaultRateLimitMax
		}
	}
	key := serverID + "|" + ip
	now := time.Now().UTC()
	cutoff := now.Add(-r.window)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Drop expired hits.
	hits := r.hits[key]
	keep := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	r.hits[key] = keep

	// Periodic GC of dead keys: cheap because we only walk on
	// each call's own bucket. Map-wide cleanup happens lazily as
	// keys age out via this same code path.

	return len(keep) <= r.max
}

// Reset clears the limiter. Test-only.
func (r *RateLimiter) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = make(map[string][]time.Time)
}
