package bolt

// Phase 4c.3 cookieless salt store. One 32-byte secret per
// virtual server. Generated lazily on first cookieless event for
// a server; never auto-rotated. Operators rotate via
// `hulactl rotate-cookieless-salt <server_id>`.
//
// Salts are HMAC keys for visitor-id derivation in cookieless mode
// — treated as cryptographic secrets:
//   * never logged
//   * 32 bytes from crypto/rand
//   * a missing key triggers lazy creation via crypto/rand on first
//     read

import (
	"crypto/rand"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// CookielessSaltLen mirrors visitorid.SaltLen. We don't import the
// visitorid package here to avoid a layer flip (visitorid is the
// pure-function layer and shouldn't depend on a storage package).
const CookielessSaltLen = 32

// ErrSaltMissing is returned when the salt isn't present and we
// don't want to auto-create. Most callers should use GetOrCreate
// instead.
var ErrSaltMissing = errors.New("cookieless salt: not yet generated")

// GetCookielessSalt returns the 32-byte salt for the given server.
// Returns ErrSaltMissing if not present. Callers in the visitor hot
// path should prefer GetOrCreateCookielessSalt.
func GetCookielessSalt(serverID string) ([]byte, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []byte
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketCookielessSalts)).Get([]byte(serverID))
		if v == nil {
			return ErrSaltMissing
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}

// PutCookielessSalt writes the salt for the given server. Used by
// the rotate CLI command.
func PutCookielessSalt(serverID string, salt []byte) error {
	if len(salt) != CookielessSaltLen {
		return fmt.Errorf("cookieless salt: must be %d bytes (got %d)", CookielessSaltLen, len(salt))
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketCookielessSalts)).Put([]byte(serverID), salt)
	})
}

// GetOrCreateCookielessSalt returns the salt, lazily generating one
// from crypto/rand if absent. Safe under concurrent access — the
// Bolt update is atomic; if two goroutines race the loser sees the
// winner's value on the next View.
func GetOrCreateCookielessSalt(serverID string) ([]byte, error) {
	salt, err := GetCookielessSalt(serverID)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, ErrSaltMissing) {
		return nil, err
	}
	fresh := make([]byte, CookielessSaltLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("cookieless salt: rand.Read: %w", err)
	}
	if err := PutCookielessSalt(serverID, fresh); err != nil {
		// Race: someone else may have just created one. Re-read.
		if salt2, err2 := GetCookielessSalt(serverID); err2 == nil {
			return salt2, nil
		}
		return nil, err
	}
	return fresh, nil
}

// RotateCookielessSalt replaces the salt with a fresh 32 random
// bytes. Yesterday's visitors become unrecognisable today.
func RotateCookielessSalt(serverID string) error {
	fresh := make([]byte, CookielessSaltLen)
	if _, err := rand.Read(fresh); err != nil {
		return fmt.Errorf("cookieless salt: rand.Read: %w", err)
	}
	return PutCookielessSalt(serverID, fresh)
}

// DeleteCookielessSalt removes the salt entry. Useful for fixture
// teardown. Callers shouldn't normally invoke this in production.
func DeleteCookielessSalt(serverID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketCookielessSalts)).Delete([]byte(serverID))
	})
}
