package handler

// Phase 4 step 2a — agent-mTLS BUILD endpoint.
//
// Mirrors handler.TriggerBuild (the admin-JWT path) but gates on the
// agent's registry record instead of admin auth. The Phase 3 mTLS
// middleware (pkg/agent/mtls.Middleware) attaches the active record
// to the request context; AgentBuild reads it and checks the
// per-site allow map before delegating to the shared build manager.
//
// Distinct from the admin endpoint by URL — agents POST to
// /api/agent/build, admins to /api/site/trigger-build. Keeps the
// two trust paths visibly separate; an operator scanning routes can
// tell at a glance which auth class each takes.
//
// The registered option-string (record.IsAllowed's second return)
// is parsed as comma-separated build args. Empty opts → no args.
// Agents do NOT get to pass args at HLAP-call time; whatever the
// registry says is what runs. (HULAAGENT_PLAN.md §Permission model.)

import (
	"encoding/json"
	"net/http"
	"strings"

	hulation "github.com/tlalocweb/hulation/app"
	agentmtls "github.com/tlalocweb/hulation/pkg/agent/mtls"
	"github.com/tlalocweb/hulation/sitedeploy"
)

type agentBuildRequest struct {
	Site string `json:"site"`
}

// AgentBuild handles POST /api/agent/build for agent-mTLS callers.
// Returns the same response shape as TriggerBuild so the Rust
// hulaagent's client and any test tooling can share a decoder.
func AgentBuild(ctx RequestCtx) error {
	rec := agentmtls.RecordFromContext(ctx.Context())
	if rec == nil {
		// Either no agent cert was presented, or the middleware
		// didn't attach a record (CA absent, registry unavailable).
		// 401 is the right code: client should re-auth, not change
		// the request shape.
		return ctx.Status(http.StatusUnauthorized).SendString("agent mTLS required")
	}

	var req agentBuildRequest
	body := ctx.Body()
	if err := json.Unmarshal(body, &req); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if req.Site == "" {
		return ctx.Status(http.StatusBadRequest).SendString("site required")
	}

	opts, ok := rec.IsAllowed(req.Site, "build")
	if !ok {
		// Distinguish "agent exists but isn't allowed for this
		// site/verb" from "agent cert isn't recognised" (401 above).
		// Operator response is different — widen the allow map vs.
		// re-mint the cert.
		return ctx.Status(http.StatusForbidden).SendString("agent not allowed: build on " + req.Site)
	}

	cfg := hulation.GetConfig()
	if cfg == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("config not loaded")
	}
	server := cfg.GetServerByID(req.Site)
	if server == nil {
		return ctx.Status(http.StatusNotFound).SendString("server not found: " + req.Site)
	}
	if server.GitAutoDeploy == nil {
		return ctx.Status(http.StatusBadRequest).SendString("server has no root_git_autodeploy config")
	}

	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("site deploy manager not initialized")
	}

	args := parseAgentBuildArgs(opts)
	buildID, err := bm.TriggerBuild(server, args)
	if err != nil {
		if bm.IsBuilding(req.Site) {
			return ctx.Status(http.StatusConflict).SendString("build already in progress for server " + req.Site)
		}
		return ctx.Status(http.StatusInternalServerError).SendString("build trigger failed: " + err.Error())
	}

	return ctx.SendJSON(triggerBuildResponse{
		Status:  "build_triggered",
		BuildID: buildID,
	})
}

// parseAgentBuildArgs splits the registered allow.build option-string
// into individual args. Empty string → nil. The format is
// comma-separated, with leading/trailing whitespace on each part
// stripped (consistent with how the registry comments document it).
//
// Exported is unnecessary; the parser is testable as a package-level
// function and that's enough.
func parseAgentBuildArgs(opts string) []string {
	opts = strings.TrimSpace(opts)
	if opts == "" {
		return nil
	}
	parts := strings.Split(opts, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
