package server

// Seeds the team's bootstrap_token into the Raft FSM at first
// boot. The token comes from the operator's HULA_TEAM_BOOTSTRAP_TOKEN
// env var (rendered into team.bootstrap_token via mustache config
// substitution); we write it once into _team/bootstrap_token so
// MembershipService.Join can verify it.
//
// Safe to run on every boot — the membership package treats an
// existing token under the same key as authoritative.

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/tlalocweb/hulation/config"
	membershipimpl "github.com/tlalocweb/hulation/pkg/api/v1/membership"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
)

func seedBootstrapToken(cfg *config.Config) {
	if cfg == nil || cfg.Team == nil || cfg.Team.BootstrapToken == "" {
		return
	}
	store := storagepkg.Global()
	if store == nil {
		return
	}
	tok := strings.TrimSpace(cfg.Team.BootstrapToken)
	// Operators typically distribute the token base64-encoded (from
	// `hula genteamcerts`); decode if it parses as base64,
	// otherwise treat as raw bytes. Either form is fine — both
	// sides do the same decode/raw mapping.
	if dec, err := base64.StdEncoding.DecodeString(tok); err == nil {
		if err := store.Put(context.Background(), membershipimpl.BootstrapTokenKey, dec); err != nil {
			unifiedLog.Warnf("seed bootstrap_token: %v", err)
		}
		return
	}
	if err := store.Put(context.Background(), membershipimpl.BootstrapTokenKey, []byte(tok)); err != nil {
		unifiedLog.Warnf("seed bootstrap_token: %v", err)
	}
}
