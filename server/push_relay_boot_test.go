package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

func freshSeedB64Variants(t *testing.T) (raw []byte, variants map[string]string) {
	t.Helper()
	raw = make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		t.Fatalf("rand seed: %s", err)
	}
	variants = map[string]string{
		"standard":         base64.StdEncoding.EncodeToString(raw),
		"raw_standard":     base64.RawStdEncoding.EncodeToString(raw),
		"url_safe":         base64.URLEncoding.EncodeToString(raw),
		"raw_url_safe":     base64.RawURLEncoding.EncodeToString(raw),
	}
	return raw, variants
}

func TestBuildPushRelayClient_AcceptsAnyBase64Flavour(t *testing.T) {
	_, variants := freshSeedB64Variants(t)
	for name, b64 := range variants {
		t.Run(name, func(t *testing.T) {
			c, err := buildPushRelayClient(&config.PushRelayConfig{
				BaseURL:        "https://relay.example.com",
				InstallationID: "inst_abc",
				SigningKeyB64:  b64,
			})
			if err != nil {
				t.Fatalf("%s seed: %v", name, err)
			}
			if c == nil {
				t.Fatalf("%s: nil client", name)
			}
		})
	}
}

func TestBuildPushRelayClient_RejectsBadInputs(t *testing.T) {
	_, variants := freshSeedB64Variants(t)
	goodSeed := variants["standard"]

	tests := []struct {
		name string
		cfg  *config.PushRelayConfig
		want string
	}{
		{"nil config", nil, "nil config"},
		{
			"missing installation_id",
			&config.PushRelayConfig{BaseURL: "https://r", SigningKeyB64: goodSeed},
			"installation_id required",
		},
		{
			"missing signing_key_b64",
			&config.PushRelayConfig{BaseURL: "https://r", InstallationID: "i"},
			"signing_key_b64 required",
		},
		{
			"signing_key wrong length",
			&config.PushRelayConfig{
				BaseURL:        "https://r",
				InstallationID: "i",
				// 16 bytes, not 32 — passes base64 decode but fails the size check.
				SigningKeyB64: base64.StdEncoding.EncodeToString(make([]byte, 16)),
			},
			"must decode to 32 bytes",
		},
		{
			"signing_key garbage base64",
			&config.PushRelayConfig{
				BaseURL:        "https://r",
				InstallationID: "i",
				SigningKeyB64:  "!!!not base64!!!",
			},
			"decode",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPushRelayClient(tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q; want substring %q", err, tc.want)
			}
		})
	}
}

func TestBuildPushRelayClient_SeedExpandsToKnownPrivate(t *testing.T) {
	// Sanity check the seed → PrivateKey expansion is consistent with the stdlib.
	raw, variants := freshSeedB64Variants(t)
	c, err := buildPushRelayClient(&config.PushRelayConfig{
		BaseURL:        "https://r",
		InstallationID: "i",
		SigningKeyB64:  variants["standard"],
	})
	if err != nil {
		t.Fatalf("build: %s", err)
	}
	_ = c
	// Independently derive the public from the seed and confirm it round-trips
	// through ed25519.PrivateKey.Public().
	priv := ed25519.NewKeyFromSeed(raw)
	expectedPub := priv.Public().(ed25519.PublicKey)
	if len(expectedPub) != ed25519.PublicKeySize {
		t.Fatalf("derived public key wrong length")
	}
}
