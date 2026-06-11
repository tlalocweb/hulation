package authware

// Device-key authentication — alternate path to bearer JWT, used by mobile devices that
// were paired via QR (no OPAQUE password). Each paired device holds a per-server ed25519
// keypair (generated in hula-mobile/common/src/keys.rs); the public half is registered
// with the Hula server at pairing time and looked up here by `device_id`.
//
// The signed-request envelope mirrors the relay's design (see ../hula-push-relay):
//
//   Hula-Device-Id:  dev_…
//   Hula-Timestamp:  RFC3339, second precision (±300s skew window)
//   Hula-Nonce:      base64 ≥16 bytes, unique per device for ~10 min
//   Hula-Signature:  base64 ed25519 over canonical bytes
//
// Canonical bytes:
//
//   TIMESTAMP \n NONCE \n DEVICE_ID
//
// Body bytes are NOT included — gRPC streaming RPCs wouldn't permit it. Replay defence
// comes from the ±300s window + nonce uniqueness; the per-(device, server) key scoping
// means signatures captured for Hula A cannot be replayed against Hula B even with
// matching device_id (different stored pubkey).
//
// The `Noise_IK` session-wrap layer (next slice) provides per-frame authentication on top
// of this call-level auth, closing the gap for streaming bodies.

import (
	"context"
	"crypto/ed25519"
	"sync"
	"time"
)

// DeviceKey is what's stored on the server side per paired mobile device.
type DeviceKey struct {
	DeviceID  string
	UserID    string
	ServerID  string
	PublicKey ed25519.PublicKey // 32 bytes
	CreatedAt time.Time
}

// DeviceKeyStore is the read-side interface the auth interceptor consults. Production
// implementations back this with bolt / sqlite / postgres; tests use the in-memory
// [`InMemoryDeviceKeyStore`].
type DeviceKeyStore interface {
	LookupDeviceKey(ctx context.Context, deviceID string) (DeviceKey, bool)
}

// DeviceKeyWriter is the small write surface the QR pair redeem handler uses to
// persist a newly-redeemed device. Both the in-memory store and the bolt-backed
// store (in pkg/store/bolt) satisfy it. Keeping it separate from `DeviceKeyStore`
// matches the read-vs-write boundary the auth interceptor cares about: handlers
// that only read the store should depend on the narrower interface.
type DeviceKeyWriter interface {
	PutDeviceKey(DeviceKey) error
}

// DeviceKeyRevoker is the delete-side counterpart to `DeviceKeyWriter`. The
// admin "revoke device" handler depends on this so we don't tunnel a concrete
// store type through the handler signature.
type DeviceKeyRevoker interface {
	Delete(deviceID string) error
}

// DeviceKeyLister is the list-side counterpart. Today both backings expose
// `ListForUser`; the in-memory variant adds it here to satisfy the interface
// even though it's only ever exercised in tests.
type DeviceKeyLister interface {
	ListForUser(ctx context.Context, userID string) ([]DeviceKey, error)
}

// DeviceKeyAllLister lists every device across all users — the admin "all paired
// devices" surface (GET /api/v1/pair/devices?all=true). A separate, optional
// interface so the narrower per-user DeviceKeyLister path can't reach it; the
// handler also gates it on the admin role.
type DeviceKeyAllLister interface {
	ListAll(ctx context.Context) ([]DeviceKey, error)
}

// InMemoryDeviceKeyStore is a sync-safe map used by tests + the v1 boot path until a
// persistent store lands alongside the QR-pairing endpoint. Production swaps this for a
// real backing store.
type InMemoryDeviceKeyStore struct {
	mu    sync.RWMutex
	byID  map[string]DeviceKey
}

func NewInMemoryDeviceKeyStore() *InMemoryDeviceKeyStore {
	return &InMemoryDeviceKeyStore{byID: make(map[string]DeviceKey)}
}

func (s *InMemoryDeviceKeyStore) Put(key DeviceKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[key.DeviceID] = key
}

// PutDeviceKey satisfies DeviceKeyWriter. Wraps Put with the error-returning
// signature persistent stores naturally need; the in-memory variant always
// succeeds.
func (s *InMemoryDeviceKeyStore) PutDeviceKey(key DeviceKey) error {
	s.Put(key)
	return nil
}

func (s *InMemoryDeviceKeyStore) Delete(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, deviceID)
	return nil
}

// ListForUser satisfies DeviceKeyLister.
func (s *InMemoryDeviceKeyStore) ListForUser(_ context.Context, userID string) ([]DeviceKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceKey, 0, len(s.byID))
	for _, k := range s.byID {
		if k.UserID == userID {
			out = append(out, k)
		}
	}
	return out, nil
}

// ListAll satisfies DeviceKeyAllLister.
func (s *InMemoryDeviceKeyStore) ListAll(_ context.Context) ([]DeviceKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceKey, 0, len(s.byID))
	for _, k := range s.byID {
		out = append(out, k)
	}
	return out, nil
}

func (s *InMemoryDeviceKeyStore) LookupDeviceKey(_ context.Context, deviceID string) (DeviceKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[deviceID]
	return k, ok
}

// NoopDeviceKeyStore returns "not found" for every lookup. Used by the v1 boot path until
// the persistent store lands so the auth interceptor is always wired but signature auth
// is effectively disabled.
type NoopDeviceKeyStore struct{}

func (NoopDeviceKeyStore) LookupDeviceKey(context.Context, string) (DeviceKey, bool) {
	return DeviceKey{}, false
}

// DeviceSignatureHeaders is the metadata key set the interceptor reads.
const (
	HeaderDeviceID  = "hula-device-id"
	HeaderTimestamp = "hula-timestamp"
	HeaderNonce     = "hula-nonce"
	HeaderSignature = "hula-signature"

	// SignatureSkew is the maximum allowed clock drift between the device's
	// `Hula-Timestamp` and the server. Mirrors the relay constant.
	SignatureSkew = 300 * time.Second
)

// CanonicalSigningBytes is the byte string the device signs and the server verifies.
// Stable across versions; do not reorder fields.
func CanonicalSigningBytes(timestamp, nonce, deviceID string) []byte {
	out := make([]byte, 0, len(timestamp)+len(nonce)+len(deviceID)+2)
	out = append(out, timestamp...)
	out = append(out, '\n')
	out = append(out, nonce...)
	out = append(out, '\n')
	out = append(out, deviceID...)
	return out
}
