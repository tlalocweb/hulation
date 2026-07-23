package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tlalocweb/hulation/config"
)

// newWSEchoUpstream builds an httptest.Server that upgrades every request to a
// WebSocket and echoes each frame back verbatim (same message type + payload),
// until the peer closes. This is the "real app behind the proxy_only vhost".
func newWSEchoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{
		// The gorilla client's default dialer sends no Origin header, so the
		// default same-origin check would already pass; make it explicit so the
		// test never depends on that detail.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upstream upgrade failed: %v", err)
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return // normal close / peer gone
			}
			if err := c.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
}

// wsHostPort turns an httptest.Server URL (http://127.0.0.1:PORT) into a ws://
// dial URL with the given path.
func wsDialURL(t *testing.T, serverURL, path string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL %q: %v", serverURL, err)
	}
	return "ws://" + u.Host + path
}

// assertBidirectionalEcho dials proxyURL with a gorilla WS client, sends two
// DISTINCT text frames in sequence, and asserts each one round-trips back
// identically. Sending a second frame after the first has returned proves the
// tunnel stays open and is genuinely bidirectional (not a one-shot).
func assertBidirectionalEcho(t *testing.T, proxyURL string) {
	t.Helper()
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 5 * time.Second

	c, resp, err := dialer.Dial(proxyURL, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %q through proxy failed: %v (handshake status=%d)", proxyURL, err, code)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status=%d, want 101 Switching Protocols", resp.StatusCode)
	}
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i, want := range []string{"hello over the proxy", "second frame, still open"} {
		if err := c.WriteMessage(websocket.TextMessage, []byte(want)); err != nil {
			t.Fatalf("frame %d: write: %v", i, err)
		}
		mt, got, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("frame %d: read echo: %v", i, err)
		}
		if mt != websocket.TextMessage {
			t.Errorf("frame %d: echo message type=%d, want TextMessage(%d)", i, mt, websocket.TextMessage)
		}
		if string(got) != want {
			t.Errorf("frame %d: echo=%q, want %q", i, got, want)
		}
	}
}

// TestProxyOnlyWebSocketPassthrough proves that a proxy_only virtual host cleanly
// proxies a WebSocket upgrade to its upstream and that frames round-trip
// bidirectionally. newPlainProxy is a stdlib httputil.ReverseProxy, which has
// supported the Connection: Upgrade / 101 Switching Protocols dance since Go
// 1.12; this exercises it end-to-end.
//
// Two variants share one echo upstream:
//
//   - "newPlainProxy" drives the reverse proxy directly (the handler
//     compileProxyOnlyHosts builds for each vhost).
//   - "proxyDispatch" drives the FULL step-0 proxy_only path — the same layer
//     that runs in production — with proxyOnly populated and nil blockCheck +
//     nil recordStats. This confirms the dispatch layer forwards the upgrade and
//     that the PR-3 server-side-stats seam does NOT interfere with a WS upgrade
//     (its page-nav filter already skips Upgrade requests; here we assert the
//     upgrade + echo still work through the dispatch regardless).
//
// Infra-free: httptest + gorilla only, no ClickHouse.
func TestProxyOnlyWebSocketPassthrough(t *testing.T) {
	upstream := newWSEchoUpstream(t)
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	t.Run("newPlainProxy", func(t *testing.T) {
		// The reverse proxy under test, in front of the WS echo upstream.
		proxy := httptest.NewServer(newPlainProxy(target))
		defer proxy.Close()

		assertBidirectionalEcho(t, wsDialURL(t, proxy.URL, "/ws"))
	})

	t.Run("proxyDispatch", func(t *testing.T) {
		// The proxy_only registry is keyed on the (port-stripped, lowercased)
		// Host. httptest dials 127.0.0.1:PORT, so key the vhost on "127.0.0.1"
		// for hostOnly(r.Host) to match in proxyDispatch step 0.
		proxyOnly := compileProxyOnlyHosts([]*config.Server{
			{Host: "127.0.0.1", ProxyOnly: true, ProxyPass: upstream.URL},
		})
		if proxyOnly == nil {
			t.Fatal("expected a proxy_only registry")
		}
		// hasRoute must never be consulted for a proxy_only host; a real WS
		// upgrade path would otherwise be a tempting thing to "claim".
		hasRoute := func(*http.Request) bool {
			t.Error("hasRoute must not be consulted for a proxy_only host")
			return false
		}
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// nil blockCheck (bad-actor disabled) + nil recordStats (no stats
			// pipeline): the WS upgrade must still forward and echo cleanly.
			if !proxyDispatch(nil, proxyOnly, nil, nil, hasRoute, w, r) {
				t.Errorf("proxy_only host must be handled by proxyDispatch (path %q)", r.URL.Path)
				http.Error(w, "not dispatched", http.StatusInternalServerError)
			}
		}))
		defer proxy.Close()

		assertBidirectionalEcho(t, wsDialURL(t, proxy.URL, "/ws/chat"))
	})
}

// Guard: the dial URL helper produces a ws:// scheme (a plain http:// dial would
// make gorilla reject the handshake before it reaches the proxy).
func TestWSDialURLScheme(t *testing.T) {
	got := wsDialURL(t, "http://127.0.0.1:8080", "/x")
	if !strings.HasPrefix(got, "ws://127.0.0.1:8080/") {
		t.Fatalf("wsDialURL=%q, want ws://127.0.0.1:8080/…", got)
	}
}
