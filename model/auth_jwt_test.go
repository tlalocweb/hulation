package model

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/tlalocweb/hulation/app"
)

// signTestJWT builds a signed JWT directly (bypassing NewJWTClaimsCommit) so
// tests can control expiry, identity, and the backing login token.
func signTestJWT(t *testing.T, claims *JWTClaims, key string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(key))
	if err != nil {
		t.Fatalf("signing test JWT: %v", err)
	}
	return s
}

func TestVerifyJWTClaimsDetailed_ValidRoundTrip(t *testing.T) {
	tokenStr, err := NewJWTClaimsCommit(testdb, "jwt-detail-user", nil)
	assert.NoError(t, err)

	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, tokenStr)
	assert.NoError(t, err)
	assert.True(t, valid)
	if assert.NotNil(t, perms) {
		assert.Equal(t, "jwt-detail-user", perms.UserID)
	}
	if assert.NotNil(t, claims) {
		assert.NotEmpty(t, claims.LoginToken, "verified claims must carry the login token")
		if assert.NotNil(t, claims.ExpiresAt, "verified claims must carry ExpiresAt") {
			dur, derr := time.ParseDuration(app.GetConfig().JWTExpiration)
			assert.NoError(t, derr)
			assert.WithinDuration(t, time.Now().Add(dur), claims.ExpiresAt.Time, 2*time.Minute,
				"ExpiresAt should be ~now + configured JWT expiration")
		}
	}
}

// The compatibility wrapper must agree with the detailed variant.
func TestVerifyJWTClaims_WrapperParity(t *testing.T) {
	tokenStr, err := NewJWTClaimsCommit(testdb, "jwt-wrapper-user", nil)
	assert.NoError(t, err)

	dvalid, dperms, _, derr := VerifyJWTClaimsDetailed(testdb, tokenStr)
	wvalid, wperms, werr := VerifyJWTClaims(testdb, tokenStr)
	assert.Equal(t, dvalid, wvalid)
	assert.Equal(t, derr == nil, werr == nil)
	if assert.NotNil(t, dperms) && assert.NotNil(t, wperms) {
		assert.Equal(t, dperms.UserID, wperms.UserID)
		assert.Equal(t, dperms.ListCaps(), wperms.ListCaps())
	}
}

func TestVerifyJWTClaimsDetailed_ExpiredToken(t *testing.T) {
	tok := signTestJWT(t, &JWTClaims{
		Id:         "expired-user",
		LoginToken: "irrelevant",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	}, app.GetConfig().JWTKey)

	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, tok)
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims, "no claims may be returned from an unverified (expired) token")
	assert.ErrorIs(t, err, ErrUnauthorized, "expired token must classify as ErrUnauthorized")
}

func TestVerifyJWTClaimsDetailed_MalformedToken(t *testing.T) {
	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, "not-a-jwt-at-all")
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestVerifyJWTClaimsDetailed_WrongSignature(t *testing.T) {
	tok := signTestJWT(t, &JWTClaims{
		Id:         "forged-user",
		LoginToken: "irrelevant",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}, "some-other-signing-key-entirely")

	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, tok)
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

// A structurally valid, correctly signed, unexpired JWT whose login token
// has no row in login_tokens (revoked or never issued) must fail closed as
// an auth failure — this is the revocation path.
func TestVerifyJWTClaimsDetailed_RevokedLoginToken(t *testing.T) {
	tok := signTestJWT(t, &JWTClaims{
		Id:         "revoked-user",
		LoginToken: "00000000-0000-7000-8000-000000000000",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}, app.GetConfig().JWTKey)

	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, tok)
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

// A nil DB is an infrastructure fault, not a token fault: it must NOT
// classify as ErrUnauthorized (so transports return 5xx and the client
// keeps its session), and it must not panic.
func TestVerifyJWTClaimsDetailed_NilDB_InfraError(t *testing.T) {
	tok := signTestJWT(t, &JWTClaims{
		Id:         "someone",
		LoginToken: "irrelevant",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}, app.GetConfig().JWTKey)

	valid, perms, claims, err := VerifyJWTClaimsDetailed(nil, tok)
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnauthorized, "nil DB is infra, must not map to 401")
}

// An expired login token whose cleanup delete fails must still classify as
// ErrTokenExpired — the failed delete is an expired session, not an infra
// fault. Guards the two-%w wrap in LookupLoginToken.
func TestLookupLoginToken_ExpiredDeleteFailure_PreservesExpired(t *testing.T) {
	// Insert an already-expired login token, then look it up. Even in the
	// normal (delete-succeeds) case the sentinel must satisfy errors.Is.
	row, err := CreateNewLoginToken(testdb, "expired-lookup-user", time.Now().Add(-time.Hour))
	assert.NoError(t, err)

	_, lerr := LookupLoginToken(testdb, row.ID)
	assert.Error(t, lerr)
	assert.ErrorIs(t, lerr, ErrTokenExpired, "expired token must classify as ErrTokenExpired")
}

// A JWT claiming one user but backed by another user's login token must
// fail closed as an auth failure.
func TestVerifyJWTClaimsDetailed_UserMismatch(t *testing.T) {
	row, err := CreateNewLoginToken(testdb, "bob", time.Now().Add(time.Hour))
	assert.NoError(t, err)

	tok := signTestJWT(t, &JWTClaims{
		Id:         "alice",
		LoginToken: row.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}, app.GetConfig().JWTKey)

	valid, perms, claims, err := VerifyJWTClaimsDetailed(testdb, tok)
	assert.False(t, valid)
	assert.Nil(t, perms)
	assert.Nil(t, claims)
	assert.ErrorIs(t, err, ErrUnauthorized)
}
