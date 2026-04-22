package server

// Per-host static file serving for the unified server.
//
// Each configured server with a `root:` path gets a net/http FileServer
// serving its root directory. Mounted as HTTP middleware that matches
// on Host header (case-insensitive, port-stripped) and serves any path
// not already claimed by gRPC / REST / ServeMux fallback / backend
// proxies.
//
// The old Fiber listener served this via a per-server handler in
// router.go; the unified equivalent lives here.

import (
	"net/http"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// staticRoute captures one host → filesystem-root pair.
type staticRoute struct {
	hostLower string
	handler   http.Handler
}

// registerStaticSites walks cfg.Servers and attaches a static FileServer
// middleware on the unified listener. No-op when no server has a Root
// configured.
func registerStaticSites(srv *unified.Server, cfg *config.Config) {
	if srv == nil || cfg == nil {
		return
	}
	var routes []*staticRoute
	for _, s := range cfg.Servers {
		if s == nil || s.Root == "" || s.Host == "" {
			continue
		}
		fs := http.FileServer(http.Dir(s.Root))
		routes = append(routes, &staticRoute{
			hostLower: strings.ToLower(s.Host),
			handler:   fs,
		})
		log.Infof("Static site route: %s → %s", s.Host, s.Root)
		for _, alias := range s.Aliases {
			if alias == "" {
				continue
			}
			routes = append(routes, &staticRoute{
				hostLower: strings.ToLower(alias),
				handler:   fs,
			})
		}
	}
	if len(routes) == 0 {
		return
	}

	srv.AttachHTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Don't intercept paths that hula serves through its own
			// routing layers: /api/* (gRPC + REST gateway + legacy
			// bridge), /v/* (visitor tracking), /scripts/* (tracking
			// scripts), and /hulastatus. Static file serving is a
			// fall-back for the host's root path namespace — not a
			// replacement for the service routes that live on the
			// same listener.
			p := r.URL.Path
			if strings.HasPrefix(p, "/api/") ||
				strings.HasPrefix(p, "/v/") ||
				strings.HasPrefix(p, "/scripts/") ||
				p == "/hulastatus" {
				next.ServeHTTP(w, r)
				return
			}
			host := strings.ToLower(r.Host)
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			for _, rt := range routes {
				if host == rt.hostLower {
					rt.handler.ServeHTTP(w, r)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	log.Infof("Registered %d static site route(s) on unified server", len(routes))
}
