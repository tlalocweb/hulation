package server

// Adapter from the live RaftStorage to the readyz.State interface.
// Lives in its own file so the unified-boot wiring stays tidy.

import (
	"github.com/tlalocweb/hulation/config"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

// currentReadyzState resolves the live RaftStorage at request time
// and wraps it in the readyz.State interface.
//
// Solo deployments (no team block configured) report trivially-alive
// — the listener itself being up is enough, and readyz.Handler
// returns 200.
//
// Team-mode deployments (team block present) report NOT alive when
// the global storage is not a RaftStorage. That state means the HA
// init didn't complete, so /readyz must answer 503 to keep an LB
// from sending writes that would silently degrade.
func currentReadyzState(cfg *config.Config) readyzStateFunc {
	return readyzStateFunc{teamMode: isTeamMode(cfg)}
}

// isTeamMode reports whether the operator declared a multi-node
// stack. The TeamConfig block being present alone isn't enough —
// solo nodes carry it too — so we key on the explicit bootstrap
// modes that imply HA.
func isTeamMode(cfg *config.Config) bool {
	if cfg == nil || cfg.Team == nil {
		return false
	}
	switch cfg.Team.Bootstrap {
	case "first-of-team", "join":
		return true
	}
	return cfg.Team.PKI != nil && cfg.Team.PKI.CACert != ""
}

// readyzStateFunc satisfies readyz.State by deferring every call
// to a fresh storage.Global() lookup. This works because Stage 2's
// preloadFastSubsystems calls storage.SetGlobal(rs) BEFORE
// BootUnifiedServer registers the listener, so by the time the
// first /readyz request lands, Global is populated.
type readyzStateFunc struct {
	teamMode bool
}

func (s readyzStateFunc) RaftAlive() bool {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		// Team mode declared but Raft never came up → not ready.
		// Solo → trivially alive, listener-up is enough.
		return !s.teamMode
	}
	return rs.RaftAlive()
}

func (s readyzStateFunc) LeaderInfo() (string, string) {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		if s.teamMode {
			return "", "" // empty addr → handler reports no_leader
		}
		return "self", "local"
	}
	return rs.LeaderInfo()
}

func (s readyzStateFunc) AppliedIndex() uint64 {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return 0
	}
	return rs.AppliedIndex()
}

func (s readyzStateFunc) LastIndex() uint64 {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return 0
	}
	return rs.LastIndex()
}
