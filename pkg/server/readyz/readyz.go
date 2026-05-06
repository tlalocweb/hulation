// Package readyz implements the /readyz HTTP probe (HA_PLAN3 §11).
// External LBs poll this every ~5s to decide whether to drain a
// node. Returns 200 only when Raft is healthy enough to serve
// reads; CH reachability is intentionally NOT a check (the relay
// outbox absorbs CH outages — Q18(a) settled in interview round 5).
package readyz

import (
	"encoding/json"
	"net/http"
)

// CatchupTolerance is how far behind the leader's known last-index
// a follower may be while still answering 200. HA_PLAN3 §11
// pins this at 100 entries.
const CatchupTolerance = 100

// State is what /readyz inspects. RaftStorage produces this via
// its accessor methods; tests pass a mock.
type State interface {
	// RaftAlive reports whether the local Raft state machine is in
	// any non-shutdown state (Leader, Follower, or Candidate).
	RaftAlive() bool
	// LeaderInfo returns (id, addr) of the current leader. Empty
	// addr means no leader is currently elected.
	LeaderInfo() (id, addr string)
	// AppliedIndex is the last log entry the local FSM has applied.
	AppliedIndex() uint64
	// LastIndex is the last log entry this node has SEEN (which
	// may be ahead of AppliedIndex if applies are still in flight).
	LastIndex() uint64
}

// Handler returns an http.Handler that probes State on each request.
// state may be nil — solo deployments without an HA stack still get
// a clean 200 because the listener itself is up.
func Handler(state State) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state == nil {
			respond(w, http.StatusOK, response{OK: true, Reason: "no_ha_stack"})
			return
		}
		if !state.RaftAlive() {
			respond(w, http.StatusServiceUnavailable, response{
				OK:     false,
				Reason: "raft_down",
				Detail: "raft state is shutdown",
			})
			return
		}
		applied := state.AppliedIndex()
		last := state.LastIndex()
		if last > applied && last-applied > CatchupTolerance {
			respond(w, http.StatusServiceUnavailable, response{
				OK:     false,
				Reason: "raft_lagging",
				Detail: detailFromIndices(applied, last),
			})
			return
		}
		_, leaderAddr := state.LeaderInfo()
		if leaderAddr == "" {
			// No leader → minority partition or election in flight.
			// Respond 503 so an LB drains us; admin reads still work
			// against the local FSM but writes would surface 503.
			respond(w, http.StatusServiceUnavailable, response{
				OK:     false,
				Reason: "no_leader",
				Detail: "no leader currently elected",
			})
			return
		}
		respond(w, http.StatusOK, response{OK: true})
	})
}

type response struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func respond(w http.ResponseWriter, code int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func detailFromIndices(applied, last uint64) string {
	return "applied=" + u64String(applied) + " last=" + u64String(last)
}

func u64String(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
