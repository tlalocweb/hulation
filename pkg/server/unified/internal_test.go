package unified

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"strings"
	"testing"
)

func TestIsInternalSNI(t *testing.T) {
	cases := []struct {
		name string
		sni  string
		want bool
	}{
		{"team internal node", "node-east.team.internal", true},
		{"team internal scoped", "team-uuid/node-east.team.internal", true},
		{"team internal uppercase", "NODE-east.Team.Internal", true},
		{"public hostname", "www.example.com", false},
		{"empty", "", false},
		{"close miss", "team.internal.example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsInternalSNI(c.sni); got != c.want {
				t.Errorf("IsInternalSNI(%q) = %v, want %v", c.sni, got, c.want)
			}
		})
	}
}

func TestEnableInternalChannel_RejectsBadInputs(t *testing.T) {
	s := &Server{
		logger: unifiedLogger,
		httpServer: &http.Server{
			TLSConfig: &tls.Config{},
		},
	}
	pool := x509.NewCertPool()
	leaf := &tls.Certificate{}

	if err := s.EnableInternalChannel(nil, leaf); err == nil {
		t.Error("expected error on nil teamCAPool")
	}
	if err := s.EnableInternalChannel(pool, nil); err == nil {
		t.Error("expected error on nil leaf")
	}
	if err := s.EnableInternalChannel(pool, leaf); err != nil {
		t.Fatalf("first enable: %v", err)
	}
	if err := s.EnableInternalChannel(pool, leaf); err == nil {
		t.Error("expected error on second enable (idempotency guard)")
	}
}

func TestGetConfigForClient_PublicSNI_FallsThrough(t *testing.T) {
	s := newServerForGate(t)
	cfg, err := s.getConfigForClient(&tls.ClientHelloInfo{ServerName: "www.example.com"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil (base config) for public SNI, got %+v", cfg)
	}
}

func TestGetConfigForClient_InternalSNI_RequiresClientCert(t *testing.T) {
	s := newServerForGate(t)
	cfg, err := s.getConfigForClient(&tls.ClientHelloInfo{ServerName: "node-east.team.internal"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected per-client config for internal SNI, got nil")
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth got=%v want=%v", cfg.ClientAuth, tls.RequireAndVerifyClientCert)
	}
	if cfg.ClientCAs != s.teamCAPool {
		t.Error("ClientCAs not set to teamCAPool")
	}
	if cfg.GetConfigForClient != nil {
		t.Error("nested GetConfigForClient must be cleared to avoid loops")
	}
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate not wired")
	}
	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != s.teamLeaf {
		t.Error("GetCertificate did not return the configured leaf")
	}
}

func TestDispatchInternalGRPC_PathPrefix(t *testing.T) {
	s := newServerForGate(t)
	if !s.dispatchInternalGRPC("/hulation.v1.internalapi.MembershipService/Join") {
		t.Error("expected dispatch=true for internal path")
	}
	if s.dispatchInternalGRPC("/hulation.v1.auth.AuthService/WhoAmI") {
		t.Error("expected dispatch=false for non-internal path")
	}
	if !strings.HasPrefix(InternalServicePathPrefix, "/") {
		t.Error("InternalServicePathPrefix must start with /")
	}
}

func newServerForGate(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		logger:     unifiedLogger,
		httpServer: &http.Server{TLSConfig: &tls.Config{}},
	}
	pool := x509.NewCertPool()
	leaf := &tls.Certificate{}
	if err := s.EnableInternalChannel(pool, leaf); err != nil {
		t.Fatalf("EnableInternalChannel: %v", err)
	}
	return s
}
