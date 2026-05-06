package server

// Adapter from the live RaftStorage to the readyz.State interface.
// Lives in its own file so the unified-boot wiring stays tidy.

import (
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

// currentReadyzState resolves the live RaftStorage at request time
// and wraps it in the readyz.State interface. Returns nil when the
// global storage is not a RaftStorage (degraded mode / pre-init);
// the readyz handler treats nil as "no HA stack — answer 200".
func currentReadyzState() readyzStateFunc {
	return readyzStateFunc{}
}

// readyzStateFunc satisfies readyz.State by deferring every call
// to a fresh storage.Global() lookup. This works because Stage 2's
// preloadFastSubsystems calls storage.SetGlobal(rs) BEFORE
// BootUnifiedServer registers the listener, so by the time the
// first /readyz request lands, Global is populated.
type readyzStateFunc struct{}

func (readyzStateFunc) RaftAlive() bool {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return true // no HA stack → trivially "alive"
	}
	return rs.RaftAlive()
}

func (readyzStateFunc) LeaderInfo() (string, string) {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return "self", "local"
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
