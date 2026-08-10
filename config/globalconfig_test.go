package config

// Tests for the process-wide config holder.
//
// The reason these exist: SIGHUP reload swaps the global config pointer while
// request-handling goroutines are calling GetConfig(). That was a plain
// pointer assignment racing plain reads — a genuine data race. The concurrency
// test below is meant to be run under `-race`, where the old implementation
// fails and the atomic one passes.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// minimalConfigYAML is the smallest config LoadConfig accepts. admin.hash is
// validated non-empty by a conftagz regexp tag, and dbconfig is mandatory
// (`disabled` satisfies it without needing a live ClickHouse). Mirrors
// testdata/nodb_disabled.yaml.
func minimalConfigYAML(hulaHost string) string {
	return `
admin:
    username: admin
    hash: "x"
jwt_key: "globalconfig-test-jwt-key-32bytes-long!!"
jwt_expiration: "72h"
port: 8443
hula_host: ` + hulaHost + `
dbconfig: disabled
servers:
    - host: static.` + hulaHost + `
      id: static
`
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "hula-config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestInitAndReloadConfig(t *testing.T) {
	p := writeTempConfig(t, minimalConfigYAML("first.example.com"))
	if err := InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}
	if got := GetConfig().HulaHost; got != "first.example.com" {
		t.Fatalf("HulaHost = %q, want first.example.com", got)
	}
	if GetConfigPath() != p {
		t.Fatalf("GetConfigPath = %q, want %q", GetConfigPath(), p)
	}

	// Edit the file in place, as an operator would, then reload.
	if err := os.WriteFile(p, []byte(minimalConfigYAML("second.example.com")), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	old, err := ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if old == nil || old.HulaHost != "first.example.com" {
		t.Fatalf("ReloadConfig returned old = %v, want the pre-reload config", old)
	}
	if got := GetConfig().HulaHost; got != "second.example.com" {
		t.Fatalf("after reload HulaHost = %q, want second.example.com", got)
	}
}

// A broken config must never replace a working one. This is the property that
// keeps a typo from taking down a live server on `hulactl reload`.
func TestReloadKeepsOldConfigOnParseError(t *testing.T) {
	p := writeTempConfig(t, minimalConfigYAML("good.example.com"))
	if err := InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}

	if err := os.WriteFile(p, []byte("this: is: not: valid: yaml: [\n"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if _, err := ReloadConfig(); err == nil {
		t.Fatal("expected ReloadConfig to reject a malformed file")
	}
	if got := GetConfig().HulaHost; got != "good.example.com" {
		t.Fatalf("broken reload clobbered the live config: HulaHost = %q", got)
	}
}

// A config that parses but fails validation must also be rejected. dbconfig is
// mandatory, so omitting it is a validation failure rather than a syntax one.
func TestReloadKeepsOldConfigOnValidationError(t *testing.T) {
	p := writeTempConfig(t, minimalConfigYAML("good.example.com"))
	if err := InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}
	invalid := "admin:\n    username: admin\n    hash: \"x\"\njwt_key: k\nport: 443\nhula_host: x.example.com\n"
	if err := os.WriteFile(p, []byte(invalid), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	if _, err := ReloadConfig(); err == nil {
		t.Fatal("expected ReloadConfig to reject a config missing mandatory dbconfig")
	}
	if got := GetConfig().HulaHost; got != "good.example.com" {
		t.Fatalf("invalid reload clobbered the live config: HulaHost = %q", got)
	}
}

func TestReloadWithoutInitIsAnError(t *testing.T) {
	// Simulate a process where InitConfig never ran.
	globalConfigFile.Store(nil)
	if _, err := ReloadConfig(); err == nil {
		t.Fatal("expected an error reloading with no recorded config path")
	}
}

// Concurrent readers during a reload. Under `-race` this is the test that
// distinguishes the atomic holder from the plain package var it replaced.
func TestGetConfigIsRaceSafeDuringReload(t *testing.T) {
	p := writeTempConfig(t, minimalConfigYAML("race-a.example.com"))
	if err := InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Mirrors the real hot path: read the pointer, then a field.
				if cfg := GetConfig(); cfg != nil {
					_ = cfg.HulaHost
				}
			}
		}()
	}

	for i := 0; i < 25; i++ {
		host := "race-a.example.com"
		if i%2 == 1 {
			host = "race-b.example.com"
		}
		if err := os.WriteFile(p, []byte(minimalConfigYAML(host)), 0o600); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		if _, err := ReloadConfig(); err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	// Whatever the interleaving, the final state must be a valid config.
	if GetConfig() == nil {
		t.Fatal("config went nil after concurrent reloads")
	}
}
