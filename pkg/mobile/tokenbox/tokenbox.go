// Package tokenbox seals APNs / FCM push tokens at rest with
// AES-256-GCM. The master key is the same 32-byte value used by
// hula's TOTP subsystem — reuses a single operator-managed secret
// instead of introducing a second key rotation surface.
//
// Format: the sealed blob is `[nonce][ciphertext||tag]` as a raw
// byte slice. No base64 — the caller persists bytes into a
// `[]byte` bolt column, so encoding overhead is pure loss.
//
// Tamper detection comes free with GCM's authentication tag: a
// bit-flipped blob returns ErrTampered instead of a corrupted
// plaintext.

package tokenbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// ErrTampered is returned when the GCM authentication tag fails to
// verify on Open. Usually indicates corruption or key mismatch; in
// either case, the ciphertext cannot be trusted.
var ErrTampered = errors.New("tokenbox: ciphertext failed authentication")

// Seal encrypts plaintext with key. Returns `[nonce][ciphertext||tag]`.
// key must be exactly 32 bytes.
func Seal(plaintext string, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("tokenbox: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("tokenbox: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("tokenbox: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("tokenbox: nonce: %w", err)
	}
	// Seal returns `nonce||ciphertext||tag` when nonce is passed as
	// the dst prefix. That single contiguous slice is what callers
	// persist.
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open reverses Seal. Returns ErrTampered when the GCM tag fails.
func Open(sealed []byte, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("tokenbox: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("tokenbox: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("tokenbox: gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return "", fmt.Errorf("tokenbox: sealed blob too short (%d < %d)", len(sealed), nonceSize)
	}
	nonce, body := sealed[:nonceSize], sealed[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", ErrTampered
	}
	return string(plaintext), nil
}
