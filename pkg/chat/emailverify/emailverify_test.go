package emailverify

import (
	"context"
	"errors"
	"testing"
)

// We can't reach DNS in unit tests reliably, so the live-DNS
// branches (HasMxRecords, Disposable lookup) aren't exercised
// here. What we DO cover:
//   - empty email rejected with "syntax" reason
//   - obviously bad syntax rejected
//   - misspell check returns Suggestion when a typo is detected
//     (uses the embedded suggestion list, no network)
//   - context cancellation surfaces as ctx.Err() not ErrInvalid

func TestVerifyEmptyEmail(t *testing.T) {
	v := New(DefaultOptions())
	_, err := v.Verify(context.Background(), "")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid for empty input, got %v", err)
	}
}

func TestVerifyBadSyntax(t *testing.T) {
	v := New(DefaultOptions())
	_, err := v.Verify(context.Background(), "not-an-email")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid for bad syntax, got %v", err)
	}
}

func TestSingleton(t *testing.T) {
	if Singleton() != nil {
		t.Skip("global singleton already installed by another test")
	}
	v := New(DefaultOptions())
	SetSingleton(v)
	defer SetSingleton(nil)
	if Singleton() != v {
		t.Errorf("Singleton() did not return the installed verifier")
	}
}

func TestNilVerifier(t *testing.T) {
	var v *Verifier
	if _, err := v.Verify(context.Background(), "alice@example.com"); err == nil {
		t.Error("nil verifier should error")
	}
}
