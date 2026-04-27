package opaque

// In-memory session cache for OPAQUE registration + login flows.
//
// Both flows are 2-pass and stateful between the init and finish
// RPCs: the server's KE2 generation produces a `ServerOutput`
// holding the derived ClientMAC + SessionSecret that LoginFinish
// needs to verify the client's KE3.
//
// We cache the per-session state under an opaque session_id the
// init handler returns to the client. Client passes session_id back
// on the finish call.
//
// TTL: 60s. If the user takes longer than that to complete a login
// they retry from scratch — acceptable cost for a defensive cap on
// memory growth (one session ≈ a few KB).

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/bytemare/opaque"
)

const sessionTTL = 60 * time.Second

// pendingLogin holds the server state between LoginInit and
// LoginFinish.
type pendingLogin struct {
	server   *opaque.Server     // the bytemare server instance with key material installed
	output   *opaque.ServerOutput
	username string             // for logging + claim issuance
	provider string             // "admin" | "internal"
	expires  time.Time
}

type sessionCache struct {
	mu    sync.Mutex
	items map[string]*pendingLogin
}

func newSessionCache() *sessionCache {
	return &sessionCache{items: make(map[string]*pendingLogin)}
}

// put generates a fresh session_id and stores the entry. Caller
// returns the id to the client.
func (c *sessionCache) put(p *pendingLogin) string {
	id := newSessionID()
	p.expires = time.Now().Add(sessionTTL)
	c.mu.Lock()
	c.items[id] = p
	c.evictExpiredLocked()
	c.mu.Unlock()
	return id
}

// take retrieves + removes the entry. Returns nil if missing or
// expired (caller maps to Unauthenticated / NotFound).
func (c *sessionCache) take(id string) *pendingLogin {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.items[id]
	if !ok {
		return nil
	}
	delete(c.items, id)
	if time.Now().After(p.expires) {
		return nil
	}
	return p
}

// evictExpiredLocked is best-effort and runs every put. With a
// 60s TTL and login frequencies in the single-digits/sec, the
// cache stays small without a background goroutine.
func (c *sessionCache) evictExpiredLocked() {
	now := time.Now()
	for id, p := range c.items {
		if now.After(p.expires) {
			delete(c.items, id)
		}
	}
}

// newSessionID returns a 24-char hex token (12 bytes of randomness).
func newSessionID() string {
	return hex.EncodeToString(RandomBytes(12))
}
