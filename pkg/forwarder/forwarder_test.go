package forwarder

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAdapter is an in-memory Adapter that records calls and lets
// tests poke at consent gating + dispatch order.
type fakeAdapter struct {
	name      string
	purpose   ConsentPurpose
	sendErr   error
	deleteErr error

	mu        sync.Mutex
	sends     []Event
	deletes   []string
}

func (f *fakeAdapter) Name() string             { return f.name }
func (f *fakeAdapter) Purpose() ConsentPurpose  { return f.purpose }
func (f *fakeAdapter) Send(ctx context.Context, ev Event) error {
	f.mu.Lock()
	f.sends = append(f.sends, ev)
	f.mu.Unlock()
	return f.sendErr
}
func (f *fakeAdapter) Delete(ctx context.Context, serverID, visitorID string) error {
	f.mu.Lock()
	f.deletes = append(f.deletes, serverID+"|"+visitorID)
	f.mu.Unlock()
	return f.deleteErr
}

func TestComposite_ConsentGating(t *testing.T) {
	market := &fakeAdapter{name: "meta", purpose: PurposeMarketing}
	analytics := &fakeAdapter{name: "ga4", purpose: PurposeAnalytics}
	c := NewComposite(market, analytics)

	cases := []struct {
		name        string
		ev          Event
		wantMarket  int
		wantAnalyt int
	}{
		{"no consent → both skip",
			Event{ConsentAnalytics: false, ConsentMarketing: false}, 0, 0},
		{"analytics-only → analytics ships, marketing skipped",
			Event{ConsentAnalytics: true, ConsentMarketing: false}, 0, 1},
		{"marketing-only → marketing ships, analytics skipped",
			Event{ConsentAnalytics: false, ConsentMarketing: true}, 1, 0},
		{"both → both ship",
			Event{ConsentAnalytics: true, ConsentMarketing: true}, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			market.sends = nil
			analytics.sends = nil
			errs := c.Send(context.Background(), tc.ev)
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if got := len(market.sends); got != tc.wantMarket {
				t.Errorf("market sends: got %d want %d", got, tc.wantMarket)
			}
			if got := len(analytics.sends); got != tc.wantAnalyt {
				t.Errorf("analytics sends: got %d want %d", got, tc.wantAnalyt)
			}
		})
	}
}

func TestComposite_DeleteFanOut(t *testing.T) {
	a := &fakeAdapter{name: "a", purpose: PurposeMarketing}
	b := &fakeAdapter{name: "b", purpose: PurposeAnalytics}
	c := NewComposite(a, b)
	errs := c.Delete(context.Background(), "srv1", "v1")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(a.deletes) != 1 || len(b.deletes) != 1 {
		t.Fatalf("expected each adapter to receive one delete; got a=%d b=%d", len(a.deletes), len(b.deletes))
	}
	want := "srv1|v1"
	if a.deletes[0] != want || b.deletes[0] != want {
		t.Errorf("delete key mismatch: a=%s b=%s", a.deletes[0], b.deletes[0])
	}
}

func TestComposite_DeleteIgnoresConsent(t *testing.T) {
	// Even with no consent, deletion fans out — privacy floor, not gate.
	a := &fakeAdapter{name: "a", purpose: PurposeMarketing}
	c := NewComposite(a)
	c.Delete(context.Background(), "srv1", "v1")
	if len(a.deletes) != 1 {
		t.Errorf("delete must fire regardless of consent; got %d calls", len(a.deletes))
	}
}

func TestComposite_SendErrorsCollected(t *testing.T) {
	bad := &fakeAdapter{name: "bad", purpose: PurposeMarketing, sendErr: errors.New("nope")}
	good := &fakeAdapter{name: "good", purpose: PurposeMarketing}
	c := NewComposite(bad, good)
	errs := c.Send(context.Background(), Event{ConsentMarketing: true})
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d (%v)", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "nope") {
		t.Errorf("unexpected error text: %v", errs[0])
	}
	// Good adapter still ran.
	if len(good.sends) != 1 {
		t.Errorf("good adapter should run despite peer failure")
	}
}

func TestWorker_BackpressureDrops(t *testing.T) {
	// Adapter that blocks on a channel — fills the queue.
	block := make(chan struct{})
	a := &slowAdapter{block: block}
	c := NewComposite(a)
	w := NewWorker(c, 2)
	w.Start()
	defer func() { close(block); w.Stop() }()

	// Enqueue 5 events into a queue of size 2 with a slow adapter.
	// First few accepted; remainder dropped.
	accepted := 0
	dropped := 0
	for i := 0; i < 10; i++ {
		if w.Enqueue(Event{ConsentAnalytics: true, ConsentMarketing: true, EventID: "e"}) {
			accepted++
		} else {
			dropped++
		}
	}
	if dropped == 0 {
		t.Errorf("expected some drops under backpressure; got accepted=%d dropped=%d", accepted, dropped)
	}
	stats := w.Stats()
	if stats.Dropped == 0 {
		t.Errorf("Stats.Dropped should be > 0; got %d", stats.Dropped)
	}
}

type slowAdapter struct {
	block chan struct{}
	calls atomic.Int64
}

func (s *slowAdapter) Name() string                                       { return "slow" }
func (s *slowAdapter) Purpose() ConsentPurpose                             { return PurposeAnalytics }
func (s *slowAdapter) Send(ctx context.Context, ev Event) error {
	s.calls.Add(1)
	select {
	case <-s.block:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *slowAdapter) Delete(ctx context.Context, serverID, visitorID string) error { return nil }

// --- Adapter integration tests against an http-recorder sidecar ---

func TestMetaCAPI_Send(t *testing.T) {
	var (
		gotPath    string
		gotBody    string
		gotMethod  string
		gotCT      string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Hijack the API base via a custom http.Client + a Director-style
	// trick: the adapter constructs the URL from APIVersion + PixelID.
	// For this test we point the underlying HTTP client at our test
	// server by overriding the transport's RoundTrip.
	ad := NewMetaCAPI("PIX", "TOK", "TEST123", map[uint64]string{1: "PageView"})
	ad.HTTPClient = &http.Client{
		Transport: &rewriteTransport{base: srv.URL},
	}

	err := ad.Send(context.Background(), Event{
		EventCode: 1, // page_view
		EventID:   "ev1",
		VisitorID: "v1",
		URL:       "https://example.com/x",
		IP:        "1.2.3.4",
		UserAgent: "ua",
		Email:     "Test@Example.com  ",
		When:      time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method: got %s want POST", gotMethod)
	}
	if !strings.Contains(gotPath, "/v19.0/PIX/events") {
		t.Errorf("path: got %s; expected to contain /v19.0/PIX/events", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type: got %s", gotCT)
	}
	// Email should be normalised to lowercase + trimmed before SHA256.
	wantEmHash := sha256Hex("test@example.com")
	if !strings.Contains(gotBody, wantEmHash) {
		t.Errorf("body missing hashed email %s; body=%s", wantEmHash, gotBody)
	}
	if !strings.Contains(gotBody, `"event_name":"PageView"`) {
		t.Errorf("body missing PageView mapping: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"test_event_code":"TEST123"`) {
		t.Errorf("body missing test_event_code: %s", gotBody)
	}
}

func TestMetaCAPI_UnmappedEventIsSkipped(t *testing.T) {
	ad := NewMetaCAPI("PIX", "TOK", "", map[uint64]string{1: "PageView"})
	err := ad.Send(context.Background(), Event{EventCode: 999})
	if !errors.Is(err, ErrSkipped) {
		t.Errorf("unmapped event should return ErrSkipped, got %v", err)
	}
}

func TestGA4MP_Send(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(204)
	}))
	defer srv.Close()

	ad := NewGA4MP("MID", "SECRET", map[uint64]string{1: "page_view"})
	ad.Endpoint = srv.URL
	err := ad.Send(context.Background(), Event{
		EventCode: 1,
		VisitorID: "v1",
		URL:       "https://example.com/y",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(gotBody, `"client_id":"v1"`) {
		t.Errorf("body missing client_id: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"name":"page_view"`) {
		t.Errorf("body missing event name: %s", gotBody)
	}
}

// rewriteTransport rewrites every request URL to point at the test
// server, preserving path + query. Used for the Meta adapter test
// where the full URL is built from the API version + pixel id.
type rewriteTransport struct {
	base string
}

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with the test server's.
	idx := strings.Index(r.base, "://")
	if idx < 0 {
		return http.DefaultTransport.RoundTrip(req)
	}
	scheme := r.base[:idx]
	host := r.base[idx+3:]
	req.URL.Scheme = scheme
	req.URL.Host = host
	return http.DefaultTransport.RoundTrip(req)
}
