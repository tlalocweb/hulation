package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/ddns"
)

// fakeProvider records EnsureRecord calls onto a channel so tests can observe
// the updater loop deterministically (no sleeps).
type fakeProvider struct {
	ch chan ddns.Record
}

func (f *fakeProvider) EnsureRecord(ctx context.Context, rec ddns.Record) error {
	select {
	case f.ch <- rec:
	case <-ctx.Done():
	}
	return nil
}

func recvRecord(t *testing.T, ch chan ddns.Record) ddns.Record {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an EnsureRecord call")
		return ddns.Record{}
	}
}

// TestDDNSUpdater_BootAndTrigger verifies the two non-interval firing paths:
// (1) an unconditional upsert on startup, and (2) an upsert when the trigger
// channel fires (the SIGHUP path). The public IP changes between the two so the
// change-detection cache doesn't suppress the second upsert.
func TestDDNSUpdater_BootAndTrigger(t *testing.T) {
	var ipVal atomic.Value
	ipVal.Store("203.0.113.10")
	ipsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ipVal.Load().(string)))
	}))
	defer ipsrv.Close()

	fp := &fakeProvider{ch: make(chan ddns.Record, 1)}
	u := &ddnsUpdater{
		provider:   fp,
		plans:      []ddnsRecordPlan{{name: "app.example.com", host: "app.example.com", apiToken: "t", zoneID: "z", proxied: true, ttl: 1, publishV4: true}},
		v4Services: []string{ipsrv.URL},
		httpClient: ipsrv.Client(),
		interval:   time.Hour, // long, so only boot + trigger fire during the test
		needV4:     true,
		lastIP:     make(map[string]string),
	}

	trigger := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.run(ctx, trigger)

	// (1) startup upsert.
	if got := recvRecord(t, fp.ch); got.Content != "203.0.113.10" || got.Type != "A" {
		t.Fatalf("boot upsert: got %+v, want A 203.0.113.10", got)
	}

	// (2) change the IP and fire the trigger (SIGHUP path).
	ipVal.Store("203.0.113.20")
	trigger <- struct{}{}
	if got := recvRecord(t, fp.ch); got.Content != "203.0.113.20" {
		t.Fatalf("trigger upsert: got %+v, want content 203.0.113.20", got)
	}
}

// TestDDNSUpdater_NoUpsertWhenUnchanged confirms the in-memory cache suppresses
// a redundant provider call when the detected IP is identical to last publish.
func TestDDNSUpdater_NoUpsertWhenUnchanged(t *testing.T) {
	ipsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.10"))
	}))
	defer ipsrv.Close()

	fp := &fakeProvider{ch: make(chan ddns.Record, 4)}
	u := &ddnsUpdater{
		provider:   fp,
		plans:      []ddnsRecordPlan{{name: "app.example.com", apiToken: "t", publishV4: true}},
		v4Services: []string{ipsrv.URL},
		httpClient: ipsrv.Client(),
		needV4:     true,
		lastIP:     make(map[string]string),
	}
	ctx := context.Background()
	u.runOnce(ctx) // first: publishes
	if len(fp.ch) != 1 {
		t.Fatalf("first runOnce should upsert once, got %d", len(fp.ch))
	}
	<-fp.ch
	u.runOnce(ctx) // second: unchanged ⇒ no call
	if len(fp.ch) != 0 {
		t.Fatalf("second runOnce should be a no-op (unchanged), got %d calls", len(fp.ch))
	}
}

// TestBuildDDNSUpdater_PlansAndCascade covers plan construction: default record
// names, the cf_proxied:false resolution (the startup-warning path — we assert
// the resolved flag), and per-source credential resolution.
func TestBuildDDNSUpdater_PlansAndCascade(t *testing.T) {
	falseVal := false
	cfg := &config.Config{
		DDNS: &config.DDNSConfig{CfAPIToken: "glob-token", CfZoneID: "glob-zone"},
		Servers: []*config.Server{
			{Host: "app.example.com", ID: "app", DDNS: &config.DDNSRecordConfig{}},                     // default record = host
			{Host: "dns.example.com", ID: "dns", DDNS: &config.DDNSRecordConfig{CfProxied: &falseVal}}, // proxied=false
			{Host: "plain.example.com", ID: "plain"},                                                   // no ddns ⇒ ignored
		},
		Proxies: []*config.Proxy{
			{ByDomain: "cdn.example.com", Target: "http://127.0.0.1:9000", DDNS: &config.DDNSRecordConfig{}},
		},
	}

	u := buildDDNSUpdater(cfg)
	if u == nil {
		t.Fatal("expected a non-nil updater")
	}

	byName := map[string]ddnsRecordPlan{}
	for _, p := range u.plans {
		byName[p.name] = p
	}

	app, ok := byName["app.example.com"]
	if !ok {
		t.Fatal("expected a plan for app.example.com (default record = host)")
	}
	if app.apiToken != "glob-token" || app.zoneID != "glob-zone" {
		t.Fatalf("app creds not inherited from global: %+v", app)
	}
	if !app.proxied {
		t.Fatal("app should default proxied=true")
	}

	dns, ok := byName["dns.example.com"]
	if !ok {
		t.Fatal("expected a plan for dns.example.com")
	}
	if dns.proxied {
		t.Fatal("dns.example.com resolved cf_proxied should be false (DNS-only warning path)")
	}

	if _, ok := byName["cdn.example.com"]; !ok {
		t.Fatal("expected a proxy plan named by its by_domain")
	}
	if _, ok := byName["plain.example.com"]; ok {
		t.Fatal("a server without a ddns block must not produce a plan")
	}
}

// TestBuildDDNSUpdater_SkipWhenNoToken: a DDNS-enabled source with no resolvable
// token is skipped (non-fatal). With no other sources, buildDDNSUpdater returns
// nil rather than failing boot.
func TestBuildDDNSUpdater_SkipWhenNoToken(t *testing.T) {
	// Ensure no ambient CF_* env leaks a token into the cascade.
	t.Setenv("CF_API_TOKEN", "")
	cfg := &config.Config{
		Servers: []*config.Server{
			{Host: "app.example.com", ID: "notoken", DDNS: &config.DDNSRecordConfig{}},
		},
	}
	if u := buildDDNSUpdater(cfg); u != nil {
		t.Fatalf("expected nil updater when no token resolves, got %d plans", len(u.plans))
	}
}

// TestBuildDDNSUpdater_NoDDNS: no ddns blocks anywhere ⇒ nil updater.
func TestBuildDDNSUpdater_NoDDNS(t *testing.T) {
	cfg := &config.Config{Servers: []*config.Server{{Host: "a.example.com", ID: "a"}}}
	if u := buildDDNSUpdater(cfg); u != nil {
		t.Fatal("expected nil updater when no ddns configured")
	}
}
