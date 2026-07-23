package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

// A proxy_only host forwards EVERY path — including hula's reserved service
// paths (/api, /v, /analytics, /hulastatus) and the root — to its upstream, and
// never lets hula's own routing layers get a look at the request. The host is
// claimed in proxyDispatch step 0, before hasRoute / the reserved-path defer.
func TestProxyOnlyForwardsAllPathsToUpstream(t *testing.T) {
	var got []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "upstream")
	}))
	defer upstream.Close()

	proxyOnly := compileProxyOnlyHosts([]*config.Server{
		{Host: "proxy.example.com", ProxyOnly: true, ProxyPass: upstream.URL},
	})
	if proxyOnly == nil {
		t.Fatal("expected a proxy_only registry")
	}

	// hasRoute must NEVER be consulted for a proxy_only host — the reserved
	// service paths belong to the upstream, not to hula, for this vhost.
	hasRoute := func(*http.Request) bool {
		t.Fatal("hasRoute must not be consulted for a proxy_only host")
		return false
	}

	paths := []string{"/api/v1/whoami", "/v/hello", "/analytics", "/hulastatus", "/"}
	for _, path := range paths {
		req := httptest.NewRequest("GET", "https://proxy.example.com"+path, nil)
		req.Host = "proxy.example.com"
		rec := httptest.NewRecorder()
		// nil blockCheck = bad-actor disabled; routes nil = no top-level proxies.
		if !proxyDispatch(nil, proxyOnly, nil, nil, hasRoute, rec, req) {
			t.Fatalf("%s: proxy_only host must be handled by the proxy dispatch", path)
		}
		if rec.Body.String() != "upstream" {
			t.Fatalf("%s: got body %q, want upstream (reserved handler wrongly claimed it?)", path, rec.Body.String())
		}
	}
	if len(got) != len(paths) {
		t.Fatalf("upstream received %d requests, want %d: %v", len(got), len(paths), got)
	}
	for i, p := range paths {
		if got[i] != p {
			t.Errorf("upstream path[%d] = %q, want %q", i, got[i], p)
		}
	}
}

// A proxy_only host also claims its aliases (case-insensitively).
func TestProxyOnlyClaimsAliases(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "upstream")
	}))
	defer upstream.Close()

	proxyOnly := compileProxyOnlyHosts([]*config.Server{
		{Host: "proxy.example.com", Aliases: []string{"alt.example.com"}, ProxyOnly: true, ProxyPass: upstream.URL},
	})

	req := httptest.NewRequest("GET", "https://alt.example.com/x", nil)
	req.Host = "ALT.example.com:8443" // mixed case + port must still match
	rec := httptest.NewRecorder()
	if !proxyDispatch(nil, proxyOnly, nil, nil, func(*http.Request) bool { return false }, rec, req) {
		t.Fatal("alias of a proxy_only host must be proxied")
	}
	if rec.Body.String() != "upstream" {
		t.Fatalf("got %q, want upstream", rec.Body.String())
	}
}

// A NON-proxy host is unaffected: proxyDispatch falls through (returns false) so
// hula's own handlers serve /api, /v, /analytics, /hulastatus, and /.
func TestProxyOnlyLeavesOtherHostsToHula(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not receive requests for a non-proxy host")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxyOnly := compileProxyOnlyHosts([]*config.Server{
		{Host: "proxy.example.com", ProxyOnly: true, ProxyPass: upstream.URL},
	})

	for _, path := range []string{"/api/v1/whoami", "/v/hello", "/analytics", "/hulastatus", "/"} {
		req := httptest.NewRequest("GET", "https://hula.example.com"+path, nil)
		req.Host = "hula.example.com"
		rec := httptest.NewRecorder()
		if proxyDispatch(nil, proxyOnly, nil, nil, func(*http.Request) bool { return false }, rec, req) {
			t.Fatalf("%s: non-proxy host must fall through to hula, not be proxied", path)
		}
	}
}

// Bad-actor enforcement runs BEFORE the upstream for a proxy_only host: when the
// gate says "block", the upstream is never contacted and the request aborts
// silently (http.ErrAbortHandler), matching the legacy h2handler path.
func TestProxyOnlyBadActorBlockedBeforeUpstream(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxyOnly := compileProxyOnlyHosts([]*config.Server{
		{Host: "proxy.example.com", ProxyOnly: true, ProxyPass: upstream.URL},
	})

	// Stub blocker → the request must be dropped before reaching the upstream.
	blocked := func(*http.Request) bool { return true }
	func() {
		defer func() {
			if rvr := recover(); rvr != http.ErrAbortHandler {
				t.Fatalf("expected http.ErrAbortHandler panic, got %v", rvr)
			}
		}()
		req := httptest.NewRequest("GET", "https://proxy.example.com/anything", nil)
		req.Host = "proxy.example.com"
		proxyDispatch(nil, proxyOnly, blocked, nil, func(*http.Request) bool { return false }, httptest.NewRecorder(), req)
		t.Fatal("proxyDispatch should have aborted before returning")
	}()
	if upstreamHits != 0 {
		t.Fatalf("upstream contacted despite bad-actor block (hits=%d)", upstreamHits)
	}

	// Control: when the gate allows, the request IS forwarded.
	allow := func(*http.Request) bool { return false }
	req := httptest.NewRequest("GET", "https://proxy.example.com/anything", nil)
	req.Host = "proxy.example.com"
	if !proxyDispatch(nil, proxyOnly, allow, nil, func(*http.Request) bool { return false }, httptest.NewRecorder(), req) {
		t.Fatal("allowed request should be proxied")
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits=%d, want 1 after an allowed request", upstreamHits)
	}
}

func TestCompileProxyOnlyHosts(t *testing.T) {
	// nil registry when nothing is proxy_only.
	if m := compileProxyOnlyHosts([]*config.Server{{Host: "a.test"}, nil}); m != nil {
		t.Fatalf("expected nil registry, got %v", m)
	}

	// Host + aliases register (lowercased); a non-proxy server is ignored; an
	// invalid proxy_pass is skipped defensively rather than panicking.
	m := compileProxyOnlyHosts([]*config.Server{
		{Host: "P.Example.com", Aliases: []string{"WWW.Example.com", "alt.example.com"}, ProxyOnly: true, ProxyPass: "http://127.0.0.1:9000"},
		{Host: "plain.test"},
		{Host: "bad.test", ProxyOnly: true, ProxyPass: "not-a-url"},
	})
	for _, want := range []string{"p.example.com", "www.example.com", "alt.example.com"} {
		if _, ok := m[want]; !ok {
			t.Errorf("registry missing lowercased host %q", want)
		}
	}
	if _, ok := m["plain.test"]; ok {
		t.Error("non-proxy server must not be in the registry")
	}
	if _, ok := m["bad.test"]; ok {
		t.Error("server with invalid proxy_pass must be skipped")
	}
}
