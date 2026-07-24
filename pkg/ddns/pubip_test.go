package ddns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stringServer returns an httptest server that replies with status+body.
func stringServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestDetectPublicIPv4_Failover: the first service 500s, the second returns
// garbage, the third returns a good IPv4 — detection walks to the good one.
func TestDetectPublicIPv4_Failover(t *testing.T) {
	bad500 := stringServer(t, 500, "nope")
	defer bad500.Close()
	garbage := stringServer(t, 200, "not-an-ip")
	defer garbage.Close()
	good := stringServer(t, 200, "203.0.113.7\n")
	defer good.Close()

	ip, err := DetectPublicIPv4(context.Background(), http.DefaultClient,
		[]string{bad500.URL, garbage.URL, good.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Fatalf("got %q, want 203.0.113.7", ip)
	}
}

// TestDetectPublicIPv4_RejectsV6: a service that returns an IPv6 address must be
// rejected by the v4 detector (family validation), failing over to the v4 one.
func TestDetectPublicIPv4_RejectsV6(t *testing.T) {
	v6 := stringServer(t, 200, "2001:db8::1")
	defer v6.Close()
	v4 := stringServer(t, 200, "198.51.100.9")
	defer v4.Close()

	ip, err := DetectPublicIPv4(context.Background(), http.DefaultClient, []string{v6.URL, v4.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "198.51.100.9" {
		t.Fatalf("got %q, want 198.51.100.9 (v6 answer must be rejected)", ip)
	}
}

// TestDetectPublicIPv6 validates the v6 detector accepts a canonical v6 and
// normalises it.
func TestDetectPublicIPv6(t *testing.T) {
	v6 := stringServer(t, 200, "2001:0db8:0000:0000:0000:0000:0000:0001")
	defer v6.Close()
	ip, err := DetectPublicIPv6(context.Background(), http.DefaultClient, []string{v6.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "2001:db8::1" {
		t.Fatalf("got %q, want compressed 2001:db8::1", ip)
	}
}

// TestDetectPublicIPv6_RejectsV4: v4 answer to a v6 request is rejected.
func TestDetectPublicIPv6_RejectsV4(t *testing.T) {
	v4 := stringServer(t, 200, "203.0.113.1")
	defer v4.Close()
	if _, err := DetectPublicIPv6(context.Background(), http.DefaultClient, []string{v4.URL}); err == nil {
		t.Fatal("expected error: a v4 answer must be rejected for a v6 request")
	}
}

// TestDetectPublicIP_AllFail: every service fails ⇒ error.
func TestDetectPublicIP_AllFail(t *testing.T) {
	bad1 := stringServer(t, 500, "x")
	defer bad1.Close()
	bad2 := stringServer(t, 200, "garbage")
	defer bad2.Close()
	if _, err := DetectPublicIPv4(context.Background(), http.DefaultClient, []string{bad1.URL, bad2.URL}); err == nil {
		t.Fatal("expected error when all services fail")
	}
}
