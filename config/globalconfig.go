package config

// Package-level global configuration state. This mirrors izcr's config
// package so authware and other adopted code can call config.GetConfig()
// directly. Hula's historical pattern kept these in the app/ package —
// those helpers now delegate here.

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/tlalocweb/hulation/log"
)

// The process-wide config is held in atomics, not plain vars.
//
// GetConfig() is called from request-handling goroutines (chat pinning, the
// visitor-chat key lookup, the widget manifest, …) while SIGHUP-driven
// ReloadConfig can swap the pointer at any moment. A plain pointer assignment
// racing those reads is a data race — unsynchronised, and the kind the race
// detector flags. atomic.Pointer makes the swap publish safely and keeps the
// read lock-free on the hot path.
//
// Readers still get a CONSISTENT SNAPSHOT only for the duration of one
// GetConfig() call: two separate calls may straddle a reload and observe
// different configs. Code that needs several fields to agree must capture the
// pointer once and pass it down — see buildBuiltinVarsFromConfig, which does
// exactly that so the widget manifest can't sign one key while advertising
// another.
var (
	globalConfig     atomic.Pointer[Config]
	globalConfigFile atomic.Pointer[string]
)

// GetConfig returns the current process-wide configuration. Returns nil
// until LoadConfig (or SetConfigForTesting) has been called.
func GetConfig() *Config {
	return globalConfig.Load()
}

// SetConfigForTesting overwrites the process-wide config. Only use in tests.
func SetConfigForTesting(c *Config) {
	globalConfig.Store(c)
}

// GetConfigPath returns the file path the config was loaded from.
func GetConfigPath() string {
	if p := globalConfigFile.Load(); p != nil {
		return *p
	}
	return ""
}

// InitConfig loads the config file at path and sets it as the process-wide
// configuration. Equivalent to izcr's config.LoadConfig in semantics; named
// differently here to avoid colliding with hula's pre-existing LoadConfig
// file-loader function.
func InitConfig(path string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	globalConfig.Store(cfg)
	p := path
	globalConfigFile.Store(&p)
	return nil
}

// ReloadConfig re-reads the config file and swaps the global pointer,
// returning the prior config so callers can diff fields.
//
// The new file is fully parsed and validated BEFORE anything is swapped, so a
// syntax error or a failed validation leaves the running config untouched and
// returns an error. A live server must never be left holding a half-applied or
// invalid config because someone saved a typo.
func ReloadConfig() (oldConf *Config, err error) {
	path := GetConfigPath()
	if path == "" {
		return nil, errors.New("reload config: no config path recorded (InitConfig never ran)")
	}
	newConf, err := LoadConfig(path)
	if err != nil {
		// Deliberately does NOT swap: the old config stays live.
		return nil, fmt.Errorf("reload config: %w", err)
	}
	return globalConfig.Swap(newConf), nil
}

// GetHulaOriginHost returns the configured origin hostname for hula.
// Returns empty string if no config is loaded.
func GetHulaOriginHost() string {
	cfg := GetConfig()
	if cfg == nil {
		return ""
	}
	return cfg.HulaHost
}

// GetHulaOriginBaseUrl returns the external base URL for the hula server.
func GetHulaOriginBaseUrl() string {
	cfg := GetConfig()
	if cfg == nil {
		return ""
	}
	return cfg.GetHulaServer().GetExternalUrl()
}

// ApplyLogTagConfig applies log tag filters from the loaded config file.
// CLI flags take precedence; this fills in what CLI didn't set.
func ApplyLogTagConfig() {
	cfg := GetConfig()
	if cfg == nil {
		return
	}
	if log.GetTagFilter() == 0 && cfg.LogTags != "" {
		if err := log.SetTagFilterFromString(cfg.LogTags); err != nil {
			log.Warnf("Invalid log_tags in config: %s", err.Error())
		}
	}
	if cfg.NoLogTags != "" {
		if err := log.SetTagBlockFilterFromString(cfg.NoLogTags); err != nil {
			log.Warnf("Invalid no_log_tags in config: %s", err.Error())
		}
	}
}

// unused-but-may-be-used: kept for compatibility with izcr helpers that
// split comma-separated hostnames.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

var _ = splitAndTrim // silence unused if nothing else imports it yet
