package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestIssueAndParseRoundTrip(t *testing.T) {
	const key = "test-key-32bytes-long-aaaaaaaaaaa"
	sid := uuid.New()
	tok, exp, err := IssueToken(key, sid, "vid-1", "tlaloc", "alice@example.com", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if tok == "" || exp.IsZero() {
		t.Fatal("expected non-empty token + exp")
	}
	if !exp.After(time.Now()) {
		t.Errorf("exp %v not in the future", exp)
	}

	claims, err := ParseToken(key, tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.SessionID != sid.String() {
		t.Errorf("sid: got %q want %q", claims.SessionID, sid.String())
	}
	if claims.VisitorID != "vid-1" || claims.ServerID != "tlaloc" || claims.VisitorEmail != "alice@example.com" {
		t.Errorf("claims mismatch: %+v", claims)
	}
}

func TestParseRejectsWrongKey(t *testing.T) {
	tok, _, _ := IssueToken("key-A-aaaaaaaaaaaaaaaaaaaaaaaaaaaa", uuid.New(), "v", "s", "e", time.Minute)
	if _, err := ParseToken("key-B-bbbbbbbbbbbbbbbbbbbbbbbbbbbb", tok); err == nil {
		t.Error("want error for wrong signing key")
	}
}

func TestParseRejectsNoneAlg(t *testing.T) {
	// Hand-craft a JWT with alg=none; the library refuses by
	// default but make sure our SigningMethodHMAC type-assertion
	// also catches it.
	claims := &ChatClaims{
		SessionID: uuid.New().String(),
		ServerID:  "tlaloc",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	if _, err := ParseToken("any-key-aaaaaaaaaaaaaaaaaaaaaaaaaaaa", signed); err == nil {
		t.Error("alg=none should be rejected")
	}
}

func TestParseRejectsExpired(t *testing.T) {
	// IssueToken normalises ttl <= 0 to DefaultTokenTTL, so to
	// produce an actually-expired token we forge one directly.
	const key = "k-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claims := &ChatClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   TokenSubject,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
		SessionID: uuid.New().String(),
		ServerID:  "tlaloc",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ParseToken(key, signed); err == nil {
		t.Error("expired token should be rejected")
	} else if !strings.Contains(err.Error(), "expired") && !strings.Contains(err.Error(), "exp") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIssueRequiresInputs(t *testing.T) {
	if _, _, err := IssueToken("", uuid.New(), "v", "s", "e", 0); err == nil {
		t.Error("missing key should error")
	}
	if _, _, err := IssueToken("k", uuid.Nil, "v", "s", "e", 0); err == nil {
		t.Error("nil session id should error")
	}
	if _, _, err := IssueToken("k", uuid.New(), "v", "", "e", 0); err == nil {
		t.Error("missing server id should error")
	}
}

// Defensive: a wrong-subject JWT (e.g. the admin token) must not
// be accepted at /chat/ws. Forge a token with the correct signing
// key but a different `sub` and verify ParseToken rejects it.
func TestParseRejectsWrongSubject(t *testing.T) {
	const key = "k-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claims := &ChatClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "admin",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		SessionID: uuid.New().String(),
		ServerID:  "tlaloc",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(key))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ParseToken(key, signed); err == nil {
		t.Error("wrong-subject token should be rejected")
	}
}
