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
	"bytes"
	"context"
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

	// The config shim — answers a fetch from the SPA after hydration.
	// Listed first so the FileServer fallback doesn't try to serve a
	// non-existent config.json from disk.
	srv.RegisterCustomHandler("GET /analytics/config.json", func(w http.ResponseWriter, r *http.Request) {
		resp := buildUIConfig(r.Context(), cfg, requestHost(r))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// SPA bundle. SvelteKit's static adapter emits an _app/ directory
	// plus per-route fallbacks; the adapter is configured with
	// fallback: 'index.html' so any unknown path under /analytics/
	// serves the root document and the client router takes over.
	srv.RegisterCustomHandler("GET /analytics/", spaHandler(root, fs, cfg))

	// The bare /analytics (no trailing slash) redirects into the app.
	srv.RegisterCustomHandler("GET /analytics", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/analytics/", http.StatusTemporaryRedirect)
	})

	unifiedLog.Infof("Analytics UI mounted from %s on /analytics/", root)
}

// requestHost returns the request's Host header with any port stripped.
// Used to match the request against a configured Server.Host/Aliases.
func requestHost(r *http.Request) string {
	h := r.Host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// hulaConfigInjectMarker is the closing tag we replace with the
// pre-hydration <script>window.hulaConfig=...</script> block. The
// SvelteKit-built index.html is plain HTML5; the </head> tag is the
// canonical insertion point.
const hulaConfigInjectMarker = "</head>"

// spaHandler strips the /analytics prefix and either serves the
// matching static asset or falls back to index.html so the
// client-side router owns deep-link routing. When serving index.html
// it inlines window.hulaConfig so the SPA has the server list,
// caller identity, and currentServerId BEFORE hydration — the
// alternative (fetch /analytics/config.json on mount) races with
// child onMount handlers that read window.hulaConfig synchronously.
func spaHandler(root string, fs http.Handler, cfg *config.Config) http.HandlerFunc {
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
		// Fall back to index.html with hulaConfig injected.
		serveIndexWithConfig(w, r, cfg, indexPath)
	}
}

// serveIndexWithConfig reads index.html and inlines window.hulaConfig
// before the closing </head> tag, then writes the result. If reading
// fails (which shouldn't happen for a properly-built bundle) we fall
// back to a plain ServeFile so the SPA still renders, just without
// pre-hydration config.
func serveIndexWithConfig(w http.ResponseWriter, r *http.Request, cfg *config.Config, indexPath string) {
	html, err := os.ReadFile(indexPath)
	if err != nil {
		http.ServeFile(w, r, indexPath)
		return
	}
	uiCfg := buildUIConfig(r.Context(), cfg, requestHost(r))
	cfgJSON, err := json.Marshal(uiCfg)
	if err != nil {
		http.ServeFile(w, r, indexPath)
		return
	}
	// Build the inline script. JSON is safe to embed in a <script>
	// tag with one caveat: a literal "</script>" sequence inside a
	// string would break out of the tag. json.Marshal won't emit one
	// (it'd encode "<" as "<" only when SetEscapeHTML is on,
	// which it is by default), but we belt-and-suspenders this by
	// rejecting any "</" sequence we could possibly produce.
	cfgJSON = bytes.ReplaceAll(cfgJSON, []byte("</"), []byte(`</`))
	inject := []byte("<script>window.hulaConfig=" + string(cfgJSON) + ";</script>" + hulaConfigInjectMarker)
	html = bytes.Replace(html, []byte(hulaConfigInjectMarker), inject, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(html)
}

// uiConfig mirrors what the UI's window.hulaConfig shape expects —
// keep it tight; only fields the client actually reads.
type uiConfig struct {
	Servers         []uiConfigServer `json:"servers"`
	CurrentServerID string           `json:"currentServerId,omitempty"`
	Username        string           `json:"username,omitempty"`
	IsAdmin         bool             `json:"isAdmin,omitempty"`
}

type uiConfigServer struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Name string `json:"name,omitempty"`
}

// buildUIConfig assembles the server list + caller identity that the
// SPA needs at boot. host is the request's Host header (stripped of
// any port), used to find which configured Server is being addressed
// so the SPA can default the dropdown to that server's analytics
// instead of an arbitrary first-in-config choice.
func buildUIConfig(ctx context.Context, cfg *config.Config, host string) *uiConfig {
	resp := &uiConfig{Servers: []uiConfigServer{}}
	if cfg == nil {
		cfg = hulaapp.GetConfig()
	}
	if cfg == nil {
		return resp
	}
	if claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); ok && claims != nil {
		resp.Username = claims.Username
		for _, role := range claims.Roles {
			if role == "admin" || role == "root" {
				resp.IsAdmin = true
				break
			}
		}
	}
	host = strings.ToLower(host)
	for _, s := range cfg.Servers {
		if s == nil || s.ID == "" {
			continue
		}
		resp.Servers = append(resp.Servers, uiConfigServer{
			ID:   s.ID,
			Host: s.Host,
			Name: s.ID,
		})
		if resp.CurrentServerID != "" || host == "" {
			continue
		}
		if matchesHost(s, host) {
			resp.CurrentServerID = s.ID
		}
	}
	return resp
}

// matchesHost reports whether the given request host matches a
// configured Server: its primary Host or any of its Aliases /
// RedirectAliases. Comparison is case-insensitive. The caller passes
// the request host already lowercased.
func matchesHost(s *config.Server, host string) bool {
	if s == nil || host == "" {
		return false
	}
	if strings.EqualFold(s.Host, host) {
		return true
	}
	for _, a := range s.Aliases {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	for _, a := range s.RedirectAliases {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}
