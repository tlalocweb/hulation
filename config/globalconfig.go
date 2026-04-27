package config

// Package-level global configuration state. This mirrors izcr's config
// package so authware and other adopted code can call config.GetConfig()
// directly. Hula's historical pattern kept these in the app/ package —
// those helpers now delegate here.

import (
	"fmt"
	"strings"

	"github.com/tlalocweb/hulation/log"
)

var (
	globalConfig     *Config
	globalConfigFile string
)

// GetConfig returns the current process-wide configuration. Returns nil
// until LoadConfig (or SetConfigForTesting) has been called.
func GetConfig() *Config {
	return globalConfig
}

// SetConfigForTesting overwrites the process-wide config. Only use in tests.
func SetConfigForTesting(c *Config) {
	globalConfig = c
}

// GetConfigPath returns the file path the config was loaded from.
func GetConfigPath() string {
	return globalConfigFile
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
	globalConfig = cfg
	globalConfigFile = path
	return nil
}

// ReloadConfig re-reads the config file and swaps the global pointer.
// Returns the prior config so callers can diff fields.
func ReloadConfig() (oldConf *Config, err error) {
	newConf, err := LoadConfig(globalConfigFile)
	if err != nil {
		err = fmt.Errorf("reload config: %w", err)
		return
	}
	oldConf = globalConfig
	globalConfig = newConf
	return
}

// GetHulaOriginHost returns the configured origin hostname for hula.
// Returns empty string if no config is loaded.
func GetHulaOriginHost() string {
	if globalConfig == nil {
		return ""
	}
	return globalConfig.HulaHost
}

// GetHulaOriginBaseUrl returns the external base URL for the hula server.
func GetHulaOriginBaseUrl() string {
	if globalConfig == nil {
		return ""
	}
	return globalConfig.GetHulaServer().GetExternalUrl()
}

// ApplyLogTagConfig applies log tag filters from the loaded config file.
// CLI flags take precedence; this fills in what CLI didn't set.
func ApplyLogTagConfig() {
	if globalConfig == nil {
		return
	}
	if log.GetTagFilter() == 0 && globalConfig.LogTags != "" {
		if err := log.SetTagFilterFromString(globalConfig.LogTags); err != nil {
			log.Warnf("Invalid log_tags in config: %s", err.Error())
		}
	}
	if globalConfig.NoLogTags != "" {
		if err := log.SetTagBlockFilterFromString(globalConfig.NoLogTags); err != nil {
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
