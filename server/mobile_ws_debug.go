package server

// Debug-publish endpoint for the Phase-5a.6 realtime hub. Lets the
// e2e harness verify WS propagation without needing the
// visitor-tracking ingest path to be wired into the hub yet (that
// plumbing is a stage-5a follow-up noted in PHASE_5A_STATUS.md).
//
// Admin-only via the authware Claims — we don't want an open
// broadcast endpoint on a public origin.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tlalocweb/hulation/pkg/realtime"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

// HandleMobileDebugPublish accepts a JSON realtime.Event body and
// hands it to the hub. Useful for:
//   - e2e harness assertions that a WS frame actually arrives
//   - local development without needing live visitor traffic
func HandleMobileDebugPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	claims, ok := r.Context().Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Admin-only. When the claims payload's is-admin bit arrives, we
	// tighten this to `claims.IsAdmin()`; for now, the bearer-token
	// presence is sufficient gating (the Phase-0 admin bearer middleware
	// only accepts the root user's JWT).
	var evt realtime.Event
	if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if evt.Ts.IsZero() {
		evt.Ts = time.Now().UTC()
	}
	hub := realtime.Global()
	if hub == nil {
		http.Error(w, "hub unavailable", http.StatusServiceUnavailable)
		return
	}
	delivered := hub.Broadcast(evt)
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"delivered_to": delivered,
		"subscribers":  hub.ActiveSubscribers(),
	})
}
