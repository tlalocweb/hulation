package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

// EncryptTOTPSecret encrypts a TOTP secret using AES-256-GCM.
// key must be 32 bytes (256 bits). Returns base64-encoded ciphertext.
func EncryptTOTPSecret(plaintext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptTOTPSecret decrypts an AES-256-GCM encrypted TOTP secret.
// key must be 32 bytes (256 bits). Input is base64-encoded ciphertext.
func DecryptTOTPSecret(ciphertext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, encryptedData := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, encryptedData, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

// GenerateRecoveryCodes generates a set of recovery codes for TOTP backup.
// Each code is an 8-character alphanumeric string.
func GenerateRecoveryCodes(count int) ([]string, error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	codes := make([]string, count)

	for i := 0; i < count; i++ {
		b := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return nil, fmt.Errorf("failed to generate random bytes: %w", err)
		}
		var code strings.Builder
		for _, byteVal := range b {
			code.WriteByte(charset[int(byteVal)%len(charset)])
		}
		codes[i] = code.String()
	}

	return codes, nil
}

// HashRecoveryCodes hashes each recovery code using Argon2.
func HashRecoveryCodes(codes []string) ([]string, error) {
	hashed := make([]string, len(codes))
	for i, code := range codes {
		hash, err := Argon2GenerateFromSecretDefaults(code)
		if err != nil {
			return nil, fmt.Errorf("failed to hash recovery code: %w", err)
		}
		hashed[i] = hash
	}
	return hashed, nil
}

// GetTOTPEncryptionKey decodes the base64 TOTP encryption key from config.
// Returns the raw 32-byte key suitable for AES-256-GCM.
func GetTOTPEncryptionKey(base64Key string) ([]byte, error) {
	if base64Key == "" {
		return nil, fmt.Errorf("TOTP encryption key is not configured")
	}
	key, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("failed to decode TOTP encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("TOTP encryption key must be 32 bytes, got %d", len(key))
	}
	return key, nil
}

// GenerateTOTPEncryptionKey generates a new random 32-byte key and returns it
// as a base64url-encoded string (no padding).
func GenerateTOTPEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("failed to generate key: %w", err)
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(key), nil
}
