package server

// Top-level reverse-proxy routing for the unified server.
//
// Wires up the `proxies:` config (config.Proxy{Target, ByDomain, ByPath}).
// Distinct from per-server `backends:`, which manage + proxy to Docker
// containers AND rewrite the request path. These proxies forward to an
// arbitrary target URL and PRESERVE the path, so they can front a sidecar HTTP
// service whose request signatures cover the path — e.g. the hula-push-relay
// running on localhost behind hulation's TLS via `by_domain: relay.example.com`.

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// proxyRoute is one `proxies:` entry compiled to a matcher + reverse proxy.
type proxyRoute struct {
	byDomain string // lowercased host to match; "" matches any host
	byPath   string // path prefix to match; "" matches any path
	handler  http.Handler
	target   string
}

func (r *proxyRoute) matchesHost(req *http.Request) bool {
	if r.byDomain == "" {
		return true
	}
	host := strings.ToLower(req.Host)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host == r.byDomain
}

func (r *proxyRoute) matchesPath(req *http.Request) bool {
	if r.byPath == "" {
		return true
	}
	prefix := strings.TrimSuffix(r.byPath, "/")
	p := req.URL.Path
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// newPlainProxy builds a path-PRESERVING reverse proxy to target. Unlike the
// backend-container proxy it does NOT strip/rewrite the path, so a signed
// downstream (the hula-push-relay signs over method+path) verifies correctly.
// It forwards the original host + scheme via X-Forwarded-* so the upstream can
// honour its own public_base.
func newPlainProxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			origHost := req.Host
			scheme := "http"
			if req.TLS != nil {
				scheme = "https"
			}
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Path left intact.
			if req.Header.Get("X-Forwarded-Host") == "" {
				req.Header.Set("X-Forwarded-Host", origHost)
			}
			if req.Header.Get("X-Forwarded-Proto") == "" {
				req.Header.Set("X-Forwarded-Proto", scheme)
			}
			// The relay routes by path and ignores Host; send the upstream's
			// own host so a vhosted target resolves correctly.
			req.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warnf("proxy: upstream error for %s%s → %s", r.Host, r.URL.Path, err.Error())
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
}

// registerProxies wires the top-level `proxies:` config into the unified server
// as path-preserving reverse proxies. No-op when none are configured.
func registerProxies(srv *unified.Server, cfg *config.Config) {
	if srv == nil || cfg == nil || len(cfg.Proxies) == 0 {
		return
	}
	var routes []*proxyRoute
	for _, p := range cfg.Proxies {
		if p == nil || strings.TrimSpace(p.Target) == "" {
			continue
		}
		if p.ByDomain == "" && p.ByPath == "" {
			log.Warnf("proxy: target %q has neither by_domain nor by_path; skipping", p.Target)
			continue
		}
		target, err := url.Parse(strings.TrimSpace(p.Target))
		if err != nil || target.Scheme == "" || target.Host == "" {
			log.Errorf("proxy: invalid target %q (need scheme://host[:port]): %v", p.Target, err)
			continue
		}
		routes = append(routes, &proxyRoute{
			byDomain: strings.ToLower(p.ByDomain),
			byPath:   p.ByPath,
			handler:  newPlainProxy(target),
			target:   p.Target,
		})
		log.Infof("Proxy route: by_domain=%q by_path=%q → %s (path preserved)", p.ByDomain, p.ByPath, p.Target)
	}
	if len(routes) == 0 {
		return
	}

	srv.AttachHTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, rt := range routes {
				if !rt.matchesHost(r) || !rt.matchesPath(r) {
					continue
				}
				// A by_path route (no dedicated host) must not shadow hula's own
				// service routes (admin API, REST gateway, …) — defer to them,
				// like the backend proxy does. A by_domain route owns its host
				// entirely, so it always wins there.
				if rt.byDomain == "" && srv.HasRoute(r) {
					break
				}
				rt.handler.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	log.Infof("Registered %d reverse-proxy route(s) on unified server", len(routes))
}
