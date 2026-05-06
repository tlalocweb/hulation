package server

// Phase-2 hulaagent boot: load (or generate) the persistent Agent CA
// and stash it where the CreateAgent handler can find it. Called from
// run_unified.go after the storage layer is up but before HTTP
// fallback routes are registered.

import (
	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	agentpki "github.com/tlalocweb/hulation/pkg/agent/pki"
)

// BootAgentCA loads the persistent Agent CA from dataDir, generating
// it on first boot. Errors are surfaced verbatim so the caller can
// fail fast — without an Agent CA, /api/agent/create would return
// 503 forever.
//
// Idempotent: subsequent boots reuse the existing files.
func BootAgentCA(dataDir string) error {
	ca, err := agentpki.LoadOrCreateCA(dataDir)
	if err != nil {
		return err
	}
	handler.SetAgentCA(ca)
	log.Infof("agent: CA loaded from %s (subject=%s)", dataDir, ca.Cert.Subject.CommonName)
	return nil
}
