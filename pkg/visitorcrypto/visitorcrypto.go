// Package visitorcrypto is the server side of the visitor-chat app-layer
// encryption. The browser chat widget seals message content to the Hula
// installation's visitor-chat X25519 public key *before* the bytes hit HTTPS,
// so a TLS-inspecting middlebox (corporate MITM proxy, captive portal, etc.)
// sees only ciphertext — not the visitor's email, first message, or any chat
// content.
//
// This is defense-in-depth on top of TLS, not a replacement for it. It does
// NOT defend against an active MITM that tampers with the delivered widget
// JavaScript itself — see docs/visitor-chat-encryption.md for the trust
// caveats (SRI, signed manifests, server-fingerprint pinning).
//
// # Wire format (v1)
//
// Identical framing to pkg/push/preview, but AES-256-GCM instead of
// ChaCha20-Poly1305 — the browser does AES-GCM natively via WebCrypto with
// zero JS dependencies, whereas ChaCha would require shipping a polyfill into
// the visitor widget.
//
//	0       32                      end
//	+-------+----------------------+
//	| epk   | AES-256-GCM          |
//	|       | ciphertext + 16-byte |
//	|       | auth tag             |
//	+-------+----------------------+
//
//   - epk (32 bytes): ephemeral X25519 public key the browser generated for
//     this envelope, fresh per seal so the AEAD nonce can be all-zeros.
//   - ciphertext: AES-256-GCM output (≥ 16 bytes, the tag).
//
// On the wire (JSON) the envelope is base64url-no-pad.
//
// # KDF
//
//	shared = X25519(ephemeral_priv, recipient_pub)            (browser)
//	       = X25519(recipient_priv, epk)                      (server)
//	key32  = HKDF-SHA256(IKM=shared, salt=epk||recipient_pub,
//	                     info="hula-visitor-chat-v1")
//	nonce  = [0u8; 12]
//
// Binding both public keys into the HKDF salt makes the derived key unique to
// the (ephemeral, recipient) pair, so a captured envelope cannot be replayed
// against a different installation even if the attacker controls the
// ephemeral key.
package visitorcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// HKDFInfo is the domain-separation tag. Distinct from pkg/push/preview's
// "hula-push-preview-v1" so a push envelope can never be mis-decoded as a
// visitor-chat envelope (or vice versa) even if an attacker swaps the
// recipient key between the two subsystems. Bump the version suffix on any
// wire-format change.
const HKDFInfo = "hula-visitor-chat-v1"

const (
	keyLen   = 32 // X25519 public key length, also AES-256 key length.
	gcmTag   = 16 // AES-GCM tag length.
	gcmNonce = 12 // AES-GCM nonce length.
)

// ErrShortEnvelope is returned by Open when the input is smaller than the
// minimum envelope size (32-byte epk + 16-byte tag).
var ErrShortEnvelope = errors.New("visitorcrypto: envelope shorter than minimum")

// ErrBadRecipientKey is returned when a key argument is not 32 bytes.
var ErrBadRecipientKey = errors.New("visitorcrypto: key must be 32 bytes")

// Seal encrypts plaintext to recipientPub, returning the `epk || ciphertext`
// envelope. `rng` is optional (nil → crypto/rand). Primarily a test +
// reference helper; production sealing happens in the browser. The server only
// ever calls Open.
func Seal(rng io.Reader, recipientPub, plaintext []byte) ([]byte, error) {
	if rng == nil {
		rng = rand.Reader
	}
	ephPriv, err := ecdh.X25519().GenerateKey(rng)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: generate ephemeral: %w", err)
	}
	// GenerateKey clamps the rng bytes into stored form, so its Bytes() are not
	// the rng bytes verbatim. That's fine for random seals; SealWithEphemeral
	// is the deterministic path.
	return sealWithEphemeral(ephPriv.Bytes(), recipientPub, plaintext)
}

// SealWithEphemeral encrypts plaintext using a caller-supplied 32-byte
// ephemeral X25519 private key (used verbatim — X25519 clamps internally at
// ECDH time, matching the browser's WebCrypto behaviour when importing a raw
// key). This is the deterministic path the cross-language test vector pins:
// the same (ephPriv, recipientPub, plaintext) yields the same envelope in Go
// and in hula-visitor-crypto.js. Production code uses Seal (random ephemeral).
func SealWithEphemeral(ephPriv, recipientPub, plaintext []byte) ([]byte, error) {
	return sealWithEphemeral(ephPriv, recipientPub, plaintext)
}

func sealWithEphemeral(ephPriv, recipientPub, plaintext []byte) ([]byte, error) {
	if len(recipientPub) != keyLen {
		return nil, ErrBadRecipientKey
	}
	if len(ephPriv) != keyLen {
		return nil, ErrBadRecipientKey
	}
	curve := ecdh.X25519()
	recipient, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: recipient pub: %w", err)
	}
	eph, err := curve.NewPrivateKey(ephPriv)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: ephemeral priv: %w", err)
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: ecdh: %w", err)
	}
	ephPub := eph.PublicKey().Bytes()
	key, err := deriveKey(shared, ephPub, recipientPub)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonce) // zeros — safe, ephPub is fresh per call.
	ct := gcm.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, keyLen+len(ct))
	out = append(out, ephPub...)
	out = append(out, ct...)
	return out, nil
}

// Open decrypts an `epk || ciphertext` envelope with the recipient's X25519
// private key. This is the routine the chat-start handler + WS bridge call on
// every inbound visitor message.
func Open(recipientPriv, envelope []byte) ([]byte, error) {
	if len(recipientPriv) != keyLen {
		return nil, ErrBadRecipientKey
	}
	if len(envelope) < keyLen+gcmTag {
		return nil, ErrShortEnvelope
	}
	epk := envelope[:keyLen]
	ct := envelope[keyLen:]

	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: recipient priv: %w", err)
	}
	ephPub, err := curve.NewPublicKey(epk)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: ephemeral pub: %w", err)
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: ecdh: %w", err)
	}
	recipientPub := priv.PublicKey().Bytes()
	key, err := deriveKey(shared, epk, recipientPub)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonce)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: AEAD open: %w", err)
	}
	return pt, nil
}

// PublicKeyFromPrivate derives the X25519 public from a 32-byte private. Used
// by the installation-identity endpoint to publish the widget's recipient key.
func PublicKeyFromPrivate(priv []byte) ([]byte, error) {
	if len(priv) != keyLen {
		return nil, ErrBadRecipientKey
	}
	p, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: private key: %w", err)
	}
	return p.PublicKey().Bytes(), nil
}

func deriveKey(shared, epk, recipientPub []byte) ([]byte, error) {
	salt := make([]byte, 0, 2*keyLen)
	salt = append(salt, epk...)
	salt = append(salt, recipientPub...)
	r := hkdf.New(sha256.New, shared, salt, []byte(HKDFInfo))
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("visitorcrypto: hkdf: %w", err)
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("visitorcrypto: gcm: %w", err)
	}
	return gcm, nil
}
