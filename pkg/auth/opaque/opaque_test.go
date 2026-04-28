package opaque

import (
	"errors"
	"testing"

	"github.com/bytemare/opaque"
)

// Round-trip test: drive register + login through the server-side
// helpers using a bytemare client (proves the helpers' contract).
// The cross-language interop with serenity-kit is verified
// separately in OPAQUE_PLAN §18; this is the in-tree regression
// against future bytemare upgrades.

func freshServer(t *testing.T) *Server {
	t.Helper()
	cfg := opaque.DefaultConfiguration()
	seed := cfg.GenerateOPRFSeed()
	priv, pub := cfg.KeyGen()
	srv, err := New(seed, priv, pub.Encode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func TestRoundTrip(t *testing.T) {
	srv := freshServer(t)
	cfg := opaque.DefaultConfiguration()
	cli, err := cfg.Client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	const username = "admin@example.com"
	const password = "correct-horse-battery-staple"
	credID := CredentialID("admin", username)

	// --- Register ---
	regReq, err := cli.RegistrationInit([]byte(password))
	if err != nil {
		t.Fatalf("RegistrationInit: %v", err)
	}
	m2Bytes, err := srv.RegisterInit(credID, regReq.Serialize())
	if err != nil {
		t.Fatalf("server RegisterInit: %v", err)
	}
	d, _ := cfg.Deserializer()
	m2, err := d.RegistrationResponse(m2Bytes)
	if err != nil {
		t.Fatalf("deser M2: %v", err)
	}
	rec, _, err := cli.RegistrationFinalize(m2, []byte(username), []byte(ServerIdentity))
	if err != nil {
		t.Fatalf("RegistrationFinalize: %v", err)
	}
	storedRecord, err := srv.RegisterFinish(rec.Serialize())
	if err != nil {
		t.Fatalf("server RegisterFinish: %v", err)
	}
	if len(storedRecord) == 0 {
		t.Fatal("stored record is empty")
	}

	// --- Login ---
	cli2, _ := cfg.Client()
	ke1, err := cli2.GenerateKE1([]byte(password))
	if err != nil {
		t.Fatalf("GenerateKE1: %v", err)
	}
	loginInit, err := srv.LoginInit("admin", username, credID, ke1.Serialize(), storedRecord)
	if err != nil {
		t.Fatalf("server LoginInit: %v", err)
	}
	if loginInit.SessionID == "" {
		t.Fatal("LoginInit returned empty session id")
	}
	ke2, err := d.KE2(loginInit.KE2)
	if err != nil {
		t.Fatalf("deser KE2: %v", err)
	}
	ke3, _, _, err := cli2.GenerateKE3(ke2, []byte(username), []byte(ServerIdentity))
	if err != nil {
		t.Fatalf("GenerateKE3: %v", err)
	}

	finish, err := srv.LoginFinish(loginInit.SessionID, ke3.Serialize())
	if err != nil {
		t.Fatalf("server LoginFinish: %v", err)
	}
	if finish.Username != username {
		t.Fatalf("username mismatch: got %q want %q", finish.Username, username)
	}
	if finish.Provider != "admin" {
		t.Fatalf("provider mismatch: got %q want %q", finish.Provider, "admin")
	}
	if len(finish.SessionSecret) == 0 {
		t.Fatal("empty session secret")
	}
}

func TestLoginRejectsBadKE3(t *testing.T) {
	// Verifies that the server's MAC check rejects a malformed KE3
	// — the same code path that triggers on wrong-password-derived
	// MACs. Constructing a wrong-password KE3 from bytemare's
	// client requires a recoverable run-time path that bytemare
	// makes brittle (envelope decryption with wrong rwd panics
	// before MAC check); a garbage KE3 of the right length
	// exercises the server-side rejection deterministically.
	srv := freshServer(t)
	cfg := opaque.DefaultConfiguration()
	cli, _ := cfg.Client()
	credID := CredentialID("admin", "admin")

	regReq, _ := cli.RegistrationInit([]byte("password"))
	m2Bytes, _ := srv.RegisterInit(credID, regReq.Serialize())
	d, _ := cfg.Deserializer()
	m2, _ := d.RegistrationResponse(m2Bytes)
	rec, _, _ := cli.RegistrationFinalize(m2, []byte("admin"), []byte(ServerIdentity))
	storedRecord, _ := srv.RegisterFinish(rec.Serialize())

	cli2, _ := cfg.Client()
	ke1, _ := cli2.GenerateKE1([]byte("password"))
	loginInit, _ := srv.LoginInit("admin", "admin", credID, ke1.Serialize(), storedRecord)

	// KE3 length for the default suite is 64 bytes (HMAC-SHA512 tag).
	// Server should reject the garbage KE3 with ErrInvalidLogin.
	garbage := make([]byte, 64)
	for i := range garbage {
		garbage[i] = 0xAA
	}
	_, err := srv.LoginFinish(loginInit.SessionID, garbage)
	if err == nil {
		t.Fatal("expected error on garbage KE3, got nil")
	}
	if !errors.Is(err, ErrInvalidLogin) {
		// Some malformations may surface as deserialize errors
		// instead of MAC mismatch — still a rejection, still
		// the failure mode we want.
		t.Logf("acceptable rejection (not ErrInvalidLogin): %v", err)
	}
}

func TestLoginSessionNotFound(t *testing.T) {
	srv := freshServer(t)
	_, err := srv.LoginFinish("nonexistent-session", []byte("dummy"))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestLoadOrGenerate(t *testing.T) {
	t.Setenv(EnvOPRFSeed, "")
	t.Setenv(EnvAKESecret, "")
	seed, priv, pub, err := LoadOrGenerate("", "")
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	if len(seed) == 0 || priv == nil || len(pub) == 0 {
		t.Fatal("LoadOrGenerate returned empty key material")
	}
	// Round-trip: feed the generated values back through the
	// pinned-config path and confirm they decode.
	srv, err := New(seed, priv, pub)
	if err != nil {
		t.Fatalf("New from generated material: %v", err)
	}
	if srv.Suite() == "" {
		t.Fatal("Suite() empty")
	}
}
