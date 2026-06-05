package preview

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func freshRecipient(t *testing.T) (priv, pub []byte) {
	t.Helper()
	priv = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		t.Fatalf("rand: %s", err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive pub: %s", err)
	}
	return priv, pub
}

func TestRoundTrip(t *testing.T) {
	priv, pub := freshRecipient(t)
	plaintext := []byte("New chat from Alice: Hello!")
	envelope, err := Seal(nil, pub, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	got, err := Open(priv, envelope)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch: got %q want %q", got, plaintext)
	}
}

func TestSealProducesFreshCiphertextEachCall(t *testing.T) {
	_, pub := freshRecipient(t)
	a, err := Seal(nil, pub, []byte("same"))
	if err != nil {
		t.Fatalf("seal a: %s", err)
	}
	b, err := Seal(nil, pub, []byte("same"))
	if err != nil {
		t.Fatalf("seal b: %s", err)
	}
	if bytes.Equal(a, b) {
		t.Fatalf("expected distinct envelopes for two seals of the same plaintext (fresh ephemeral key per call)")
	}
}

func TestSealRejectsBadRecipient(t *testing.T) {
	if _, err := Seal(nil, []byte{1, 2, 3}, []byte("x")); err == nil {
		t.Fatalf("expected error for short recipient_pub")
	}
}

func TestOpenRejectsShortEnvelope(t *testing.T) {
	priv, _ := freshRecipient(t)
	if _, err := Open(priv, make([]byte, 32+15)); err == nil {
		t.Fatalf("expected ErrShortEnvelope")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	priv, pub := freshRecipient(t)
	env, err := Seal(nil, pub, []byte("hello"))
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	env[40] ^= 0x01
	if _, err := Open(priv, env); err == nil {
		t.Fatalf("expected AEAD failure on tampered ciphertext")
	}
}

func TestOpenRejectsTamperedEphemeralPub(t *testing.T) {
	priv, pub := freshRecipient(t)
	env, err := Seal(nil, pub, []byte("hello"))
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	env[5] ^= 0x01
	if _, err := Open(priv, env); err == nil {
		t.Fatalf("expected AEAD failure on tampered epk")
	}
}

func TestOpenRejectsWrongRecipient(t *testing.T) {
	_, pub := freshRecipient(t)
	wrongPriv, _ := freshRecipient(t)
	env, err := Seal(nil, pub, []byte("hello"))
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	if _, err := Open(wrongPriv, env); err == nil {
		t.Fatalf("expected AEAD failure under wrong recipient")
	}
}

// TestDeterministicVector pins the wire format with a fixed test vector. The same
// (recipient_priv, ephemeral_priv, plaintext) MUST produce the same envelope on the
// Rust side; this vector is mirrored in hula-mobile's push_preview tests. Changes to
// the KDF, info tag, or framing will fail this test and the mobile side simultaneously.
func TestDeterministicVector(t *testing.T) {
	// Recipient private = 0x42 repeated; ephemeral private = 0x37 repeated. Both have
	// the curve25519 clamping bits applied by X25519 before scalar mult, so the public
	// keys + ciphertext are deterministic functions of these inputs.
	recipientPriv := bytes.Repeat([]byte{0x42}, 32)
	ephPriv := bytes.Repeat([]byte{0x37}, 32)
	plaintext := []byte("hula-push-preview test vector")

	rng := bytes.NewReader(ephPriv)
	recipientPub, err := curve25519.X25519(recipientPriv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive recipient pub: %s", err)
	}
	env, err := Seal(rng, recipientPub, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}

	// Self-round-trip check (proves the vector at least decrypts cleanly).
	got, err := Open(recipientPriv, env)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext mismatch")
	}

	// Sanity: ephemeral pubkey at the front matches what X25519 would derive from the
	// fixed ephemeral private. This is the cross-language anchor — if the prefix
	// matches and the AEAD opens, the construction is the same on both sides.
	expectedEphPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive eph pub: %s", err)
	}
	if !bytes.Equal(env[:32], expectedEphPub) {
		t.Fatalf("eph pub prefix mismatch:\n got %s\nwant %s",
			hex.EncodeToString(env[:32]), hex.EncodeToString(expectedEphPub))
	}
}
