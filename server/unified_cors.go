package server

// CORS middleware for the unified server.
//
// Hula has long parsed `cors:` blocks (top-level + per-server) into
// CORSConfig structs but never applied them. This file does that
// application: it inspects the request's Origin and Host, looks up
// the matching CORSConfig, sets the standard Access-Control-* headers,
// and short-circuits OPTIONS preflights with 204.
//
// Resolution order per request:
//  1. Per-server config (Server.CORS) when the request's Host matches
//     a configured server.Host or alias.
//  2. Global config (cfg.CORS) otherwise.
//
// AllowOrigins is parsed once at attach time into a hostname-keyed
// map of allow-origin sets, so the per-request work is a hash lookup
// + a couple of header writes. AllowOrigins values are matched
// exactly against the request's Origin header (scheme + host + port
// per RFC6454), with the special tokens `*` (any origin) and
// `cors.UnsafeAnyOrigin = true` (echo Origin verbatim, dev only).

import (
	"net/http"
	"strings"

	"github.com/tlalocweb/hulation/config"
)

// corsHostBinding is the precomputed CORS state for a single host.
type corsHostBinding struct {
	cfg          *config.CORSConfig
	allowedSet   map[string]struct{} // exact-match origins, lowercased
	wildcard     bool                // AllowOrigins contained "*"
	allowMethods string              // ready-to-write header value
	allowHeaders string
}

// CORSMiddleware returns an HTTP middleware that applies CORS rules
// from cfg. Pass the resolved *config.Config produced by
// config.Validate so per-server overrides + the post-load
// AllowOrigins augmentation (config.go: NoAddInHula handling) are
// already in place.
func CORSMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	hosts := make(map[string]*corsHostBinding)
	for _, s := range cfg.Servers {
		if s == nil {
			continue
		}
		cc := s.CORS
		if cc == nil {
			cc = &cfg.CORS
		}
		bind := buildCORSBinding(cc)
		hosts[strings.ToLower(s.Host)] = bind
		for _, a := range s.Aliases {
			if a == "" {
				continue
			}
			hosts[strings.ToLower(a)] = bind
		}
	}
	global := buildCORSBinding(&cfg.CORS)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			bind := hosts[hostOnly(r.Host)]
			if bind == nil {
				bind = global
			}

			allowed, echoOrigin := bind.match(origin)
			if !allowed {
				// Origin is set but not allowed — let the request
				// through without CORS headers. Browser will reject
				// based on the missing header; backend code still
				// runs in case it logs / counts the failure.
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("Access-Control-Allow-Origin", echoOrigin)
			h.Add("Vary", "Origin")
			if bind.cfg.AllowCredentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			// Preflight: OPTIONS + Access-Control-Request-Method.
			// We answer 204 directly; downstream handlers never see
			// the preflight (they wouldn't know how to respond
			// anyway — gRPC-gateway treats raw OPTIONS as a 405).
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if bind.allowMethods != "" {
					h.Set("Access-Control-Allow-Methods", bind.allowMethods)
				}
				if bind.allowHeaders != "" {
					h.Set("Access-Control-Allow-Headers", bind.allowHeaders)
				} else if reqHdrs := r.Header.Get("Access-Control-Request-Headers"); reqHdrs != "" {
					// No explicit allowlist configured — reflect the
					// request's headers. Safer than a bare "*" because
					// of the Allow-Credentials interaction.
					h.Set("Access-Control-Allow-Headers", reqHdrs)
				}
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func buildCORSBinding(cc *config.CORSConfig) *corsHostBinding {
	b := &corsHostBinding{cfg: cc}
	if cc == nil {
		b.cfg = &config.CORSConfig{}
		return b
	}
	b.allowMethods = strings.TrimSpace(cc.AllowMethods)
	b.allowHeaders = strings.TrimSpace(cc.AllowHeaders)
	b.allowedSet = make(map[string]struct{})
	for _, raw := range strings.Split(cc.AllowOrigins, ",") {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if v == "*" {
			b.wildcard = true
			continue
		}
		b.allowedSet[strings.ToLower(v)] = struct{}{}
	}
	return b
}

// match reports whether origin is permitted under the binding, and
// returns the value to put in Access-Control-Allow-Origin. With
// AllowCredentials=true, we MUST echo the specific origin (the spec
// forbids "*" alongside credentials).
func (b *corsHostBinding) match(origin string) (allowed bool, echo string) {
	if b == nil || b.cfg == nil {
		return false, ""
	}
	if b.cfg.UnsafeAnyOrigin {
		return true, origin
	}
	o := strings.ToLower(origin)
	if _, ok := b.allowedSet[o]; ok {
		return true, origin
	}
	if b.wildcard {
		if b.cfg.AllowCredentials {
			// "*" + credentials is invalid; echo the requested
			// origin so the browser's credential check passes.
			return true, origin
		}
		return true, "*"
	}
	return false, ""
}

// hostOnly returns the lowercased hostname portion of a Host header
// (port stripped). IPv6 brackets aren't expected on the public API
// surface, but we handle them defensively.
func hostOnly(host string) string {
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i > 0 {
			return host[1:i]
		}
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}
