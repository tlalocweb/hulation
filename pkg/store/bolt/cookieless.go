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
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// CookielessSaltLen mirrors visitorid.SaltLen. We don't import the
// visitorid package here to avoid a layer flip (visitorid is the
// pure-function layer and shouldn't depend on a storage package).
const CookielessSaltLen = 32

// ErrSaltMissing is returned when the salt isn't present and we
// don't want to auto-create. Most callers should use GetOrCreate
// instead.
var ErrSaltMissing = errors.New("cookieless salt: not yet generated")

func cookielessSaltKey(serverID string) string {
	return "cookieless_salts/" + serverID
}

// GetCookielessSalt returns the 32-byte salt for the given server.
// Returns ErrSaltMissing if not present.
func GetCookielessSalt(ctx context.Context, s storage.Storage, serverID string) ([]byte, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, cookielessSaltKey(serverID))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrSaltMissing
	}
	return v, err
}

// PutCookielessSalt writes the salt for the given server.
func PutCookielessSalt(ctx context.Context, s storage.Storage, serverID string, salt []byte) error {
	if s == nil {
		return ErrNotOpen
	}
	if len(salt) != CookielessSaltLen {
		return fmt.Errorf("cookieless salt: must be %d bytes (got %d)", CookielessSaltLen, len(salt))
	}
	return s.Put(ctx, cookielessSaltKey(serverID), salt)
}

// GetOrCreateCookielessSalt returns the salt, lazily generating one
// from crypto/rand if absent. Race-safe: under concurrent first-read
// pressure, the storage layer's CompareAndCreate ensures only one
// salt is persisted; losers see the winner's value on retry.
func GetOrCreateCookielessSalt(ctx context.Context, s storage.Storage, serverID string) ([]byte, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	salt, err := GetCookielessSalt(ctx, s, serverID)
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
	err = s.CompareAndCreate(ctx, cookielessSaltKey(serverID), fresh)
	if err == nil {
		return fresh, nil
	}
	if errors.Is(err, storage.ErrCASFailed) {
		// Someone won the race. Re-read.
		if salt2, err2 := GetCookielessSalt(ctx, s, serverID); err2 == nil {
			return salt2, nil
		}
	}
	return nil, err
}

// RotateCookielessSalt replaces the salt with a fresh 32 random
// bytes. Yesterday's visitors become unrecognisable today.
func RotateCookielessSalt(ctx context.Context, s storage.Storage, serverID string) error {
	if s == nil {
		return ErrNotOpen
	}
	fresh := make([]byte, CookielessSaltLen)
	if _, err := rand.Read(fresh); err != nil {
		return fmt.Errorf("cookieless salt: rand.Read: %w", err)
	}
	return PutCookielessSalt(ctx, s, serverID, fresh)
}

// DeleteCookielessSalt removes the salt entry. Useful for fixture
// teardown. Callers shouldn't normally invoke this in production.
func DeleteCookielessSalt(ctx context.Context, s storage.Storage, serverID string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, cookielessSaltKey(serverID))
}
