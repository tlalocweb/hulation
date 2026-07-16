package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ctxWithClaims(c *authware.Claims) context.Context {
	return context.WithValue(context.Background(), authware.ClaimsKey, c)
}

func TestWhoAmI_ReturnsRFC3339TokenExpires(t *testing.T) {
	exp := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	claims := &authware.Claims{Username: "admin", Roles: []string{"admin"}}
	claims.ExpiresAt = jwt.NewNumericDate(exp)

	resp, err := New().WhoAmI(ctxWithClaims(claims), &authspec.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if resp.TokenExpires != "2026-07-19T12:00:00Z" {
		t.Fatalf("token_expires = %q, want 2026-07-19T12:00:00Z", resp.TokenExpires)
	}
	if _, perr := time.Parse(time.RFC3339, resp.TokenExpires); perr != nil {
		t.Fatalf("token_expires is not RFC3339: %v", perr)
	}
	if resp.Username != "admin" || !resp.IsAdmin {
		t.Fatalf("identity/role regression: %+v", resp)
	}
}

// jwt decodes `exp` into the server's local zone; WhoAmI must normalize to
// UTC so token_expires doesn't depend on the server's TZ.
func TestWhoAmI_NormalizesZoneToUTC(t *testing.T) {
	zone := time.FixedZone("TEST+5", 5*3600)
	exp := time.Date(2026, 7, 19, 17, 0, 0, 0, zone) // == 12:00Z
	claims := &authware.Claims{Username: "admin"}
	claims.ExpiresAt = jwt.NewNumericDate(exp)

	resp, err := New().WhoAmI(ctxWithClaims(claims), &authspec.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if resp.TokenExpires != "2026-07-19T12:00:00Z" {
		t.Fatalf("token_expires = %q, want UTC-normalized 2026-07-19T12:00:00Z", resp.TokenExpires)
	}
}

// Claims without ExpiresAt (e.g. QR-paired device signature auth) keep the
// backward-compatible empty field.
func TestWhoAmI_NoExpiry_EmptyField(t *testing.T) {
	claims := &authware.Claims{Username: "alice", Roles: []string{"qr_paired"}}

	resp, err := New().WhoAmI(ctxWithClaims(claims), &authspec.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if resp.TokenExpires != "" {
		t.Fatalf("token_expires = %q, want empty for claims without expiry", resp.TokenExpires)
	}
	if resp.IsAdmin {
		t.Fatal("non-admin claims must not report is_admin")
	}
}

func TestWhoAmI_NoClaims_Unauthenticated(t *testing.T) {
	_, err := New().WhoAmI(context.Background(), &authspec.WhoAmIRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestRefreshToken_NoClaims_Unauthenticated(t *testing.T) {
	_, err := New().RefreshToken(context.Background(), &authspec.RefreshTokenRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

// A totp_pending token must not be refreshable — refreshing would mint a
// full-privilege session without the second factor.
func TestRefreshToken_TotpPending_Rejected(t *testing.T) {
	claims := &authware.Claims{
		Username:    "admin",
		Permissions: []string{"totp_pending"},
	}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(4 * time.Minute))

	_, err := New().RefreshToken(ctxWithClaims(claims), &authspec.RefreshTokenRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated for totp_pending refresh, got %v", err)
	}
}

// Expired claims must never be refreshable, even if they somehow slip past
// the transport-layer rejection.
func TestRefreshToken_ExpiredClaims_Rejected(t *testing.T) {
	claims := &authware.Claims{Username: "admin", Roles: []string{"admin"}}
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))

	_, err := New().RefreshToken(ctxWithClaims(claims), &authspec.RefreshTokenRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated for expired refresh, got %v", err)
	}
}
