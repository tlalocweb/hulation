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
	"unicode"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// proxyRoute is one `proxies:` entry compiled to a matcher + reverse proxy.
type proxyRoute struct {
	byDomain string // lowercased host to match; "" matches any host
	byPath   string // path prefix to match; "" matches any path
	handler  http.Handler
}

func (r *proxyRoute) matchesHost(req *http.Request) bool {
	if r.byDomain == "" {
		return true
	}
	// hostOnly (unified_cors.go) lowercases and strips an optional ":port",
	// handling bracketed IPv6 ("[::1]:8443" → "::1") — a naive LastIndex(":")
	// would truncate the address.
	return hostOnly(req.Host) == r.byDomain
}

func (r *proxyRoute) matchesPath(req *http.Request) bool {
	if r.byPath == "" {
		return true
	}
	prefix := strings.TrimSuffix(r.byPath, "/")
	p := req.URL.Path
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// normaliseByDomain validates a configured by_domain and returns the bare host
// to match against (lowercased, with any port + IPv6 brackets stripped, exactly
// as hostOnly derives it from the request Host). ok is false for anything that
// isn't a clean host: a URL/path/space, an empty host, a scheme like
// "https:host", a host:port whose port is empty or non-numeric, or an
// unbracketed IPv6 literal (which must be written "[::1]"). An empty input is
// valid and yields ("", true) — the caller treats that as a path-only route.
func normaliseByDomain(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if strings.ContainsRune(raw, '/') || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", false // URL/path separator or any whitespace (space, tab, newline…)
	}
	h := strings.ToLower(raw)
	// Bracketed IPv6: "[::1]" or "[::1]:8443".
	if strings.HasPrefix(h, "[") {
		end := strings.IndexByte(h, ']')
		if end < 0 {
			return "", false // unterminated bracket
		}
		host := h[1:end]
		rest := h[end+1:]
		if host == "" {
			return "", false
		}
		if rest != "" && !(rest[0] == ':' && isNumericPort(rest[1:])) {
			return "", false // trailing junk or non-numeric port
		}
		return host, true
	}
	switch strings.Count(h, ":") {
	case 0: // bare host / IPv4
		return h, true
	case 1: // host:port
		i := strings.IndexByte(h, ':')
		host, port := h[:i], h[i+1:]
		if host == "" || !isNumericPort(port) {
			return "", false
		}
		return host, true
	default: // unbracketed IPv6 literal — require "[...]"
		return "", false
	}
}

func isNumericPort(p string) bool {
	if p == "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
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
			// Derive the client IP BEFORE rewriting the forwarding headers,
			// honouring an upstream X-Forwarded-For / CF-Connecting-IP so the
			// real client survives when hulation sits behind a front proxy/CDN.
			peerIP := extractPeerIP(req)
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			// Path left intact. Set the forwarding headers authoritatively, following
			// the backend proxy's convention (backend/proxy.go) so a public client
			// can't spoof host/proto: X-Forwarded-Host/Proto come from the observed
			// request. X-Real-IP is the derived client IP; X-Forwarded-For is seeded
			// with it and httputil.ReverseProxy appends the immediate hop (RemoteAddr)
			// after this Director runs, so the upstream sees "client[, hop]" (real
			// client first). This proxy additionally honours CF-Connecting-IP (via
			// extractPeerIP) and strips the RFC 7239 Forwarded header, neither of which
			// the backend proxy does — so the two are similar, not identical.
			req.Header.Set("X-Forwarded-Host", origHost)
			req.Header.Set("X-Forwarded-Proto", scheme)
			req.Header.Set("X-Forwarded-For", peerIP)
			req.Header.Set("X-Real-IP", peerIP)
			// We express forwarding via X-Forwarded-*; strip the RFC 7239
			// `Forwarded` header so a client can't smuggle a conflicting
			// for/host/proto through an upstream that prefers it.
			req.Header.Del("Forwarded")
			// The relay routes by path and ignores Host; send the upstream's
			// own host so a vhosted target resolves correctly.
			req.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// r.URL.Host is the rewritten upstream target; X-Forwarded-Host is
			// the original client-facing host. Label both so the line isn't read
			// as "host → target".
			log.Warnf("proxy: upstream error: target=%q path=%q client_host=%q err=%v",
				r.URL.Host, r.URL.Path, r.Header.Get("X-Forwarded-Host"), err)
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
	routes := compileProxyRoutes(cfg.Proxies)
	if len(routes) == 0 {
		return
	}

	srv.AttachHTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if proxyDispatch(routes, srv.HasRoute, w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	log.Infof("Registered %d reverse-proxy route(s) on unified server", len(routes))
}

// compileProxyRoutes validates and compiles `proxies:` config entries into
// matchers, logging and skipping any that are malformed. Split out from
// registerProxies so the validation is unit-testable without a live server.
func compileProxyRoutes(proxies []*config.Proxy) []*proxyRoute {
	var routes []*proxyRoute
	for _, p := range proxies {
		if p == nil {
			continue
		}
		targetRaw := strings.TrimSpace(p.Target)
		if targetRaw == "" {
			continue
		}
		rawDomain := strings.TrimSpace(p.ByDomain)
		path := strings.TrimSpace(p.ByPath)
		// Validate + normalise by_domain to a bare host (lowercased, port and
		// IPv6 brackets stripped) the same way matchesHost sees the request Host.
		// Rejects URLs/paths, a scheme like "https:host", an empty or non-numeric
		// port, and unbracketed IPv6 — each of which would otherwise truncate into
		// a bogus host that silently never matches (or, when it empties out, fails
		// open into a match-any route).
		domain, ok := normaliseByDomain(rawDomain)
		if !ok {
			log.Errorf("proxy: by_domain %q is not a valid host (use a bare host, host:port, or bracketed IPv6 like [::1]:8443); skipping target %q", rawDomain, targetRaw)
			continue
		}
		if domain == "" && path == "" {
			log.Warnf("proxy: target %q has neither by_domain nor by_path; skipping", targetRaw)
			continue
		}
		if path != "" && !strings.HasPrefix(path, "/") {
			log.Errorf("proxy: by_path %q must start with '/'; skipping target %q", path, targetRaw)
			continue
		}
		target, err := url.Parse(targetRaw)
		if err != nil {
			log.Errorf("proxy: invalid target %q: %v", targetRaw, err)
			continue
		}
		// httputil.ReverseProxy only speaks http/https.
		if target.Scheme != "http" && target.Scheme != "https" {
			log.Errorf("proxy: target %q must use http:// or https:// (got scheme %q); skipping", targetRaw, target.Scheme)
			continue
		}
		if target.Host == "" {
			log.Errorf("proxy: target %q is missing a host (need scheme://host[:port]); skipping", targetRaw)
			continue
		}
		// The request path is always preserved, so a path/query/fragment on the
		// target would be silently ignored — reject it rather than mislead.
		if (target.Path != "" && target.Path != "/") || target.RawQuery != "" || target.Fragment != "" {
			log.Errorf("proxy: target %q must be scheme://host[:port] with no path/query/fragment (the request path is forwarded as-is); skipping", targetRaw)
			continue
		}
		routes = append(routes, &proxyRoute{
			byDomain: domain,
			byPath:   path,
			handler:  newPlainProxy(target),
		})
		// Redacted() masks any userinfo password so credentials never hit the log.
		log.Infof("Proxy route: by_domain=%q by_path=%q → %s (path preserved)", domain, path, target.Redacted())
	}
	return routes
}

// proxyDispatch routes r to a matching proxy and reports whether it handled the
// request. Order-independent precedence:
//   1. by_domain routes are checked first — a dedicated host is unambiguous, so
//      a matching by_domain proxy always wins regardless of config order. A
//      dedicated host fully owns itself, so service-path reservations don't apply.
//   2. A by_path route shares hula's host, so it must never shadow hula's own
//      routes: it defers both to a route the core dispatcher claims (hasRoute)
//      and to the reserved service prefixes (/api/*, /v/*, /scripts/*, /analytics,
//      /hulastatus, …) via staticPassthrough — covering present and future REST
//      endpoints the same way the static layer does.
func proxyDispatch(
	routes []*proxyRoute,
	hasRoute func(*http.Request) bool,
	w http.ResponseWriter,
	r *http.Request,
) bool {
	// 1. Host-scoped (by_domain) routes win first.
	if rt := longestMatch(routes, r, true); rt != nil {
		rt.handler.ServeHTTP(w, r)
		return true
	}
	// 2. A path-only proxy must defer to hula's own routes — those the core
	//    dispatcher claims and the reserved service prefixes — so it can't shadow
	//    a (present or future) REST/admin endpoint.
	if (hasRoute != nil && hasRoute(r)) || staticPassthrough(r.URL.Path) {
		return false
	}
	// 3. Path-only (by_path) routes.
	if rt := longestMatch(routes, r, false); rt != nil {
		rt.handler.ServeHTTP(w, r)
		return true
	}
	return false
}

// longestMatch returns the matching route with the most specific (longest) path
// prefix, so overlapping prefixes (e.g. /relay vs /relay/v1) resolve
// deterministically regardless of config order. hostScoped selects between
// by_domain routes (true) and path-only routes (false).
func longestMatch(routes []*proxyRoute, r *http.Request, hostScoped bool) *proxyRoute {
	var best *proxyRoute
	bestLen := -1
	for _, rt := range routes {
		if hostScoped != (rt.byDomain != "") {
			continue
		}
		if rt.byDomain != "" && !rt.matchesHost(r) {
			continue
		}
		if !rt.matchesPath(r) {
			continue
		}
		if l := len(strings.TrimSuffix(rt.byPath, "/")); l > bestLen {
			best, bestLen = rt, l
		}
	}
	return best
}
