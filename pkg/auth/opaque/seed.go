package opaque

// OPRF seed + AKE keypair lifecycle.
//
// The seed is 64 bytes (Ristretto255-SHA512 default) and represents
// the server's identity — it must be:
//   * stable across restarts (existing records validate against it)
//   * distinct per deploy (staging + production should not share)
//   * loaded from operator-controlled config or env, NOT derived
//     from a default
//
// Same workflow as TOTP encryption key: env var wins, config falls
// back, generate-and-warn-loud on first boot if neither is present.

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/bytemare/ecc"
	"github.com/bytemare/opaque"

	"github.com/tlalocweb/hulation/log"
)

var seedLog = log.GetTaggedLogger("opaque-seed", "OPAQUE OPRF seed loader")

// EnvOPRFSeed is the env-var hula consults first when constructing
// an OPAQUE server. Operator workflow mirrors HULA_TOTP_ENCRYPTION_KEY.
const EnvOPRFSeed = "HULA_OPAQUE_OPRF_SEED"

// EnvAKESecret holds the AKE long-lived private-key seed used to
// derive a deterministic keypair. 64 bytes (matches OPRF seed).
const EnvAKESecret = "HULA_OPAQUE_AKE_SECRET"

// LoadOrGenerate returns key material for an OPAQUE Server.
// Resolution order for both seeds:
//
//  1. Env var (operator-pinned).
//  2. Provided base64-encoded value (typically from yaml config).
//  3. Generate fresh + log a one-time WRN with the values so the
//     operator can pin them.
//
// Pass empty strings for cfgSeed/cfgAKE when the operator hasn't
// pinned them in yaml (relies on env or generate-and-log path).
//
// Generated values are random per process start; operators MUST
// pin them in env or config for the server to be useful — without
// pinning, every restart invalidates every existing OPAQUE record.
func LoadOrGenerate(cfgSeed, cfgAKE string) (seed []byte, akePriv *ecc.Scalar, akePubBytes []byte, err error) {
	cfg := opaque.DefaultConfiguration()

	// --- OPRF seed ---
	seed, source, err := decodeSeedFromEnvOrConfig(EnvOPRFSeed, cfgSeed, 64)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opaque: oprf seed: %w", err)
	}
	if seed == nil {
		seed = cfg.GenerateOPRFSeed()
		source = "generated"
		seedLog.Warnf("OPAQUE oprf seed not configured — generated a fresh one. " +
			"PIN IT to %s or config.opaque.oprf_seed before relying on registrations:", EnvOPRFSeed)
		seedLog.Warnf("  %s=%s", EnvOPRFSeed, base64.RawURLEncoding.EncodeToString(seed))
	}
	seedLog.Infof("oprf-seed source=%s len=%d", source, len(seed))

	// --- AKE keypair ---
	akeBytes, akeSource, err := decodeSeedFromEnvOrConfig(EnvAKESecret, cfgAKE, 0) // var-length; see below
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opaque: ake secret: %w", err)
	}
	if akeBytes != nil {
		akePriv, err = decodeAKEPrivate(akeBytes)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("opaque: decode pinned ake secret: %w", err)
		}
		pubElement := cfg.OPRF.Group().Base().Multiply(akePriv)
		akePubBytes = pubElement.Encode()
	} else {
		akePriv, akePubBytes, err = generateAKEKeyPair(cfg)
		if err != nil {
			return nil, nil, nil, err
		}
		akeSource = "generated"
		seedLog.Warnf("OPAQUE ake secret not configured — generated a fresh keypair. " +
			"PIN IT to %s or config.opaque.ake_secret to keep records valid across restarts:", EnvAKESecret)
		seedLog.Warnf("  %s=%s", EnvAKESecret, base64.RawURLEncoding.EncodeToString(akePriv.Encode()))
	}
	seedLog.Infof("ake-keypair source=%s pub-len=%d", akeSource, len(akePubBytes))
	return seed, akePriv, akePubBytes, nil
}

// decodeSeedFromEnvOrConfig returns the decoded bytes + a "source"
// tag for logging. Returns (nil, "", nil) when neither env nor
// config has a value — caller decides whether to generate.
//
// requiredLen=0 disables the length check (the AKE secret is just
// a scalar — the bytemare ecc package handles decoding).
func decodeSeedFromEnvOrConfig(envKey, cfgValue string, requiredLen int) ([]byte, string, error) {
	if v := os.Getenv(envKey); v != "" {
		raw, err := tryDecode(v)
		if err != nil {
			return nil, "", fmt.Errorf("decode env %s: %w", envKey, err)
		}
		if requiredLen > 0 && len(raw) != requiredLen {
			return nil, "", fmt.Errorf("env %s wrong length: got %d want %d",
				envKey, len(raw), requiredLen)
		}
		return raw, "env", nil
	}
	if cfgValue != "" {
		raw, err := tryDecode(cfgValue)
		if err != nil {
			return nil, "", fmt.Errorf("decode config: %w", err)
		}
		if requiredLen > 0 && len(raw) != requiredLen {
			return nil, "", fmt.Errorf("config wrong length: got %d want %d",
				len(raw), requiredLen)
		}
		return raw, "config", nil
	}
	return nil, "", nil
}

// tryDecode handles both std-base64 and base64url, with or without
// padding. Operators may paste either form.
func tryDecode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("not valid base64 (any of url/std, padded/raw)")
}

// generateAKEKeyPair returns a random scalar + its public element
// bytes. Used when the operator hasn't pinned the secret.
func generateAKEKeyPair(cfg *opaque.Configuration) (*ecc.Scalar, []byte, error) {
	priv, pub := cfg.KeyGen()
	if priv == nil || pub == nil {
		return nil, nil, fmt.Errorf("opaque: KeyGen returned nil")
	}
	return priv, pub.Encode(), nil
}

// decodeAKEPrivate parses a base64-decoded scalar back into the
// bytemare scalar type. Length validation is delegated to the
// scalar's Decode method.
func decodeAKEPrivate(raw []byte) (*ecc.Scalar, error) {
	cfg := opaque.DefaultConfiguration()
	scalar := cfg.OPRF.Group().NewScalar()
	if err := scalar.Decode(raw); err != nil {
		return nil, fmt.Errorf("decode scalar: %w", err)
	}
	return scalar, nil
}

// GenerateSeedB64 returns a fresh OPAQUE OPRF seed encoded as
// raw base64url — the format `hulactl opaque-seed` outputs and
// operators paste into config / env.
func GenerateSeedB64() string {
	cfg := opaque.DefaultConfiguration()
	return base64.RawURLEncoding.EncodeToString(cfg.GenerateOPRFSeed())
}

// GenerateAKESecretB64 returns a fresh AKE private-scalar encoded
// as raw base64url. Companion to GenerateSeedB64.
func GenerateAKESecretB64() (string, error) {
	cfg := opaque.DefaultConfiguration()
	priv, _ := cfg.KeyGen()
	if priv == nil {
		return "", fmt.Errorf("opaque: KeyGen returned nil")
	}
	return base64.RawURLEncoding.EncodeToString(priv.Encode()), nil
}

// RandomBytes is a thin wrapper around crypto/rand for use in
// session-id generation. Exported so tests can stub.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("opaque: rand: %w", err))
	}
	return b
}
