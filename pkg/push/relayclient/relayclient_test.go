package relayclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay verifies the signed-request envelope the way the real relay would, then
// returns a canned response. The matcher mirrors the relay's signed::Signed extractor
// so any drift in canonical form is caught here without needing the relay binary.
type fakeRelay struct {
	mu             sync.Mutex
	installID      string
	verifyingKey   ed25519.PublicKey
	scenario       string // "dispatched" | "channel_revoked" | "bad_request" | "retryable"
	receivedBodies [][]byte
	receivedAuth   []map[string]string
}

func (f *fakeRelay) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/push/send" || r.Method != http.MethodPost {
		http.Error(w, "wrong endpoint", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	installID := r.Header.Get(HeaderInstallationID)
	timestamp := r.Header.Get(HeaderTimestamp)
	nonce := r.Header.Get(HeaderNonce)
	bodyHashHdr := r.Header.Get(HeaderBodySHA256)
	sigB64 := r.Header.Get(HeaderSignature)

	if installID != f.installID {
		http.Error(w, "wrong installation", http.StatusUnauthorized)
		return
	}
	bodyHash := sha256.Sum256(body)
	if hex.EncodeToString(bodyHash[:]) != bodyHashHdr {
		http.Error(w, "body hash mismatch", http.StatusUnauthorized)
		return
	}
	canonical := canonicalSigningBytes("POST", "/v1/push/send", timestamp, nonce, bodyHashHdr)
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || !ed25519.Verify(f.verifyingKey, canonical, sig) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}

	f.mu.Lock()
	f.receivedBodies = append(f.receivedBodies, body)
	f.receivedAuth = append(f.receivedAuth, map[string]string{
		HeaderInstallationID: installID,
		HeaderTimestamp:      timestamp,
		HeaderNonce:          nonce,
		HeaderBodySHA256:     bodyHashHdr,
		HeaderSignature:      sigB64,
	})
	scenario := f.scenario
	f.mu.Unlock()

	switch scenario {
	case "dispatched":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"delivery_id": "del_test_abcdef",
			"status":      "dispatched",
		})
	case "channel_revoked":
		http.Error(w, `{"error":{"code":"channel_revoked"}}`, http.StatusForbidden)
	case "bad_request":
		http.Error(w, `{"error":{"code":"bad_request"}}`, http.StatusBadRequest)
	case "retryable":
		http.Error(w, "upstream provider down", http.StatusServiceUnavailable)
	default:
		http.Error(w, "no scenario", http.StatusInternalServerError)
	}
}

func newFakeRelay(t *testing.T, scenario string) (*fakeRelay, *httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %s", err)
	}
	relay := &fakeRelay{
		installID:    "inst_test_1234",
		verifyingKey: pub,
		scenario:     scenario,
	}
	srv := httptest.NewServer(http.HandlerFunc(relay.handler))
	t.Cleanup(srv.Close)
	return relay, srv, priv
}

func TestSendPush_Dispatched(t *testing.T) {
	relay, srv, priv := newFakeRelay(t, "dispatched")
	client, err := New(Config{
		BaseURL:        srv.URL,
		InstallationID: relay.installID,
		SigningKey:     priv,
	})
	if err != nil {
		t.Fatalf("client: %s", err)
	}
	outcome, res, err := client.SendPush(context.Background(), SendPushParams{
		PushChannelID: "pch_abc",
		ChannelAuth:   "secret-abc",
		Ciphertext:    "AAAA",
		CollapseID:    "session-1",
		TTLSeconds:    600,
	})
	if err != nil {
		t.Fatalf("send: %s", err)
	}
	if outcome != OutcomeDispatched {
		t.Fatalf("outcome: got %v want OutcomeDispatched", outcome)
	}
	if res.DeliveryID != "del_test_abcdef" || res.Status != "dispatched" {
		t.Fatalf("parsed result: %+v", res)
	}

	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.receivedBodies) != 1 {
		t.Fatalf("expected exactly one received body, got %d", len(relay.receivedBodies))
	}
	var body map[string]any
	_ = json.Unmarshal(relay.receivedBodies[0], &body)
	if body["push_channel_id"] != "pch_abc" {
		t.Fatalf("channel id: %+v", body)
	}
	if body["channel_auth"] != "secret-abc" {
		t.Fatalf("channel auth: %+v", body)
	}
	if body["ciphertext"] != "AAAA" {
		t.Fatalf("ciphertext: %+v", body)
	}
	if body["collapse_id"] != "session-1" {
		t.Fatalf("collapse_id: %+v", body)
	}
	if body["ttl_seconds"].(float64) != 600 {
		t.Fatalf("ttl_seconds: %+v", body)
	}

	auth := relay.receivedAuth[0]
	if auth[HeaderInstallationID] != relay.installID {
		t.Fatalf("install header: %+v", auth)
	}
	// Timestamp must parse as RFC3339 within 5 minutes of now.
	parsed, err := time.Parse(time.RFC3339, auth[HeaderTimestamp])
	if err != nil {
		t.Fatalf("timestamp parse: %s", err)
	}
	if d := time.Since(parsed); d < -5*time.Minute || d > 5*time.Minute {
		t.Fatalf("timestamp skew: %v", d)
	}
}

func TestSendPush_ChannelRevoked(t *testing.T) {
	relay, srv, priv := newFakeRelay(t, "channel_revoked")
	client, _ := New(Config{
		BaseURL:        srv.URL,
		InstallationID: relay.installID,
		SigningKey:     priv,
	})
	outcome, _, err := client.SendPush(context.Background(), SendPushParams{
		PushChannelID: "pch", ChannelAuth: "s", Ciphertext: "AAAA",
	})
	if outcome != OutcomeChannelRevoked {
		t.Fatalf("outcome: got %v want OutcomeChannelRevoked", outcome)
	}
	if err == nil || !strings.Contains(err.Error(), "channel_revoked") {
		t.Fatalf("err: %v", err)
	}
}

func TestSendPush_BadRequest(t *testing.T) {
	relay, srv, priv := newFakeRelay(t, "bad_request")
	client, _ := New(Config{
		BaseURL:        srv.URL,
		InstallationID: relay.installID,
		SigningKey:     priv,
	})
	outcome, _, _ := client.SendPush(context.Background(), SendPushParams{
		PushChannelID: "pch", ChannelAuth: "s", Ciphertext: "AAAA",
	})
	if outcome != OutcomeBadRequest {
		t.Fatalf("outcome: got %v want OutcomeBadRequest", outcome)
	}
}

func TestSendPush_Retryable(t *testing.T) {
	relay, srv, priv := newFakeRelay(t, "retryable")
	client, _ := New(Config{
		BaseURL:        srv.URL,
		InstallationID: relay.installID,
		SigningKey:     priv,
	})
	outcome, _, _ := client.SendPush(context.Background(), SendPushParams{
		PushChannelID: "pch", ChannelAuth: "s", Ciphertext: "AAAA",
	})
	if outcome != OutcomeRetryable {
		t.Fatalf("outcome: got %v want OutcomeRetryable", outcome)
	}
}

func TestSendPush_RejectsEmptyFields(t *testing.T) {
	relay, srv, priv := newFakeRelay(t, "dispatched")
	client, _ := New(Config{
		BaseURL:        srv.URL,
		InstallationID: relay.installID,
		SigningKey:     priv,
	})
	for _, p := range []SendPushParams{
		{ChannelAuth: "s", Ciphertext: "AAAA"},     // missing channel id
		{PushChannelID: "pch", Ciphertext: "AAAA"}, // missing auth
		{PushChannelID: "pch", ChannelAuth: "s"},   // missing ciphertext
	} {
		outcome, _, err := client.SendPush(context.Background(), p)
		if outcome != OutcomeBadRequest || err == nil {
			t.Fatalf("expected bad-request error for %+v, got outcome=%v err=%v", p, outcome, err)
		}
	}
}

func TestNew_RejectsBadConfig(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty BaseURL", Config{InstallationID: "i", SigningKey: priv}},
		{"empty InstallationID", Config{BaseURL: "https://x", SigningKey: priv}},
		{"short SigningKey", Config{BaseURL: "https://x", InstallationID: "i", SigningKey: []byte{1, 2, 3}}},
	}
	for _, tc := range tests {
		if _, err := New(tc.cfg); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestCanonicalSigningBytes_MatchesRelaySpec(t *testing.T) {
	got := canonicalSigningBytes("POST", "/v1/push/send", "2026-05-14T18:00:00Z", "nonce123", "deadbeef")
	want := "POST\n/v1/push/send\n2026-05-14T18:00:00Z\nnonce123\ndeadbeef"
	if string(got) != want {
		t.Fatalf("canonical form drift: got %q want %q", string(got), want)
	}
}
