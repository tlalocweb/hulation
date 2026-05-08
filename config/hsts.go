package config

// HTTP Strict Transport Security helpers. The runtime middleware in
// server/hsts_middleware.go (package server, attached from
// server/unified_boot.go) consults BuildHSTSHeader to produce the
// per-response value, given a resolved per-vhost override (if any) and
// the global tunables.

import (
	"fmt"
	"strings"
	"time"
)

// HSTSConfig is the per-virtualhost override block. nil = inherit
// every tunables.hsts_* default. A field whose pointer is nil = inherit
// that single field; non-nil pointer = override.
//
// `Disable: true` is a hard off-switch for this virtualhost regardless
// of global tunables — useful when a specific server entry intentionally
// supports HTTP-only legacy clients on a sibling subdomain.
type HSTSConfig struct {
	// Disable hard-off the header on this virtualhost. Wins over every
	// other field.
	Disable bool `yaml:"disable,omitempty"`

	// MaxAge is a Go duration string (e.g. "8760h" for 1 year). Empty
	// = inherit tunables.hsts_max_age. The header always emits seconds.
	MaxAge string `yaml:"max_age,omitempty"`

	// IncludeSubDomains is a tri-state pointer: nil = inherit,
	// non-nil = override. Operators with a subdomain that serves
	// HTTP-only content set this to false on the parent's vhost so the
	// browser doesn't escalate the subdomain to HTTPS-only.
	IncludeSubDomains *bool `yaml:"include_subdomains,omitempty"`

	// Preload is also a tri-state pointer. The default (nil) inherits
	// from tunables.hsts_preload (which is itself off by default).
	// Setting this to true commits the host to the browser's bundled
	// HSTS list and is a one-way door — review the policy first.
	Preload *bool `yaml:"preload,omitempty"`
}

// HSTSDefaults bundles the tunables-side global defaults so config can
// resolve a header value without depending on pkg/tune (which would
// create an import cycle: config → tune → conftagz → many things).
type HSTSDefaults struct {
	Enabled           bool
	MaxAge            time.Duration
	IncludeSubDomains bool
	Preload           bool
}

// BuildHSTSHeader returns the formatted Strict-Transport-Security value
// for a given vhost override + global defaults. Empty string means "do
// not set the header" — either tunables disabled it globally, or this
// vhost's `Disable: true` opted out.
//
// Precedence:
//   1. tunables.HSTSEnabled=false → "" (off everywhere)
//   2. server.HSTS.Disable=true   → "" (off for this vhost)
//   3. server.HSTS field set      → use it
//   4. otherwise                  → tunables default
func BuildHSTSHeader(override *HSTSConfig, defaults HSTSDefaults) string {
	if !defaults.Enabled {
		return ""
	}
	if override != nil && override.Disable {
		return ""
	}

	maxAge := defaults.MaxAge
	if override != nil && override.MaxAge != "" {
		// Parse fault-tolerant: a malformed override silently falls
		// back to the default rather than emitting a broken header.
		// We don't log here — BuildHSTSHeader is called per-request
		// from the middleware hot path, so any noisy logging would
		// repeat on every request. Validate vhost MaxAge values at
		// config-load time if you want a one-shot warning.
		if d, err := time.ParseDuration(override.MaxAge); err == nil && d > 0 {
			maxAge = d
		}
	}

	includeSubs := defaults.IncludeSubDomains
	if override != nil && override.IncludeSubDomains != nil {
		includeSubs = *override.IncludeSubDomains
	}

	preload := defaults.Preload
	if override != nil && override.Preload != nil {
		preload = *override.Preload
	}

	parts := []string{fmt.Sprintf("max-age=%d", int64(maxAge.Seconds()))}
	if includeSubs {
		parts = append(parts, "includeSubDomains")
	}
	if preload {
		parts = append(parts, "preload")
	}
	return strings.Join(parts, "; ")
}
