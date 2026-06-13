package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

// The relay signs over method+path, so the proxy MUST forward the path (and
// query) byte-for-byte — no prefix stripping like the backend-container proxy.
func TestPlainProxyPreservesPathAndQuery(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	target, _ := url.Parse(backend.URL)
	proxy := newPlainProxy(target)

	req := httptest.NewRequest(http.MethodPost, "https://relay.example.com/v1/push/send?collapse=abc", nil)
	req.Host = "relay.example.com"
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if gotPath != "/v1/push/send" {
		t.Fatalf("path not preserved: got %q, want /v1/push/send", gotPath)
	}
	if gotQuery != "collapse=abc" {
		t.Fatalf("query not preserved: got %q, want collapse=abc", gotQuery)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method: got %q", gotMethod)
	}
}

func TestPlainProxyForwardsOriginalHostAndProto(t *testing.T) {
	var xfHost, xfProto string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xfHost = r.Header.Get("X-Forwarded-Host")
		xfProto = r.Header.Get("X-Forwarded-Proto")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	target, _ := url.Parse(backend.URL)
	req := httptest.NewRequest(http.MethodGet, "https://relay.example.com/healthz", nil)
	req.Host = "relay.example.com"
	rec := httptest.NewRecorder()
	newPlainProxy(target).ServeHTTP(rec, req)

	if xfHost != "relay.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want relay.example.com", xfHost)
	}
	if xfProto != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", xfProto)
	}
}

func TestCompileProxyRoutes(t *testing.T) {
	cases := []struct {
		name              string
		in                *config.Proxy
		wantN             int
		wantDom, wantPath string // checked when wantN == 1
	}{
		{"valid by_domain", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay.example.com"}, 1, "relay.example.com", ""},
		{"by_domain normalises case+port", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "Relay.Example.com:443"}, 1, "relay.example.com", ""},
		{"valid by_path", &config.Proxy{Target: "http://127.0.0.1:8088", ByPath: "/relay"}, 1, "", "/relay"},
		{"by_domain as URL rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "https://relay.example.com"}, 0, "", ""},
		{"by_domain with path rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay.example.com/x"}, 0, "", ""},
		{"by_domain port-only rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: ":443"}, 0, "", ""},
		{"by_domain empty-brackets rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "[]"}, 0, "", ""},
		{"by_domain scheme-no-slashes rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "https:relay.example.com"}, 0, "", ""},
		{"by_domain non-numeric port rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay.example.com:abc"}, 0, "", ""},
		{"by_domain unbracketed IPv6 rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "::1"}, 0, "", ""},
		{"by_domain internal tab rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay\t.example.com"}, 0, "", ""},
		{"by_domain userinfo rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "user@relay.example.com"}, 0, "", ""},
		{"by_domain query rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay.example.com?x=1"}, 0, "", ""},
		{"by_domain fragment rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "relay.example.com#f"}, 0, "", ""},
		{"by_domain bracketed IPv6 accepted", &config.Proxy{Target: "http://127.0.0.1:8088", ByDomain: "[2001:db8::1]:8443"}, 1, "2001:db8::1", ""},
		{"target with path rejected", &config.Proxy{Target: "http://127.0.0.1:8088/foo", ByDomain: "relay.example.com"}, 0, "", ""},
		{"target with query rejected", &config.Proxy{Target: "http://127.0.0.1:8088?x=1", ByDomain: "relay.example.com"}, 0, "", ""},
		{"by_path without slash rejected", &config.Proxy{Target: "http://127.0.0.1:8088", ByPath: "relay"}, 0, "", ""},
		{"empty target skipped", &config.Proxy{ByDomain: "relay.example.com"}, 0, "", ""},
		{"neither domain nor path skipped", &config.Proxy{Target: "http://127.0.0.1:8088"}, 0, "", ""},
		{"invalid target rejected", &config.Proxy{Target: "127.0.0.1:8088", ByDomain: "relay.example.com"}, 0, "", ""},
		{"non-http scheme rejected", &config.Proxy{Target: "ftp://127.0.0.1:8088", ByDomain: "relay.example.com"}, 0, "", ""},
		{"ws scheme rejected", &config.Proxy{Target: "ws://127.0.0.1:8088", ByDomain: "relay.example.com"}, 0, "", ""},
		{"scheme without host rejected", &config.Proxy{Target: "http://", ByDomain: "relay.example.com"}, 0, "", ""},
		{"target with userinfo rejected", &config.Proxy{Target: "http://user:pass@127.0.0.1:8088", ByDomain: "relay.example.com"}, 0, "", ""},
		{"https target accepted", &config.Proxy{Target: "https://relay.internal:8443", ByDomain: "relay.example.com"}, 1, "relay.example.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := compileProxyRoutes([]*config.Proxy{tc.in})
			if len(routes) != tc.wantN {
				t.Fatalf("got %d routes, want %d", len(routes), tc.wantN)
			}
			if tc.wantN == 1 {
				if routes[0].byDomain != tc.wantDom {
					t.Errorf("byDomain = %q, want %q", routes[0].byDomain, tc.wantDom)
				}
				if routes[0].byPath != tc.wantPath {
					t.Errorf("byPath = %q, want %q", routes[0].byPath, tc.wantPath)
				}
			}
		})
	}
	// A nil entry is skipped without panicking.
	if n := len(compileProxyRoutes([]*config.Proxy{nil})); n != 0 {
		t.Errorf("nil entry: got %d routes, want 0", n)
	}
}

func TestProxyDispatchPrecedence(t *testing.T) {
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		})
	}
	domainRoute := &proxyRoute{byDomain: "relay.example.com", handler: marker("domain")}
	pathRoute := &proxyRoute{byPath: "/relay", handler: marker("path")}

	t.Run("by_domain wins regardless of config order", func(t *testing.T) {
		req := httptest.NewRequest("GET", "https://relay.example.com/relay/x", nil)
		req.Host = "relay.example.com"
		rec := httptest.NewRecorder()
		// path route listed FIRST and also matches — by_domain must still win.
		handled := proxyDispatch([]*proxyRoute{pathRoute, domainRoute}, func(*http.Request) bool { return false }, rec, req)
		if !handled || rec.Body.String() != "domain" {
			t.Fatalf("handled=%v body=%q, want domain", handled, rec.Body.String())
		}
	})

	t.Run("by_path defers to a hula service route", func(t *testing.T) {
		req := httptest.NewRequest("GET", "https://other.test/relay/x", nil)
		req.Host = "other.test"
		rec := httptest.NewRecorder()
		handled := proxyDispatch([]*proxyRoute{pathRoute}, func(*http.Request) bool { return true }, rec, req)
		if handled {
			t.Fatal("by_path must not handle when hula has a route (hasRoute=true)")
		}
	})

	t.Run("by_path handles when no hula route matches", func(t *testing.T) {
		req := httptest.NewRequest("GET", "https://other.test/relay/x", nil)
		req.Host = "other.test"
		rec := httptest.NewRecorder()
		handled := proxyDispatch([]*proxyRoute{pathRoute}, func(*http.Request) bool { return false }, rec, req)
		if !handled || rec.Body.String() != "path" {
			t.Fatalf("handled=%v body=%q, want path", handled, rec.Body.String())
		}
	})

	t.Run("no match falls through", func(t *testing.T) {
		req := httptest.NewRequest("GET", "https://nope.test/other", nil)
		req.Host = "nope.test"
		rec := httptest.NewRecorder()
		if proxyDispatch([]*proxyRoute{domainRoute, pathRoute}, func(*http.Request) bool { return false }, rec, req) {
			t.Fatal("expected no route to match")
		}
	})
}

func TestProxyDispatchLongestPrefix(t *testing.T) {
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		})
	}
	short := &proxyRoute{byPath: "/relay", handler: marker("short")}
	long := &proxyRoute{byPath: "/relay/v1", handler: marker("long")}

	// The most specific prefix must win regardless of config order.
	for _, order := range [][]*proxyRoute{{short, long}, {long, short}} {
		req := httptest.NewRequest("GET", "https://x.test/relay/v1/push", nil)
		rec := httptest.NewRecorder()
		if proxyDispatch(order, func(*http.Request) bool { return false }, rec, req); rec.Body.String() != "long" {
			t.Fatalf("order %v: got %q, want long (most specific prefix)", order, rec.Body.String())
		}
	}
	// A path under only the short prefix still routes there.
	req := httptest.NewRequest("GET", "https://x.test/relay/other", nil)
	rec := httptest.NewRecorder()
	if proxyDispatch([]*proxyRoute{long, short}, func(*http.Request) bool { return false }, rec, req); rec.Body.String() != "short" {
		t.Fatalf("got %q, want short", rec.Body.String())
	}
}

func TestProxyDispatchReservesServicePaths(t *testing.T) {
	marker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "proxy")
	})
	// A catch-all by_path proxy must still defer hula's reserved service paths
	// (via staticPassthrough) even when HasRoute doesn't claim them, so it can't
	// shadow present/future REST/admin endpoints.
	catchAll := []*proxyRoute{{byPath: "/", handler: marker}}
	never := func(*http.Request) bool { return false }
	for _, reserved := range []string{"/api/health", "/scripts/x.js", "/analytics", "/v/foo"} {
		req := httptest.NewRequest("GET", "https://hula.example.com"+reserved, nil)
		rec := httptest.NewRecorder()
		if proxyDispatch(catchAll, never, rec, req) {
			t.Errorf("%s: by_path proxy must defer to hula's reserved service path", reserved)
		}
	}
	// A non-reserved path is still proxied.
	req := httptest.NewRequest("GET", "https://hula.example.com/custom/thing", nil)
	rec := httptest.NewRecorder()
	if !proxyDispatch(catchAll, never, rec, req) || rec.Body.String() != "proxy" {
		t.Errorf("non-reserved path should be proxied, got body=%q", rec.Body.String())
	}
}

func TestMatchesHostPortAndIPv6(t *testing.T) {
	// Config carrying a stray :port still matches (normalised via hostOnly).
	rt := &proxyRoute{byDomain: hostOnly("Relay.Example.com:443")}
	if rt.byDomain != "relay.example.com" {
		t.Fatalf("config not normalised: %q", rt.byDomain)
	}
	for _, host := range []string{"relay.example.com", "relay.example.com:8443"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		if !rt.matchesHost(req) {
			t.Errorf("host %q should match", host)
		}
	}
	// Bracketed IPv6 with and without port must not be truncated by a naive
	// LastIndex(":") split.
	v6 := &proxyRoute{byDomain: hostOnly("[2001:db8::1]")}
	if v6.byDomain != "2001:db8::1" {
		t.Fatalf("IPv6 config not normalised: %q", v6.byDomain)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "[2001:db8::1]:8443"
	if !v6.matchesHost(req) {
		t.Error("bracketed IPv6 host with port should match")
	}
}

// hulation owns the host/proto headers and strips the RFC 7239 Forwarded
// header, so a client can't influence them regardless of what it sends.
func TestPlainProxyHostProtoNotClientControlled(t *testing.T) {
	var xfHost, xfProto, forwarded string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xfHost = r.Header.Get("X-Forwarded-Host")
		xfProto = r.Header.Get("X-Forwarded-Proto")
		forwarded = r.Header.Get("Forwarded")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	target, _ := url.Parse(backend.URL)
	req := httptest.NewRequest(http.MethodGet, "https://relay.example.com/healthz", nil)
	req.Host = "relay.example.com"
	// Client tries to spoof host/proto via both header families.
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("Forwarded", "for=1.2.3.4;host=evil.example.com;proto=http")
	rec := httptest.NewRecorder()
	newPlainProxy(target).ServeHTTP(rec, req)

	if xfHost != "relay.example.com" {
		t.Errorf("X-Forwarded-Host = %q, want relay.example.com (must reflect the real edge host)", xfHost)
	}
	if xfProto != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https (must reflect the real scheme)", xfProto)
	}
	if forwarded != "" {
		t.Errorf("Forwarded = %q, want empty (RFC 7239 header must be stripped)", forwarded)
	}
}

// Client-IP forwarding mirrors the backend proxy: X-Forwarded-For / X-Real-IP
// collapse to a single derived peer IP — RemoteAddr for a direct client, or the
// first upstream X-Forwarded-For hop when hulation is behind a trusted CDN, so
// the real client IP survives.
func TestPlainProxyClientIPForwarding(t *testing.T) {
	var xff, xRealIP string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xff = r.Header.Get("X-Forwarded-For")
		xRealIP = r.Header.Get("X-Real-IP")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)

	// X-Real-IP is the clean single derived value; X-Forwarded-For has the real
	// client as its first hop (ReverseProxy appends the immediate RemoteAddr,
	// same as backend/proxy.go).

	// (a) direct client, no upstream XFF → RemoteAddr is the client.
	req := httptest.NewRequest(http.MethodGet, "https://relay.example.com/healthz", nil)
	req.Host = "relay.example.com"
	req.RemoteAddr = "192.0.2.7:5555"
	newPlainProxy(target).ServeHTTP(httptest.NewRecorder(), req)
	if xRealIP != "192.0.2.7" {
		t.Errorf("direct: X-Real-IP=%q, want 192.0.2.7 (from RemoteAddr)", xRealIP)
	}
	if !strings.HasPrefix(xff, "192.0.2.7") {
		t.Errorf("direct: X-Forwarded-For=%q, want it to start with 192.0.2.7", xff)
	}

	// (b) behind a front proxy/CDN: the first upstream XFF hop is the real client.
	req2 := httptest.NewRequest(http.MethodGet, "https://relay.example.com/healthz", nil)
	req2.Host = "relay.example.com"
	req2.RemoteAddr = "203.0.113.9:443" // the CDN
	req2.Header.Set("X-Forwarded-For", "198.51.100.5, 203.0.113.9")
	newPlainProxy(target).ServeHTTP(httptest.NewRecorder(), req2)
	if xRealIP != "198.51.100.5" {
		t.Errorf("behind CDN: X-Real-IP=%q, want 198.51.100.5 (real client preserved)", xRealIP)
	}
	if !strings.HasPrefix(xff, "198.51.100.5") {
		t.Errorf("behind CDN: X-Forwarded-For=%q, want it to start with 198.51.100.5", xff)
	}
}

func TestProxyRouteMatchesHost(t *testing.T) {
	rt := &proxyRoute{byDomain: "relay.example.com"}
	cases := map[string]bool{
		"relay.example.com":      true,
		"Relay.Example.com":      true, // case-insensitive
		"relay.example.com:8443": true, // port ignored
		"other.example.com":      false,
		"example.com":            false,
	}
	for host, want := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		if got := rt.matchesHost(req); got != want {
			t.Errorf("matchesHost(%q) = %v, want %v", host, got, want)
		}
	}
	// Empty byDomain matches any host.
	any := &proxyRoute{}
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "whatever.test"
	if !any.matchesHost(req) {
		t.Error("empty byDomain should match any host")
	}
}

func TestProxyRouteMatchesPath(t *testing.T) {
	rt := &proxyRoute{byPath: "/relay"}
	cases := map[string]bool{
		"/relay":            true, // exact
		"/relay/v1/push":    true, // under prefix
		"/relay/":           true,
		"/relayed":          false, // not a path segment boundary
		"/other":            false,
		"/v1/relay":         false,
	}
	for p, want := range cases {
		req := httptest.NewRequest("GET", "https://x.test"+p, nil)
		if got := rt.matchesPath(req); got != want {
			t.Errorf("matchesPath(%q) = %v, want %v", p, got, want)
		}
	}
	// Trailing-slash byPath normalises the same way.
	rt2 := &proxyRoute{byPath: "/relay/"}
	req := httptest.NewRequest("GET", "https://x.test/relay", nil)
	if !rt2.matchesPath(req) {
		t.Error("byPath '/relay/' should match '/relay'")
	}
	// Empty byPath matches any path.
	any := &proxyRoute{}
	req3 := httptest.NewRequest("GET", "https://x.test/anything", nil)
	if !any.matchesPath(req3) {
		t.Error("empty byPath should match any path")
	}
}
