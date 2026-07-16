package server

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tlalocweb/hulation/model"
)

func TestClaimsFromVerifiedBearerPreservesExpiration(t *testing.T) {
	expires := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	verified := &model.JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	perms := &model.UserPermissions{UserID: "admin"}

	claims := claimsFromVerifiedBearer(perms, verified, "admin")

	if claims == nil || claims.ExpiresAt == nil {
		t.Fatal("expected bearer expiration in authware claims")
	}
	if !claims.ExpiresAt.Time.Equal(expires) {
		t.Fatalf("expiration = %s, want %s", claims.ExpiresAt.Time, expires)
	}
	if claims.Subject != "admin" || claims.Username != "admin" {
		t.Fatalf("unexpected identity: %+v", claims)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("expected admin role, got %v", claims.Roles)
	}
}
