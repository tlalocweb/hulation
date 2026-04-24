package server

// Phase-2 UI integration: serve the compiled SvelteKit static bundle
// at /analytics/*, plus /analytics/config.json which the UI reads on
// boot to populate the server selector.
//
// Assets live at /hula/web/analytics/ inside the container. When the
// directory is absent (e.g., running hula from source without a
// frontend build) the registration is a no-op and /analytics returns
// 404 — nothing else is affected.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// AnalyticsUIRoot is the on-disk directory the static SvelteKit
// bundle is expected at. Overridable via HULA_ANALYTICS_UI_ROOT env
// var, which development hula instances can set to
// web/analytics/build in the source tree.
const AnalyticsUIRoot = "/hula/web/analytics"

// registerAnalyticsUI wires /analytics/* and /analytics/config.json
// onto the unified server. No-op when the bundle isn't present.
func registerAnalyticsUI(srv *unified.Server, cfg *config.Config) {
	root := os.Getenv("HULA_ANALYTICS_UI_ROOT")
	if root == "" {
		root = AnalyticsUIRoot
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		unifiedLog.Infof("analytics UI bundle not found at %s; /analytics routes disabled", root)
		return
	}
	fs := http.FileServer(http.Dir(root))

	// The config shim — answers the client's hydration fetch. Listed
	// first so the FileServer fallback doesn't try to serve a non-
	// existent config.json from disk.
	srv.RegisterCustomHandler("GET /analytics/config.json", func(w http.ResponseWriter, r *http.Request) {
		resp := buildUIConfig(r.Context(), cfg)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// SPA bundle. SvelteKit's static adapter emits an _app/ directory
	// plus per-route fallbacks, but since the adapter was configured
	// with fallback: 'index.html', any unknown path under /analytics/
	// should serve the root document so the client router can handle
	// routing. We hand to FileServer first (so CSS/JS hashes resolve)
	// and rewrite 404s into the index.
	srv.RegisterCustomHandler("/analytics/", spaHandler(root, fs))

	// The bare /analytics (no trailing slash) redirects into the app.
	srv.RegisterCustomHandler("/analytics", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/analytics/", http.StatusTemporaryRedirect)
	})

	unifiedLog.Infof("Analytics UI mounted from %s on /analytics/", root)
}

// spaHandler strips the /analytics prefix and either serves the
// matching static asset or falls back to index.html so the
// client-side router owns deep-link routing.
func spaHandler(root string, fs http.Handler) http.HandlerFunc {
	indexPath := filepath.Join(root, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		// Strip the /analytics prefix.
		stripped := strings.TrimPrefix(r.URL.Path, "/analytics")
		if stripped == "" {
			stripped = "/"
		}
		// Resolve against the root. Serve the asset when it exists.
		candidate := filepath.Join(root, filepath.FromSlash(stripped))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			r2 := r.Clone(r.Context())
			r2.URL.Path = stripped
			fs.ServeHTTP(w, r2)
			return
		}
		// Fall back to index.html for SPA routing (/analytics/pages,
		// /analytics/sources, etc. all live inside one HTML document).
		http.ServeFile(w, r, indexPath)
	}
}

// uiConfig mirrors what the UI's window.hulaConfig shape expects —
// keep it tight; only fields the client actually reads.
type uiConfig struct {
	Servers  []uiConfigServer `json:"servers"`
	Username string           `json:"username,omitempty"`
	IsAdmin  bool             `json:"isAdmin,omitempty"`
}

type uiConfigServer struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Name string `json:"name,omitempty"`
}

func buildUIConfig(ctx interface {
	Value(any) any
}, cfg *config.Config) *uiConfig {
	resp := &uiConfig{Servers: []uiConfigServer{}}
	if cfg == nil {
		cfg = hulaapp.GetConfig()
	}
	// Identity from authware.Claims. Middleware populates this on
	// every /api/* request; the UI-serving route shares the same
	// middleware chain so config.json reflects the current caller.
	if claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); ok && claims != nil {
		resp.Username = claims.Username
		for _, role := range claims.Roles {
			if role == "admin" || role == "root" {
				resp.IsAdmin = true
				break
			}
		}
	}
	for _, s := range cfg.Servers {
		if s == nil || s.ID == "" {
			continue
		}
		resp.Servers = append(resp.Servers, uiConfigServer{
			ID:   s.ID,
			Host: s.Host,
			Name: s.ID,
		})
	}
	return resp
}
