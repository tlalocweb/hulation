package opaque

// Registration round-trip helpers. Two RPCs in the OPAQUE flow:
// Init takes M1 from the client, returns M2. Finish takes M3 and
// returns the bytes the caller should persist as the user's
// "password file" (envelope).
//
// Registration is idempotent server-side: replaying init with the
// same credential identifier is fine. Finish writes the record
// fresh — the caller may overwrite an existing record (password
// rotation).

import (
	"errors"
	"fmt"
)

// RegisterInit consumes M1 (the client's RegistrationRequest) and
// returns M2 (the server's RegistrationResponse). credentialID is
// the per-user identifier the OPRF is keyed on; for hula it's
// `"<provider>|<username>"`.
//
// This call is stateless — no cached server state crosses init →
// finish. Each call derives the per-client OPRF key from the
// global seed + credentialID.
func (s *Server) RegisterInit(credentialID, m1 []byte) (m2 []byte, err error) {
	if len(credentialID) == 0 {
		return nil, errors.New("opaque: RegisterInit: credentialID required")
	}
	if len(m1) == 0 {
		return nil, errors.New("opaque: RegisterInit: m1 required")
	}
	srv, err := s.newServerInstance()
	if err != nil {
		return nil, err
	}
	d, err := s.deserializer()
	if err != nil {
		return nil, err
	}
	req, err := d.RegistrationRequest(m1)
	if err != nil {
		return nil, fmt.Errorf("opaque: RegisterInit: deserialize M1: %w", err)
	}
	resp, err := srv.RegistrationResponse(req, credentialID, nil)
	if err != nil {
		return nil, fmt.Errorf("opaque: RegisterInit: response: %w", err)
	}
	return resp.Serialize(), nil
}

// RegisterFinish validates that M3 (the client's RegistrationRecord)
// parses correctly, then returns the canonical serialized form for
// the caller to persist. The on-disk shape is exactly what
// `*opaque.Deserializer.RegistrationRecord` reads back: opaque to
// hula, just bytes.
//
// This method does NOT perform persistence — it returns the bytes
// and lets the caller (auth RPC handler) write them to the bolt
// store keyed however the caller wants.
func (s *Server) RegisterFinish(m3 []byte) (record []byte, err error) {
	if len(m3) == 0 {
		return nil, errors.New("opaque: RegisterFinish: m3 required")
	}
	d, err := s.deserializer()
	if err != nil {
		return nil, err
	}
	rec, err := d.RegistrationRecord(m3)
	if err != nil {
		return nil, fmt.Errorf("opaque: RegisterFinish: deserialize M3: %w", err)
	}
	return rec.Serialize(), nil
}
