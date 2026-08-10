package server

// Tests for SIGHUP config reload.
//
// The bug being guarded: Go's default disposition for SIGHUP is to TERMINATE
// the process. Before this, the only signal.Notify(SIGHUP) in the boot path
// belonged to the DDNS updater and was registered only when DDNS happened to
// be configured — so on every deployment without DDNS, `hulactl reload` killed
// the server it was meant to reload. TestSIGHUPDoesNotKillProcess is the
// regression test for exactly that, and it fails (by dying) without the
// handler.

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/config"
)

func reloadTestConfigYAML(hulaHost string) string {
	return `
admin:
    username: admin
    hash: "x"
jwt_key: "reload-test-jwt-key-32-bytes-long-ok!!"
jwt_expiration: "72h"
port: 8443
hula_host: ` + hulaHost + `
dbconfig: disabled
servers:
    - host: static.` + hulaHost + `
      id: static
`
}

func writeReloadConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hula-config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// TestSIGHUPDoesNotKillProcess is the whole point of the feature: with a
// handler installed, delivering SIGHUP to ourselves must be survivable. Without
// startSignalReload the default disposition terminates the test binary, so this
// test does not merely fail — it takes the run down, which is precisely the
// production behaviour being fixed.
func TestSIGHUPDoesNotKillProcess(t *testing.T) {
	resetReloadHooksForTest()
	p := writeReloadConfig(t, reloadTestConfigYAML("sighup.example.com"))
	if err := config.InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var reloads atomic.Int32
	RegisterReloadHook("test-counter", func(_, _ *config.Config) {
		reloads.Add(1)
	})
	startSignalReload(ctx)
	// Give the handler goroutine a moment to install its notify before the
	// signal is raised; otherwise the default disposition could still win.
	time.Sleep(150 * time.Millisecond)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("raise SIGHUP: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reloads.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reloads.Load() == 0 {
		t.Fatal("SIGHUP did not trigger a reload (handler not installed?)")
	}
	// Reaching here at all proves the process survived the signal.
}

func TestReloadFromSignalSwapsConfigAndRunsHooks(t *testing.T) {
	resetReloadHooksForTest()
	p := writeReloadConfig(t, reloadTestConfigYAML("before.example.com"))
	if err := config.InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}

	var gotOld, gotNew string
	RegisterReloadHook("capture", func(old, newCfg *config.Config) {
		if old != nil {
			gotOld = old.HulaHost
		}
		if newCfg != nil {
			gotNew = newCfg.HulaHost
		}
	})

	if err := os.WriteFile(p, []byte(reloadTestConfigYAML("after.example.com")), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := ReloadFromSignal(); err != nil {
		t.Fatalf("ReloadFromSignal: %v", err)
	}

	if got := config.GetConfig().HulaHost; got != "after.example.com" {
		t.Errorf("live config HulaHost = %q, want after.example.com", got)
	}
	if gotOld != "before.example.com" {
		t.Errorf("hook old HulaHost = %q, want before.example.com", gotOld)
	}
	if gotNew != "after.example.com" {
		t.Errorf("hook new HulaHost = %q, want after.example.com", gotNew)
	}
}

// A bad edit must not disturb the running server: no swap, and — critically —
// no hooks fired, since a hook seeing a rejected config could act on values
// that are not live.
func TestReloadFromSignalKeepsConfigAndSkipsHooksOnError(t *testing.T) {
	resetReloadHooksForTest()
	p := writeReloadConfig(t, reloadTestConfigYAML("stable.example.com"))
	if err := config.InitConfig(p); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}

	var fired atomic.Int32
	RegisterReloadHook("must-not-fire", func(_, _ *config.Config) { fired.Add(1) })

	if err := os.WriteFile(p, []byte("not: [valid: yaml\n"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	if err := ReloadFromSignal(); err == nil {
		t.Fatal("expected an error from a malformed config")
	}
	if got := config.GetConfig().HulaHost; got != "stable.example.com" {
		t.Errorf("live config was clobbered by a failed reload: %q", got)
	}
	if n := fired.Load(); n != 0 {
		t.Errorf("hooks ran %d time(s) on a failed reload; want 0", n)
	}
}

func TestRestartRequiredChanges(t *testing.T) {
	base := &config.Config{
		Port:     443,
		HulaHost: "a.example.com",
		Chat:     &config.ChatConfig{ResumeWindow: "24h"},
	}

	// A chat-tunable change is genuinely live after a reload, so it must NOT
	// be reported as needing a restart — crying wolf here would train
	// operators to ignore the warning that matters.
	liveOnly := &config.Config{
		Port:     443,
		HulaHost: "a.example.com",
		Chat:     &config.ChatConfig{ResumeWindow: "1h"},
	}
	if got := restartRequiredChanges(base, liveOnly); len(got) != 0 {
		t.Errorf("chat tunable change reported as restart-required: %v", got)
	}

	// Boot-wired fields must be reported.
	rebound := &config.Config{
		Port:     8443,
		HulaHost: "b.example.com",
		Chat:     &config.ChatConfig{ResumeWindow: "24h"},
	}
	got := restartRequiredChanges(base, rebound)
	want := map[string]bool{"port": true, "hula_host": true}
	if len(got) != len(want) {
		t.Fatalf("restartRequiredChanges = %v, want exactly %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected restart-required field %q", g)
		}
	}

	// Stable ordering: the log line must not reshuffle between runs.
	for i := 0; i < 20; i++ {
		again := restartRequiredChanges(base, rebound)
		if len(again) != len(got) {
			t.Fatalf("unstable length: %v vs %v", again, got)
		}
		for j := range again {
			if again[j] != got[j] {
				t.Fatalf("unstable order: %v vs %v", again, got)
			}
		}
	}

	// Nil-safe: the first reload has no prior config to diff against.
	if got := restartRequiredChanges(nil, base); got != nil {
		t.Errorf("nil old should yield no changes, got %v", got)
	}
}

func TestRegisterReloadHookIgnoresNil(t *testing.T) {
	resetReloadHooksForTest()
	RegisterReloadHook("nil-hook", nil)
	if n := len(snapshotReloadHooks()); n != 0 {
		t.Errorf("nil hook was registered (%d hooks)", n)
	}
}
