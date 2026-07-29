package server

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// gRPC needs HTTP/2 (for trailers) AND the application/grpc content type.
// Getting either half wrong misroutes traffic: too loose and ordinary HTTP/2
// browser traffic gets claimed by the gRPC route; too strict and real RPCs
// fall through to the web upstream and 404.
func TestIsGRPCRequest(t *testing.T) {
	cases := []struct {
		name        string
		proto       int
		contentType string
		want        bool
	}{
		{"h2 + application/grpc", 2, "application/grpc", true},
		{"h2 + grpc+proto subtype", 2, "application/grpc+proto", true},
		{"h2 + json is ordinary web traffic", 2, "application/json", false},
		{"h2 + no content type", 2, "", false},
		{"h1 cannot carry grpc", 1, "application/grpc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://api.example.com/pkg.Svc/M", nil)
			r.ProtoMajor = tc.proto
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if got := isGRPCRequest(r); got != tc.want {
				t.Fatalf("isGRPCRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

// grpc: true claims a whole host's gRPC namespace, so it must name one.
// Allowing it without by_domain would silently swallow gRPC for every host
// hula serves.
func TestCompileProxyRoutesRejectsGRPCWithoutDomain(t *testing.T) {
	routes := compileProxyRoutes([]*config.Proxy{
		{Target: "http://127.0.0.1:9001", GRPC: true},                              // no by_domain: dropped
		{Target: "http://127.0.0.1:9002", ByPath: "/x", GRPC: true},                // by_path only: dropped
		{Target: "http://127.0.0.1:9003", ByDomain: "api.example.com", GRPC: true}, // kept
	})
	if len(routes) != 1 {
		t.Fatalf("expected 1 compiled route, got %d", len(routes))
	}
	if !routes[0].grpc || routes[0].byDomain != "api.example.com" {
		t.Fatalf("unexpected route: %+v", routes[0])
	}
}

// The headline routing property: a gRPC request goes to the gRPC upstream even
// though a path route for the same host would otherwise match, while non-gRPC
// traffic on that host still follows the normal path rules. That split is what
// lets one hostname serve a web UI and a gRPC API from different upstreams.
func TestProxyDispatchRoutesGRPCAheadOfPathRules(t *testing.T) {
	var hitGRPC, hitWeb bool
	routes := []*proxyRoute{
		{byDomain: "cloud.example.com", grpc: true, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitGRPC = true
		})},
		{byDomain: "cloud.example.com", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitWeb = true
		})},
	}

	t.Run("grpc request bypasses path matching", func(t *testing.T) {
		hitGRPC, hitWeb = false, false
		req := httptest.NewRequest("POST", "https://cloud.example.com/mitosu.coordinator.auth.v1.AuthService/Login", nil)
		req.ProtoMajor = 2
		req.Header.Set("Content-Type", "application/grpc")
		if !proxyDispatch(routes, nil, nil, nil, nil, httptest.NewRecorder(), req) {
			t.Fatal("dispatch did not handle the gRPC request")
		}
		if !hitGRPC || hitWeb {
			t.Fatalf("wrong upstream: grpc=%v web=%v", hitGRPC, hitWeb)
		}
	})

	t.Run("ordinary request on the same host still follows path rules", func(t *testing.T) {
		hitGRPC, hitWeb = false, false
		req := httptest.NewRequest("GET", "https://cloud.example.com/dashboard", nil)
		if !proxyDispatch(routes, nil, nil, nil, nil, httptest.NewRecorder(), req) {
			t.Fatal("dispatch did not handle the web request")
		}
		if hitGRPC || !hitWeb {
			t.Fatalf("wrong upstream: grpc=%v web=%v", hitGRPC, hitWeb)
		}
	})

	t.Run("grpc on an unclaimed host is not swallowed", func(t *testing.T) {
		hitGRPC, hitWeb = false, false
		req := httptest.NewRequest("POST", "https://other.example.com/pkg.Svc/M", nil)
		req.ProtoMajor = 2
		req.Header.Set("Content-Type", "application/grpc")
		if proxyDispatch(routes, nil, nil, nil, nil, httptest.NewRecorder(), req) {
			t.Fatal("dispatch claimed a gRPC request for a host with no matching route")
		}
	})
}

// The actual defect this change fixes: the default transport speaks HTTP/1.1
// to an http:// upstream, so gRPC never reached the backend as HTTP/2 and its
// trailers — where the gRPC status lives — were lost.
//
// Serves a real h2c upstream and asserts the proxied request arrives as HTTP/2
// with `Te: trailers` intact, and that a response trailer survives the hop.
func TestPlainProxyCarriesGRPCOverH2C(t *testing.T) {
	var gotProto string
	var gotTE string

	h2s := &http2.Server{}
	backend := httptest.NewUnstartedServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Proto
		gotTE = r.Header.Get("Te")
		// Announce + set a trailer the way gRPC carries its status.
		w.Header().Set("Trailer", "Grpc-Status")
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Header().Set("Grpc-Status", "0")
	}), h2s))
	backend.Start()
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse backend url: %v", err)
	}
	proxy := newPlainProxy(target)

	// Front the proxy with an h2c server so the request reaches it as
	// HTTP/2 — matching how a real client talks to hula's TLS listener
	// after ALPN negotiates h2.
	front := httptest.NewUnstartedServer(h2c.NewHandler(proxy, h2s))
	front.Start()
	defer front.Close()

	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	c := &http.Client{Transport: tr}

	req, err := http.NewRequest("POST", front.URL+"/pkg.Svc/Method", strings.NewReader(""))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("Te", "trailers")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("proxied gRPC request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if gotProto != "HTTP/2.0" {
		t.Fatalf("upstream saw %q, want HTTP/2.0 — the proxy downgraded gRPC to HTTP/1.1", gotProto)
	}
	if !strings.Contains(gotTE, "trailers") {
		t.Fatalf("upstream saw Te=%q, want it to contain \"trailers\"", gotTE)
	}
	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Fatalf("trailer Grpc-Status=%q, want \"0\" — trailers did not survive the proxy hop", got)
	}
}
