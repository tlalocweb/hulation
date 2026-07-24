package config

import (
	"testing"
	"time"
)

func ddnsBoolPtr(b bool) *bool { return &b }

// TestDDNSResolveCFAPIToken exercises the full token cascade:
// per-config yaml → env CF_API_TOKEN_{id} → global yaml → env CF_API_TOKEN →
// origin-CA fallback → "".
func TestDDNSResolveCFAPIToken(t *testing.T) {
	global := &DDNSConfig{CfAPIToken: "global-yaml"}

	t.Run("per-config yaml wins", func(t *testing.T) {
		t.Setenv("CF_API_TOKEN_app", "id-env")
		t.Setenv("CF_API_TOKEN", "global-env")
		r := &DDNSRecordConfig{CfAPIToken: "per-config"}
		if got := r.ResolveCFAPIToken("app", global, "origin-ca"); got != "per-config" {
			t.Fatalf("got %q, want per-config", got)
		}
	})

	t.Run("id env beats global yaml", func(t *testing.T) {
		t.Setenv("CF_API_TOKEN_app", "id-env")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("app", global, "origin-ca"); got != "id-env" {
			t.Fatalf("got %q, want id-env", got)
		}
	})

	t.Run("global yaml", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("app", global, "origin-ca"); got != "global-yaml" {
			t.Fatalf("got %q, want global-yaml", got)
		}
	})

	t.Run("global env CF_API_TOKEN", func(t *testing.T) {
		t.Setenv("CF_API_TOKEN", "global-env")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("app", nil, "origin-ca"); got != "global-env" {
			t.Fatalf("got %q, want global-env", got)
		}
	})

	t.Run("origin-CA fallback", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("app", nil, "origin-ca"); got != "origin-ca" {
			t.Fatalf("got %q, want origin-ca", got)
		}
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("app", nil, ""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("id dashes become underscores", func(t *testing.T) {
		t.Setenv("CF_API_TOKEN_tlaloc_staging", "dashed")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("tlaloc-staging", nil, ""); got != "dashed" {
			t.Fatalf("got %q, want dashed", got)
		}
	})

	t.Run("proxy has no id-env fallback", func(t *testing.T) {
		// id "" ⇒ CF_API_TOKEN_ must not be consulted; falls through to global.
		t.Setenv("CF_API_TOKEN_", "should-not-be-used")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFAPIToken("", global, ""); got != "global-yaml" {
			t.Fatalf("got %q, want global-yaml", got)
		}
	})
}

// TestDDNSResolveCFZoneID exercises the zone cascade (empty result ⇒ provider
// auto-resolves).
func TestDDNSResolveCFZoneID(t *testing.T) {
	global := &DDNSConfig{CfZoneID: "global-zone"}

	t.Run("per-config yaml wins", func(t *testing.T) {
		t.Setenv("CF_ZONE_ID_app", "id-env")
		r := &DDNSRecordConfig{CfZoneID: "per-config"}
		if got := r.ResolveCFZoneID("app", global, "origin-zone"); got != "per-config" {
			t.Fatalf("got %q, want per-config", got)
		}
	})
	t.Run("id env", func(t *testing.T) {
		t.Setenv("CF_ZONE_ID_app", "id-env")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFZoneID("app", global, "origin-zone"); got != "id-env" {
			t.Fatalf("got %q, want id-env", got)
		}
	})
	t.Run("global yaml", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFZoneID("app", global, "origin-zone"); got != "global-zone" {
			t.Fatalf("got %q, want global-zone", got)
		}
	})
	t.Run("global env", func(t *testing.T) {
		t.Setenv("CF_ZONE_ID", "global-env")
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFZoneID("app", nil, "origin-zone"); got != "global-env" {
			t.Fatalf("got %q, want global-env", got)
		}
	})
	t.Run("origin-CA fallback", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFZoneID("app", nil, "origin-zone"); got != "origin-zone" {
			t.Fatalf("got %q, want origin-zone", got)
		}
	})
	t.Run("empty ⇒ auto-resolve", func(t *testing.T) {
		r := &DDNSRecordConfig{}
		if got := r.ResolveCFZoneID("app", nil, ""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// TestDDNSResolveCFProxied covers the default-true, explicit-false, and inherit
// paths for the orange-cloud flag.
func TestDDNSResolveCFProxied(t *testing.T) {
	cases := []struct {
		name   string
		rec    *DDNSRecordConfig
		global *DDNSConfig
		want   bool
	}{
		{"default true (nothing set)", &DDNSRecordConfig{}, nil, true},
		{"per-config false", &DDNSRecordConfig{CfProxied: ddnsBoolPtr(false)}, nil, false},
		{"per-config true over global false", &DDNSRecordConfig{CfProxied: ddnsBoolPtr(true)}, &DDNSConfig{CfProxied: ddnsBoolPtr(false)}, true},
		{"inherit global false", &DDNSRecordConfig{}, &DDNSConfig{CfProxied: ddnsBoolPtr(false)}, false},
		{"inherit global true", &DDNSRecordConfig{}, &DDNSConfig{CfProxied: ddnsBoolPtr(true)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.ResolveCFProxied(tc.global); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDDNSResolveIPv4IPv6 covers the A/AAAA publication cascade (per-config
// overrides global, default true).
func TestDDNSResolveIPv4IPv6(t *testing.T) {
	cases := []struct {
		name   string
		rec    *DDNSRecordConfig
		global *DDNSConfig
		want4  bool
		want6  bool
	}{
		{"defaults true", &DDNSRecordConfig{}, nil, true, true},
		{"per-config disables v6", &DDNSRecordConfig{IPv6: ddnsBoolPtr(false)}, nil, true, false},
		{"global disables v4, per-config re-enables", &DDNSRecordConfig{IPv4: ddnsBoolPtr(true)}, &DDNSConfig{IPv4: ddnsBoolPtr(false)}, true, true},
		{"inherit global v4=false", &DDNSRecordConfig{}, &DDNSConfig{IPv4: ddnsBoolPtr(false)}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.ResolveIPv4(tc.global); got != tc.want4 {
				t.Fatalf("ipv4 got %v, want %v", got, tc.want4)
			}
			if got := tc.rec.ResolveIPv6(tc.global); got != tc.want6 {
				t.Fatalf("ipv6 got %v, want %v", got, tc.want6)
			}
		})
	}
}

// TestDDNSResolveIntervalProviderTTL covers the neutral scalar defaults.
func TestDDNSResolveIntervalProviderTTL(t *testing.T) {
	if got := (*DDNSConfig)(nil).ResolveInterval(); got != 4*time.Hour {
		t.Fatalf("nil interval got %v, want 4h", got)
	}
	if got := (&DDNSConfig{Interval: "5m"}).ResolveInterval(); got != 5*time.Minute {
		t.Fatalf("interval 5m got %v", got)
	}
	if got := (&DDNSConfig{Interval: "garbage"}).ResolveInterval(); got != 4*time.Hour {
		t.Fatalf("garbage interval should fall back to 4h, got %v", got)
	}
	if got := (*DDNSConfig)(nil).ResolveProvider(); got != "cloudflare" {
		t.Fatalf("nil provider got %q", got)
	}
	if got := (&DDNSConfig{Provider: "route53"}).ResolveProvider(); got != "route53" {
		t.Fatalf("provider got %q", got)
	}
	if got := (&DDNSRecordConfig{}).ResolveTTL(nil); got != 1 {
		t.Fatalf("default ttl got %d, want 1", got)
	}
	if got := (&DDNSRecordConfig{TTL: 300}).ResolveTTL(&DDNSConfig{TTL: 60}); got != 300 {
		t.Fatalf("per-config ttl got %d, want 300", got)
	}
	if got := (&DDNSRecordConfig{}).ResolveTTL(&DDNSConfig{TTL: 60}); got != 60 {
		t.Fatalf("global ttl got %d, want 60", got)
	}
}

// TestValidateDDNS covers the two load-time invariants.
func TestValidateDDNS(t *testing.T) {
	t.Run("unknown provider errors", func(t *testing.T) {
		cfg := &Config{DDNS: &DDNSConfig{Provider: "route53"}}
		if err := validateDDNS(cfg); err == nil {
			t.Fatal("expected error for unknown provider")
		}
	})
	t.Run("cloudflare provider ok", func(t *testing.T) {
		cfg := &Config{DDNS: &DDNSConfig{Provider: "cloudflare"}}
		if err := validateDDNS(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("nil global provider defaults ok", func(t *testing.T) {
		cfg := &Config{}
		if err := validateDDNS(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("by_path-only proxy + ddns errors", func(t *testing.T) {
		cfg := &Config{Proxies: []*Proxy{{ByPath: "/api", Target: "http://127.0.0.1:9000", DDNS: &DDNSRecordConfig{}}}}
		if err := validateDDNS(cfg); err == nil {
			t.Fatal("expected error for by_path-only proxy with ddns")
		}
	})
	t.Run("by_domain proxy + ddns ok", func(t *testing.T) {
		cfg := &Config{Proxies: []*Proxy{{ByDomain: "cdn.example.com", Target: "http://127.0.0.1:9000", DDNS: &DDNSRecordConfig{}}}}
		if err := validateDDNS(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("by_path-only proxy WITHOUT ddns ok", func(t *testing.T) {
		cfg := &Config{Proxies: []*Proxy{{ByPath: "/api", Target: "http://127.0.0.1:9000"}}}
		if err := validateDDNS(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestLoadConfig_DDNS is a full round-trip: the global block's scalar defaults
// materialize (present ⇒ conftagz applies them), DDNS-enabled server/proxy carry
// their blocks, and a server WITHOUT ddns stays nil (conf:"skipnil").
func TestLoadConfig_DDNS(t *testing.T) {
	cfg, err := LoadConfig("testdata/ddns.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DDNS == nil {
		t.Fatal("global ddns block should be present")
	}
	if got := cfg.DDNS.ResolveProvider(); got != "cloudflare" {
		t.Fatalf("provider default got %q", got)
	}
	if got := cfg.DDNS.ResolveInterval(); got != 4*time.Hour {
		t.Fatalf("interval default got %v, want 4h", got)
	}
	if cfg.DDNS.TTL != 1 {
		t.Fatalf("ttl default got %d, want 1 (conftagz default on present block)", cfg.DDNS.TTL)
	}

	app := cfg.GetServerByID("app")
	if app == nil || app.DDNS == nil {
		t.Fatal("server app should have a ddns block")
	}
	if app.DDNS.CfProxied == nil || *app.DDNS.CfProxied != false {
		t.Fatal("server app cf_proxied should be explicit false")
	}
	if len(app.DDNS.Records) != 2 {
		t.Fatalf("server app should have 2 records, got %v", app.DDNS.Records)
	}

	bare := cfg.GetServerByID("bare")
	if bare == nil {
		t.Fatal("server bare missing")
	}
	if bare.DDNS != nil {
		t.Fatal("server bare has no ddns block — must stay nil despite conftagz materialisation (conf:skipnil)")
	}

	if len(cfg.Proxies) != 1 || cfg.Proxies[0].DDNS == nil {
		t.Fatal("proxy should carry a ddns block")
	}
}
