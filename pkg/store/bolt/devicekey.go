package bolt

// Persistent DeviceKeyStore. Implements `authware.DeviceKeyStore` + writer
// methods (Put / Delete) against a `storage.Storage`. Mirrors the shape of
// authware.InMemoryDeviceKeyStore so the QR pair handler can swap stores
// with a single type assertion (or, better, a small interface — see
// DeviceKeyStoreWriter below).
//
// Keyspace: `device_keys/<device_id>` → JSON-encoded DeviceKey envelope.
// PublicKey is stored base64-encoded so the JSON shape is portable across a
// future schema migration (raw bytes inside JSON would require base64 anyway
// because JSON has no byte-array type).

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

const deviceKeyPrefix = "device_keys/"

func deviceKeyStoreKey(deviceID string) string { return deviceKeyPrefix + deviceID }

// storedDeviceKey is the on-disk JSON shape. We don't reuse
// authware.DeviceKey directly because PublicKey is a []byte, which JSON
// marshals as base64 by default but with the standard alphabet — being
// explicit about the encoding inside this struct keeps the on-disk shape
// stable across stdlib versions.
type storedDeviceKey struct {
	DeviceID     string    `json:"device_id"`
	UserID       string    `json:"user_id"`
	ServerID     string    `json:"server_id,omitempty"`
	PublicKeyB64 string    `json:"public_key_b64"`
	CreatedAt    time.Time `json:"created_at"`
}

func toStored(k authware.DeviceKey) storedDeviceKey {
	return storedDeviceKey{
		DeviceID:     k.DeviceID,
		UserID:       k.UserID,
		ServerID:     k.ServerID,
		PublicKeyB64: base64.StdEncoding.EncodeToString(k.PublicKey),
		CreatedAt:    k.CreatedAt,
	}
}

func fromStored(s storedDeviceKey) (authware.DeviceKey, error) {
	pub, err := base64.StdEncoding.DecodeString(s.PublicKeyB64)
	if err != nil {
		return authware.DeviceKey{}, fmt.Errorf("device_key: decode public_key_b64: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return authware.DeviceKey{}, fmt.Errorf(
			"device_key: public_key length: got %d want %d",
			len(pub), ed25519.PublicKeySize,
		)
	}
	return authware.DeviceKey{
		DeviceID:  s.DeviceID,
		UserID:    s.UserID,
		ServerID:  s.ServerID,
		PublicKey: pub,
		CreatedAt: s.CreatedAt,
	}, nil
}

// DeviceKeyStore wraps a `storage.Storage` and satisfies the read-only
// `authware.DeviceKeyStore` interface + writer helpers. Construct once at
// boot and pass to both the auth interceptors AND the QR pair handler.
type DeviceKeyStore struct {
	store storage.Storage
}

// NewDeviceKeyStore returns a store backed by `s`. nil yields a degenerate
// store whose LookupDeviceKey always returns "not found" (matches the
// authware.NoopDeviceKeyStore contract).
func NewDeviceKeyStore(s storage.Storage) *DeviceKeyStore {
	return &DeviceKeyStore{store: s}
}

// LookupDeviceKey implements authware.DeviceKeyStore.
func (s *DeviceKeyStore) LookupDeviceKey(ctx context.Context, deviceID string) (authware.DeviceKey, bool) {
	if s == nil || s.store == nil {
		return authware.DeviceKey{}, false
	}
	raw, err := s.store.Get(ctx, deviceKeyStoreKey(deviceID))
	if err != nil {
		// Includes ErrNotFound and any transient transport error — treat all
		// as "no claim" so a downstream "not authenticated" surfaces.
		return authware.DeviceKey{}, false
	}
	var stored storedDeviceKey
	if uerr := json.Unmarshal(raw, &stored); uerr != nil {
		return authware.DeviceKey{}, false
	}
	dk, err := fromStored(stored)
	if err != nil {
		return authware.DeviceKey{}, false
	}
	return dk, true
}

// PutDeviceKey satisfies authware.DeviceKeyWriter. Writes the device key,
// overwriting any prior entry for the same device_id. Used by the QR pair
// redeem handler.
func (s *DeviceKeyStore) PutDeviceKey(key authware.DeviceKey) error {
	return s.Put(key)
}

// Put writes the device key, overwriting any prior entry for the same
// device_id.
func (s *DeviceKeyStore) Put(key authware.DeviceKey) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("device_key: store not open")
	}
	if key.DeviceID == "" {
		return fmt.Errorf("device_key: device_id required")
	}
	if len(key.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("device_key: public_key must be %d bytes", ed25519.PublicKeySize)
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	bytes, err := json.Marshal(toStored(key))
	if err != nil {
		return fmt.Errorf("device_key: marshal: %w", err)
	}
	return s.store.Put(context.Background(), deviceKeyStoreKey(key.DeviceID), bytes)
}

// Delete removes the entry. Idempotent — no error on missing.
func (s *DeviceKeyStore) Delete(deviceID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("device_key: store not open")
	}
	return s.store.Delete(context.Background(), deviceKeyStoreKey(deviceID))
}

// ListForUser returns every device key persisted for `userID`, sorted by
// CreatedAt descending. Used by the (forthcoming) admin "paired devices"
// listing surface; left here today as a thin scan because the bucket stays
// small (one row per pair redemption).
func (s *DeviceKeyStore) ListForUser(ctx context.Context, userID string) ([]authware.DeviceKey, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("device_key: store not open")
	}
	rows, err := s.store.List(ctx, deviceKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]authware.DeviceKey, 0, len(rows))
	for _, raw := range rows {
		var stored storedDeviceKey
		if uerr := json.Unmarshal(raw, &stored); uerr != nil {
			continue
		}
		if stored.UserID != userID {
			continue
		}
		dk, derr := fromStored(stored)
		if derr != nil {
			continue
		}
		out = append(out, dk)
	}
	return out, nil
}
