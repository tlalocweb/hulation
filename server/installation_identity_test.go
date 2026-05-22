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
	if body["code"] != "noise_unavailable" {
		t.Fatalf("code: got %v, want noise_unavailable", body["code"])
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
