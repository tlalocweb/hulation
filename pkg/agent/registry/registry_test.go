package registry_test

// Round-trip tests for the agent registry. We use the local-bbolt
// Storage implementation so the tests are hermetic — no raft, no
// network, just bolt under a temp dir.

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"path/filepath"
	"testing"
	"time"

	agentpki "github.com/tlalocweb/hulation/pkg/agent/pki"
	"github.com/tlalocweb/hulation/pkg/agent/registry"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
)

func newStore(t *testing.T) *local.LocalStorage {
	t.Helper()
	st, err := local.Open(local.Options{Path: filepath.Join(t.TempDir(), "data.db")})
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// freshAgentCert generates a one-off CA + leaf cert + parses it. We
// need a real *x509.Certificate to fingerprint, and going through
// the full ceremony exercises the same path the handler will.
func freshAgentCert(t *testing.T, agentID string) *x509.Certificate {
	t.Helper()
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	leaf, err := agentpki.GenerateAgentCert(ca, agentID, time.Hour)
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	block, _ := pem.Decode(leaf.CertPEM)
	if block == nil {
		t.Fatalf("decode pem: nil block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func mkRecord(t *testing.T, id string, perms map[string]map[string]string) *registry.Record {
	t.Helper()
	cert := freshAgentCert(t, id)
	now := time.Now().UTC()
	return &registry.Record{
		ID:          id,
		Permissions: perms,
		CertSHA256:  registry.FingerprintFromCert(cert),
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
	}
}

func TestPutAndGetByID(t *testing.T) {
	store := newStore(t)
	rec := mkRecord(t, "alpha", map[string]map[string]string{
		"gravhl": {"build": ""},
	})
	if err := registry.Put(context.Background(), store, rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := registry.GetByID(context.Background(), store, "alpha")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "alpha" {
		t.Fatalf("id mismatch: got %q", got.ID)
	}
	if opts, ok := got.IsAllowed("gravhl", "build"); !ok || opts != "" {
		t.Fatalf("permissions round-trip: got opts=%q ok=%v", opts, ok)
	}
	if _, ok := got.IsAllowed("gravhl", "push"); ok {
		t.Fatalf("unspecified verb should be denied")
	}
}

func TestGetByIDMissing(t *testing.T) {
	store := newStore(t)
	_, err := registry.GetByID(context.Background(), store, "nope")
	if !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetByFingerprint(t *testing.T) {
	store := newStore(t)
	rec := mkRecord(t, "beta", map[string]map[string]string{
		"x": {"build": ""},
	})
	if err := registry.Put(context.Background(), store, rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := registry.GetByFingerprint(context.Background(), store, rec.CertSHA256)
	if err != nil {
		t.Fatalf("get-by-fingerprint: %v", err)
	}
	if got.ID != "beta" {
		t.Fatalf("id mismatch: got %q", got.ID)
	}
}

func TestSetRevoked(t *testing.T) {
	store := newStore(t)
	rec := mkRecord(t, "gamma", map[string]map[string]string{"x": {"build": ""}})
	if err := registry.Put(context.Background(), store, rec); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := registry.SetRevoked(context.Background(), store, "gamma", true); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ := registry.GetByID(context.Background(), store, "gamma")
	if !got.Revoked {
		t.Fatalf("revoked flag did not stick")
	}
	if got.IsActive(time.Now()) {
		t.Fatalf("IsActive should be false for revoked")
	}
	// Idempotent.
	if err := registry.SetRevoked(context.Background(), store, "gamma", true); err != nil {
		t.Fatalf("revoke idempotent: %v", err)
	}
}

func TestIsActiveExpired(t *testing.T) {
	rec := mkRecord(t, "delta", map[string]map[string]string{})
	rec.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	if rec.IsActive(time.Now()) {
		t.Fatalf("expired record should not be Active")
	}
}

func TestList(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{"a", "b", "c"} {
		rec := mkRecord(t, name, map[string]map[string]string{name: {"build": ""}})
		if err := registry.Put(context.Background(), store, rec); err != nil {
			t.Fatalf("put %s: %v", name, err)
		}
	}
	all, err := registry.List(context.Background(), store)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list len: got %d want 3", len(all))
	}
}
