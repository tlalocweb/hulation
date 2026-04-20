package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/emersion/go-webdav"
	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	hulation "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/sitedeploy"
)

type stagingBuildRequest struct {
	ID string `json:"id"`
}

type stagingBuildResponse struct {
	BuildID    string   `json:"build_id"`
	Status     string   `json:"status"`
	StatusText string   `json:"status_text"`
	Logs       []string `json:"logs,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// StagingBuild handles POST /api/staging/build.
// Triggers a rebuild in the staging container for the given server ID.
func StagingBuild(ctx RequestCtx) error {
	var req stagingBuildRequest
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

	sm := sitedeploy.GetStagingManager()
	if sm == nil {
		return ctx.Status(http.StatusServiceUnavailable).SendString("staging manager not initialized")
	}

	sc := sm.GetStagingContainer(req.ID)
	if sc == nil {
		return ctx.Status(http.StatusNotFound).SendString("no staging container for server: " + req.ID)
	}

	bs, err := sm.RebuildStaging(req.ID)
	if errors.Is(err, sitedeploy.ErrBuildInProgress) {
		return ctx.Status(http.StatusConflict).SendString("build already in progress for server " + req.ID)
	}
	if err != nil {
		resp := stagingBuildResponse{
			Status:     "failed",
			StatusText: "failed",
			Error:      err.Error(),
		}
		if bs != nil {
			resp.BuildID = bs.BuildID
			snap := bs.Snapshot()
			resp.Logs = snap.Logs
		}
		return ctx.Status(http.StatusInternalServerError).SendJSON(resp)
	}

	snap := bs.Snapshot()
	return ctx.SendJSON(stagingBuildResponse{
		BuildID:    snap.BuildID,
		Status:     "complete",
		StatusText: snap.StatusText,
		Logs:       snap.Logs,
	})
}

// webdav handler cache per server
var (
	davHandlers   = make(map[string]*webdav.Handler)
	davHandlersMu sync.RWMutex
)

func getOrCreateDAVHandler(serverID, hostDir string) *webdav.Handler {
	davHandlersMu.RLock()
	h, ok := davHandlers[serverID]
	davHandlersMu.RUnlock()
	if ok {
		return h
	}

	davHandlersMu.Lock()
	defer davHandlersMu.Unlock()
	// double check
	if h, ok = davHandlers[serverID]; ok {
		return h
	}
	h = &webdav.Handler{
		FileSystem: webdav.LocalFileSystem(hostDir),
	}
	davHandlers[serverID] = h
	return h
}

// StagingWebDAVNetHTTP returns an http.Handler that serves WebDAV requests
// for staging containers. It does its own Bearer-token auth check (verifying
// the token has admin capability) then delegates to the emersion/go-webdav
// handler with the URL prefix stripped. Used by the net/http (h2) router.
func StagingWebDAVNetHTTP(verifyToken func(token string) (bool, bool, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract serverID from the path: /api/staging/{serverID}/dav/...
		path := r.URL.Path
		const prefix = "/api/staging/"
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		rest := path[len(prefix):]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			http.NotFound(w, r)
			return
		}
		serverID := rest[:slash]
		if serverID == "" {
			http.NotFound(w, r)
			return
		}

		// Auth check: Bearer token must have admin cap
		ahdr := r.Header.Get("Authorization")
		var token string
		n, err := fmt.Sscanf(ahdr, "Bearer %s", &token)
		if err != nil || n < 1 {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		valid, isAdmin, err := verifyToken(token)
		if err != nil || !valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if !isAdmin {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}

		sm := sitedeploy.GetStagingManager()
		if sm == nil {
			http.Error(w, "staging manager not initialized", http.StatusServiceUnavailable)
			return
		}
		sc := sm.GetStagingContainer(serverID)
		if sc == nil {
			http.Error(w, "no staging container for server: "+serverID, http.StatusNotFound)
			return
		}

		davHandler := getOrCreateDAVHandler(serverID, sc.HostSrcDir)
		davPrefix := "/api/staging/" + serverID + "/dav"
		// Strip the prefix. The WebDAV LocalFileSystem requires an absolute
		// path, so if the remaining path is empty, set it to "/".
		r2 := *r
		u2 := *r.URL
		u2.Path = strings.TrimPrefix(r.URL.Path, davPrefix)
		if u2.Path == "" {
			u2.Path = "/"
		}
		u2.RawPath = ""
		r2.URL = &u2
		davHandler.ServeHTTP(w, &r2)
	})
}

// StagingWebDAVFiber returns a fiber.Handler that serves WebDAV requests for
// staging containers. It bridges the emersion/go-webdav Handler (which implements
// net/http.Handler) into Fiber via fasthttpadaptor.
//
// The OPA middleware is applied by the caller in the router.
func StagingWebDAVFiber() fiber.Handler {
	return func(c *fiber.Ctx) error {
		serverID := c.Params("serverid")
		if serverID == "" {
			return c.Status(http.StatusBadRequest).SendString("server id required")
		}

		sm := sitedeploy.GetStagingManager()
		if sm == nil {
			return c.Status(http.StatusServiceUnavailable).SendString("staging manager not initialized")
		}

		sc := sm.GetStagingContainer(serverID)
		if sc == nil {
			return c.Status(http.StatusNotFound).SendString("no staging container for server: " + serverID)
		}

		davHandler := getOrCreateDAVHandler(serverID, sc.HostSrcDir)

		// Strip the prefix so WebDAV sees paths relative to the root
		prefix := "/api/staging/" + serverID + "/dav"

		// Use fasthttpadaptor to bridge net/http.Handler into fasthttp
		adapted := fasthttpadaptor.NewFastHTTPHandler(
			http.StripPrefix(prefix, davHandler),
		)
		adapted(c.Context())

		// Check if fasthttp has already sent a response
		if c.Context().Response.StatusCode() == 0 {
			c.Context().Response.SetStatusCode(http.StatusOK)
		}

		return nil
	}
}

// StagingWebDAVWrapped returns a Fiber handler with OPA auth for the WebDAV endpoint.
// This is used to register the WebDAV route with auth in the router.
func StagingWebDAVWrapped(opa Middleware) fiber.Handler {
	davFiber := StagingWebDAVFiber()

	return func(c *fiber.Ctx) error {
		// Apply OPA auth — the OPA middleware writes the denial response directly
		ctx := NewFiberCtx(c)
		authed := false

		opaHandler := opa(func(_ RequestCtx) error {
			authed = true
			return nil
		})
		if err := opaHandler(ctx); err != nil {
			return err
		}
		if !authed {
			// OPA denied — response already written
			return nil
		}

		return davFiber(c)
	}
}
