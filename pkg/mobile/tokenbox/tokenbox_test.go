package tokenbox

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func makeKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	key := makeKey(t)
	plaintext := "apns-device-token-deadbeef1234"
	sealed, err := Seal(plaintext, key)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, []byte(plaintext)) {
		t.Fatal("sealed blob still contains plaintext")
	}
	got, err := Open(sealed, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plaintext {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestTamperDetection(t *testing.T) {
	key := makeKey(t)
	sealed, err := Seal("hello world", key)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Flip one bit in the middle.
	sealed[len(sealed)/2] ^= 0x80
	if _, err := Open(sealed, key); !errors.Is(err, ErrTampered) {
		t.Fatalf("expected ErrTampered, got %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	k1 := makeKey(t)
	k2 := makeKey(t)
	sealed, err := Seal("token", k1)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(sealed, k2); !errors.Is(err, ErrTampered) {
		t.Fatalf("expected ErrTampered with wrong key, got %v", err)
	}
}

func TestKeyLengthEnforced(t *testing.T) {
	short := make([]byte, 16)
	if _, err := Seal("x", short); err == nil {
		t.Fatal("expected error on 16-byte key")
	}
	long := make([]byte, 64)
	if _, err := Seal("x", long); err == nil {
		t.Fatal("expected error on 64-byte key")
	}
}
