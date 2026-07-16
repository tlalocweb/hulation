package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

func TestWhoAmIReturnsTokenExpiration(t *testing.T) {
	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	claims := &authware.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expires),
		},
		Username: "admin",
		Roles:    []string{"admin"},
	}
	ctx := context.WithValue(context.Background(), authware.ClaimsKey, claims)

	resp, err := New().WhoAmI(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.TokenExpires != expires.Format(time.RFC3339) {
		t.Fatalf("token_expires = %q, want %q", resp.TokenExpires, expires.Format(time.RFC3339))
	}
}
