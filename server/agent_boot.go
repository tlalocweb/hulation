package server

// hulaagent boot wiring.
//
// Phase 2 (already shipped): load (or generate) the persistent Agent
// CA at boot so /api/agent/create can sign leaf certs.
//
// Phase 3 (this file extends): expose the Agent CA pool so the
// unified listener trusts Agent-CA-signed client certs at handshake
// time, and attach the request-time agent-auth middleware to the
// unified server's HTTP handler chain.

import (
	"crypto/x509"

	"github.com/tlalocweb/hulation/handler"
	"github.com/tlalocweb/hulation/log"
	agentmtls "github.com/tlalocweb/hulation/pkg/agent/mtls"
	agentpki "github.com/tlalocweb/hulation/pkg/agent/pki"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	"github.com/tlalocweb/hulation/pkg/store/storage"
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

// agentClientCAPool returns a *x509.CertPool containing the Agent
// CA's cert, or nil if the Agent CA hasn't loaded yet (degraded
// boot, dev/test setups without storage). Phase 3 boot passes this
// pool through to unified.Config.ClientCAs so Agent-CA-signed leaves
// validate at handshake time.
func agentClientCAPool() *x509.CertPool {
	ca := handler.GetAgentCA()
	if ca == nil || ca.Cert == nil {
		return nil
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// attachAgentMTLSMiddleware wires the request-time agent-auth
// middleware into the unified server's HTTP handler chain. The
// middleware is a no-op for any request that didn't present an
// Agent-CA-signed leaf, so non-agent traffic flows through
// untouched. Active agents end up with their registry.Record
// attached to the request context — Phase 5 route handlers consult
// it via agentmtls.RecordFromContext.
func attachAgentMTLSMiddleware(srv *unified.Server) {
	if srv == nil {
		return
	}
	lookup := agentmtls.BindRegistryLookup(func() storage.Storage {
		return storage.Global()
	})
	srv.AttachHTTPMiddleware(agentmtls.Middleware(handler.GetAgentCA, lookup, nil))
	log.Infof("agent: mTLS verification middleware attached")
}
