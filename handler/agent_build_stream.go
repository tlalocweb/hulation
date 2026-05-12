package handler

// Phase 4 step 2b — agent-mTLS BUILD log-streaming endpoint.
//
// Companion to AgentBuild. After an agent POSTs /api/agent/build and
// receives a build_id, it GETs this endpoint to receive the build's
// log lines as they accumulate and a terminal envelope when the
// build hits Complete or Failed.
//
// Wire shape: newline-delimited JSON (one envelope per `\n`),
// streamed via chunked transfer. Matches HLAP's own JSON-Lines
// framing so the hulaagent process can re-emit each envelope onto
// its HLAP socket with minimal translation:
//
//   {"type":"log","line":"resolving deps..."}
//   {"type":"log","line":"compiling 12 files..."}
//   {"type":"end","status":"complete","build_id":"b_123","error":""}
//
// `type:"end"` always fires last; the response then closes.
//
// We poll the BuildState snapshot every 250ms rather than
// subscribing to a per-build channel. Polling avoids invasive
// changes to sitedeploy.BuildState (which would otherwise need a
// broadcast / fan-out mechanism); the cost is a ~250ms ceiling on
// log-line latency, which is well below human perception for a
// build that takes seconds to minutes.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	agentmtls "github.com/tlalocweb/hulation/pkg/agent/mtls"
	"github.com/tlalocweb/hulation/sitedeploy"
)

// agentBuildLogEnvelope is emitted once per new log line.
type agentBuildLogEnvelope struct {
	Type string `json:"type"`
	Line string `json:"line"`
}

// agentBuildEndEnvelope is emitted exactly once, after which the
// connection closes. `status` is the textual form of the terminal
// BuildStatus ("complete" / "failed"); `error` is empty on success.
type agentBuildEndEnvelope struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	BuildID string `json:"build_id"`
	Error   string `json:"error,omitempty"`
}

// agentBuildStreamPollInterval is how often we re-snapshot the build
// state when waiting for new log lines or a terminal status.
const agentBuildStreamPollInterval = 250 * time.Millisecond

// AgentBuildStream handles GET /api/agent/build/{buildid}/stream.
// Streams the addressed build's log lines as NDJSON envelopes,
// terminating with a `type:"end"` envelope when the build reaches
// a terminal status.
//
// Auth: requires an agent record on the request context (Phase 3
// middleware). Authz: the agent must have allow.build for the
// build's ServerID — prevents one agent from peeking at another
// agent's build by guessing the build_id.
func AgentBuildStream(w http.ResponseWriter, r *http.Request) {
	rec := agentmtls.RecordFromContext(r.Context())
	if rec == nil {
		http.Error(w, "agent mTLS required", http.StatusUnauthorized)
		return
	}

	buildID := r.PathValue("buildid")
	if buildID == "" {
		http.Error(w, "build id required", http.StatusBadRequest)
		return
	}

	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		http.Error(w, "site deploy manager not initialized", http.StatusServiceUnavailable)
		return
	}

	bs := bm.GetBuild(buildID)
	if bs == nil {
		http.Error(w, "build not found: "+buildID, http.StatusNotFound)
		return
	}

	// Authz: agent must be allowed for the build's site. Same
	// allow-key as the trigger endpoint so the two checks can't
	// disagree.
	snap := bs.Snapshot()
	if _, ok := rec.IsAllowed(snap.ServerID, "build"); !ok {
		http.Error(w, "agent not allowed: build on "+snap.ServerID, http.StatusForbidden)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// The server we run under must support flushing — if it
		// doesn't, surfacing a clean 500 is better than silently
		// buffering an entire build's output.
		http.Error(w, "streaming not supported by transport", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	// Hint to reverse proxies (nginx in particular) that we don't
	// want them buffering the response.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	streamBuild(r.Context(), bs, enc, flusher)
}

// streamBuild is the polling loop split out so it can be unit-tested
// against a constructed BuildState without standing up an http server.
func streamBuild(ctx context.Context, bs *sitedeploy.BuildState, enc *json.Encoder, flusher http.Flusher) {
	cursor := 0
	ticker := time.NewTicker(agentBuildStreamPollInterval)
	defer ticker.Stop()

	for {
		snap := bs.Snapshot()
		// Drain any log lines past the cursor.
		for i := cursor; i < len(snap.Logs); i++ {
			if err := enc.Encode(agentBuildLogEnvelope{
				Type: "log",
				Line: snap.Logs[i],
			}); err != nil {
				return // client gone; stop polling
			}
		}
		cursor = len(snap.Logs)
		flusher.Flush()

		if isTerminal(snap.Status) {
			_ = enc.Encode(agentBuildEndEnvelope{
				Type:    "end",
				Status:  snap.Status.String(),
				BuildID: snap.BuildID,
				Error:   snap.Error,
			})
			flusher.Flush()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// loop back to re-snapshot
		}
	}
}

func isTerminal(s sitedeploy.BuildStatus) bool {
	return s == sitedeploy.BuildComplete || s == sitedeploy.BuildFailed
}
