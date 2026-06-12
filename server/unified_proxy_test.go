package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
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
