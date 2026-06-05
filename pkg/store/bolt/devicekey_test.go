package bolt_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

func freshEd25519Pub(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %s", err)
	}
	return pub
}

func TestBoltDeviceKeyStore_PutLookupRoundTrip(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	pub := freshEd25519Pub(t)
	created := time.Now().UTC()

	dk := authware.DeviceKey{
		DeviceID:  "device-1",
		UserID:    "marcus",
		ServerID:  "site-a",
		PublicKey: pub,
		CreatedAt: created,
	}
	if err := store.PutDeviceKey(dk); err != nil {
		t.Fatalf("put: %s", err)
	}
	got, ok := store.LookupDeviceKey(context.Background(), "device-1")
	if !ok {
		t.Fatalf("device not found after put")
	}
	if got.UserID != "marcus" || got.ServerID != "site-a" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !bytes.Equal(got.PublicKey, pub) {
		t.Fatalf("public key drift")
	}
	// CreatedAt round-trips to second-grain (json/Time loses sub-ns).
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at: got %v want %v", got.CreatedAt, created)
	}
}

func TestBoltDeviceKeyStore_LookupMissing(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	if _, ok := store.LookupDeviceKey(context.Background(), "unknown"); ok {
		t.Fatalf("expected not-found for unknown device")
	}
}

func TestBoltDeviceKeyStore_DeleteRemovesEntry(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	_ = store.PutDeviceKey(authware.DeviceKey{
		DeviceID:  "device-1",
		UserID:    "u",
		PublicKey: freshEd25519Pub(t),
	})
	if err := store.Delete("device-1"); err != nil {
		t.Fatalf("delete: %s", err)
	}
	if _, ok := store.LookupDeviceKey(context.Background(), "device-1"); ok {
		t.Fatalf("device still present after delete")
	}
	// Delete is idempotent — second call must not error.
	if err := store.Delete("device-1"); err != nil {
		t.Fatalf("second delete: %s", err)
	}
}

func TestBoltDeviceKeyStore_RejectsBadInputs(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	if err := store.PutDeviceKey(authware.DeviceKey{PublicKey: freshEd25519Pub(t)}); err == nil {
		t.Fatalf("empty device_id: expected error")
	}
	if err := store.PutDeviceKey(authware.DeviceKey{
		DeviceID:  "device-1",
		PublicKey: []byte{1, 2, 3},
	}); err == nil {
		t.Fatalf("short public key: expected error")
	}
}

func TestBoltDeviceKeyStore_NilStorageDegradesSafely(t *testing.T) {
	store := hulabolt.NewDeviceKeyStore(nil)
	if _, ok := store.LookupDeviceKey(context.Background(), "x"); ok {
		t.Fatalf("nil store should always lookup-miss")
	}
	if err := store.PutDeviceKey(authware.DeviceKey{
		DeviceID:  "x",
		PublicKey: freshEd25519Pub(t),
	}); err == nil {
		t.Fatalf("nil store put: expected error")
	}
}

func TestBoltDeviceKeyStore_ListForUser(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	pub := freshEd25519Pub(t)
	for _, dk := range []authware.DeviceKey{
		{DeviceID: "d1", UserID: "marcus", PublicKey: pub},
		{DeviceID: "d2", UserID: "marcus", PublicKey: pub},
		{DeviceID: "d3", UserID: "alice", PublicKey: pub},
	} {
		if err := store.PutDeviceKey(dk); err != nil {
			t.Fatalf("put: %s", err)
		}
	}
	got, err := store.ListForUser(context.Background(), "marcus")
	if err != nil {
		t.Fatalf("list: %s", err)
	}
	if len(got) != 2 {
		t.Fatalf("listForUser marcus: got %d, want 2", len(got))
	}
}

func TestBoltDeviceKeyStore_SurvivesReopen(t *testing.T) {
	s := newTestStorage(t)
	store := hulabolt.NewDeviceKeyStore(s)
	pub := freshEd25519Pub(t)
	if err := store.PutDeviceKey(authware.DeviceKey{
		DeviceID:  "persist",
		UserID:    "u",
		PublicKey: pub,
	}); err != nil {
		t.Fatalf("put: %s", err)
	}
	// Fresh handle, same bolt — proves the data's on disk.
	store2 := hulabolt.NewDeviceKeyStore(s)
	got, ok := store2.LookupDeviceKey(context.Background(), "persist")
	if !ok {
		t.Fatalf("device not visible from fresh handle")
	}
	if !bytes.Equal(got.PublicKey, pub) {
		t.Fatalf("pub key drift across handle")
	}
}
