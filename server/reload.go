package server

// SIGHUP config reload.
//
// `hulactl reload` sends SIGHUP to the running hula process. Until now nothing
// in the boot path installed a handler for it — except the DDNS updater, which
// registered its own only when DDNS happened to be configured. Go's default
// disposition for SIGHUP is to TERMINATE the process, so on any deployment
// without DDNS configured, `hulactl reload` killed the server it was meant to
// reload. That is the bug this file closes: the handler is now installed
// unconditionally at boot, so SIGHUP can never again take hula down.
//
// What a reload does and does not do
//
// Hula wires most of itself once, at boot: TLS listeners, route tables, the
// ClickHouse pool, the chat hub/router singletons, docker-spawned backends.
// Rebuilding those under live traffic is a different (and much riskier)
// feature than re-reading a file, so this deliberately does NOT attempt it.
//
// A reload:
//   - re-reads and fully validates the config file; on ANY error the running
//     config is left untouched and the error is logged (never fatal),
//   - atomically swaps the process-wide config so everything calling
//     config.GetConfig() per-use picks the new values up immediately,
//   - re-applies log tag filters,
//   - runs registered hooks so subsystems can react (DDNS re-resolves and
//     forces an immediate update),
//   - and LOGS, explicitly, which changed sections need a restart to take
//     effect — silently ignoring them would be worse than not reloading at
//     all, because the operator would believe a change had landed.

import (
	"context"
	"os"
	"os/signal"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// ReloadHook is called after a successful config swap. old may be nil on the
// very first invocation; new is never nil. Hooks run synchronously on the
// reload goroutine, so they must not block — kick off slow work themselves.
type ReloadHook func(old, newCfg *config.Config)

type namedHook struct {
	name string
	fn   ReloadHook
}

var (
	reloadMu    sync.Mutex
	reloadHooks []namedHook
)

// RegisterReloadHook registers fn to run on every successful SIGHUP reload.
// Safe to call at any point during boot; hooks run in registration order.
func RegisterReloadHook(name string, fn ReloadHook) {
	if fn == nil {
		return
	}
	reloadMu.Lock()
	defer reloadMu.Unlock()
	reloadHooks = append(reloadHooks, namedHook{name: name, fn: fn})
}

// resetReloadHooksForTest clears the registry between tests.
func resetReloadHooksForTest() {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	reloadHooks = nil
}

// snapshotReloadHooks copies the registry under lock so a hook registering
// another hook can't deadlock or mutate the slice mid-iteration.
func snapshotReloadHooks() []namedHook {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	return append([]namedHook(nil), reloadHooks...)
}

// startSignalReload installs the process-wide SIGHUP handler. It returns
// immediately; the handler runs until ctx is cancelled.
//
// Installed unconditionally — that is the point. An unhandled SIGHUP kills the
// process, so "no subsystem happens to want reloads" must not mean "reload
// terminates the server".
func startSignalReload(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				log.Infof("reload: SIGHUP received — re-reading %s", config.GetConfigPath())
				if err := ReloadFromSignal(); err != nil {
					// Non-fatal by design: a bad edit must not take down a
					// running server. The previous config stays live.
					log.Errorf("reload: FAILED, keeping previous config: %s", err.Error())
				}
			}
		}
	}()
	log.Infof("reload: SIGHUP handler installed (hulactl reload)")
}

// ReloadFromSignal performs one reload cycle. Exported so tests (and any
// future admin "reload now" RPC) can drive it without sending a real signal.
func ReloadFromSignal() error {
	old, err := config.ReloadConfig()
	if err != nil {
		return err
	}
	newCfg := config.GetConfig()

	// Log filters are cheap and safe to re-apply in place.
	config.ApplyLogTagConfig()

	// Tell the operator what actually took effect. Anything boot-wired that
	// changed needs a restart, and saying so is the difference between a
	// reload they can trust and one that quietly does nothing.
	if changed := restartRequiredChanges(old, newCfg); len(changed) > 0 {
		log.Warnf("reload: applied, but these changed sections need a RESTART to take effect: %s",
			strings.Join(changed, ", "))
	}

	for _, h := range snapshotReloadHooks() {
		h.fn(old, newCfg)
	}
	log.Infof("reload: config reloaded successfully (%d hook(s) run)", len(snapshotReloadHooks()))
	return nil
}

// restartRequiredChanges reports which boot-wired sections differ between old
// and newCfg. These are the parts hula binds once at startup — listeners,
// route tables, the DB pool, docker-spawned backends — so a change to them is
// read from the file but not actually live until the process restarts.
//
// Deliberately a whitelist of known-boot-wired fields rather than a diff of
// the whole struct: most of Config IS re-read usefully (chat tunables, DDNS,
// bad-actor knobs, keys), and flagging those as "needs restart" would train
// operators to ignore the warning.
func restartRequiredChanges(old, newCfg *config.Config) []string {
	if old == nil || newCfg == nil {
		return nil
	}
	checks := map[string]func() bool{
		// Listener identity: port/scheme/bind are bound at boot.
		"port":                  func() bool { return old.Port != newCfg.Port },
		"external_publish_port": func() bool { return old.ExternalPublishPort != newCfg.ExternalPublishPort },
		"external_scheme":       func() bool { return old.ExternalScheme != newCfg.ExternalScheme },
		"listen_on":             func() bool { return old.ListenOn != newCfg.ListenOn },
		"hula_host":             func() bool { return old.HulaHost != newCfg.HulaHost },
		// Virtual hosts + proxy routes build the route table and the per-host
		// certificate map at boot.
		"servers": func() bool { return !reflect.DeepEqual(old.Servers, newCfg.Servers) },
		"proxies": func() bool { return !reflect.DeepEqual(old.Proxies, newCfg.Proxies) },
		// TLS material is loaded into the listener's cert resolver at boot.
		"hula_ssl": func() bool { return !reflect.DeepEqual(old.HulaSSL, newCfg.HulaSSL) },
		"ssl":      func() bool { return !reflect.DeepEqual(old.SSL, newCfg.SSL) },
		// The ClickHouse pool and migrations run once at boot.
		"dbconfig": func() bool { return !reflect.DeepEqual(old.DBConfig, newCfg.DBConfig) },
		// Cluster identity/membership is established at boot.
		"team": func() bool { return !reflect.DeepEqual(old.Team, newCfg.Team) },
		// Backend registries spawn docker containers at boot.
		"registries": func() bool { return !reflect.DeepEqual(old.Registries, newCfg.Registries) },
	}
	var out []string
	for name, changed := range checks {
		if changed() {
			out = append(out, name)
		}
	}
	// Stable order: map iteration is randomised, and a log line that reorders
	// itself between runs is needlessly hard to diff.
	sort.Strings(out)
	return out
}
