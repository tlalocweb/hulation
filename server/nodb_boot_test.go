package server

// Boot-path gating tests for no-DB mode (dbconfig: disabled). These exercise
// the DB-disabled gating decision without a real database or listener:
//
//   - The gating predicate (config.DBDisabled()) drives every no-DB guard.
//   - RegisterFallbackRoutes must NOT register the DB-backed visitor beacon
//     routes when the DB is disabled (a stray beacon would otherwise
//     nil-deref model.GetDB()), while the DB-free KEEP routes (/hulastatus,
//     /readyz, chat widget assets) stay registered.
//   - The proxy/static KEEP layers wire up from a DBDisabled config without
//     panicking.

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/server/unified"
)

// newTestUnifiedServer builds a listener-less unified.Server for route
// introspection. NewServer only constructs (no bind happens until Start), so
// this is infra-free.
func newTestUnifiedServer(t *testing.T) *unified.Server {
	t.Helper()
	cert, err := GenerateSelfSignedCert([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatalf("self-signed cert: %v", err)
	}
	srv, err := unified.NewServer(&unified.Config{
		Address: "127.0.0.1:0",
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cert, nil
		},
	})
	if err != nil {
		t.Fatalf("unified.NewServer: %v", err)
	}
	return srv
}

func hasRoute(srv *unified.Server, method, path string) bool {
	req := httptest.NewRequest(method, path, nil)
	return srv.HasRoute(req)
}

// TestDBDisabledGatingDecision pins the single predicate every no-DB gate
// keys off.
func TestDBDisabledGatingDecision(t *testing.T) {
	disabled := &config.Config{DBConfig: &config.DBConfig{Disabled: true}}
	if !disabled.DBDisabled() {
		t.Fatal("disabled config: DBDisabled()=false; want true")
	}
	enabled := &config.Config{DBConfig: &config.DBConfig{Host: "localhost", Port: 9000}}
	if enabled.DBDisabled() {
		t.Fatal("enabled config: DBDisabled()=true; want false")
	}
}

// TestRegisterFallbackRoutes_DBDisabled_SkipsVisitorBeacon verifies the
// no-DB gate in RegisterFallbackRoutes: the DB-backed visitor beacon +
// tracking-script routes are absent, while the DB-free KEEP routes remain.
func TestRegisterFallbackRoutes_DBDisabled_SkipsVisitorBeacon(t *testing.T) {
	prev := config.GetConfig()
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	cfg := &config.Config{
		DBConfig:                     &config.DBConfig{Disabled: true},
		VisitorPrefix:                "/v",
		PublishedHelloScriptFilename: "hello.js",
		Servers:                      []*config.Server{{Host: "static.test", ID: "static"}},
	}
	config.SetConfigForTesting(cfg)

	srv := newTestUnifiedServer(t)
	// Must not panic on the nil-DB path.
	RegisterFallbackRoutes(srv)

	// KEEP (DB-free) routes stay registered.
	if !hasRoute(srv, http.MethodGet, "/hulastatus") {
		t.Error("/hulastatus should be registered in no-DB mode")
	}
	if !hasRoute(srv, http.MethodGet, "/readyz") {
		t.Error("/readyz should be registered in no-DB mode")
	}
	// DB-backed visitor beacon + tracking script are gated OFF.
	if hasRoute(srv, http.MethodPost, "/v/hello") {
		t.Error("/v/hello should NOT be registered in no-DB mode (DB-backed beacon)")
	}
	if hasRoute(srv, http.MethodGet, "/scripts/hello.js") {
		t.Error("/scripts/hello.js should NOT be registered in no-DB mode")
	}
	// Legacy DB-backed admin routes are gated OFF.
	if hasRoute(srv, http.MethodPost, "/api/auth/login") {
		t.Error("/api/auth/login should NOT be registered in no-DB mode")
	}
}

// TestRegisterFallbackRoutes_DBEnabled_RegistersVisitorBeacon is the
// counterpart: with the DB enabled the same routes ARE registered, proving
// the gate (not some other condition) is what removes them.
func TestRegisterFallbackRoutes_DBEnabled_RegistersVisitorBeacon(t *testing.T) {
	prev := config.GetConfig()
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	cfg := &config.Config{
		DBConfig:                     &config.DBConfig{Host: "localhost", Port: 9000, DBName: "hula"},
		VisitorPrefix:                "/v",
		PublishedHelloScriptFilename: "hello.js",
		Servers:                      []*config.Server{{Host: "static.test", ID: "static"}},
	}
	config.SetConfigForTesting(cfg)

	srv := newTestUnifiedServer(t)
	RegisterFallbackRoutes(srv)

	if !hasRoute(srv, http.MethodPost, "/v/hello") {
		t.Error("/v/hello should be registered when the DB is enabled")
	}
	if !hasRoute(srv, http.MethodPost, "/api/auth/login") {
		t.Error("/api/auth/login should be registered when the DB is enabled")
	}
}

// TestKeepLayers_DBDisabled_NoPanic wires the proxy + static + backend KEEP
// layers from a DBDisabled config and asserts no nil-DB panic. These are the
// features that must keep working in no-DB mode.
func TestKeepLayers_DBDisabled_NoPanic(t *testing.T) {
	prev := config.GetConfig()
	t.Cleanup(func() { config.SetConfigForTesting(prev) })

	cfg := &config.Config{
		DBConfig: &config.DBConfig{Disabled: true},
		Servers: []*config.Server{
			{Host: "static.test", ID: "static", Root: t.TempDir()},
			{Host: "proxyonly.test", ID: "po", ProxyOnly: true, ProxyPass: "http://127.0.0.1:8081"},
		},
		Proxies: []*config.Proxy{
			{ByDomain: "cdn.test", Target: "http://127.0.0.1:9000"},
		},
	}
	config.SetConfigForTesting(cfg)

	srv := newTestUnifiedServer(t)
	// None of these may nil-deref model.GetDB() in no-DB mode.
	registerBackendProxies(srv, cfg)
	registerStaticSites(srv, cfg)
	registerProxies(srv, cfg)
}
