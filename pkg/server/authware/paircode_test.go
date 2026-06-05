package authware

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerateAndParseRoundTrip(t *testing.T) {
	for i := 0; i < 20; i++ {
		code, err := GeneratePairCode(nil)
		if err != nil {
			t.Fatalf("generate: %s", err)
		}
		if !strings.HasPrefix(code, PairCodePrefix) {
			t.Fatalf("missing prefix: %q", code)
		}
		// 9 prefix + 14 (4 + 1 + 4 + 1 + 4) = 23 chars total.
		if len(code) != len(PairCodePrefix)+14 {
			t.Fatalf("unexpected length: %q (%d)", code, len(code))
		}
		body, err := ParsePairCode(code)
		if err != nil {
			t.Fatalf("parse: %s", err)
		}
		if len(body) != 12 {
			t.Fatalf("body len: %d", len(body))
		}
		// Body characters all in alphabet.
		for _, c := range body {
			if !strings.ContainsRune(pairCodeAlphabet, c) {
				t.Fatalf("char %q not in alphabet (body=%q)", c, body)
			}
		}
	}
}

func TestParseRejectsBadChecksum(t *testing.T) {
	code, _ := GeneratePairCode(nil)
	// Flip the last character to a different alphabet char.
	asBytes := []byte(code)
	last := asBytes[len(asBytes)-1]
	var replacement byte = 'X'
	if replacement == last {
		replacement = 'Y'
	}
	asBytes[len(asBytes)-1] = replacement
	tampered := string(asBytes)
	if _, err := ParsePairCode(tampered); !errors.Is(err, ErrPairCodeChecksum) {
		t.Fatalf("expected ErrPairCodeChecksum, got %v", err)
	}
}

func TestParseRejectsWrongPrefix(t *testing.T) {
	if _, err := ParsePairCode("NOTHULA-PAIR-XXXX-YYYY-ZZZZ"); err == nil {
		t.Fatalf("expected error for wrong prefix")
	}
}

func TestParseRejectsAmbiguousChars(t *testing.T) {
	// Crockford-style alphabet omits 0 / 1 / I / O.
	for _, bad := range []string{"0", "1", "I", "O"} {
		s := PairCodePrefix + strings.Repeat(bad, 4) + "-AAAA-BBBB"
		if _, err := ParsePairCode(s); err == nil {
			t.Fatalf("expected error for ambiguous char %q (input %q)", bad, s)
		}
	}
}

func TestParseAcceptsLowercaseAndWhitespace(t *testing.T) {
	code, _ := GeneratePairCode(nil)
	lower := strings.ToLower("  " + code + "  ")
	if _, err := ParsePairCode(lower); err != nil {
		t.Fatalf("lowercase+whitespace should parse: %s", err)
	}
}

func TestPutConsumeRoundTrip(t *testing.T) {
	s := NewInMemoryPairCodeStore()
	now := time.Now().UTC()
	entry := PairCode{
		Code:      "HULA-PAIR-ABCD-EFGH-JKLM",
		UserID:    "marcus",
		ServerID:  "site-a",
		Label:     "Marcus iPhone",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Minute),
	}
	s.Put(entry)
	got, err := s.Consume(entry.Code, now)
	if err != nil {
		t.Fatalf("consume: %s", err)
	}
	if got.UserID != "marcus" || got.ServerID != "site-a" {
		t.Fatalf("entry round-trip: %+v", got)
	}
	// Single-use: a second consume must return not-found.
	if _, err := s.Consume(entry.Code, now); !errors.Is(err, ErrPairCodeNotFound) {
		t.Fatalf("second consume: want ErrPairCodeNotFound, got %v", err)
	}
}

func TestConsumeRejectsExpired(t *testing.T) {
	s := NewInMemoryPairCodeStore()
	t0 := time.Now().UTC()
	s.Put(PairCode{
		Code:      "HULA-PAIR-XXXX-YYYY-ZZZZ",
		ExpiresAt: t0.Add(time.Second),
	})
	if _, err := s.Consume("HULA-PAIR-XXXX-YYYY-ZZZZ", t0.Add(2*time.Second)); !errors.Is(err, ErrPairCodeNotFound) {
		t.Fatalf("expired consume: want ErrPairCodeNotFound, got %v", err)
	}
	// Also: the expired entry must be gone from the map (Consume deletes
	// even when returning not-found, so the issue-but-never-redeem path
	// can't grow it unbounded).
	if _, err := s.Consume("HULA-PAIR-XXXX-YYYY-ZZZZ", t0.Add(2*time.Second)); !errors.Is(err, ErrPairCodeNotFound) {
		t.Fatalf("second consume after expiry: %v", err)
	}
}

func TestPurgeExpiredDropsOnlyOld(t *testing.T) {
	s := NewInMemoryPairCodeStore()
	t0 := time.Now().UTC()
	s.Put(PairCode{Code: "alive", ExpiresAt: t0.Add(time.Hour)})
	s.Put(PairCode{Code: "dead", ExpiresAt: t0.Add(-time.Hour)})
	dropped := s.PurgeExpired(t0)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if _, err := s.Consume("alive", t0); err != nil {
		t.Fatalf("alive entry should still be there: %v", err)
	}
}

func TestGeneratePairCode_DeterministicWithFixedReader(t *testing.T) {
	// Drive GeneratePairCode with a deterministic byte reader so two calls
	// from the same seed produce the same code. Used by the integration
	// test suite where reproducible vectors help with failure diagnosis.
	seed := bytes.Repeat([]byte{0x42}, 32)
	a, err := GeneratePairCode(bytes.NewReader(seed))
	if err != nil {
		t.Fatalf("a: %s", err)
	}
	b, err := GeneratePairCode(bytes.NewReader(seed))
	if err != nil {
		t.Fatalf("b: %s", err)
	}
	if a != b {
		t.Fatalf("same seed produced different codes: %q vs %q", a, b)
	}
	// Self-validates.
	if _, err := ParsePairCode(a); err != nil {
		t.Fatalf("deterministic code didn't parse: %s", err)
	}
}
