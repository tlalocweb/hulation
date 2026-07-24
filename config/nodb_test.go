package config

// Tests for no-DB mode (`dbconfig: disabled`): the custom DBConfig
// UnmarshalYAML, the IsDisabled/DBDisabled helpers, and the LoadConfig
// mandatory-vs-opt-out behaviour. Infra-free — no ClickHouse involved.

import (
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v2"
)

// dbWrap mirrors the shape LoadConfig sees: a `dbconfig:` key holding a
// *DBConfig, decoded with the same yaml.v2 library config.go uses.
type dbWrap struct {
	DBConfig *DBConfig `yaml:"dbconfig,omitempty"`
}

func TestDBConfigUnmarshalYAML_DisabledScalar(t *testing.T) {
	var w dbWrap
	if err := yaml.Unmarshal([]byte("dbconfig: disabled\n"), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.DBConfig == nil {
		t.Fatal("DBConfig is nil; want a disabled config")
	}
	if !w.DBConfig.Disabled {
		t.Fatalf("Disabled=false; want true for `dbconfig: disabled`")
	}
	if !w.DBConfig.IsDisabled() {
		t.Fatal("IsDisabled()=false; want true")
	}
}

func TestDBConfigUnmarshalYAML_DisabledCaseInsensitive(t *testing.T) {
	for _, in := range []string{"Disabled", "DISABLED", "  disabled  "} {
		var w dbWrap
		if err := yaml.Unmarshal([]byte("dbconfig: "+in+"\n"), &w); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if w.DBConfig == nil || !w.DBConfig.Disabled {
			t.Fatalf("input %q: want Disabled=true", in)
		}
	}
}

func TestDBConfigUnmarshalYAML_Mapping(t *testing.T) {
	y := "dbconfig:\n  host: db.internal\n  port: 9111\n  user: analyst\n  dbname: metrics\n"
	var w dbWrap
	if err := yaml.Unmarshal([]byte(y), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.DBConfig == nil {
		t.Fatal("DBConfig is nil; want a populated mapping")
	}
	if w.DBConfig.Disabled {
		t.Fatal("Disabled=true; want false for a mapping form")
	}
	if w.DBConfig.Host != "db.internal" || w.DBConfig.Port != 9111 ||
		w.DBConfig.Username != "analyst" || w.DBConfig.DBName != "metrics" {
		t.Fatalf("mapping fields not decoded: %+v", *w.DBConfig)
	}
}

func TestDBConfigUnmarshalYAML_BogusScalarErrors(t *testing.T) {
	var w dbWrap
	err := yaml.Unmarshal([]byte("dbconfig: frobnicate\n"), &w)
	if err == nil {
		t.Fatal("expected an error for an unrecognized dbconfig scalar")
	}
	if !strings.Contains(err.Error(), "unrecognized scalar") {
		t.Fatalf("error %q does not mention the unrecognized scalar", err)
	}
}

func TestDBConfigHelpers_NilSafe(t *testing.T) {
	var nilDB *DBConfig
	if nilDB.IsDisabled() {
		t.Fatal("nil *DBConfig.IsDisabled() = true; want false")
	}
	var nilCfg *Config
	if nilCfg.DBDisabled() {
		t.Fatal("nil *Config.DBDisabled() = true; want false")
	}
	// A Config whose DBConfig is nil is NOT disabled (that's the fatal
	// "mandatory" case, handled in LoadConfig — a distinct condition).
	cfg := &Config{}
	if cfg.DBDisabled() {
		t.Fatal("Config{DBConfig:nil}.DBDisabled() = true; want false")
	}
	cfg.DBConfig = &DBConfig{Disabled: true}
	if !cfg.DBDisabled() {
		t.Fatal("Config with disabled DBConfig: DBDisabled()=false; want true")
	}
}

// TestLoadConfig_DBDisabled is the end-to-end load test the plan asks for:
// `dbconfig: disabled` loads cleanly (no mandatory error), DBDisabled() is
// true, and conftagz filling struct defaults must NOT flip the disabled flag
// (i.e. the auto-filled host/port are never treated as a real DB).
func TestLoadConfig_DBDisabled(t *testing.T) {
	cfg, err := LoadConfig("testdata/nodb_disabled.yaml")
	if err != nil {
		t.Fatalf("LoadConfig(disabled) returned error: %v", err)
	}
	if cfg.DBConfig == nil {
		t.Fatal("DBConfig is nil after load; want a disabled config")
	}
	if !cfg.DBDisabled() {
		t.Fatalf("DBDisabled()=false after load; want true (DBConfig=%+v)", *cfg.DBConfig)
	}
	if !cfg.DBConfig.Disabled {
		t.Fatal("conftagz processing cleared DBConfig.Disabled; want it preserved")
	}
	// KEEP subsystems must still be present in the loaded config.
	if len(cfg.Servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(cfg.Servers))
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("want 1 proxy, got %d", len(cfg.Proxies))
	}
}

// TestLoadConfig_DBMissing confirms a genuinely-absent dbconfig still fails
// with the mandatory-DB error — the opt-out is `dbconfig: disabled`, not an
// omitted key.
func TestLoadConfig_DBMissing(t *testing.T) {
	_, err := LoadConfig("testdata/nodb_missing.yaml")
	if err == nil {
		t.Fatal("LoadConfig(missing dbconfig) returned nil error; want the mandatory-DB error")
	}
	if !strings.Contains(err.Error(), "no db config") {
		t.Fatalf("error %q does not mention the missing db config", err)
	}
}
