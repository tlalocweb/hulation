package enrich

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionWindow is the inactivity threshold that closes a session.
// Industry standard across Plausible, GA, and Fathom is 30 minutes.
const SessionWindow = 30 * time.Minute

// sessionCache holds the last-seen timestamp and session_id per visitor.
// It is intentionally in-memory only: on restart, the next event for a
// visitor will open a fresh session (which is the correct behavior when
// the server has been unavailable anyway).
type sessionCache struct {
	mu sync.Mutex
	// visitorState maps visitor_id → (last_event_at, session_id).
	visitorState map[string]*sessionState
}

type sessionState struct {
	lastSeenAt time.Time
	sessionID  string
}

var globalSessionCache = &sessionCache{
	visitorState: make(map[string]*sessionState),
}

// SessionIDForVisitor returns the active session_id for a visitor. If the
// visitor was last seen within SessionWindow, the existing ID is reused;
// otherwise a fresh UUID is minted and cached.
//
// Callers should invoke this once per event immediately before inserting
// the row. The returned ID is deterministic within a session and safe to
// query against.
func SessionIDForVisitor(visitorID string, now time.Time) string {
	if visitorID == "" {
		return ""
	}
	globalSessionCache.mu.Lock()
	defer globalSessionCache.mu.Unlock()

	st, ok := globalSessionCache.visitorState[visitorID]
	if !ok || now.Sub(st.lastSeenAt) > SessionWindow {
		st = &sessionState{
			sessionID: uuid.Must(uuid.NewV7()).String(),
		}
		globalSessionCache.visitorState[visitorID] = st
	}
	st.lastSeenAt = now
	return st.sessionID
}

// ResetSessionCacheForTesting clears the in-memory cache. Only for tests.
func ResetSessionCacheForTesting() {
	globalSessionCache.mu.Lock()
	defer globalSessionCache.mu.Unlock()
	globalSessionCache.visitorState = make(map[string]*sessionState)
}

// SessionCacheSize reports the number of tracked visitors. Used by tests
// and for observability.
func SessionCacheSize() int {
	globalSessionCache.mu.Lock()
	defer globalSessionCache.mu.Unlock()
	return len(globalSessionCache.visitorState)
}

// PruneExpiredSessions removes entries older than retentionWindow. Call
// periodically (e.g., once per minute) to bound memory under heavy
// visitor turnover. A retentionWindow of 2*SessionWindow is a reasonable
// default — anything older definitely won't extend an existing session.
func PruneExpiredSessions(now time.Time, retentionWindow time.Duration) int {
	globalSessionCache.mu.Lock()
	defer globalSessionCache.mu.Unlock()

	cutoff := now.Add(-retentionWindow)
	removed := 0
	for id, st := range globalSessionCache.visitorState {
		if st.lastSeenAt.Before(cutoff) {
			delete(globalSessionCache.visitorState, id)
			removed++
		}
	}
	return removed
}
