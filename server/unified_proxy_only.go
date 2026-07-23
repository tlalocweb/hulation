package server

// Per-vhost `proxy_only` reverse proxying for the unified server.
//
// A config.Server marked `proxy_only: true` is a PURE reverse proxy: every
// request for its Host (and Aliases) is forwarded verbatim to `proxy_pass`,
// bypassing ALL of hula's own serving handlers (/api, /v/*, /analytics,
// /hulastatus, gRPC, static roots, backends). This is the Servers[] equivalent
// of a top-level `proxies:` entry scoped with `by_domain`; both are dispatched
// by the single proxyDispatch path in unified_proxy.go so there is one coherent
// "host is a proxy, skip hula's handlers" mechanism.
//
// proxy_only hosts still flow through the per-host TLS cert loop in
// unified_boot.go (static / Cloudflare Origin CA / ACME) — nothing special-cases
// them out — and hula's cross-cutting bad-actor monitoring still applies (see
// badactorBlockCheck). Server-side analytics for these hosts is a separate PR;
// the seam is marked in proxyDispatch.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// compileProxyOnlyHosts builds the O(1) host→handler registry for every
// proxy_only server: the (lowercased) Host and each Alias map to a single
// path-preserving reverse proxy aimed at the server's proxy_pass upstream. The
// handler is built ONCE here (no per-request proxy construction). proxy_pass has
// already been validated at config load (validateProxyServer), so a parse
// failure here is defensive — logged and skipped rather than fatal. Returns nil
// when no proxy_only server is configured. Standalone + unit-testable.
func compileProxyOnlyHosts(servers []*config.Server) map[string]http.Handler {
	m := make(map[string]http.Handler)
	for _, s := range servers {
		if s == nil || !s.ProxyOnly {
			continue
		}
		target, err := url.Parse(strings.TrimSpace(s.ProxyPass))
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
			log.Errorf("proxy_only: server %q has invalid proxy_pass %q; skipping (this should have been caught at config load)", s.Host, s.ProxyPass)
			continue
		}
		// Reuse the path-preserving reverse proxy the top-level `proxies:`
		// layer uses, so a proxy_only host forwards method + path + query
		// byte-for-byte and sets the X-Forwarded-* headers authoritatively.
		h := newPlainProxy(target)
		if s.Host != "" {
			m[strings.ToLower(s.Host)] = h
		}
		for _, alias := range s.Aliases {
			if alias == "" {
				continue
			}
			m[strings.ToLower(alias)] = h
		}
		log.Infof("proxy_only host: %s (+%d alias) → %s (all paths, hula handlers bypassed)", s.Host, len(s.Aliases), target.Redacted())
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// badactorBlockCheck is the cross-cutting bad-actor gate applied to proxy_only
// hosts before their request is forwarded upstream. It mirrors the legacy
// h2handler path: derive the real client IP (honouring CF-Connecting-IP behind
// verified Cloudflare ranges via extractIPFromRequest), then consult the
// bad-actor store's CheckAndBlock. Returns true when the request must be dropped.
// A no-op (returns false) when bad-actor detection is disabled or uninitialised.
func badactorBlockCheck(r *http.Request) bool {
	if !badactor.IsEnabled() {
		return false
	}
	store := badactor.GetStore()
	if store == nil {
		return false
	}
	ip := extractIPFromRequest(r)
	block, _ := store.CheckAndBlock(ip, r.UserAgent(), r.Method, r.URL.Path, r.URL.RawQuery, r.Host)
	return block
}
