package ddns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// cfMock is a minimal in-memory Cloudflare v4 API for tests. It records every
// request (method + path) and the Authorization header, and serves canned zones
// and DNS records.
type cfMock struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string // "METHOD path"
	authSeen []string
	zones    []cfZone
	records  []cfDNSRecord // existing records returned by GET
	created  []cfDNSRecordPayload
	patched  []cfDNSRecordPayload
	server   *httptest.Server
}

func newCFMock(t *testing.T) *cfMock {
	m := &cfMock{t: t}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *cfMock) ok(w http.ResponseWriter, result interface{}) {
	raw, _ := json.Marshal(result)
	env := cfAPIResponse{Success: true, Result: raw}
	_ = json.NewEncoder(w).Encode(env)
}

func (m *cfMock) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.calls = append(m.calls, r.Method+" "+r.URL.Path)
	m.authSeen = append(m.authSeen, r.Header.Get("Authorization"))
	m.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		m.ok(w, m.zones)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
		m.ok(w, m.records)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
		var p cfDNSRecordPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		m.mu.Lock()
		m.created = append(m.created, p)
		m.mu.Unlock()
		m.ok(w, cfDNSRecord{ID: "new1", Type: p.Type, Name: p.Name, Content: p.Content, Proxied: p.Proxied, TTL: p.TTL})
	case r.Method == http.MethodPatch:
		var p cfDNSRecordPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		m.mu.Lock()
		m.patched = append(m.patched, p)
		m.mu.Unlock()
		m.ok(w, cfDNSRecord{ID: "patched", Type: p.Type, Name: p.Name, Content: p.Content, Proxied: p.Proxied, TTL: p.TTL})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (m *cfMock) provider() *CloudflareProvider {
	return NewCloudflareProvider(WithAPIBase(m.server.URL), WithHTTPClient(m.server.Client()))
}

func (m *cfMock) hasCall(method, pathContains string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if strings.HasPrefix(c, method+" ") && strings.Contains(c, pathContains) {
			return true
		}
	}
	return false
}

// TestCloudflare_ZoneAutoResolve: with no ZoneID on the record, the provider
// lists zones and picks the longest-suffix match (example.com over com).
func TestCloudflare_ZoneAutoResolve(t *testing.T) {
	m := newCFMock(t)
	m.zones = []cfZone{{ID: "zc", Name: "com"}, {ID: "zexample", Name: "example.com"}}
	m.records = nil // create path

	err := m.provider().EnsureRecord(context.Background(), Record{
		Name: "app.example.com", Type: "A", Content: "203.0.113.5", Proxied: true, TTL: 1, APIToken: "tok",
	})
	if err != nil {
		t.Fatalf("EnsureRecord: %v", err)
	}
	if !m.hasCall(http.MethodGet, "/zones") {
		t.Fatal("expected a GET /zones for auto-resolve")
	}
	// The dns_records call must target the longest-suffix zone id.
	if !m.hasCall(http.MethodGet, "/zones/zexample/dns_records") {
		t.Fatalf("expected dns_records under zexample; calls=%v", m.calls)
	}
	if len(m.created) != 1 || m.created[0].Content != "203.0.113.5" {
		t.Fatalf("expected create with content 203.0.113.5, got %+v", m.created)
	}
}

// TestCloudflare_CreateWhenMissing: no existing record ⇒ POST with proxied+ttl,
// and the Bearer token is sent.
func TestCloudflare_CreateWhenMissing(t *testing.T) {
	m := newCFMock(t)
	m.records = nil

	err := m.provider().EnsureRecord(context.Background(), Record{
		Name: "app.example.com", Type: "AAAA", Content: "2001:db8::9", Proxied: false, TTL: 300, ZoneID: "z1", APIToken: "tok-abc",
	})
	if err != nil {
		t.Fatalf("EnsureRecord: %v", err)
	}
	if len(m.created) != 1 {
		t.Fatalf("expected 1 create, got %d", len(m.created))
	}
	c := m.created[0]
	if c.Type != "AAAA" || c.Content != "2001:db8::9" || c.Proxied != false || c.TTL != 300 {
		t.Fatalf("create payload wrong: %+v", c)
	}
	// ZoneID supplied ⇒ no /zones lookup.
	if m.hasCall(http.MethodGet, "/zones\n") || m.hasCall(http.MethodGet, "/zones?") {
		t.Fatal("should not list zones when ZoneID is supplied")
	}
	// Bearer header on every call.
	for _, a := range m.authSeen {
		if a != "Bearer tok-abc" {
			t.Fatalf("expected Bearer tok-abc, got %q", a)
		}
	}
}

// TestCloudflare_PatchWhenContentDiffers: existing record with a stale IP ⇒ PATCH.
func TestCloudflare_PatchWhenContentDiffers(t *testing.T) {
	m := newCFMock(t)
	m.records = []cfDNSRecord{{ID: "rec1", Type: "A", Name: "app.example.com", Content: "203.0.113.1", Proxied: true, TTL: 1}}

	err := m.provider().EnsureRecord(context.Background(), Record{
		Name: "app.example.com", Type: "A", Content: "203.0.113.99", Proxied: true, TTL: 1, ZoneID: "z1", APIToken: "t",
	})
	if err != nil {
		t.Fatalf("EnsureRecord: %v", err)
	}
	if len(m.patched) != 1 || m.patched[0].Content != "203.0.113.99" {
		t.Fatalf("expected PATCH to 203.0.113.99, got %+v", m.patched)
	}
	if !m.hasCall(http.MethodPatch, "/dns_records/rec1") {
		t.Fatalf("expected PATCH on rec1; calls=%v", m.calls)
	}
	if len(m.created) != 0 {
		t.Fatal("should not create when a record exists")
	}
}

// TestCloudflare_NoPatchWhenUnchanged: existing record already matches ⇒ GET
// only, no POST, no PATCH (the key efficiency guarantee).
func TestCloudflare_NoPatchWhenUnchanged(t *testing.T) {
	m := newCFMock(t)
	m.records = []cfDNSRecord{{ID: "rec1", Type: "A", Name: "app.example.com", Content: "203.0.113.5", Proxied: true, TTL: 1}}

	err := m.provider().EnsureRecord(context.Background(), Record{
		Name: "app.example.com", Type: "A", Content: "203.0.113.5", Proxied: true, TTL: 1, ZoneID: "z1", APIToken: "t",
	})
	if err != nil {
		t.Fatalf("EnsureRecord: %v", err)
	}
	if len(m.patched) != 0 || len(m.created) != 0 {
		t.Fatalf("expected no write when unchanged; created=%v patched=%v", m.created, m.patched)
	}
}

// TestPickZone unit-tests the longest-suffix matcher directly.
func TestPickZone(t *testing.T) {
	zones := []cfZone{{ID: "a", Name: "com"}, {ID: "b", Name: "example.com"}, {ID: "c", Name: "other.net"}}
	got, ok := pickZone("app.example.com", zones)
	if !ok || got.ID != "b" {
		t.Fatalf("got %+v ok=%v, want example.com (b)", got, ok)
	}
	if _, ok := pickZone("nope.invalid", zones); ok {
		t.Fatal("expected no match for nope.invalid")
	}
	// exact match on the zone apex.
	got, ok = pickZone("example.com", zones)
	if !ok || got.ID != "b" {
		t.Fatalf("apex: got %+v ok=%v", got, ok)
	}
}

// TestNewProvider covers provider selection.
func TestNewProvider(t *testing.T) {
	if _, err := NewProvider(""); err != nil {
		t.Fatalf("empty ⇒ cloudflare, got %v", err)
	}
	if _, err := NewProvider("cloudflare"); err != nil {
		t.Fatalf("cloudflare, got %v", err)
	}
	if _, err := NewProvider("route53"); err == nil {
		t.Fatal("unknown provider should error")
	}
}
