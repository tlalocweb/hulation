// Package preview seals user-visible push notification text to a device's per-server
// X25519 public key. The mobile core (hula-mobile, common/src/push_preview.rs) opens
// the envelope on-device inside the iOS NSE or Android FirebaseMessagingService and
// renders the resulting plaintext as the notification body.
//
// Wire format (v1):
//
//	0       32                      end
//	+-------+----------------------+
//	| epk   | ChaCha20-Poly1305    |
//	|       | ciphertext + 16-byte |
//	|       | auth tag             |
//	+-------+----------------------+
//
// - epk: ephemeral X25519 public key, fresh per envelope (so the AEAD nonce can be
//   all-zeros without nonce-reuse risk).
// - ciphertext: AEAD output. Minimum length is 16 bytes (the Poly1305 tag alone).
//
// KDF:
//
//	shared = X25519(ephemeral_priv, recipient_pub)
//	key    = HKDF-SHA256(IKM=shared, salt=epk||recipient_pub, info="hula-push-preview-v1", L=32)
//	nonce  = [0u8; 12]
//
// Binding both pubkeys into the HKDF salt makes the derived key unique to this
// (sender_epk, recipient) pair — an attacker can't replay a captured envelope at a
// different recipient even if they could substitute the sender's ephemeral key.
//
// The matching open routine lives in the Rust mobile core; this file is the seal half
// and a self-contained reference for any other producer that needs to format an
// envelope.
package preview

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"crypto/sha256"
)

// HKDFInfo is the domain-separation tag woven into the key derivation. Bumping it (e.g.
// "hula-push-preview-v2") makes a v1 envelope decrypt to garbage against a v2 reader —
// the AEAD tag check catches it before any plaintext is exposed.
const HKDFInfo = "hula-push-preview-v1"

const (
	keyLen   = 32 // X25519 public key length, also ChaCha20-Poly1305 key length.
	tagLen   = 16 // Poly1305 tag length.
	nonceLen = 12 // ChaCha20-Poly1305 nonce length.
)

// ErrShortEnvelope is returned by Open when the input is smaller than the minimum
// envelope size (32-byte epk + 16-byte tag).
var ErrShortEnvelope = errors.New("preview: envelope shorter than minimum")

// ErrBadRecipientKey is returned by Seal/Open when recipient_pub is not 32 bytes.
var ErrBadRecipientKey = errors.New("preview: recipient_pub must be 32 bytes")

// Seal returns a sealed envelope for `plaintext` addressed to `recipientPub`. Generates
// an ephemeral X25519 keypair on every call, so two seals of the same plaintext under
// the same recipient produce distinct envelopes.
//
// The caller passes `rng = nil` to use crypto/rand; tests pass a deterministic reader
// to produce stable vectors.
func Seal(rng io.Reader, recipientPub []byte, plaintext []byte) ([]byte, error) {
	if len(recipientPub) != keyLen {
		return nil, ErrBadRecipientKey
	}
	if rng == nil {
		rng = rand.Reader
	}

	ephPriv := make([]byte, keyLen)
	if _, err := io.ReadFull(rng, ephPriv); err != nil {
		return nil, fmt.Errorf("preview: read ephemeral private: %w", err)
	}
	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("preview: derive ephemeral public: %w", err)
	}
	shared, err := curve25519.X25519(ephPriv, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("preview: x25519: %w", err)
	}
	key, err := deriveKey(shared, ephPub, recipientPub)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("preview: chacha20poly1305: %w", err)
	}
	nonce := make([]byte, nonceLen) // all zeros — safe because ephPub is fresh per call.
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, keyLen+len(ciphertext))
	out = append(out, ephPub...)
	out = append(out, ciphertext...)
	return out, nil
}

// Open is the canonical decrypt routine. The mobile-side equivalent lives in
// hula-mobile's `push_preview::unseal_envelope`; this exists so server-side code can
// verify a self-issued envelope without dragging in the mobile crate.
func Open(recipientPriv []byte, envelope []byte) ([]byte, error) {
	if len(recipientPriv) != keyLen {
		return nil, ErrBadRecipientKey
	}
	if len(envelope) < keyLen+tagLen {
		return nil, ErrShortEnvelope
	}
	epk := envelope[:keyLen]
	ciphertext := envelope[keyLen:]

	recipientPub, err := curve25519.X25519(recipientPriv, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("preview: derive recipient public: %w", err)
	}
	shared, err := curve25519.X25519(recipientPriv, epk)
	if err != nil {
		return nil, fmt.Errorf("preview: x25519: %w", err)
	}
	key, err := deriveKey(shared, epk, recipientPub)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("preview: chacha20poly1305: %w", err)
	}
	nonce := make([]byte, nonceLen)
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("preview: AEAD open: %w", err)
	}
	return plaintext, nil
}

func deriveKey(shared, epk, recipientPub []byte) ([]byte, error) {
	salt := make([]byte, 0, 2*keyLen)
	salt = append(salt, epk...)
	salt = append(salt, recipientPub...)
	reader := hkdf.New(sha256.New, shared, salt, []byte(HKDFInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("preview: hkdf expand: %w", err)
	}
	return key, nil
}
