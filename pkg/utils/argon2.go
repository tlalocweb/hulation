package utils

import (
	"crypto/sha256"
	"fmt"

	"github.com/alexedwards/argon2id"
)

// Argon2GenerateFromSecretDefaults hashes a secret with argon2id default params.
func Argon2GenerateFromSecretDefaults(password string) (string, error) {
	return Argon2GenerateFromSecret(password, argon2id.DefaultParams)
}

// Argon2GenerateFromSecret hashes a secret with the supplied params.
func Argon2GenerateFromSecret(password string, p *argon2id.Params) (string, error) {
	return argon2id.CreateHash(password, p)
}

// Argon2CompareHashAndSecret compares a network-hashed secret against a stored hash.
func Argon2CompareHashAndSecret(secret, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(secret, hash)
}

// GenerateNetworkPassHash produces the sha256 hash that auth APIs expect to
// receive — the actual password never crosses the wire.
func GenerateNetworkPassHash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return fmt.Sprintf("%x", sum)
}

// GenerateHashFromPlaintextPass produces both the sha256 network hash and the
// argon2id stored hash from a plaintext password.
func GenerateHashFromPlaintextPass(password string) (argonhash, stringsum string, err error) {
	stringsum = GenerateNetworkPassHash(password)
	argonhash, err = Argon2GenerateFromSecretDefaults(stringsum)
	return
}
