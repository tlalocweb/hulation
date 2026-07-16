package server

// Regression tests for the bearer-auth paths of the unified interceptors:
//
//   - a presented-but-invalid bearer (expired / malformed / bad signature)
//     is rejected with 401 / Unauthenticated on auth-required routes,
//     instead of continuing and surfacing "no claims in context" downstream;
//   - noauth routes (login retries with a stale token) and non-gateway
//     paths keep the legacy pass-through;
//   - a valid bearer's verified RegisteredClaims — ExpiresAt in
//     particular — survive conversion into authware.Claims on both the
//     HTTP (grpc-gateway) and native-gRPC paths. (DB-backed; skipped when
//     ClickHouse isn't reachable.)

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/model"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

var (
	bearerCfgOnce sync.Once
	bearerCfgErr  error
)

func loadBearerTestConfig(t *testing.T) {
	t.Helper()
	bearerCfgOnce.Do(func() {
		_, bearerCfgErr = app.LoadConfigWithFile("testdata/bearer-test.yaml")
	})
	if bearerCfgErr != nil {
		t.Fatalf("loading testdata/bearer-test.yaml: %v", bearerCfgErr)
	}
}

func signBearerTestJWT(t *testing.T, claims *model.JWTClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(app.GetConfig().JWTKey))
	if err != nil {
		t.Fatalf("signing test JWT: %v", err)
	}
	return s
}

func expiredBearerJWT(t *testing.T) string {
	t.Helper()
	return signBearerTestJWT(t, &model.JWTClaims{
		Id:         "admin",
		LoginToken: "irrelevant",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
}

// nextRecorder is the wrapped handler: records whether it ran and what
// claims (if any) were on the request context.
type nextRecorder struct {
	called bool
	claims *authware.Claims
}

func (n *nextRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.called = true
		n.claims, _ = r.Context().Value(authware.ClaimsKey).(*authware.Claims)
		w.WriteHeader(http.StatusOK)
	})
}

func doBearerRequest(t *testing.T, method, path, bearer string) (*httptest.ResponseRecorder, *nextRecorder) {
	t.Helper()
	rec := &nextRecorder{}
	mw := AdminBearerHTTPMiddleware(rec.handler())
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	return w, rec
}

// ---------- HTTP (grpc-gateway) path ----------

func TestHTTP_ExpiredBearer_AuthRequiredRoute_401(t *testing.T) {
	loadBearerTestConfig(t)
	w, rec := doBearerRequest(t, "GET", "/api/v1/auth/whoami", expiredBearerJWT(t))
	if rec.called {
		t.Fatal("handler ran despite expired bearer — expired sessions must be cut off at the middleware")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("401 must carry WWW-Authenticate")
	}
}

func TestHTTP_MalformedBearer_AuthRequiredRoute_401(t *testing.T) {
	loadBearerTestConfig(t)
	w, rec := doBearerRequest(t, "POST", "/api/v1/auth/refresh", "garbage-token")
	if rec.called {
		t.Fatal("handler ran despite malformed bearer")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (refresh with invalid token must be rejected)", w.Code)
	}
}

// A stale bearer on a login retry must not lock the client out of the
// noauth login endpoints.
func TestHTTP_InvalidBearer_NoauthRoute_PassesThrough(t *testing.T) {
	loadBearerTestConfig(t)
	w, rec := doBearerRequest(t, "POST", "/api/v1/auth/opaque/login/init", expiredBearerJWT(t))
	if !rec.called {
		t.Fatalf("noauth route must pass through an invalid bearer, got status %d", w.Code)
	}
	if rec.claims != nil {
		t.Fatal("invalid bearer must not yield claims")
	}
}

// Non-gateway paths (visitor endpoints, static sites, host-routed proxies)
// are none of this middleware's business.
func TestHTTP_InvalidBearer_NonGatewayPath_PassesThrough(t *testing.T) {
	loadBearerTestConfig(t)
	_, rec := doBearerRequest(t, "GET", "/v/hello", "garbage-token")
	if !rec.called {
		t.Fatal("non-gateway path must pass through")
	}
}

// No credentials at all keeps the legacy contract: handlers that require
// claims return Unauthenticated themselves.
func TestHTTP_NoBearer_PassesThrough(t *testing.T) {
	loadBearerTestConfig(t)
	_, rec := doBearerRequest(t, "GET", "/api/v1/auth/whoami", "")
	if !rec.called {
		t.Fatal("request without credentials must reach the handler")
	}
	if rec.claims != nil {
		t.Fatal("no credentials must mean no claims")
	}
}

// ---------- native gRPC path ----------

func bearerMD(token string) context.Context {
	md := metadata.New(map[string]string{"authorization": "Bearer " + token})
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestGRPC_ExpiredBearer_AuthRequiredMethod_Unauthenticated(t *testing.T) {
	loadBearerTestConfig(t)
	interceptor := AdminBearerInterceptor(nil)
	called := false
	_, err := interceptor(bearerMD(expiredBearerJWT(t)),
		nil,
		&grpc.UnaryServerInfo{FullMethod: authspec.AuthService_WhoAmI_FullMethodName},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	if called {
		t.Fatal("handler ran despite expired bearer")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestGRPC_ExpiredBearer_RefreshMethod_Unauthenticated(t *testing.T) {
	loadBearerTestConfig(t)
	interceptor := AdminBearerInterceptor(nil)
	_, err := interceptor(bearerMD(expiredBearerJWT(t)),
		nil,
		&grpc.UnaryServerInfo{FullMethod: authspec.AuthService_RefreshToken_FullMethodName},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expired token must not be able to refresh, got %v", err)
	}
}

func TestGRPC_InvalidBearer_NoauthMethod_PassesThrough(t *testing.T) {
	loadBearerTestConfig(t)
	interceptor := AdminBearerInterceptor(nil)
	called := false
	_, err := interceptor(bearerMD("garbage-token"),
		nil,
		&grpc.UnaryServerInfo{FullMethod: authspec.AuthService_ListAuthProviders_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			called = true
			if _, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); ok {
				t.Fatal("invalid bearer must not yield claims")
			}
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("noauth RPC must run without claims (called=%v, err=%v)", called, err)
	}
}

// Methods missing from the annotation registry must fail closed.
func TestGRPC_InvalidBearer_UnknownMethod_Unauthenticated(t *testing.T) {
	loadBearerTestConfig(t)
	interceptor := AdminBearerInterceptor(nil)
	_, err := interceptor(bearerMD("garbage-token"),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/hulation.v1.notreal.Nope/Nada"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown method must default to auth-required, got %v", err)
	}
}

type testServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *testServerStream) Context() context.Context { return s.ctx }

func TestGRPCStream_ExpiredBearer_Unauthenticated(t *testing.T) {
	loadBearerTestConfig(t)
	interceptor := AdminBearerStreamInterceptor(nil)
	called := false
	err := interceptor(nil,
		&testServerStream{ctx: bearerMD(expiredBearerJWT(t))},
		&grpc.StreamServerInfo{FullMethod: authspec.AuthService_WhoAmI_FullMethodName},
		func(srv any, ss grpc.ServerStream) error { called = true; return nil })
	if called {
		t.Fatal("stream handler ran despite expired bearer")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

// ---------- valid-token conversion (DB-backed) ----------

var (
	bearerDBOnce sync.Once
	bearerDBErr  error
)

// setupBearerTestDB connects the model layer to the test ClickHouse the
// same way model's own TestMain does. Skips (not fails) when CH isn't
// reachable so `go test ./server` still passes in CH-less environments.
func setupBearerTestDB(t *testing.T) {
	t.Helper()
	loadBearerTestConfig(t)
	bearerDBOnce.Do(func() {
		_, _, _, bearerDBErr = model.SetupAppDB(app.GetConfig())
	})
	if bearerDBErr != nil || model.GetDB() == nil {
		t.Skipf("ClickHouse not available for DB-backed bearer tests: %v", bearerDBErr)
	}
}

func TestHTTP_ValidBearer_ClaimsCarryExpiresAt(t *testing.T) {
	setupBearerTestDB(t)
	token, err := model.NewJWTClaimsCommit(model.GetDB(), "admin", &model.LoginOpts{IsAdmin: true})
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}

	w, rec := doBearerRequest(t, "GET", "/api/v1/auth/whoami", token)
	if !rec.called || w.Code != http.StatusOK {
		t.Fatalf("valid bearer rejected: called=%v status=%d", rec.called, w.Code)
	}
	if rec.claims == nil {
		t.Fatal("valid bearer must yield claims")
	}
	if rec.claims.Username != "admin" || rec.claims.Subject != "admin" {
		t.Fatalf("identity regression: %+v", rec.claims)
	}
	if !hasRoleStr(rec.claims.Roles, "admin") {
		t.Fatalf("admin role regression: %+v", rec.claims.Roles)
	}
	if rec.claims.ExpiresAt == nil {
		t.Fatal("verified ExpiresAt must survive conversion into authware.Claims")
	}
	if until := time.Until(rec.claims.ExpiresAt.Time); until < 71*time.Hour || until > 73*time.Hour {
		t.Fatalf("ExpiresAt %v not ~72h out", rec.claims.ExpiresAt.Time)
	}
	if rec.claims.ID == "" || rec.claims.SessionID == "" {
		t.Fatal("login-token session handle must survive conversion (claims.ID / SessionID)")
	}
}

func TestGRPC_ValidBearer_ClaimsCarryExpiresAt(t *testing.T) {
	setupBearerTestDB(t)
	token, err := model.NewJWTClaimsCommit(model.GetDB(), "admin", &model.LoginOpts{IsAdmin: true})
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}

	interceptor := AdminBearerInterceptor(nil)
	var got *authware.Claims
	_, err = interceptor(bearerMD(token),
		nil,
		&grpc.UnaryServerInfo{FullMethod: authspec.AuthService_WhoAmI_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			got, _ = ctx.Value(authware.ClaimsKey).(*authware.Claims)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("valid bearer rejected: %v", err)
	}
	if got == nil || got.ExpiresAt == nil {
		t.Fatalf("gRPC path must retain registered claims, got %+v", got)
	}
	if got.Username != "admin" || !hasRoleStr(got.Roles, "admin") {
		t.Fatalf("identity/role regression: %+v", got)
	}
}

func hasRoleStr(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
