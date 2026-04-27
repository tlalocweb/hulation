package opaque_test

import (
	"testing"

	"github.com/bytemare/opaque"
	hulaopaque "github.com/tlalocweb/hulation/pkg/auth/opaque"
)

// TestRoundTripWithOpaqueKEOptions: run register+login WITH the opaque-ke
// compatibility options (KSFSalt=16zeros, KSFLength=64). If the roundtrip
// still works, the patches don't break Go-internal interop.
func TestRoundTripWithOpaqueKEOptions(t *testing.T) {
	cfg := opaque.DefaultConfiguration()
	seed := cfg.GenerateOPRFSeed()
	priv, pub := cfg.KeyGen()
	srv, err := hulaopaque.New(seed, priv, pub.Encode())
	if err != nil { t.Fatalf("New: %v", err) }
	cli, _ := cfg.Client()

	const username = "admin@example.com"
	const password = "correct-horse-battery-staple"
	credID := hulaopaque.CredentialID("admin", username)

	salt := make([]byte, 16)
	opts := &opaque.ClientOptions{KSFSalt: salt, KSFLength: 64}

	// Register
	regReq, _ := cli.RegistrationInit([]byte(password))
	m2Bytes, err := srv.RegisterInit(credID, regReq.Serialize())
	if err != nil { t.Fatalf("RegisterInit: %v", err) }
	d, _ := cfg.Deserializer()
	m2, _ := d.RegistrationResponse(m2Bytes)
	rec, _, err := cli.RegistrationFinalize(m2, []byte(username), []byte(hulaopaque.ServerIdentity), opts)
	if err != nil { t.Fatalf("RegistrationFinalize: %v", err) }
	storedRecord, err := srv.RegisterFinish(rec.Serialize())
	if err != nil { t.Fatalf("RegisterFinish: %v", err) }

	// Login
	cli2, _ := cfg.Client()
	ke1, err := cli2.GenerateKE1([]byte(password))
	if err != nil { t.Fatalf("GenerateKE1: %v", err) }
	loginInit, err := srv.LoginInit("admin", username, credID, ke1.Serialize(), storedRecord)
	if err != nil { t.Fatalf("LoginInit: %v", err) }
	ke2, _ := d.KE2(loginInit.KE2)
	ke3, _, _, err := cli2.GenerateKE3(ke2, []byte(username), []byte(hulaopaque.ServerIdentity), opts)
	if err != nil { t.Fatalf("GenerateKE3: %v", err) }
	if _, err := srv.LoginFinish(loginInit.SessionID, ke3.Serialize()); err != nil {
		t.Fatalf("LoginFinish: %v", err)
	}
}
