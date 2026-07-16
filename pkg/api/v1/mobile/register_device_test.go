package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mobilespec "github.com/tlalocweb/hulation/pkg/apispec/v1/mobile"
	"github.com/tlalocweb/hulation/pkg/mobile/tokenbox"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
)

// makeServer builds a Server with an in-memory token key + a fresh bolt-backed
// storage.Global(). Returns the server + a context carrying admin claims so
// currentUserID() resolves.
func makeServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	s, err := local.Open(local.Options{Path: filepath.Join(t.TempDir(), "test.bolt")})
	if err != nil {
		t.Fatalf("open storage: %s", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	prev := storage.Global()
	storage.SetGlobal(s)
	t.Cleanup(func() { storage.SetGlobal(prev) })

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("rand key: %s", err)
	}
	srv := New(nil, nil, nil, nil, nil, func() ([]byte, error) { return key, nil })

	ctx := context.WithValue(context.Background(), authware.ClaimsKey,
		&authware.Claims{Username: "test-user"})
	return srv, ctx
}

func TestRegisterDevice_RejectsMissingPlatform(t *testing.T) {
	srv, ctx := makeServer(t)
	_, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Token: "tok",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestRegisterDevice_RejectsAllFieldsEmpty(t *testing.T) {
	srv, ctx := makeServer(t)
	_, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform: mobilespec.Platform_PLATFORM_APNS,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestRegisterDevice_RejectsPartialRelayFields(t *testing.T) {
	srv, ctx := makeServer(t)
	// Each variant supplies *some* relay fields but not all three — every one
	// must be rejected, otherwise the fan-out path silently fails later when it
	// can't resolve the missing piece.
	cases := []*mobilespec.RegisterDeviceRequest{
		{
			Platform:       mobilespec.Platform_PLATFORM_APNS,
			RelayChannelId: "pch_abc",
		},
		{
			Platform:         mobilespec.Platform_PLATFORM_APNS,
			RelayChannelAuth: "secret",
		},
		{
			Platform:              mobilespec.Platform_PLATFORM_APNS,
			NoiseEncryptionPubB64: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
		{
			Platform:         mobilespec.Platform_PLATFORM_APNS,
			RelayChannelId:   "pch_abc",
			RelayChannelAuth: "secret",
			// noise pub missing
		},
	}
	for i, req := range cases {
		if _, err := srv.RegisterDevice(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("case %d: want InvalidArgument, got %v", i, err)
		}
	}
}

func TestRegisterDevice_RejectsBadNoisePubBase64(t *testing.T) {
	srv, ctx := makeServer(t)
	_, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		RelayChannelId:        "pch_abc",
		RelayChannelAuth:      "secret",
		NoiseEncryptionPubB64: "!!!not base64!!!",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestRegisterDevice_RejectsWrongLengthNoisePub(t *testing.T) {
	srv, ctx := makeServer(t)
	// 16 raw bytes (not 32) — must reject.
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		RelayChannelId:        "pch_abc",
		RelayChannelAuth:      "secret",
		NoiseEncryptionPubB64: short,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestRegisterDevice_RelayPath_StoresAllFieldsAndSealsAuth(t *testing.T) {
	srv, ctx := makeServer(t)

	pubBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, pubBytes); err != nil {
		t.Fatalf("rand pub: %s", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)

	d, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		DeviceFingerprint:     "fp-relay-1",
		Label:                 "iPhone 15 Pro",
		RelayChannelId:        "pch_abc",
		RelayChannelAuth:      "channel-secret-xyz",
		NoiseEncryptionPubB64: pubB64,
	})
	if err != nil {
		t.Fatalf("register: %s", err)
	}
	// Load the stored row directly to inspect the sealed material.
	stored, err := hulabolt.GetDevice(ctx, storage.Global(), d.GetId())
	if err != nil || stored == nil {
		t.Fatalf("get device: %v / %+v", err, stored)
	}
	if stored.RelayChannelID != "pch_abc" {
		t.Errorf("RelayChannelID: got %q want pch_abc", stored.RelayChannelID)
	}
	if stored.NoiseEncryptionPub != pubB64 {
		t.Errorf("NoiseEncryptionPub mismatch")
	}
	if len(stored.RelayChannelAuthCipher) == 0 {
		t.Errorf("RelayChannelAuthCipher empty — auth was not sealed")
	}
	// The raw secret must not be stored in plaintext anywhere on the row.
	if bytesContain(stored.RelayChannelAuthCipher, []byte("channel-secret-xyz")) {
		t.Errorf("relay auth plaintext leaked into sealed cipher bytes")
	}
	// Unseal with the same key to confirm round-trip works (sanity check).
	key, _ := srv.tokenKeyFn()
	got, err := tokenbox.Open(stored.RelayChannelAuthCipher, key)
	if err != nil {
		t.Fatalf("tokenbox open: %s", err)
	}
	if string(got) != "channel-secret-xyz" {
		t.Errorf("unsealed auth: got %q want channel-secret-xyz", got)
	}
	// Legacy TokenCipher must be empty when only relay fields were supplied.
	if len(stored.TokenCipher) != 0 {
		t.Errorf("TokenCipher non-empty when only relay path provided")
	}
}

func TestRegisterDevice_LegacyPath_StillWorks(t *testing.T) {
	srv, ctx := makeServer(t)
	d, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:          mobilespec.Platform_PLATFORM_APNS,
		Token:             "legacy-apns-token",
		DeviceFingerprint: "fp-legacy-1",
	})
	if err != nil {
		t.Fatalf("register: %s", err)
	}
	stored, err := hulabolt.GetDevice(ctx, storage.Global(), d.GetId())
	if err != nil || stored == nil {
		t.Fatalf("get device: %v", err)
	}
	if len(stored.TokenCipher) == 0 {
		t.Errorf("TokenCipher empty after legacy registration")
	}
	if stored.RelayChannelID != "" {
		t.Errorf("RelayChannelID populated on legacy path")
	}
	if len(stored.RelayChannelAuthCipher) != 0 {
		t.Errorf("RelayChannelAuthCipher populated on legacy path")
	}
}

func TestRegisterDevice_FirstRegistrationEnablesPushPrefs(t *testing.T) {
	srv, ctx := makeServer(t)
	pubBytes := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, pubBytes)

	if _, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		DeviceFingerprint:     "fp-prefs-1",
		RelayChannelId:        "pch_prefs",
		RelayChannelAuth:      "secret-prefs",
		NoiseEncryptionPubB64: base64.StdEncoding.EncodeToString(pubBytes),
	}); err != nil {
		t.Fatalf("register: %s", err)
	}

	prefs, err := hulabolt.GetNotificationPrefs(ctx, storage.Global(), "test-user")
	if err != nil {
		t.Fatalf("get prefs: %s", err)
	}
	if !prefs.PushEnabled {
		t.Fatalf("PushEnabled = false, want true after first device registration")
	}
}

func TestRegisterDevice_PreservesExplicitPushDisabledPrefs(t *testing.T) {
	srv, ctx := makeServer(t)
	if _, err := hulabolt.PutNotificationPrefs(ctx, storage.Global(), hulabolt.StoredNotificationPrefs{
		UserID:       "test-user",
		EmailEnabled: true,
		PushEnabled:  false,
	}); err != nil {
		t.Fatalf("put prefs: %s", err)
	}
	pubBytes := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, pubBytes)

	if _, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		DeviceFingerprint:     "fp-prefs-2",
		RelayChannelId:        "pch_prefs_2",
		RelayChannelAuth:      "secret-prefs-2",
		NoiseEncryptionPubB64: base64.StdEncoding.EncodeToString(pubBytes),
	}); err != nil {
		t.Fatalf("register: %s", err)
	}

	prefs, err := hulabolt.GetNotificationPrefs(ctx, storage.Global(), "test-user")
	if err != nil {
		t.Fatalf("get prefs: %s", err)
	}
	if prefs.PushEnabled {
		t.Fatalf("PushEnabled = true, want explicit disabled preference preserved")
	}
}

func TestRegisterDevice_DualPath_BothFieldsStored(t *testing.T) {
	// A device that registers with both legacy + relay fields keeps both on the
	// row — `resolveChatRecipientCohorts` picks the relay path when both exist
	// so the user only gets one notification, but the legacy token stays in
	// place for graceful downgrade if the relay client gets disabled later.
	srv, ctx := makeServer(t)
	pubBytes := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, pubBytes)
	d, err := srv.RegisterDevice(ctx, &mobilespec.RegisterDeviceRequest{
		Platform:              mobilespec.Platform_PLATFORM_APNS,
		Token:                 "legacy-token",
		DeviceFingerprint:     "fp-dual-1",
		RelayChannelId:        "pch_dual",
		RelayChannelAuth:      "secret-dual",
		NoiseEncryptionPubB64: base64.StdEncoding.EncodeToString(pubBytes),
	})
	if err != nil {
		t.Fatalf("register: %s", err)
	}
	stored, _ := hulabolt.GetDevice(ctx, storage.Global(), d.GetId())
	if stored == nil {
		t.Fatalf("get device: nil")
	}
	if len(stored.TokenCipher) == 0 || len(stored.RelayChannelAuthCipher) == 0 {
		t.Errorf("both legacy and relay material should be stored: %+v", stored)
	}
}

func bytesContain(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
