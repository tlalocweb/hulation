package handler

import (
	"encoding/json"
	"net/http"

	hulation "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/sitedeploy"
)

type triggerBuildRequest struct {
	ID   string   `json:"id"`
	Args []string `json:"args,omitempty"`
}

type triggerBuildResponse struct {
	Status  string `json:"status"`
	BuildID string `json:"build_id"`
}

// TriggerBuild handles POST /api/site/trigger-build.
// Triggers an asynchronous site build for the given server ID.
func TriggerBuild(ctx RequestCtx) error {
	var req triggerBuildRequest
	body := ctx.Body()
	if err := json.Unmarshal(body, &req); err != nil {
		return ctx.Status(http.StatusBadRequest).SendString("bad request: " + err.Error())
	}
	if req.ID == "" {
		return ctx.Status(http.StatusBadRequest).SendString("server id required")
	}

	cfg := hulation.GetConfig()
	server := cfg.GetServerByID(req.ID)
	if server == nil {
		return ctx.Status(http.StatusNotFound).SendString("server not found: " + req.ID)
	}
	if server.GitAutoDeploy == nil {
		return ctx.Status(http.StatusBadRequest).SendString("server has no root_git_autodeploy config")
	}

	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("site deploy manager not initialized")
	}

	buildID, err := bm.TriggerBuild(server, req.Args)
	if err != nil {
		if bm.IsBuilding(req.ID) {
			return ctx.Status(http.StatusConflict).SendString("build already in progress for server " + req.ID)
		}
		return ctx.Status(http.StatusInternalServerError).SendString("build trigger failed: " + err.Error())
	}

	return ctx.SendJSON(triggerBuildResponse{
		Status:  "build_triggered",
		BuildID: buildID,
	})
}

// BuildStatus handles GET /api/site/build-status/:buildid.
// Returns the current state of a build.
func BuildStatus(ctx RequestCtx) error {
	buildID := ctx.Param("buildid")
	if buildID == "" {
		return ctx.Status(http.StatusBadRequest).SendString("build id required")
	}

	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("site deploy manager not initialized")
	}

	bs := bm.GetBuild(buildID)
	if bs == nil {
		return ctx.Status(http.StatusNotFound).SendString("build not found: " + buildID)
	}

	snap := bs.Snapshot()
	return ctx.SendJSON(snap)
}

// ListBuilds handles GET /api/site/builds/:serverid.
// Returns all builds for a server, newest first.
func ListBuilds(ctx RequestCtx) error {
	serverID := ctx.Param("serverid")
	if serverID == "" {
		return ctx.Status(http.StatusBadRequest).SendString("server id required")
	}

	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("site deploy manager not initialized")
	}

	builds := bm.GetBuildsForServer(serverID)
	return ctx.SendJSON(builds)
}
