package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/pkg/server/authware"
)

// setupPairFixtures plants fresh in-memory stores in the singletons and
// returns them so tests can inspect / inject. Restores prior values via
// t.Cleanup so other tests in the package aren't affected.
func setupPairFixtures(t *testing.T) (*authware.InMemoryPairCodeStore, *authware.InMemoryDeviceKeyStore) {
	t.Helper()
	codes := authware.NewInMemoryPairCodeStore()
	devices := authware.NewInMemoryDeviceKeyStore()
	prevCodes := pairCodeStoreSingleton
	prevDevs := deviceKeyStoreSingleton
	wirePairHandlers(codes, devices)
	t.Cleanup(func() {
		pairCodeStoreSingleton = prevCodes
		deviceKeyStoreSingleton = prevDevs
	})
	return codes, devices
}

func adminContext() context.Context {
	return context.WithValue(context.Background(),
		authware.ClaimsKey,
		&authware.Claims{Username: "admin", Roles: []string{"admin"}},
	)
}

func TestPairIssue_RejectsNonAdmin(t *testing.T) {
	setupPairFixtures(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/issue", strings.NewReader("{}"))
	// Authenticated as a non-admin user.
	r = r.WithContext(context.WithValue(r.Context(),
		authware.ClaimsKey,
		&authware.Claims{Username: "marketing-lead", Roles: []string{"marketer"}}))
	w := httptest.NewRecorder()
	pairIssueHandler()(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestPairIssue_RejectsUnauthenticated(t *testing.T) {
	setupPairFixtures(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/issue", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	pairIssueHandler()(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestPairIssue_HappyPath(t *testing.T) {
	codes, _ := setupPairFixtures(t)
	body, _ := json.Marshal(map[string]string{
		"user_id":   "marketing-lead",
		"server_id": "site-a",
		"label":     "Marcus iPhone",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/issue", bytes.NewReader(body))
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairIssueHandler()(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	code, _ := resp["code"].(string)
	if !strings.HasPrefix(code, authware.PairCodePrefix) {
		t.Fatalf("code: %q", code)
	}
	if resp["user_id"] != "marketing-lead" {
		t.Fatalf("user_id: %+v", resp)
	}
	if resp["server_id"] != "site-a" {
		t.Fatalf("server_id: %+v", resp)
	}
	if resp["label"] != "Marcus iPhone" {
		t.Fatalf("label: %+v", resp)
	}
	if _, ok := resp["expires_at"].(string); !ok {
		t.Fatalf("expires_at missing")
	}
	// Code landed in the store.
	if _, err := codes.Consume(code, /* now */ ctxNow()); err != nil {
		t.Fatalf("issued code not in store: %v", err)
	}
}

func TestPairIssue_DefaultsToAdminUser(t *testing.T) {
	setupPairFixtures(t)
	// Empty body → user_id falls back to the admin's username.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/issue", strings.NewReader("{}"))
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairIssueHandler()(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["user_id"] != "admin" {
		t.Fatalf("expected admin fallback, got %+v", resp)
	}
}

func TestPairRedeem_HappyPath(t *testing.T) {
	codes, devices := setupPairFixtures(t)
	// Pre-issue a code directly (the issue endpoint isn't under test here).
	issueReq := httptest.NewRequest(http.MethodPost, "/api/v1/pair/issue",
		strings.NewReader(`{"user_id":"u1","server_id":"site-x","label":"Test"}`))
	issueReq = issueReq.WithContext(adminContext())
	issueResp := httptest.NewRecorder()
	pairIssueHandler()(issueResp, issueReq)
	var issued map[string]any
	_ = json.Unmarshal(issueResp.Body.Bytes(), &issued)
	code := issued["code"].(string)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	body, _ := json.Marshal(map[string]string{
		"code":                   code,
		"device_id":              "device-uuid-1",
		"signing_public_key_b64": pubB64,
		"label":                  "Test",
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/redeem", bytes.NewReader(body))
	w := httptest.NewRecorder()
	pairRedeemHandler()(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["user_id"] != "u1" || resp["server_id"] != "site-x" {
		t.Fatalf("resp: %+v", resp)
	}
	// Device landed in the store.
	dk, ok := devices.LookupDeviceKey(context.Background(), "device-uuid-1")
	if !ok {
		t.Fatalf("device not in store after redeem")
	}
	if dk.UserID != "u1" || dk.ServerID != "site-x" {
		t.Fatalf("stored device: %+v", dk)
	}
	if !bytes.Equal(dk.PublicKey, pub) {
		t.Fatalf("stored public key mismatch")
	}
	// Code is now single-use-consumed.
	if _, err := codes.Consume(code, ctxNow()); err == nil {
		t.Fatalf("expected code to be gone after redeem")
	}
}

func TestPairRedeem_RejectsBadChecksum(t *testing.T) {
	setupPairFixtures(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	body, _ := json.Marshal(map[string]string{
		"code":                   "HULA-PAIR-AAAA-AAAA-AAAA",
		"device_id":              "d",
		"signing_public_key_b64": base64.StdEncoding.EncodeToString(pub),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/redeem", bytes.NewReader(body))
	w := httptest.NewRecorder()
	pairRedeemHandler()(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != "bad_code" {
		t.Fatalf("error code: %+v", resp)
	}
}

func TestPairRedeem_RejectsUnknownCode(t *testing.T) {
	setupPairFixtures(t)
	// A code that PARSES (valid checksum) but isn't in the store.
	code, _ := authware.GeneratePairCode(nil)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	body, _ := json.Marshal(map[string]string{
		"code":                   code,
		"device_id":              "d",
		"signing_public_key_b64": base64.StdEncoding.EncodeToString(pub),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/redeem", bytes.NewReader(body))
	w := httptest.NewRecorder()
	pairRedeemHandler()(w, r)
	if w.Code != http.StatusGone {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
}

func TestPairRedeem_RejectsBadPubKey(t *testing.T) {
	setupPairFixtures(t)
	code, _ := authware.GeneratePairCode(nil)
	for _, badPub := range []string{
		"not-base64",
		base64.StdEncoding.EncodeToString(make([]byte, 16)), // wrong length
	} {
		body, _ := json.Marshal(map[string]string{
			"code":                   code,
			"device_id":              "d",
			"signing_public_key_b64": badPub,
		})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/redeem", bytes.NewReader(body))
		w := httptest.NewRecorder()
		pairRedeemHandler()(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("badPub=%q: status %d; body=%s", badPub, w.Code, w.Body.String())
		}
	}
}

func TestPairRedeem_RejectsMissingFields(t *testing.T) {
	setupPairFixtures(t)
	// Only `code` supplied — device_id + signing_public_key_b64 missing.
	body, _ := json.Marshal(map[string]string{"code": "HULA-PAIR-AAAA-AAAA-AAAA"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/redeem", bytes.NewReader(body))
	w := httptest.NewRecorder()
	pairRedeemHandler()(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
}

// ctxNow returns the same time-grain the handler uses, so post-action store
// inspection lines up with what the handler would have written.
func ctxNow() time.Time { return time.Now().UTC() }

// --- list / revoke endpoint tests ---

// seedDevice plants a device key directly in the store (bypassing the redeem
// handler) so list/revoke tests can run without the issue/redeem dance.
func seedDevice(t *testing.T, devices *authware.InMemoryDeviceKeyStore, deviceID, userID, serverID string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("seed key: %s", err)
	}
	devices.Put(authware.DeviceKey{
		DeviceID:  deviceID,
		UserID:    userID,
		ServerID:  serverID,
		PublicKey: pub,
		CreatedAt: ctxNow(),
	})
}

func ownerContext(username string) context.Context {
	return context.WithValue(context.Background(),
		authware.ClaimsKey,
		&authware.Claims{Username: username, Roles: []string{"marketer"}},
	)
}

func TestPairListDevices_OwnerSeesOwnDevices(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")
	seedDevice(t, devices, "d-b", "alice", "site-2")
	seedDevice(t, devices, "d-c", "bob", "site-1")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/pair/devices", nil)
	r = r.WithContext(ownerContext("alice"))
	w := httptest.NewRecorder()
	pairListDevicesHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	rows, _ := resp["devices"].([]any)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (alice has two devices, bob shouldn't appear)", len(rows))
	}
}

func TestPairListDevices_AdminCanQueryOtherUser(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")
	seedDevice(t, devices, "d-b", "bob", "site-2")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/pair/devices?user_id=bob", nil)
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairListDevicesHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	rows, _ := resp["devices"].([]any)
	if len(rows) != 1 {
		t.Fatalf("got %d, want 1", len(rows))
	}
	got, _ := rows[0].(map[string]any)
	if got["user_id"] != "bob" || got["device_id"] != "d-b" {
		t.Fatalf("row: %+v", got)
	}
}

func TestPairListDevices_NonAdminCannotQueryOtherUser(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/pair/devices?user_id=alice", nil)
	r = r.WithContext(ownerContext("bob"))
	w := httptest.NewRecorder()
	pairListDevicesHandler()(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
}

func TestPairListDevices_RejectsUnauthenticated(t *testing.T) {
	setupPairFixtures(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pair/devices", nil)
	w := httptest.NewRecorder()
	pairListDevicesHandler()(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestPairRevoke_OwnerCanRevokeOwn(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")
	body, _ := json.Marshal(map[string]string{"device_id": "d-a"})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/devices/revoke", bytes.NewReader(body))
	r = r.WithContext(ownerContext("alice"))
	w := httptest.NewRecorder()
	pairRevokeDeviceHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["revoked"] != true || resp["device_id"] != "d-a" {
		t.Fatalf("resp: %+v", resp)
	}
	if _, ok := devices.LookupDeviceKey(context.Background(), "d-a"); ok {
		t.Fatalf("device still present after revoke")
	}
}

func TestPairRevoke_AdminCanRevokeAnything(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")
	body, _ := json.Marshal(map[string]string{"device_id": "d-a"})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/devices/revoke", bytes.NewReader(body))
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairRevokeDeviceHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if _, ok := devices.LookupDeviceKey(context.Background(), "d-a"); ok {
		t.Fatalf("device still present after admin revoke")
	}
}

func TestPairRevoke_NonAdminCannotRevokeOtherUsersDevice(t *testing.T) {
	_, devices := setupPairFixtures(t)
	seedDevice(t, devices, "d-a", "alice", "site-1")
	body, _ := json.Marshal(map[string]string{"device_id": "d-a"})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/devices/revoke", bytes.NewReader(body))
	r = r.WithContext(ownerContext("bob"))
	w := httptest.NewRecorder()
	pairRevokeDeviceHandler()(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status: %d; body=%s", w.Code, w.Body.String())
	}
	// Device must still exist — non-admin revoke must not delete.
	if _, ok := devices.LookupDeviceKey(context.Background(), "d-a"); !ok {
		t.Fatalf("device was deleted by a non-admin caller")
	}
}

func TestPairRevoke_UnknownDeviceIsIdempotent(t *testing.T) {
	setupPairFixtures(t)
	body, _ := json.Marshal(map[string]string{"device_id": "nonexistent"})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/devices/revoke", bytes.NewReader(body))
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairRevokeDeviceHandler()(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d; want 200 (idempotent on unknown)", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["revoked"] != false {
		t.Fatalf("expected revoked=false for unknown device, got %+v", resp)
	}
}

func TestPairRevoke_RejectsMissingDeviceID(t *testing.T) {
	setupPairFixtures(t)
	body, _ := json.Marshal(map[string]string{})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pair/devices/revoke", bytes.NewReader(body))
	r = r.WithContext(adminContext())
	w := httptest.NewRecorder()
	pairRevokeDeviceHandler()(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", w.Code)
	}
}
