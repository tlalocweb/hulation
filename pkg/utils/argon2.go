package utils

import (
	"github.com/alexedwards/argon2id"
)

// Argon2GenerateFromSecretDefaults hashes a secret with argon2id default
// params. Used by TOTP recovery-code storage; password authentication
// itself goes through OPAQUE PAKE (see pkg/api/v1/auth/opaque.go).
func Argon2GenerateFromSecretDefaults(password string) (string, error) {
	return Argon2GenerateFromSecret(password, argon2id.DefaultParams)
}

// Argon2GenerateFromSecret hashes a secret with the supplied params.
func Argon2GenerateFromSecret(password string, p *argon2id.Params) (string, error) {
	return argon2id.CreateHash(password, p)
}

// Argon2CompareHashAndSecret compares a secret against a stored argon2id
// hash. Used by TOTP recovery-code verification.
func Argon2CompareHashAndSecret(secret, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(secret, hash)
}
