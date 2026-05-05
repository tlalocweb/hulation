package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

// downstreamProbe records that the wrapped handler was invoked. CORS
// preflight responses must NOT call into downstream — that would
// route an OPTIONS into the gRPC gateway.
type downstreamProbe struct{ called bool }

func (d *downstreamProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.called = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func newCORSCfg(t *testing.T, hostCORS map[string]*config.CORSConfig, global config.CORSConfig) *config.Config {
	t.Helper()
	cfg := &config.Config{CORS: global}
	for host, cc := range hostCORS {
		cfg.Servers = append(cfg.Servers, &config.Server{
			Host: host,
			CORS: cc,
		})
	}
	return cfg
}

func runCORS(t *testing.T, cfg *config.Config, req *http.Request) (*http.Response, *downstreamProbe) {
	t.Helper()
	probe := &downstreamProbe{}
	mw := CORSMiddleware(cfg)
	rec := httptest.NewRecorder()
	mw(probe).ServeHTTP(rec, req)
	return rec.Result(), probe
}

func TestCORS_NoOriginPassthrough(t *testing.T) {
	cfg := newCORSCfg(t, nil, config.CORSConfig{AllowOrigins: "https://app.example.com"})
	req := httptest.NewRequest(http.MethodGet, "https://www.tlaloc.us/api/v1/whoami", nil)
	res, probe := runCORS(t, cfg, req)
	if !probe.called {
		t.Fatal("non-CORS request should pass through to downstream")
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("non-CORS request should not get Allow-Origin header, got %q", got)
	}
}

func TestCORS_AllowedOriginEchoed(t *testing.T) {
	cfg := newCORSCfg(t, map[string]*config.CORSConfig{
		"www.gravhl.com": {
			AllowOrigins: "https://www.gravhl.com,https://gravhl.com",
			AllowMethods: "POST,OPTIONS",
		},
	}, config.CORSConfig{})
	req := httptest.NewRequest(http.MethodPost, "https://www.gravhl.com/api/beta-signup", nil)
	req.Header.Set("Origin", "https://www.gravhl.com")
	res, probe := runCORS(t, cfg, req)
	if !probe.called {
		t.Fatal("simple request should reach downstream")
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://www.gravhl.com" {
		t.Errorf("Allow-Origin = %q, want exact echo", got)
	}
	if got := res.Header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary should include Origin, got %q", got)
	}
}

func TestCORS_DisallowedOriginPassesThroughWithoutHeaders(t *testing.T) {
	cfg := newCORSCfg(t, map[string]*config.CORSConfig{
		"www.gravhl.com": {AllowOrigins: "https://www.gravhl.com"},
	}, config.CORSConfig{})
	req := httptest.NewRequest(http.MethodPost, "https://www.gravhl.com/api/beta-signup", nil)
	req.Header.Set("Origin", "https://attacker.example.com")
	res, probe := runCORS(t, cfg, req)
	if !probe.called {
		t.Fatal("disallowed-origin request should still reach downstream (browser drops the response)")
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not get Allow-Origin, got %q", got)
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	cfg := newCORSCfg(t, map[string]*config.CORSConfig{
		"www.gravhl.com": {
			AllowOrigins: "https://www.gravhl.com",
			AllowMethods: "POST,OPTIONS",
			AllowHeaders: "Content-Type",
		},
	}, config.CORSConfig{})
	req := httptest.NewRequest(http.MethodOptions, "https://www.gravhl.com/api/beta-signup", nil)
	req.Header.Set("Origin", "https://www.gravhl.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	res, probe := runCORS(t, cfg, req)
	if probe.called {
		t.Fatal("preflight must NOT reach downstream")
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got != "POST,OPTIONS" {
		t.Errorf("Allow-Methods = %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("Allow-Headers = %q", got)
	}
	if got := res.Header.Get("Access-Control-Max-Age"); got == "" {
		t.Error("preflight should set Max-Age")
	}
}

func TestCORS_BareOptionsIsNotPreflight(t *testing.T) {
	// OPTIONS without Access-Control-Request-Method is a normal
	// CORS-tagged request (e.g. some gRPC-Web clients) — it should
	// NOT short-circuit.
	cfg := newCORSCfg(t, map[string]*config.CORSConfig{
		"www.gravhl.com": {AllowOrigins: "https://www.gravhl.com"},
	}, config.CORSConfig{})
	req := httptest.NewRequest(http.MethodOptions, "https://www.gravhl.com/api/x", nil)
	req.Header.Set("Origin", "https://www.gravhl.com")
	_, probe := runCORS(t, cfg, req)
	if !probe.called {
		t.Fatal("OPTIONS without preflight headers should pass through")
	}
}

func TestCORS_HostFallsBackToGlobal(t *testing.T) {
	cfg := newCORSCfg(t, nil, config.CORSConfig{AllowOrigins: "https://api.example.com"})
	req := httptest.NewRequest(http.MethodPost, "https://random.host/x", nil)
	req.Header.Set("Origin", "https://api.example.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://api.example.com" {
		t.Errorf("global fallback failed: Allow-Origin = %q", got)
	}
}

func TestCORS_HostHeaderWithPort(t *testing.T) {
	cfg := newCORSCfg(t, map[string]*config.CORSConfig{
		"www.gravhl.com": {AllowOrigins: "https://www.gravhl.com"},
	}, config.CORSConfig{})
	req := httptest.NewRequest(http.MethodPost, "https://www.gravhl.com:443/x", nil)
	req.Header.Set("Origin", "https://www.gravhl.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://www.gravhl.com" {
		t.Errorf("port stripping failed: Allow-Origin = %q", got)
	}
}

func TestCORS_AliasInheritsServerConfig(t *testing.T) {
	cfg := &config.Config{
		Servers: []*config.Server{
			{
				Host:    "www.gravhl.com",
				Aliases: []string{"gravhl.com"},
				CORS: &config.CORSConfig{
					AllowOrigins: "https://www.gravhl.com,https://gravhl.com",
				},
			},
		},
	}
	req := httptest.NewRequest(http.MethodPost, "https://gravhl.com/x", nil)
	req.Header.Set("Origin", "https://gravhl.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://gravhl.com" {
		t.Errorf("alias did not inherit CORS: Allow-Origin = %q", got)
	}
}

func TestCORS_WildcardWithCredentialsEchoesOrigin(t *testing.T) {
	// "*" + Allow-Credentials: true is invalid per spec — echo the
	// concrete origin instead so the browser accepts the response.
	cfg := newCORSCfg(t, nil, config.CORSConfig{
		AllowOrigins:     "*",
		AllowCredentials: true,
	})
	req := httptest.NewRequest(http.MethodGet, "https://anywhere/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("wildcard+creds should echo origin, got %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
}

func TestCORS_WildcardWithoutCredentialsReturnsStar(t *testing.T) {
	cfg := newCORSCfg(t, nil, config.CORSConfig{AllowOrigins: "*"})
	req := httptest.NewRequest(http.MethodGet, "https://anywhere/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("wildcard without creds should return *, got %q", got)
	}
}

func TestCORS_UnsafeAnyOriginEchoes(t *testing.T) {
	cfg := newCORSCfg(t, nil, config.CORSConfig{UnsafeAnyOrigin: true})
	req := httptest.NewRequest(http.MethodGet, "https://anywhere/x", nil)
	req.Header.Set("Origin", "https://attacker.example.com")
	res, _ := runCORS(t, cfg, req)
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://attacker.example.com" {
		t.Errorf("unsafe-any-origin should echo, got %q", got)
	}
}
