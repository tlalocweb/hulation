package server

// HSTS — Strict-Transport-Security response-header middleware.
//
// Configuration precedence (highest first):
//
//   1. Global on/off: pkg/tune.GetHSTSEnabled() (default: true).
//   2. Per-vhost hard-disable: server.HSTS.Disable=true on the matched
//      *config.Server. Wins over per-field overrides.
//   3. Per-vhost field override: server.HSTS.{MaxAge,IncludeSubDomains,
//      Preload} when non-nil/non-empty.
//   4. Global tunables: tune.GetHSTSMaxAge / GetHSTSIncludeSubDomains
//      / GetHSTSPreload.
//
// Plumbed in BootUnifiedServer via srv.AttachHTTPMiddleware so every
// HTTPS response — gateway REST, static-site serving, custom handlers
// alike — gets the header. Plain-HTTP redirect responses don't, since
// HSTS over HTTP is meaningless (browsers ignore it).

import (
	"net"
	"net/http"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/tune"
)

// hstsMiddleware returns an http.Handler middleware that emits the
// Strict-Transport-Security header on TLS responses. Non-TLS requests
// (the optional HTTP-redirect listener path) are passed through
// untouched.
func hstsMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Non-TLS request → no header. Lets the HTTP-redirect
			// path keep working without an HSTS-on-HTTP warning in
			// scanners that flag that pattern.
			if r.TLS == nil {
				next.ServeHTTP(w, r)
				return
			}

			defaults := config.HSTSDefaults{
				Enabled:           tune.GetHSTSEnabled(),
				MaxAge:            tune.GetHSTSMaxAge(),
				IncludeSubDomains: tune.GetHSTSIncludeSubDomains(),
				Preload:           tune.GetHSTSPreload(),
			}

			// Pre-flight: if globally disabled, skip the per-vhost
			// lookup entirely. Hot path on every HTTPS request.
			if !defaults.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			var override *config.HSTSConfig
			if cfg != nil {
				host := stripPort(r.Host)
				if s := cfg.GetServer(host); s != nil {
					override = s.HSTS
				} else if s := cfg.GetServerByAnyAlias(host); s != nil {
					override = s.HSTS
				}
			}

			if value := config.BuildHSTSHeader(override, defaults); value != "" {
				w.Header().Set("Strict-Transport-Security", value)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// stripPort returns r.Host without the trailing :<port>, if any. The
// vhost map keys on bare hostname. net.SplitHostPort handles IPv4,
// IPv6 (bracketed), and DNS-shape hosts uniformly.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
