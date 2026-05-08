package server

// Adapter from the live RaftStorage to the readyz.State interface.
// Lives in its own file so the unified-boot wiring stays tidy.

import (
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/server/readyz"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

// currentReadyzState returns the readyz.State the handler should
// inspect.
//
//   - Solo deployments (cfg.Team == nil): returns nil. The handler's
//     nil-state path answers 200 — there's no Raft to be unhealthy
//     about.
//   - Team deployments: returns a real state. If Global() isn't yet
//     a *RaftStorage (Raft init failed or hasn't completed), the
//     methods report RaftAlive=false and an empty leader addr so
//     the handler responds 503 and the external LB drains us. Without
//     this distinction a broken team node looked healthy to the LB.
func currentReadyzState(cfg *config.Config) readyz.State {
	if cfg == nil || cfg.Team == nil {
		return nil
	}
	return readyzStateFunc{}
}

// readyzStateFunc satisfies readyz.State by deferring every call
// to a fresh storage.Global() lookup. This works because Stage 2's
// preloadFastSubsystems calls storage.SetGlobal(rs) BEFORE
// BootUnifiedServer registers the listener, so by the time the
// first /readyz request lands, Global is populated.
//
// In team mode this struct is only constructed when cfg.Team is
// non-nil; a Global() that isn't a *RaftStorage therefore signals
// "Raft init failed / not yet complete" and methods return values
// that drive the handler to 503.
type readyzStateFunc struct{}

func (readyzStateFunc) RaftAlive() bool {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return false
	}
	return rs.RaftAlive()
}

func (readyzStateFunc) LeaderInfo() (string, string) {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return "", ""
	}
	return rs.LeaderInfo()
}

func (readyzStateFunc) AppliedIndex() uint64 {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return 0
	}
	return rs.AppliedIndex()
}

func (readyzStateFunc) LastIndex() uint64 {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return 0
	}
	return rs.LastIndex()
}
