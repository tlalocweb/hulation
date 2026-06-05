package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/curve25519"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/visitorcrypto"
	"github.com/tlalocweb/hulation/utils"
)

func TestInstallationIdentity_ReturnsDerivedPublicKey(t *testing.T) {
	secretB64, err := utils.GenerateNoiseStaticKey()
	if err != nil {
		t.Fatalf("gen secret: %s", err)
	}
	// Compute expected public independently.
	priv, err := utils.DecodeNoiseStaticKey(secretB64)
	if err != nil {
		t.Fatalf("decode secret: %s", err)
	}
	expectedPub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive pub: %s", err)
	}

	prev := hulaapp.GetConfig()
	config.SetConfigForTesting(&config.Config{NoiseStaticKey: secretB64})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body unmarshal: %s; raw=%s", err, w.Body.String())
	}
	pubB64, ok := body["noise_static_public_key_b64"]
	if !ok {
		t.Fatalf("missing noise_static_public_key_b64; body=%+v", body)
	}
	got, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(pubB64)
	if err != nil {
		t.Fatalf("decode response pub: %s", err)
	}
	if string(got) != string(expectedPub) {
		t.Fatalf("public key mismatch")
	}
}

func TestInstallationIdentity_404WhenUnconfigured(t *testing.T) {
	prev := hulaapp.GetConfig()
	config.SetConfigForTesting(&config.Config{}) // NoiseStaticKey empty
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "no_identity_keys" {
		t.Fatalf("code: got %v, want no_identity_keys", body["code"])
	}
}

func TestInstallationIdentity_VisitorChatKeyOnly(t *testing.T) {
	// An install with only the visitor-chat key configured (no Noise) must
	// return 200 with just visitor_chat_public_key_b64.
	prev := hulaapp.GetConfig()
	keyB64, err := utils.GenerateNoiseStaticKey() // generic 32-byte X25519 generator
	if err != nil {
		t.Fatalf("gen key: %s", err)
	}
	config.SetConfigForTesting(&config.Config{VisitorChatKey: keyB64})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["visitor_chat_public_key_b64"] == "" {
		t.Fatalf("missing visitor_chat_public_key_b64; body=%+v", body)
	}
	if _, has := body["noise_static_public_key_b64"]; has {
		t.Fatalf("noise key present when not configured: %+v", body)
	}
	// The published public must equal what visitorcrypto derives from the priv.
	priv, _ := utils.DecodeNoiseStaticKey(keyB64)
	wantPub, _ := visitorcrypto.PublicKeyFromPrivate(priv)
	wantB64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(wantPub)
	if body["visitor_chat_public_key_b64"] != wantB64 {
		t.Fatalf("visitor pub mismatch: got %s want %s",
			body["visitor_chat_public_key_b64"], wantB64)
	}
}

func TestInstallationIdentity_BothKeys(t *testing.T) {
	prev := hulaapp.GetConfig()
	noiseB64, _ := utils.GenerateNoiseStaticKey()
	visitorB64, _ := utils.GenerateNoiseStaticKey()
	config.SetConfigForTesting(&config.Config{
		NoiseStaticKey: noiseB64,
		VisitorChatKey: visitorB64,
	})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["noise_static_public_key_b64"] == "" || body["visitor_chat_public_key_b64"] == "" {
		t.Fatalf("expected both keys present: %+v", body)
	}
	// The two publics must differ — they come from independent private keys.
	if body["noise_static_public_key_b64"] == body["visitor_chat_public_key_b64"] {
		t.Fatalf("noise and visitor publics are identical — keys not domain-separated")
	}
}

func TestInstallationIdentity_VisitorChatMalformed(t *testing.T) {
	prev := hulaapp.GetConfig()
	config.SetConfigForTesting(&config.Config{VisitorChatKey: "not-base64!!!"})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
}

func TestInstallationIdentity_500WhenKeyMalformed(t *testing.T) {
	prev := hulaapp.GetConfig()
	config.SetConfigForTesting(&config.Config{NoiseStaticKey: "not-base64!!!"})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("GET", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", w.Code)
	}
}

func TestInstallationIdentity_RejectsNonGET(t *testing.T) {
	prev := hulaapp.GetConfig()
	config.SetConfigForTesting(&config.Config{NoiseStaticKey: "abc"})
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	r := httptest.NewRequest("POST", "/api/v1/installation/identity", nil)
	w := httptest.NewRecorder()
	installationIdentityHandler()(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", w.Code)
	}
}
