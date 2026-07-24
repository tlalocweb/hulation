package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Provider-neutral defaults for the DDNS subsystem. Neutral keys are unprefixed;
// Cloudflare-specific keys carry a cf_ prefix (and CF_ env vars) so a future
// provider (route53, etc.) can slot in without churning the config surface.
const (
	// DefaultDDNSProvider is the only provider implemented today.
	DefaultDDNSProvider = "cloudflare"
	// DefaultDDNSInterval is the fallback update cadence when ddns.interval is
	// unset or unparseable. The updater ALSO runs once on boot and on SIGHUP,
	// so this only governs the steady-state polling interval.
	DefaultDDNSInterval = 4 * time.Hour
	// DefaultDDNSTTL is Cloudflare's "automatic" TTL.
	DefaultDDNSTTL = 1
)

// DDNSConfig is the top-level `ddns:` block: global defaults plus a credential
// fallback shared by every per-vhost / per-proxy record that doesn't override
// them. Provider-neutral fields (Provider, Interval, service lists, IPv4/IPv6,
// TTL) apply to any provider; the Cf* fields are Cloudflare-specific.
//
// Boolean tri-states use *bool so "unset" (inherit / default true) is
// distinguishable from an explicit false. conftagz ignores default: tags on
// bool/*bool, so those defaults are resolved in code (see the Resolve* helpers).
//
// The pointer carries conf:"skipnil" so conftagz does NOT materialise an empty
// struct when the operator omits the block — presence of the YAML mapping is
// what enables DDNS.
type DDNSConfig struct {
	// Provider selects the DNS backend. Neutral; default "cloudflare".
	// An unknown value is a config error (see validateDDNS).
	Provider string `yaml:"provider,omitempty" default:"cloudflare"`
	// Interval is the steady-state update cadence as a Go duration string
	// (e.g. "4h", "5m"). Neutral; default 4h. The updater additionally runs
	// on boot and on SIGHUP regardless of this value.
	Interval string `yaml:"interval,omitempty" default:"4h"`
	// IPv4Services / IPv6Services are the ordered public-IP detection
	// endpoints tried in turn until one returns a valid IP of the right
	// family. Neutral; sensible defaults are supplied in code when omitted.
	IPv4Services []string `yaml:"ipv4_services,omitempty"`
	IPv6Services []string `yaml:"ipv6_services,omitempty"`
	// IPv4 / IPv6 gate publication of A / AAAA records globally.
	// Neutral; default true. Per-record values override.
	IPv4 *bool `yaml:"ipv4,omitempty"`
	IPv6 *bool `yaml:"ipv6,omitempty"`
	// TTL is the record TTL. Neutral; default 1 (Cloudflare "automatic").
	TTL int `yaml:"ttl,omitempty" default:"1"`
	// CfAPIToken / CfZoneID are the Cloudflare-specific global credential
	// fallback (env CF_API_TOKEN / CF_ZONE_ID).
	CfAPIToken string `yaml:"cf_api_token,omitempty"`
	CfZoneID   string `yaml:"cf_zone_id,omitempty"`
	// CfProxied is the Cloudflare-specific global default for the orange
	// cloud. Default true. Per-record false is a security downgrade and
	// triggers a startup WARNING (it exposes the origin IP).
	CfProxied *bool `yaml:"cf_proxied,omitempty"`
}

// DDNSRecordConfig is the per-vhost (`servers[].ddns`) / per-proxy
// (`proxies[].ddns`) opt-in block. Presence of the mapping enables DDNS for
// that host; every field is optional and inherits from DDNSConfig (or the
// built-in default) when unset. The pointer carries conf:"skipnil" so an
// omitted block stays nil rather than being materialised by conftagz.
type DDNSRecordConfig struct {
	// CfAPIToken / CfZoneID override the Cloudflare credentials for this
	// record (env CF_API_TOKEN_{id} / CF_ZONE_ID_{id} for id-bearing servers).
	CfAPIToken string `yaml:"cf_api_token,omitempty"`
	CfZoneID   string `yaml:"cf_zone_id,omitempty"`
	// CfProxied overrides the orange-cloud flag for this record. false is a
	// DNS-only downgrade that exposes the origin IP → startup WARNING.
	CfProxied *bool `yaml:"cf_proxied,omitempty"`
	// IPv4 / IPv6 override A / AAAA publication for this record.
	IPv4 *bool `yaml:"ipv4,omitempty"`
	IPv6 *bool `yaml:"ipv6,omitempty"`
	// TTL overrides the record TTL (0 = inherit).
	TTL int `yaml:"ttl,omitempty"`
	// Records overrides the published record names. Default = the vhost host
	// (aliases are NOT auto-published) or the proxy by_domain.
	Records []string `yaml:"records,omitempty"`
}

// ddnsEnvKey normalises an id for shell env-var use, matching the origin-CA
// convention (dashes → underscores).
func ddnsEnvKey(id string) string {
	return strings.ReplaceAll(id, "-", "_")
}

// DDNSEnvID exposes ddnsEnvKey for callers building user-facing guidance about
// the id-suffixed env var names (e.g. CF_API_TOKEN_{id}).
func DDNSEnvID(id string) string { return ddnsEnvKey(id) }

// ResolveProvider returns the configured provider, defaulting to "cloudflare".
// Safe to call on a nil receiver (no global ddns block).
func (g *DDNSConfig) ResolveProvider() string {
	if g == nil || g.Provider == "" {
		return DefaultDDNSProvider
	}
	return g.Provider
}

// ResolveInterval parses the configured interval, falling back to
// DefaultDDNSInterval when unset, unparseable, or non-positive. Safe on nil.
func (g *DDNSConfig) ResolveInterval() time.Duration {
	if g != nil && g.Interval != "" {
		if d, err := time.ParseDuration(g.Interval); err == nil && d > 0 {
			return d
		}
	}
	return DefaultDDNSInterval
}

// ResolveCFAPIToken resolves the Cloudflare API token for a record following the
// cascade: per-config cf_api_token → env CF_API_TOKEN_{id} → global
// ddns.cf_api_token → env CF_API_TOKEN → originCAToken (the server's resolved
// ssl.cloudflare_origin_ca token). Returns "" when nothing supplies one, in
// which case the caller SKIPS the record with a warning (never fatal).
//
// id is the server id ("" for proxies, which have no id and therefore no
// id-suffixed env var and no origin-CA fallback — pass "" for originCAToken).
func (r *DDNSRecordConfig) ResolveCFAPIToken(id string, global *DDNSConfig, originCAToken string) string {
	if r != nil && r.CfAPIToken != "" {
		return r.CfAPIToken
	}
	if id != "" {
		if v := os.Getenv("CF_API_TOKEN_" + ddnsEnvKey(id)); v != "" {
			return v
		}
	}
	if global != nil && global.CfAPIToken != "" {
		return global.CfAPIToken
	}
	if v := os.Getenv("CF_API_TOKEN"); v != "" {
		return v
	}
	return originCAToken
}

// ResolveCFZoneID resolves the Cloudflare zone id for a record following the
// cascade: per-config cf_zone_id → env CF_ZONE_ID_{id} → global ddns.cf_zone_id
// → env CF_ZONE_ID → originCAZoneID. Returns "" when nothing supplies one, in
// which case the provider auto-resolves the zone from the record hostname via
// the Cloudflare zones API (longest-suffix match).
func (r *DDNSRecordConfig) ResolveCFZoneID(id string, global *DDNSConfig, originCAZoneID string) string {
	if r != nil && r.CfZoneID != "" {
		return r.CfZoneID
	}
	if id != "" {
		if v := os.Getenv("CF_ZONE_ID_" + ddnsEnvKey(id)); v != "" {
			return v
		}
	}
	if global != nil && global.CfZoneID != "" {
		return global.CfZoneID
	}
	if v := os.Getenv("CF_ZONE_ID"); v != "" {
		return v
	}
	return originCAZoneID
}

// ResolveIPv4 reports whether A records should be published for this record.
// Precedence: per-config IPv4 (most specific) → global IPv4 → default true.
func (r *DDNSRecordConfig) ResolveIPv4(global *DDNSConfig) bool {
	if r != nil && r.IPv4 != nil {
		return *r.IPv4
	}
	if global != nil && global.IPv4 != nil {
		return *global.IPv4
	}
	return true
}

// ResolveIPv6 reports whether AAAA records should be published for this record.
// Precedence: per-config IPv6 → global IPv6 → default true.
func (r *DDNSRecordConfig) ResolveIPv6(global *DDNSConfig) bool {
	if r != nil && r.IPv6 != nil {
		return *r.IPv6
	}
	if global != nil && global.IPv6 != nil {
		return *global.IPv6
	}
	return true
}

// ResolveCFProxied reports whether the record should sit behind Cloudflare's
// orange cloud. Precedence: per-config CfProxied → global CfProxied → default
// true. A resolved false is a DNS-only downgrade the caller warns about.
func (r *DDNSRecordConfig) ResolveCFProxied(global *DDNSConfig) bool {
	if r != nil && r.CfProxied != nil {
		return *r.CfProxied
	}
	if global != nil && global.CfProxied != nil {
		return *global.CfProxied
	}
	return true
}

// ResolveTTL returns the record TTL. Precedence: per-config TTL (>0) → global
// TTL (>0) → default 1 (Cloudflare automatic).
func (r *DDNSRecordConfig) ResolveTTL(global *DDNSConfig) int {
	if r != nil && r.TTL > 0 {
		return r.TTL
	}
	if global != nil && global.TTL > 0 {
		return global.TTL
	}
	return DefaultDDNSTTL
}

// validateDDNS runs the load-time DDNS invariants: the global provider must be
// known, and a DDNS-enabled proxy must carry a host (by_domain) since DDNS needs
// a record name. Missing credentials are deliberately NOT fatal here — they are
// resolved (and warned about) at boot so a bad token can't block serving.
func validateDDNS(cfg *Config) error {
	if cfg.DDNS != nil {
		if p := cfg.DDNS.ResolveProvider(); p != DefaultDDNSProvider {
			return fmt.Errorf("ddns: unknown provider %q (supported: %q)", p, DefaultDDNSProvider)
		}
	}
	for _, p := range cfg.Proxies {
		if p != nil && p.DDNS != nil && strings.TrimSpace(p.ByDomain) == "" {
			return fmt.Errorf("proxy %q: ddns requires by_domain (DDNS needs a host to publish; a by_path-only proxy has no record name)", p.ByPath)
		}
	}
	return nil
}
