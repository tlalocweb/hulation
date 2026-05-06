package mtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentpki "github.com/tlalocweb/hulation/pkg/agent/pki"
	"github.com/tlalocweb/hulation/pkg/agent/registry"
)

// build a CA + signed leaf for the agent path; returns parsed leaf.
func mintAgentLeaf(t *testing.T, ca *agentpki.CA, id string) *x509.Certificate {
	t.Helper()
	leaf, err := agentpki.GenerateAgentCert(ca, id, time.Hour)
	if err != nil {
		t.Fatalf("GenerateAgentCert: %v", err)
	}
	block, _ := pem.Decode(leaf.CertPEM)
	if block == nil {
		t.Fatal("no PEM block in leaf")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert
}

func newReq(leaf *x509.Certificate) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/site/trigger-build", nil)
	if leaf != nil {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	}
	return req
}

func TestMiddleware_NoCA_PassesThrough(t *testing.T) {
	called := false
	h := Middleware(func() *agentpki.CA { return nil }, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if rec := RecordFromContext(r.Context()); rec != nil {
			t.Errorf("unexpected agent record on no-CA path")
		}
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(nil))
	if !called {
		t.Fatal("next handler not invoked")
	}
	if w.Code != http.StatusOK {
		t.Errorf("got code=%d want 200", w.Code)
	}
}

func TestMiddleware_NoClientCert_PassesThrough(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h := Middleware(func() *agentpki.CA { return ca }, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if rec := RecordFromContext(r.Context()); rec != nil {
			t.Errorf("unexpected agent record on no-cert path")
		}
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(nil))
	if !called {
		t.Fatal("next handler not invoked")
	}
}

func TestMiddleware_NonAgentCert_PassesThrough(t *testing.T) {
	// Build an Agent CA AND a separate "Team-style" CA + leaf. The
	// leaf is signed by the team CA, not the agent CA, so the
	// middleware must let it through (no fingerprint lookup, no 401).
	agentCA, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	teamCA, err := agentpki.NewAgentCA(time.Hour) // wrong-issuer stand-in
	if err != nil {
		t.Fatal(err)
	}
	teamLeaf := mintAgentLeaf(t, teamCA, "x")

	lookupCalled := false
	lookup := func(_ context.Context, _ string) (*registry.Record, error) {
		lookupCalled = true
		return nil, registry.ErrNotFound
	}
	called := false
	h := Middleware(func() *agentpki.CA { return agentCA }, lookup, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(teamLeaf))
	if !called {
		t.Fatal("next handler not invoked")
	}
	if lookupCalled {
		t.Errorf("registry lookup ran for non-agent cert — should have skipped")
	}
}

func TestMiddleware_AgentCert_NotInRegistry_401(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mintAgentLeaf(t, ca, "abc")

	lookup := func(_ context.Context, _ string) (*registry.Record, error) {
		return nil, registry.ErrNotFound
	}
	called := false
	h := Middleware(func() *agentpki.CA { return ca }, lookup, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(leaf))
	if called {
		t.Fatal("next handler ran on a 401 path")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got code=%d want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "agent not registered") {
		t.Errorf("body=%q wanted agent-not-registered reason", w.Body.String())
	}
}

func TestMiddleware_AgentCert_Revoked_401(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mintAgentLeaf(t, ca, "abc")

	lookup := func(_ context.Context, _ string) (*registry.Record, error) {
		return &registry.Record{
			ID:         "abc",
			ExpiresAt:  time.Now().Add(time.Hour),
			Revoked:    true,
			CertSHA256: registry.FingerprintFromCert(leaf),
		}, nil
	}
	h := Middleware(func() *agentpki.CA { return ca }, lookup, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called for revoked agent")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(leaf))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got code=%d want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "revoked") {
		t.Errorf("body=%q wanted 'revoked'", w.Body.String())
	}
}

func TestMiddleware_AgentCert_Expired_401(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mintAgentLeaf(t, ca, "abc")
	now := time.Now()
	lookup := func(_ context.Context, _ string) (*registry.Record, error) {
		return &registry.Record{
			ID:         "abc",
			ExpiresAt:  now.Add(-time.Minute), // already expired
			CertSHA256: registry.FingerprintFromCert(leaf),
		}, nil
	}
	h := Middleware(func() *agentpki.CA { return ca }, lookup, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called for expired agent")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(leaf))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got code=%d want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("body=%q wanted 'expired'", w.Body.String())
	}
}

func TestMiddleware_AgentCert_Active_AttachesRecord(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mintAgentLeaf(t, ca, "abc")
	rec := &registry.Record{
		ID:         "abc",
		ExpiresAt:  time.Now().Add(time.Hour),
		CertSHA256: registry.FingerprintFromCert(leaf),
		Permissions: map[string]map[string]string{
			"site1": {"build": ""},
		},
	}
	lookup := func(_ context.Context, fingerprint string) (*registry.Record, error) {
		if fingerprint != rec.CertSHA256 {
			return nil, errors.New("unexpected fingerprint")
		}
		return rec, nil
	}
	called := false
	h := Middleware(func() *agentpki.CA { return ca }, lookup, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got := RecordFromContext(r.Context())
		if got == nil {
			t.Fatal("expected agent record on context, got nil")
		}
		if got.ID != "abc" {
			t.Errorf("record ID got=%q want abc", got.ID)
		}
		if opts, ok := got.IsAllowed("site1", "build"); !ok || opts != "" {
			t.Errorf("IsAllowed(site1, build) got=(%q,%v) want=(\"\", true)", opts, ok)
		}
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(leaf))
	if !called {
		t.Fatal("next handler not invoked on active-agent path")
	}
	if w.Code != http.StatusOK {
		t.Errorf("got code=%d want 200", w.Code)
	}
}

func TestMiddleware_RegistryError_503(t *testing.T) {
	ca, err := agentpki.NewAgentCA(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := mintAgentLeaf(t, ca, "abc")
	lookup := func(_ context.Context, _ string) (*registry.Record, error) {
		return nil, errors.New("storage unavailable")
	}
	h := Middleware(func() *agentpki.CA { return ca }, lookup, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called when registry errors")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(leaf))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got code=%d want 503", w.Code)
	}
}
