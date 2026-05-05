package unified

import (
	"sync"
	"testing"
)

// recordedIncident is one captured RecordIncident call.
type recordedIncident struct {
	ip, category, reason string
	score                int
}

// fakeRecorder is a minimal IncidentRecorder for assertions.
type fakeRecorder struct {
	mu  sync.Mutex
	got []recordedIncident
}

func (r *fakeRecorder) RecordIncident(ip, category, reason string, score int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, recordedIncident{ip, category, reason, score})
}

func TestClassifyTLSError(t *testing.T) {
	cases := []struct {
		reason   string
		wantCat  string
		wantScore int
	}{
		// Mid-handshake EOF — port scan / probe drop.
		{"EOF", "tls_handshake_eof", 5},
		// Cipher fingerprinting — deliberate scanner behaviour.
		{
			"tls: no cipher suite supported by both client and server; client offered: [16 33 67]",
			"tls_no_cipher", 10,
		},
		{
			"remote error: tls: handshake failure",
			"tls_handshake_other", 10,
		},
		{
			"tls: first record does not look like a TLS handshake",
			"tls_handshake_other", 10,
		},
		// Catch-all — score lightly so unknown errors don't false-positive.
		{"some unfamiliar reason", "tls_handshake_other", 5},
	}
	for _, c := range cases {
		gotCat, gotScore := classifyTLSError(c.reason)
		if gotCat != c.wantCat || gotScore != c.wantScore {
			t.Errorf("classifyTLSError(%q) = (%q, %d), want (%q, %d)",
				c.reason, gotCat, gotScore, c.wantCat, c.wantScore)
		}
	}
}

func TestIncidentLogWriter_ParsesAndRecords(t *testing.T) {
	rec := &fakeRecorder{}
	w := &incidentLogWriter{
		loadRec:     func() IncidentRecorder { return rec },
		passthrough: nil, // skip stdlog passthrough in tests
	}

	// Two TLS handshake errors of different shapes, plus a non-matching
	// line that must NOT trigger scoring.
	lines := []string{
		"http: TLS handshake error from 20.65.193.34:35660: EOF\n",
		"http: TLS handshake error from 20.65.193.34:35674: tls: no cipher suite supported by both client and server; client offered: [16 33]\n",
		"http: Accept error: redirected HTTP to HTTPS; retrying in 5ms\n",
	}
	for _, l := range lines {
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if len(rec.got) != 2 {
		t.Fatalf("want 2 recorded incidents, got %d: %+v", len(rec.got), rec.got)
	}
	if rec.got[0].ip != "20.65.193.34" || rec.got[0].category != "tls_handshake_eof" || rec.got[0].score != 5 {
		t.Errorf("first incident: got %+v", rec.got[0])
	}
	if rec.got[1].ip != "20.65.193.34" || rec.got[1].category != "tls_no_cipher" || rec.got[1].score != 10 {
		t.Errorf("second incident: got %+v", rec.got[1])
	}
}

func TestIncidentLogWriter_NilRecorderIsSafe(t *testing.T) {
	w := &incidentLogWriter{
		loadRec:     func() IncidentRecorder { return nil },
		passthrough: nil,
	}
	if _, err := w.Write([]byte("http: TLS handshake error from 1.2.3.4:1234: EOF\n")); err != nil {
		t.Fatalf("Write with nil recorder must not error: %v", err)
	}
}

// IPv6 remotes are bracketed by net/http (e.g. "[2001:db8::1]:1234").
// The parser must strip the brackets/port and record only the host so
// scanner activity from v6 sources is still scored.
func TestIncidentLogWriter_IPv6Remote(t *testing.T) {
	rec := &fakeRecorder{}
	w := &incidentLogWriter{
		loadRec:     func() IncidentRecorder { return rec },
		passthrough: nil,
	}
	line := "http: TLS handshake error from [2001:db8::1]:35660: EOF\n"
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("want 1 recorded incident, got %d: %+v", len(rec.got), rec.got)
	}
	if rec.got[0].ip != "2001:db8::1" {
		t.Errorf("ipv6 host: got %q, want %q", rec.got[0].ip, "2001:db8::1")
	}
	if rec.got[0].category != "tls_handshake_eof" {
		t.Errorf("ipv6 category: got %q", rec.got[0].category)
	}
}
