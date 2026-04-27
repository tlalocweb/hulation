// Package opaque is hula's OPAQUE PAKE wrapper.
//
// OPAQUE (RFC 9807) replaces plaintext-password-over-TLS in the
// admin + internal-provider login paths with a key-exchange protocol
// where the server never sees the password — not even during
// registration.
//
// The wire library is bytemare/opaque (RFC 9807 conformant; the
// author of the RFC maintains it). Browser-side counterpart is
// @serenity-kit/opaque, which interops with bytemare on the
// default suite (Ristretto255 + Argon2id, t=3 m=64MiB p=4); the
// interop is documented in OPAQUE_PLAN §18.
//
// Public surface:
//   * Server — process-global handle holding the OPRF seed +
//     long-term AKE keypair. One per hula instance.
//   * RegisterInit / RegisterFinish — registration round-trip helpers.
//   * LoginInit / LoginFinish — login round-trip helpers.
//   * SetGlobal / Global — process-wide accessor for RPC handlers.
//
// The package keeps no proto types — it deals in raw bytes (the
// wire form bytemare/opaque produces). Conversion to/from base64url
// happens at the RPC boundary in pkg/api/v1/auth.

package opaque

import (
	"errors"
	"fmt"
	"sync"

	"github.com/bytemare/ecc"
	"github.com/bytemare/opaque"
)

// ServerIdentity is the AKE-transcript identity hula uses for itself.
// Constant across virtual hosts so a single OPAQUE record validates
// regardless of which hostname the client is hitting (per
// OPAQUE_PLAN §4.2).
const ServerIdentity = "hula"

// Server holds the bytemare configuration + long-lived key material.
// Configurations should not be mutated after construction; access
// from RPC handlers is read-only.
type Server struct {
	cfg         *opaque.Configuration
	oprfSeed    []byte
	akePriv     *ecc.Scalar
	akePubBytes []byte

	sessions *sessionCache
}

// New constructs a Server from raw key material. seed must be 64 bytes
// (the OPAQUE OPRF seed length for the default suite); akePriv +
// akePubBytes are the long-lived server AKE keypair.
//
// Use LoadOrGenerate to populate these from config + env.
func New(seed []byte, akePriv *ecc.Scalar, akePubBytes []byte) (*Server, error) {
	cfg := opaque.DefaultConfiguration()
	if len(seed) == 0 {
		return nil, errors.New("opaque: oprf seed is required")
	}
	if akePriv == nil || len(akePubBytes) == 0 {
		return nil, errors.New("opaque: ake keypair is required")
	}
	return &Server{
		cfg:         cfg,
		oprfSeed:    seed,
		akePriv:     akePriv,
		akePubBytes: akePubBytes,
		sessions:    newSessionCache(),
	}, nil
}

// Suite reports the cipher suite this Server is configured for —
// useful for boot-time logging and the e2e suite's compat check.
func (s *Server) Suite() string {
	return fmt.Sprintf("OPAQUE/oprf=%v/ake=%v/ksf=%v", s.cfg.OPRF, s.cfg.AKE, s.cfg.KSF)
}

// PublicKey returns a copy of the server's AKE public-key bytes.
// Used by hulactl's set-password command to verify it's pinned to
// the same server it's about to register against.
func (s *Server) PublicKey() []byte {
	out := make([]byte, len(s.akePubBytes))
	copy(out, s.akePubBytes)
	return out
}

// newServerInstance builds a fresh bytemare *opaque.Server with
// this Server's key material installed. The bytemare API expects a
// per-call instance; we don't share state across calls.
func (s *Server) newServerInstance() (*opaque.Server, error) {
	srv, err := s.cfg.Server()
	if err != nil {
		return nil, fmt.Errorf("opaque: cfg.Server: %w", err)
	}
	if err := srv.SetKeyMaterial(&opaque.ServerKeyMaterial{
		PrivateKey:     s.akePriv,
		PublicKeyBytes: s.akePubBytes,
		OPRFGlobalSeed: s.oprfSeed,
		Identity:       []byte(ServerIdentity),
	}); err != nil {
		return nil, fmt.Errorf("opaque: SetKeyMaterial: %w", err)
	}
	return srv, nil
}

// deserializer returns a configured bytemare deserializer.
func (s *Server) deserializer() (*opaque.Deserializer, error) {
	d, err := s.cfg.Deserializer()
	if err != nil {
		return nil, fmt.Errorf("opaque: deserializer: %w", err)
	}
	return d, nil
}

// --- process-global handle ----------------------------------------

var (
	globalMu sync.RWMutex
	global   *Server
)

// SetGlobal installs the process-wide OPAQUE Server. Call once at
// boot in server.RunUnified; subsequent calls replace.
func SetGlobal(s *Server) {
	globalMu.Lock()
	defer globalMu.Unlock()
	global = s
}

// Global returns the process-wide Server or nil when unset. RPC
// handlers must tolerate nil and return FailedPrecondition.
func Global() *Server {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
