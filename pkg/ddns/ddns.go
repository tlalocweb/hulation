// Package ddns implements dynamic-DNS publication: detect the host's public
// IPv4/IPv6 from free external services and upsert the corresponding A/AAAA
// records at a DNS provider. Cloudflare is the only provider today; the Provider
// interface keeps route53/etc. a drop-in away.
//
// The package is deliberately free of any dependency on the config package so it
// stays unit-testable against httptest servers with no real network or
// credentials. Credential/zone/flag resolution lives in the config package; the
// caller passes the already-resolved values in via Record.
package ddns

import (
	"context"
	"fmt"
)

// Record is a single DNS record to upsert. Content is the IP for the record's
// family (Type "A" ⇒ IPv4, "AAAA" ⇒ IPv6). ZoneID may be empty, in which case a
// provider that supports it auto-resolves the zone from Name.
type Record struct {
	// Name is the FQDN of the record, e.g. "app.example.com".
	Name string
	// Type is "A" (IPv4) or "AAAA" (IPv6).
	Type string
	// Content is the IP address to publish.
	Content string
	// Proxied requests the Cloudflare orange cloud (ignored by providers that
	// have no equivalent concept).
	Proxied bool
	// TTL is the record TTL (1 = Cloudflare "automatic").
	TTL int
	// ZoneID is the provider zone. Empty ⇒ auto-resolve from Name.
	ZoneID string
	// APIToken is the provider credential (Cloudflare bearer token).
	APIToken string
}

// Provider upserts DNS records at a backend.
type Provider interface {
	// EnsureRecord upserts (Name, Type) → Content honoring Proxied+TTL. It is a
	// no-op when the record already matches. Errors are returned (never fatal to
	// the caller) so the updater can retry next cycle.
	EnsureRecord(ctx context.Context, rec Record) error
}

// NewProvider selects a Provider by name. "" and "cloudflare" both yield the
// Cloudflare provider; any other name is an error (mirrors the config-load
// validation so an unknown provider fails fast).
func NewProvider(name string) (Provider, error) {
	switch name {
	case "", "cloudflare":
		return NewCloudflareProvider(), nil
	default:
		return nil, fmt.Errorf("ddns: unknown provider %q", name)
	}
}
