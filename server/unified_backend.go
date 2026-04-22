package server

// Per-host backend proxy routing for the unified server.
//
// The legacy Fiber path registered a `<virtualPath>/*` handler per
// configured server × backend pair. When a request came in, it checked
// that the Host header matched the server's configured host and then
// proxied to the backend container.
//
// The unified server serves everything from one listener, so we replicate
// the dispatch as an HTTP middleware that wraps the unified server's
// request pipeline. Requests whose (Host, Path-prefix) matches a
// configured backend are routed to its httputil.ReverseProxy; otherwise
// the request falls through to the gRPC/REST/ServeMux stack.

import (
	"net/http"
	"strings"

	"github.com/tlalocweb/hulation/backend"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// backendRoute captures a single host + virtual-path → proxy mapping.
type backendRoute struct {
	host        string
	hostLower   string
	virtualPath string
	handler     http.Handler
	backendName string
}

// matches reports whether this route applies to the given request.
// Host comparison is case-insensitive (RFC 7230 §2.7.3). The port on the
// Host header is ignored — hula's one-listener model doesn't use port
// to disambiguate virtual servers.
func (r *backendRoute) matches(req *http.Request) bool {
	host := strings.ToLower(req.Host)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host != r.hostLower {
		return false
	}
	p := req.URL.Path
	return p == r.virtualPath || strings.HasPrefix(p, r.virtualPath+"/")
}

// registerBackendProxies builds an HTTP middleware that dispatches
// matching requests to configured backend proxies, then attaches it to
// the unified server. No-op when no servers have backends configured.
func registerBackendProxies(srv *unified.Server, cfg *config.Config) {
	if srv == nil || cfg == nil {
		return
	}
	var routes []*backendRoute
	for _, s := range cfg.Servers {
		if s == nil || len(s.Backends) == 0 {
			continue
		}
		for _, b := range s.Backends {
			if b == nil || !b.IsReady() {
				if b != nil {
					log.Warnf("Backend %s for server %s not ready; skipping proxy route", b.ContainerName, s.Host)
				}
				continue
			}
			ph := backend.NewProxyHandler(b)
			routes = append(routes, &backendRoute{
				host:        s.Host,
				hostLower:   strings.ToLower(s.Host),
				virtualPath: b.VirtualPath,
				handler:     ph,
				backendName: b.ContainerName,
			})
			log.Infof("Backend proxy route: %s %s → container %s (%s)", s.Host, b.VirtualPath, b.ContainerName, b.GetProxyTarget())
		}
	}
	if len(routes) == 0 {
		return
	}

	srv.AttachHTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, rt := range routes {
				if rt.matches(r) {
					rt.handler.ServeHTTP(w, r)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	log.Infof("Registered %d backend proxy route(s) on unified server", len(routes))
}
