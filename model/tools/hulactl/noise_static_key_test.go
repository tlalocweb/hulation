package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"

	"github.com/tlalocweb/hulation/utils"
)

func TestGenerateNoiseStaticKeyPair_RoundTripDecodes(t *testing.T) {
	privB64, pubB64, err := generateNoiseStaticKeyPair()
	if err != nil {
		t.Fatalf("generate: %s", err)
	}
	// Private decodes via the same helper hulation uses at boot.
	priv, err := utils.DecodeNoiseStaticKey(privB64)
	if err != nil {
		t.Fatalf("decode priv: %s", err)
	}
	if len(priv) != 32 {
		t.Fatalf("priv len: got %d want 32", len(priv))
	}
	// Public decodes as URL-safe-no-pad (the format
	// server/installation_identity.go publishes).
	pub, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode pub: %s", err)
	}
	if len(pub) != 32 {
		t.Fatalf("pub len: got %d want 32", len(pub))
	}
}

// TestGenerateNoiseStaticKeyPair_PublicMatchesIdentityEndpoint pins the contract
// that the public-key derivation in hulactl matches what
// server/installation_identity.go computes. Operators rely on this so
// `hulactl noise-static-key` output is verifiable against
// `GET /api/v1/installation/identity` once the key is in config.
func TestGenerateNoiseStaticKeyPair_PublicMatchesIdentityEndpoint(t *testing.T) {
	privB64, pubB64, err := generateNoiseStaticKeyPair()
	if err != nil {
		t.Fatalf("generate: %s", err)
	}
	priv, err := utils.DecodeNoiseStaticKey(privB64)
	if err != nil {
		t.Fatalf("decode priv: %s", err)
	}
	// Mirror server/installation_identity.go::installationIdentityHandler.
	wantPub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive expected pub: %s", err)
	}
	wantPubB64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(wantPub)
	if pubB64 != wantPubB64 {
		t.Fatalf("public key mismatch:\n hulactl: %s\n endpoint: %s", pubB64, wantPubB64)
	}
}

func TestGenerateNoiseStaticKeyPair_DifferentEachCall(t *testing.T) {
	a, _, err := generateNoiseStaticKeyPair()
	if err != nil {
		t.Fatalf("a: %s", err)
	}
	b, _, err := generateNoiseStaticKeyPair()
	if err != nil {
		t.Fatalf("b: %s", err)
	}
	if a == b {
		t.Fatalf("two consecutive calls returned identical secrets — RNG wedged?")
	}
}

func TestGenerateNoiseStaticKeyPair_OutputIsURLSafeNoPad(t *testing.T) {
	privB64, pubB64, err := generateNoiseStaticKeyPair()
	if err != nil {
		t.Fatalf("generate: %s", err)
	}
	// No trailing `=`; no `+` / `/` (those are stdlib base64.StdEncoding's
	// alphabet — operators pasting into env vars / TOML want the URL-safe
	// alphabet so quoting rules don't bite them).
	for _, s := range []string{privB64, pubB64} {
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("output not URL-safe-no-pad: %q", s)
		}
	}
}
