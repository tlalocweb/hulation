package storage

import "sync"

// The package-global Storage handle is set at boot by the hula
// process (see server/run_unified.go) and consumed by code that
// can't easily plumb a *Storage through every call site (the
// existing accessor functions in pkg/store/bolt are the main
// users today). Callers that CAN take a Storage argument
// explicitly should — Global() is the legacy fallback.

var (
	globalMu sync.RWMutex
	global   Storage
)

// SetGlobal installs the process-wide Storage. Idempotent for
// the same instance; replaces any prior installation otherwise.
func SetGlobal(s Storage) {
	globalMu.Lock()
	global = s
	globalMu.Unlock()
}

// Global returns the process-wide Storage or nil when unset.
// Callers should tolerate nil (most production paths see a
// configured Storage by the time they're invoked, but unit
// tests sometimes don't bother).
func Global() Storage {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// ClearGlobal is intended for tests that want to reset the
// global between test cases. Production code should never call
// this.
func ClearGlobal() {
	globalMu.Lock()
	global = nil
	globalMu.Unlock()
}
