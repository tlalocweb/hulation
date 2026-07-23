package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/config"
)

// --- Test sinks ------------------------------------------------------------

type nopSink struct{}

func (nopSink) write(statsPageView) {}

type chanSink struct{ ch chan statsPageView }

func (c chanSink) write(pv statsPageView) { c.ch <- pv }

func navReq(method, target string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// evalPageView is the PURE page-nav filter + field extractor. These cases run
// entirely in-memory (no config/storage/DB).
func TestEvalPageViewFilter(t *testing.T) {
	html := map[string]string{"Accept": "text/html,application/xhtml+xml"}

	cases := []struct {
		name       string
		method     string
		target     string
		headers    map[string]string
		wantRecord bool
		wantPath   string
	}{
		{"page nav with html accept", "GET", "https://proxy.example.com/about", html, true, "/about"},
		{"page nav empty accept non-asset", "GET", "https://proxy.example.com/docs/guide", nil, true, "/docs/guide"},
		{"root path", "GET", "https://proxy.example.com/", html, true, "/"},
		{"asset .js beats accept", "GET", "https://proxy.example.com/static/app.js", html, false, ""},
		{"asset .css", "GET", "https://proxy.example.com/style.css", nil, false, ""},
		{"asset .png", "GET", "https://proxy.example.com/img/logo.png", html, false, ""},
		{"asset .woff2", "GET", "https://proxy.example.com/f/font.woff2", nil, false, ""},
		{"non-GET POST", "POST", "https://proxy.example.com/submit", html, false, ""},
		{"xhr json accept", "GET", "https://proxy.example.com/api/data", map[string]string{"Accept": "application/json"}, false, ""},
		{"websocket via Upgrade header", "GET", "https://proxy.example.com/ws", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Accept": "text/html"}, false, ""},
		{"websocket via Connection token list", "GET", "https://proxy.example.com/ws", map[string]string{"Connection": "keep-alive, Upgrade", "Accept": "text/html"}, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pv, ok := evalPageView(navReq(tc.method, tc.target, tc.headers))
			if ok != tc.wantRecord {
				t.Fatalf("recorded=%v, want %v", ok, tc.wantRecord)
			}
			if !tc.wantRecord {
				return
			}
			if pv.host != "proxy.example.com" {
				t.Errorf("host=%q, want proxy.example.com", pv.host)
			}
			if pv.urlPath != tc.wantPath {
				t.Errorf("urlPath=%q, want %q", pv.urlPath, tc.wantPath)
			}
			if pv.method != serverSideStatsMethod {
				t.Errorf("method=%q, want %q (tag distinct from beacon hits)", pv.method, serverSideStatsMethod)
			}
		})
	}
}

// The reconstructed URL honours X-Forwarded-Proto and preserves the query.
func TestEvalPageViewURLReconstruction(t *testing.T) {
	r := navReq("GET", "http://proxy.example.com/p/x?y=2", map[string]string{
		"Accept":            "text/html",
		"X-Forwarded-Proto": "https",
	})
	pv, ok := evalPageView(r)
	if !ok {
		t.Fatal("expected a recorded pageview")
	}
	if want := "https://proxy.example.com/p/x?y=2"; pv.url != want {
		t.Fatalf("url=%q, want %q", pv.url, want)
	}
}

// enqueue is non-blocking and DROPS (never blocks) when the buffer is full.
// No workers are started, so nothing drains the queue: if enqueue could block,
// this test would hang instead of completing.
func TestStatsPipelineEnqueueDropsWhenFull(t *testing.T) {
	const buf = 4
	p := newStatsPipeline(nopSink{}, buf)

	accepted, dropped := 0, 0
	for i := 0; i < 10; i++ {
		if p.enqueue(statsPageView{host: "h"}) {
			accepted++
		} else {
			dropped++
		}
	}
	if accepted != buf {
		t.Errorf("accepted=%d, want %d (buffer cap)", accepted, buf)
	}
	if dropped != 10-buf {
		t.Errorf("dropped=%d, want %d", dropped, 10-buf)
	}
	if got := p.enqueued.Load(); got != uint64(buf) {
		t.Errorf("enqueued counter=%d, want %d", got, buf)
	}
	if got := p.dropped.Load(); got != uint64(10-buf) {
		t.Errorf("dropped counter=%d, want %d", got, 10-buf)
	}
}

// The recorder has NO effect on the forwarded response: the proxied body,
// status, and upstream headers are identical whether the recorder is wired
// (on) or nil (off). The capturing sink also proves the recorder actually ran
// end-to-end through proxyDispatch.
func TestProxyOnlyRecorderNoSideEffect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus) // 207 — a distinctive status
		_, _ = io.WriteString(w, "hello-from-upstream")
	}))
	defer upstream.Close()

	proxyOnly := compileProxyOnlyHosts([]*config.Server{
		{Host: "proxy.example.com", ProxyOnly: true, ProxyPass: upstream.URL},
	})

	// extractIPFromRequest reads the global config; an empty config is enough
	// (no Cloudflare ranges → falls back to RemoteAddr) and keeps it nil-safe.
	config.SetConfigForTesting(&config.Config{})
	defer config.SetConfigForTesting(nil)

	captured := make(chan statsPageView, 4)
	p := newStatsPipeline(chanSink{captured}, 8)
	p.start(2)
	defer p.stop()
	resolve := func(string) (string, string, bool) { return "srv1", "proxy.example.com", true }
	recorder := func(r *http.Request) { recordProxyPageview(p, resolve, r) }

	doReq := func(rec func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://proxy.example.com/about?x=1", nil)
		req.Host = "proxy.example.com"
		req.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		if !proxyDispatch(nil, proxyOnly, nil, rec, func(*http.Request) bool { return false }, w, req) {
			t.Fatal("proxy_only host must be handled by the dispatch")
		}
		return w
	}

	off := doReq(nil)     // recorder OFF
	on := doReq(recorder) // recorder ON

	if off.Code != on.Code {
		t.Errorf("status differs: off=%d on=%d", off.Code, on.Code)
	}
	if off.Body.String() != on.Body.String() {
		t.Errorf("body differs: off=%q on=%q", off.Body.String(), on.Body.String())
	}
	for _, h := range []string{"X-Upstream", "Content-Type"} {
		if off.Header().Get(h) != on.Header().Get(h) {
			t.Errorf("header %q differs: off=%q on=%q", h, off.Header().Get(h), on.Header().Get(h))
		}
	}
	// Sanity: the proxied response really is the upstream's.
	if on.Body.String() != "hello-from-upstream" || on.Code != http.StatusMultiStatus {
		t.Fatalf("unexpected proxied response: code=%d body=%q", on.Code, on.Body.String())
	}

	// The recorder ran and enqueued exactly the expected pageview.
	select {
	case pv := <-captured:
		if pv.serverID != "srv1" {
			t.Errorf("serverID=%q, want srv1", pv.serverID)
		}
		if pv.host != "proxy.example.com" {
			t.Errorf("host=%q, want proxy.example.com", pv.host)
		}
		if pv.urlPath != "/about" {
			t.Errorf("urlPath=%q, want /about", pv.urlPath)
		}
		if pv.method != serverSideStatsMethod {
			t.Errorf("method=%q, want %q", pv.method, serverSideStatsMethod)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recorder ON did not capture a pageview")
	}

	// Recorder OFF must not have produced a second pageview.
	select {
	case pv := <-captured:
		t.Fatalf("unexpected extra capture: %+v", pv)
	case <-time.After(100 * time.Millisecond):
	}
}

// The cookieless visitor id is stable for the same (IP, UA) within a day and
// differs across IPs — the request-only identity used when a proxied app can't
// round-trip hula's analytics cookie.
func TestDeriveCookielessVisitorID(t *testing.T) {
	salt := bytes.Repeat([]byte{0x2a}, 32) // visitorid.SaltLen

	id1, ok := deriveCookielessVisitorID(salt, "203.0.113.7", "UA/1.0")
	if !ok || id1 == "" {
		t.Fatalf("derive failed: ok=%v id=%q", ok, id1)
	}
	id1b, _ := deriveCookielessVisitorID(salt, "203.0.113.7", "UA/1.0")
	if id1 != id1b {
		t.Errorf("same (ip,ua,day) must be stable: %q != %q", id1, id1b)
	}

	id2, _ := deriveCookielessVisitorID(salt, "198.51.100.9", "UA/1.0")
	if id1 == id2 {
		t.Errorf("different IPs must yield different ids (%q)", id1)
	}

	// A wrong-length salt is rejected (ok=false) rather than producing a weak id.
	if _, ok := deriveCookielessVisitorID([]byte("short"), "203.0.113.7", "UA/1.0"); ok {
		t.Error("expected derive to fail for a non-32-byte salt")
	}
}
