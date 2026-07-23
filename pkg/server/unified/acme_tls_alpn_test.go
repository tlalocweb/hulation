package unified

import (
	"crypto/tls"
	"testing"
)

// stubGetCert returns a canned certificate and records whether it was
// invoked, so tests can assert the dynamic (ACME) getter was or was not
// consulted during certificate selection.
func stubGetCert(cert *tls.Certificate) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), *bool) {
	called := new(bool)
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		*called = true
		return cert, nil
	}, called
}

// TestNewServer_ACMETLSALPN_NextProtos verifies the flag controls whether
// "acme-tls/1" is advertised in the TLS handshake's ALPN list.
func TestNewServer_ACMETLSALPN_NextProtos(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		wantACMEALP bool
	}{
		{"flag set advertises acme-tls/1", true, true},
		{"flag unset omits acme-tls/1", false, false},
	}
	dummy, _ := stubGetCert(&tls.Certificate{})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, err := NewServer(&Config{
				Address:        "127.0.0.1:0",
				GetCertificate: dummy,
				ACMETLSALPN:    c.enabled,
			})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			protos := srv.httpServer.TLSConfig.NextProtos
			if got := containsACMETLSALPN(protos); got != c.wantACMEALP {
				t.Errorf("NextProtos %v: contains acme-tls/1 = %v, want %v", protos, got, c.wantACMEALP)
			}
			// h2 + http/1.1 must always be advertised for gRPC/HTTP.
			if !containsProto(protos, "h2") || !containsProto(protos, "http/1.1") {
				t.Errorf("NextProtos %v missing baseline h2/http-1.1 protocols", protos)
			}
		})
	}
}

func containsProto(protos []string, want string) bool {
	for _, p := range protos {
		if p == want {
			return true
		}
	}
	return false
}

// TestSelectCertificate_ACMEChallenge_NotShadowedByHostCert proves a
// TLS-ALPN-01 challenge ClientHello is routed to the dynamic (ACME) getter
// even when a per-host static cert is ALSO registered for that SNI. Without
// the early branch, the per-host cert would shadow the challenge and ACME
// issuance would stall.
func TestSelectCertificate_ACMEChallenge_NotShadowedByHostCert(t *testing.T) {
	hostCert := &tls.Certificate{} // Cloudflare Origin CA / static cert for the host
	acmeCert := &tls.Certificate{} // sentinel returned by the ACME getter
	getter, called := stubGetCert(acmeCert)

	s := &Server{
		logger:         unifiedLogger,
		hostCerts:      map[string]*tls.Certificate{"example.com": hostCert},
		dynamicGetCert: getter,
		acmeTLSALPN:    true,
	}

	hello := &tls.ClientHelloInfo{
		ServerName:      "example.com",
		SupportedProtos: []string{acmeTLSALPNProto},
	}
	got, err := s.selectCertificate(hello)
	if err != nil {
		t.Fatalf("selectCertificate: %v", err)
	}
	if !*called {
		t.Fatal("dynamic (ACME) getter was not consulted for acme-tls/1 challenge")
	}
	if got != acmeCert {
		t.Errorf("got host/static cert, want ACME challenge cert (per-host cert shadowed the challenge)")
	}
}

// TestSelectCertificate_NormalTraffic_UsesHostCert proves ordinary
// (non-challenge) ClientHellos are unaffected: the per-host static cert
// still wins and the dynamic (ACME) getter is not consulted, even with the
// acme-tls/1 flag enabled.
func TestSelectCertificate_NormalTraffic_UsesHostCert(t *testing.T) {
	cases := []struct {
		name string
		alpn []string
	}{
		{"browser/cloudflare alpn", []string{"h2", "http/1.1"}},
		{"no alpn", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hostCert := &tls.Certificate{}
			getter, called := stubGetCert(&tls.Certificate{})
			s := &Server{
				logger:         unifiedLogger,
				hostCerts:      map[string]*tls.Certificate{"example.com": hostCert},
				dynamicGetCert: getter,
				acmeTLSALPN:    true,
			}
			hello := &tls.ClientHelloInfo{
				ServerName:      "example.com",
				SupportedProtos: c.alpn,
			}
			got, err := s.selectCertificate(hello)
			if err != nil {
				t.Fatalf("selectCertificate: %v", err)
			}
			if *called {
				t.Error("dynamic (ACME) getter consulted for normal traffic; static host cert should have won")
			}
			if got != hostCert {
				t.Errorf("got %p, want per-host static cert %p", got, hostCert)
			}
		})
	}
}

// TestSelectCertificate_ACMEChallenge_NilGetter guards against a panic when
// a challenge arrives but no dynamic getter is configured: it must surface a
// clear error instead.
func TestSelectCertificate_ACMEChallenge_NilGetter(t *testing.T) {
	s := &Server{
		logger:      unifiedLogger,
		hostCerts:   map[string]*tls.Certificate{},
		acmeTLSALPN: true,
		// dynamicGetCert intentionally nil
	}
	hello := &tls.ClientHelloInfo{
		ServerName:      "example.com",
		SupportedProtos: []string{acmeTLSALPNProto},
	}
	got, err := s.selectCertificate(hello)
	if err == nil {
		t.Fatalf("expected error for acme-tls/1 challenge with nil getter, got cert %v", got)
	}
	if got != nil {
		t.Errorf("expected nil cert on error, got %v", got)
	}
}

// TestSelectCertificate_ACMEDisabled_IgnoresChallengeALPN proves the early
// branch is gated on the flag: with ACMETLSALPN disabled, an acme-tls/1
// ClientHello falls through to normal selection (per-host cert wins) rather
// than the dynamic getter.
func TestSelectCertificate_ACMEDisabled_IgnoresChallengeALPN(t *testing.T) {
	hostCert := &tls.Certificate{}
	getter, called := stubGetCert(&tls.Certificate{})
	s := &Server{
		logger:         unifiedLogger,
		hostCerts:      map[string]*tls.Certificate{"example.com": hostCert},
		dynamicGetCert: getter,
		acmeTLSALPN:    false, // flag off
	}
	hello := &tls.ClientHelloInfo{
		ServerName:      "example.com",
		SupportedProtos: []string{acmeTLSALPNProto},
	}
	got, err := s.selectCertificate(hello)
	if err != nil {
		t.Fatalf("selectCertificate: %v", err)
	}
	if *called {
		t.Error("dynamic getter consulted despite ACMETLSALPN disabled")
	}
	if got != hostCert {
		t.Errorf("got %p, want per-host static cert %p", got, hostCert)
	}
}
