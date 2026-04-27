package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Both Turnstile and Recaptcha verifiers wrap the same wire shape:
// POST form-encoded {secret, response, remoteip}, parse a JSON
// response, return ErrInvalidToken on success=false. The verifiers
// expose a URL field for tests so we don't need to monkey-patch
// the upstream constants.

func TestTurnstileSuccessAndFailure(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"success", `{"success":true}`, nil},
		{"failure", `{"success":false,"error-codes":["invalid-input-response"]}`, ErrInvalidToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.Form.Get("secret") != "S" || r.Form.Get("response") != "TOK" {
					t.Errorf("unexpected form: secret=%q response=%q", r.Form.Get("secret"), r.Form.Get("response"))
				}
				if r.Form.Get("remoteip") != "1.2.3.4" {
					t.Errorf("remoteip not forwarded: got %q", r.Form.Get("remoteip"))
				}
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			tv := Turnstile{SecretKey: "S", URL: srv.URL, HTTP: srv.Client()}
			err := tv.Verify(context.Background(), "TOK", "1.2.3.4")
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("want errors.Is(%v), got %v", c.wantErr, err)
			}
		})
	}
}

func TestTurnstileMissingSecret(t *testing.T) {
	tv := Turnstile{}
	if err := tv.Verify(context.Background(), "TOK", ""); err == nil {
		t.Error("missing secret should error")
	}
	tv = Turnstile{SecretKey: "S"}
	if err := tv.Verify(context.Background(), "", ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("empty token should fail with ErrInvalidToken, got %v", err)
	}
}

func TestRecaptchaV3Score(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"v3 above threshold", `{"success":true,"score":0.9}`, nil},
		{"v3 below threshold", `{"success":true,"score":0.1}`, ErrInvalidToken},
		{"v2 (no score)", `{"success":true}`, nil},
		{"v2 failure", `{"success":false,"error-codes":["timeout-or-duplicate"]}`, ErrInvalidToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			rv := Recaptcha{SecretKey: "S", URL: srv.URL, HTTP: srv.Client()}
			err := rv.Verify(context.Background(), "TOK", "")
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("want errors.Is(%v), got %v", c.wantErr, err)
			}
		})
	}
}

func TestUpstream5xxIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	tv := Turnstile{SecretKey: "S", URL: srv.URL, HTTP: srv.Client()}
	err := tv.Verify(context.Background(), "TOK", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("want ErrUnavailable, got %v", err)
	}
}

func TestTestBypass(t *testing.T) {
	tb := TestBypass{}
	if err := tb.Verify(context.Background(), "anything", ""); err != nil {
		t.Errorf("non-empty token should pass, got %v", err)
	}
	if err := tb.Verify(context.Background(), "", ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("empty token should fail, got %v", err)
	}
}

func TestNamesForLogging(t *testing.T) {
	tv := Turnstile{}
	if tv.Name() != "turnstile" {
		t.Error("Turnstile.Name should be 'turnstile'")
	}
	rv := Recaptcha{}
	if rv.Name() != "recaptcha" {
		t.Error("Recaptcha.Name should be 'recaptcha'")
	}
	bv := TestBypass{}
	if bv.Name() != "test-bypass" {
		t.Error("TestBypass.Name should be 'test-bypass'")
	}
	// Defensive: Verifier interface is satisfied by all three.
	var _ Verifier = tv
	var _ Verifier = rv
	var _ Verifier = bv
	if !strings.HasPrefix(EnvTestBypassEnabled, "HULA_") {
		t.Errorf("env var %q lost HULA_ prefix", EnvTestBypassEnabled)
	}
}
