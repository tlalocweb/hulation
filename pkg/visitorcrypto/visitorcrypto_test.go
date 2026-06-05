package visitorcrypto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"io"
	"testing"
)

func freshRecipient(t *testing.T) (priv, pub []byte) {
	t.Helper()
	p, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %s", err)
	}
	return p.Bytes(), p.PublicKey().Bytes()
}

func TestRoundTrip(t *testing.T) {
	priv, pub := freshRecipient(t)
	plaintext := []byte(`{"email":"visitor@example.com","first_message":"do you ship to MX?"}`)
	env, err := Seal(nil, pub, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	got, err := Open(priv, env)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("mismatch: got %q", got)
	}
}

func TestSealFreshPerCall(t *testing.T) {
	_, pub := freshRecipient(t)
	a, _ := Seal(nil, pub, []byte("same"))
	b, _ := Seal(nil, pub, []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatalf("two seals of same plaintext produced identical envelopes")
	}
}

func TestSealRejectsBadRecipient(t *testing.T) {
	if _, err := Seal(nil, []byte{1, 2, 3}, []byte("x")); err == nil {
		t.Fatalf("expected error for short recipient_pub")
	}
}

func TestOpenRejectsShort(t *testing.T) {
	priv, _ := freshRecipient(t)
	if _, err := Open(priv, make([]byte, keyLen+gcmTag-1)); err == nil {
		t.Fatalf("expected ErrShortEnvelope")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	priv, pub := freshRecipient(t)
	env, _ := Seal(nil, pub, []byte("hello"))
	env[keyLen+2] ^= 0x01
	if _, err := Open(priv, env); err == nil {
		t.Fatalf("expected AEAD failure on tampered ciphertext")
	}
}

func TestOpenRejectsTamperedEphemeralPub(t *testing.T) {
	priv, pub := freshRecipient(t)
	env, _ := Seal(nil, pub, []byte("hello"))
	env[3] ^= 0x01
	if _, err := Open(priv, env); err == nil {
		t.Fatalf("expected failure on tampered epk")
	}
}

func TestOpenRejectsWrongRecipient(t *testing.T) {
	_, pub := freshRecipient(t)
	wrongPriv, _ := freshRecipient(t)
	env, _ := Seal(nil, pub, []byte("hello"))
	if _, err := Open(wrongPriv, env); err == nil {
		t.Fatalf("expected AEAD failure under wrong recipient")
	}
}

func TestPublicKeyFromPrivate(t *testing.T) {
	priv, pub := freshRecipient(t)
	got, err := PublicKeyFromPrivate(priv)
	if err != nil {
		t.Fatalf("derive: %s", err)
	}
	if !bytes.Equal(got, pub) {
		t.Fatalf("derived pub mismatch")
	}
	if _, err := PublicKeyFromPrivate([]byte{1}); err == nil {
		t.Fatalf("expected error for short priv")
	}
}

// TestDeterministicVector pins the wire format. The browser-side
// hula-visitor-crypto.js carries the same vector; if either side changes the
// KDF, info tag, or framing, both fail together. We feed a fixed ephemeral
// private via a deterministic reader so the envelope is reproducible.
//
// recipient_priv = 0x11 × 32, ephemeral_priv = 0x22 × 32,
// plaintext = "hula-visitor-chat vector".
func TestDeterministicVector(t *testing.T) {
	recipientPriv := bytes.Repeat([]byte{0x11}, 32)
	ephPriv := bytes.Repeat([]byte{0x22}, 32)
	plaintext := []byte("hula-visitor-chat vector")

	curve := ecdh.X25519()
	rp, err := curve.NewPrivateKey(recipientPriv)
	if err != nil {
		t.Fatalf("recipient priv: %s", err)
	}
	recipientPub := rp.PublicKey().Bytes()

	// Deterministic ephemeral: supply the fixed private verbatim.
	env, err := SealWithEphemeral(ephPriv, recipientPub, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	// Self round-trips.
	got, err := Open(recipientPriv, env)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch")
	}
	// The epk prefix is the public derived from the fixed ephemeral private —
	// the cross-language anchor.
	ep, err := curve.NewPrivateKey(ephPriv)
	if err != nil {
		t.Fatalf("eph priv: %s", err)
	}
	wantEpk := ep.PublicKey().Bytes()
	if !bytes.Equal(env[:keyLen], wantEpk) {
		t.Fatalf("epk prefix mismatch:\n got %s\nwant %s",
			hex.EncodeToString(env[:keyLen]), hex.EncodeToString(wantEpk))
	}

	// Pinned cross-language fixture. hula-visitor-crypto.js carries the same
	// (recipient_priv, ephemeral_priv, plaintext) → envelope vector; a change
	// to the KDF, info tag, or AEAD here flips this hash and the JS test
	// simultaneously, catching wire drift between server and browser.
	const wantEnvHex = "0faa684ed28867b97f4a6a2dee5df8ce974e76b7018e3f22a1c4cf2678570f20" +
		"0df6b2c5367ec9bc233992f772a3d2277c1d59d08ffad05333edb99a3734c989" +
		"3e05b79b384b7703"
	if got := hex.EncodeToString(env); got != wantEnvHex {
		t.Fatalf("envelope vector drift:\n got  %s\nwant %s", got, wantEnvHex)
	}
}

// TestSealUsesAllNonceZeros is a guard: the construction relies on a fresh
// ephemeral per envelope to make the all-zeros nonce safe. If someone later
// "optimizes" Seal to cache the ephemeral, this catches the resulting
// catastrophic nonce reuse by detecting identical ciphertext prefixes for
// identical plaintext.
func TestSealNonceReuseSafety(t *testing.T) {
	_, pub := freshRecipient(t)
	const n = 8
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		env, err := Seal(nil, pub, []byte("identical plaintext"))
		if err != nil {
			t.Fatalf("seal %d: %s", i, err)
		}
		// Whole envelope (epk + ct) must be unique each time.
		k := string(env)
		if seen[k] {
			t.Fatalf("duplicate envelope at iteration %d — ephemeral reuse!", i)
		}
		seen[k] = true
	}
}

var _ = io.Discard
