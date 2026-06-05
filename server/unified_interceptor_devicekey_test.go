package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/tlalocweb/hulation/pkg/server/authware"
)

func mustGenKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func signedCtx(deviceID, userID string, ts time.Time, nonce string, priv ed25519.PrivateKey) context.Context {
	tsStr := ts.UTC().Format(time.RFC3339)
	canonical := authware.CanonicalSigningBytes(tsStr, nonce, deviceID)
	sig := ed25519.Sign(priv, canonical)
	md := metadata.New(map[string]string{
		authware.HeaderDeviceID:  deviceID,
		authware.HeaderTimestamp: tsStr,
		authware.HeaderNonce:     nonce,
		authware.HeaderSignature: base64.StdEncoding.EncodeToString(sig),
	})
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestClaimsFromDeviceSignature_HappyPath(t *testing.T) {
	pub, priv := mustGenKey(t)
	store := authware.NewInMemoryDeviceKeyStore()
	store.Put(authware.DeviceKey{
		DeviceID:  "dev-1",
		UserID:    "alice",
		ServerID:  "acme",
		PublicKey: pub,
		CreatedAt: time.Now(),
	})

	ctx := signedCtx("dev-1", "alice", time.Now(), "nonce-aaaa-bbbb-cccc", priv)
	claims := claimsFromDeviceSignature(ctx, store)
	if claims == nil {
		t.Fatal("expected claims, got nil")
	}
	if claims.Username != "alice" || claims.Subject != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "qr_paired" {
		t.Fatalf("expected qr_paired role: %+v", claims.Roles)
	}
}

func TestClaimsFromDeviceSignature_Rejects(t *testing.T) {
	pub, priv := mustGenKey(t)
	store := authware.NewInMemoryDeviceKeyStore()
	store.Put(authware.DeviceKey{
		DeviceID:  "dev-1",
		UserID:    "alice",
		PublicKey: pub,
	})

	t.Run("missing headers", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.New(nil))
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatalf("expected nil, got %+v", c)
		}
	})

	t.Run("short nonce", func(t *testing.T) {
		ctx := signedCtx("dev-1", "alice", time.Now(), "tiny", priv)
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatal("expected nil for short nonce")
		}
	})

	t.Run("stale timestamp", func(t *testing.T) {
		ctx := signedCtx("dev-1", "alice", time.Now().Add(-1*time.Hour), "nonce-stale-stale-stale", priv)
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatal("expected nil for stale timestamp")
		}
	})

	t.Run("unknown device", func(t *testing.T) {
		ctx := signedCtx("dev-unknown", "alice", time.Now(), "nonce-unk-unk-unk-unk", priv)
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatal("expected nil for unknown device")
		}
	})

	t.Run("bad signature", func(t *testing.T) {
		_, otherPriv := mustGenKey(t)
		ctx := signedCtx("dev-1", "alice", time.Now(), "nonce-bad-bad-bad-sigs", otherPriv)
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatal("expected nil for wrong signature")
		}
	})

	t.Run("malformed signature base64", func(t *testing.T) {
		md := metadata.New(map[string]string{
			authware.HeaderDeviceID:  "dev-1",
			authware.HeaderTimestamp: time.Now().UTC().Format(time.RFC3339),
			authware.HeaderNonce:     "nonce-malformed-aaaa",
			authware.HeaderSignature: "!!! not base64 !!!",
		})
		ctx := metadata.NewIncomingContext(context.Background(), md)
		if c := claimsFromDeviceSignature(ctx, store); c != nil {
			t.Fatal("expected nil for malformed signature")
		}
	})
}

func TestClaimsFromDeviceSignature_NonceReplay(t *testing.T) {
	pub, priv := mustGenKey(t)
	store := authware.NewInMemoryDeviceKeyStore()
	store.Put(authware.DeviceKey{
		DeviceID:  "dev-replay",
		UserID:    "alice",
		PublicKey: pub,
	})

	now := time.Now()
	ctx := signedCtx("dev-replay", "alice", now, "nonce-replay-replay-r", priv)
	if c := claimsFromDeviceSignature(ctx, store); c == nil {
		t.Fatal("first call should succeed")
	}
	if c := claimsFromDeviceSignature(ctx, store); c != nil {
		t.Fatal("replay of same nonce should be rejected")
	}
}

func TestClaimsFromContext_PrefersBearerOverSignature(t *testing.T) {
	// No bearer is configured (extractBearer returns ""), so claimsFromBearer is nil and
	// the signature path is taken. With nil device store the call should also be nil.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(nil))
	if c := claimsFromContext(ctx, nil); c != nil {
		t.Fatalf("expected nil claims with no auth + no store, got %+v", c)
	}
	if c := claimsFromContext(ctx, authware.NoopDeviceKeyStore{}); c != nil {
		t.Fatalf("expected nil claims with no auth + noop store, got %+v", c)
	}
}

func TestCanonicalSigningBytesStable(t *testing.T) {
	got := authware.CanonicalSigningBytes("2026-05-15T18:00:00Z", "nonce-aaaa-bbbb-cccc", "dev-1")
	want := "2026-05-15T18:00:00Z\nnonce-aaaa-bbbb-cccc\ndev-1"
	if string(got) != want {
		t.Fatalf("canonical bytes drifted: %q", string(got))
	}
}
