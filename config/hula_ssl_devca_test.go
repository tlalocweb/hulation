package config

import "testing"

// TestLoadConfig_DevCAWithoutStaticCert is a regression test for a dev-CA
// config-load bug: hula_ssl with only dev_ca (no static cert / ACME / CF) must
// load cleanly. conftagz auto-materializes an empty cloudflare_origin_ca
// struct, and the CF validation used to run whenever no static cert was
// present — which tripped "cloudflare_origin_ca requires api_token" and made
// dev_ca unusable outside a static-cert setup. The load path now validates
// Cloudflare Origin CA only when it is genuinely configured (api_token/zone_id
// in YAML or env), so the empty materialized struct is ignored.
func TestLoadConfig_DevCAWithoutStaticCert(t *testing.T) {
	cfg, err := LoadConfig("testdata/hula-ssl-devca.yaml")
	if err != nil {
		t.Fatalf("dev_ca-only hula_ssl should load without error, got: %v", err)
	}
	if cfg == nil || cfg.HulaSSL == nil || !cfg.HulaSSL.hasDevCA() {
		t.Fatal("expected dev_ca to be enabled on the loaded config")
	}
	if cfg.HulaSSL.hasStaticCert() {
		t.Fatal("fixture should have no static cert")
	}
}
