package opaque

// Login round-trip helpers. Stateful between init and finish via
// the sessionCache.
//
// LoginInit takes KE1 + the user's stored RegistrationRecord and
// returns KE2 + a session_id. LoginFinish takes session_id + KE3
// and returns the AKE-derived session secret on success (which the
// caller can discard — the JWT issuance downstream is independent).

import (
	"errors"
	"fmt"

	"github.com/bytemare/opaque"
)

// LoginInitResult is what the RPC handler hands back to the client
// (via REST gateway). Both fields go on the wire.
type LoginInitResult struct {
	KE2       []byte
	SessionID string
}

// LoginInit takes the client's KE1 + the user's stored OPAQUE
// record and returns KE2 + a session_id. Caller persists session_id
// in the response; client returns it on LoginFinish.
//
// `provider` and `username` are recorded on the cached pending-login
// so LoginFinish can route to the right JWT-issuance path without
// re-fetching from storage.
func (s *Server) LoginInit(provider, username string, credentialID, ke1, storedRecord []byte) (*LoginInitResult, error) {
	if len(credentialID) == 0 {
		return nil, errors.New("opaque: LoginInit: credentialID required")
	}
	if len(ke1) == 0 {
		return nil, errors.New("opaque: LoginInit: ke1 required")
	}
	if len(storedRecord) == 0 {
		return nil, errors.New("opaque: LoginInit: storedRecord required")
	}
	srv, err := s.newServerInstance()
	if err != nil {
		return nil, err
	}
	d, err := s.deserializer()
	if err != nil {
		return nil, err
	}
	regRec, err := d.RegistrationRecord(storedRecord)
	if err != nil {
		return nil, fmt.Errorf("opaque: LoginInit: deserialize stored record: %w", err)
	}
	ke1Msg, err := d.KE1(ke1)
	if err != nil {
		return nil, fmt.Errorf("opaque: LoginInit: deserialize KE1: %w", err)
	}
	clientRec := &opaque.ClientRecord{
		RegistrationRecord:   regRec,
		CredentialIdentifier: credentialID,
		// ClientIdentity must mirror what the browser-side serenity-kit
		// puts in `identifiers.client` — see OPAQUE_PLAN §18.3(c).
		// hula uses the same string for both: the username is the
		// authoritative identity in the AKE transcript.
		ClientIdentity: []byte(username),
	}
	ke2, output, err := srv.GenerateKE2(ke1Msg, clientRec)
	if err != nil {
		return nil, fmt.Errorf("opaque: LoginInit: GenerateKE2: %w", err)
	}
	sid := s.sessions.put(&pendingLogin{
		server:   srv,
		output:   output,
		username: username,
		provider: provider,
	})
	return &LoginInitResult{
		KE2:       ke2.Serialize(),
		SessionID: sid,
	}, nil
}

// LoginFinishResult carries the post-AKE session metadata. Caller
// uses Username + Provider to issue a JWT via the existing
// model.NewJWTClaimsCommit path. SessionSecret is exposed for
// future use (channel binding, encrypted-bootstrap responses) but
// is OK to discard.
type LoginFinishResult struct {
	Username      string
	Provider      string
	SessionSecret []byte
}

// LoginFinish verifies KE3 against the cached server state. Returns
// the username + provider on success so the caller can issue the
// JWT. Returns ErrInvalidLogin on MAC failure (wrong password)
// and ErrSessionNotFound on cache miss / expiry.
func (s *Server) LoginFinish(sessionID string, ke3 []byte) (*LoginFinishResult, error) {
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	if len(ke3) == 0 {
		return nil, errors.New("opaque: LoginFinish: ke3 required")
	}
	pending := s.sessions.take(sessionID)
	if pending == nil {
		return nil, ErrSessionNotFound
	}
	d, err := s.deserializer()
	if err != nil {
		return nil, err
	}
	ke3Msg, err := d.KE3(ke3)
	if err != nil {
		return nil, fmt.Errorf("opaque: LoginFinish: deserialize KE3: %w", err)
	}
	if err := pending.server.LoginFinish(ke3Msg, pending.output.ClientMAC); err != nil {
		return nil, ErrInvalidLogin
	}
	return &LoginFinishResult{
		Username:      pending.username,
		Provider:      pending.provider,
		SessionSecret: pending.output.SessionSecret,
	}, nil
}

// Sentinels — caller maps to gRPC codes at the RPC boundary.
var (
	// ErrSessionNotFound — session_id missing or expired (60s TTL).
	ErrSessionNotFound = errors.New("opaque: login session not found or expired")
	// ErrInvalidLogin — KE3 MAC didn't verify, i.e. wrong password.
	// Same surface as legacy "invalid credentials".
	ErrInvalidLogin = errors.New("opaque: invalid credentials")
)

// CredentialID assembles the per-user identifier the OPRF keys on.
// Constant across virtual hosts (the username is the authoritative
// identity, not a per-host pair).
func CredentialID(provider, username string) []byte {
	return []byte(provider + "|" + username)
}
