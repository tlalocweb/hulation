package server

// Minimal gRPC auth interceptor for the unified server. The full izcr
// authware.AuthInterceptor requires Bolt storage + OPA policy wiring
// that hasn't landed in Phase 0; until that does, this interceptor
// accepts the admin bearer token used by the REST /api/auth/login flow,
// populates a skeletal authware.Claims so downstream RPCs can read
// identity from ctx.Value(authware.ClaimsKey), and lets unauth-required
// RPCs (StatusService.Status, AuthService.ListAuthProviders, etc.)
// continue when no token is presented.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

var authLog = log.GetTaggedLogger("bearer-auth", "Unified server bearer authentication")

// totp_pending is a LIMITED token: the password check passed but the second
// factor hasn't. It may only reach the endpoints needed to complete or
// enroll TOTP (plus WhoAmI). The full authware middleware enforces this via
// checkTotpPendingToken, but that middleware isn't wired on the unified
// server — these bearer interceptors are the only gate, so the same
// restriction lives here. Keep the two maps in sync.
var totpPendingAllowedMethods = map[string]bool{
	authspec.AuthService_TotpValidate_FullMethodName:    true,
	authspec.AuthService_TotpSetup_FullMethodName:       true,
	authspec.AuthService_TotpVerifySetup_FullMethodName: true,
	authspec.AuthService_TotpStatus_FullMethodName:      true,
	authspec.AuthService_WhoAmI_FullMethodName:          true,
}

var totpPendingAllowedHTTP = map[string]bool{
	"POST /api/v1/auth/totp/validate":     true,
	"POST /api/v1/auth/totp/setup":        true,
	"POST /api/v1/auth/totp/verify-setup": true,
	"GET /api/v1/auth/totp/status":        true,
	"GET /api/v1/auth/whoami":             true,
}

// AdminBearerHTTPMiddleware mirrors AdminBearerInterceptor for the
// grpc-gateway path. grpc-gateway calls the gRPC service
// implementation directly (`RegisterFooHandlerServer`) which bypasses
// unary interceptors; the only way to inject authware.Claims into the
// ctx that reaches the handler is to mutate the *http.Request context
// before it hits the gateway mux. This middleware does that for every
// /api/v1/* request.
func AdminBearerHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authz := r.Header.Get("Authorization")
		if len(authz) > len(prefix) && authz[:len(prefix)] == prefix {
			token := authz[len(prefix):]
			claims, err := buildBearerClaims(token)
			switch {
			case claims != nil:
				// A limited totp_pending bearer may only reach the
				// TOTP-completion endpoints. Anywhere else, reject with
				// 403 — the token is valid but the second factor isn't
				// done yet, so it's forbidden here rather than unauthenticated.
				if claims.TotpPending && !totpPendingAllowedHTTP[r.Method+" "+r.URL.Path] {
					authLog.Debugf("totp_pending bearer blocked on %s %s", r.Method, r.URL.Path)
					writeTotpPendingReject(w)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), authware.ClaimsKey, claims))
			case bearerRequiredForHTTP(r):
				// A bearer was presented and failed verification on a
				// route that requires auth: reject here with a clean
				// status instead of letting the handler surface an
				// ambiguous "no claims in context". Mobile clients key
				// session teardown off this 401.
				writeBearerReject(w, r, err)
				return
			default:
				// Public (noauth) gateway routes and non-gateway paths
				// keep the legacy pass-through: a stale bearer on a
				// login retry must not lock the client out, and host-
				// routed proxies / static sites do their own auth.
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bearerRequiredForHTTP reports whether r targets a grpc-gateway route that
// requires authentication, per the proto `noauth` annotations surfaced
// through authware's method registry. Only such routes reject invalid
// bearers at the middleware; everything else preserves pass-through.
func bearerRequiredForHTTP(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return false
	}
	info, ok := authware.GetMethodInfoByHTTP(r.Method, r.URL.Path)
	return ok && info.NeedAuth
}

// writeBearerReject maps a bearer verification failure onto the wire: 401
// (grpc-gateway UNAUTHENTICATED envelope) when the token itself is at
// fault, 503 when verification failed for infrastructure reasons — clients
// must not treat a server-side blip as an invalid session.
func writeBearerReject(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err == nil || errors.Is(err, model.ErrUnauthorized) {
		authLog.Debugf("rejecting invalid bearer on %s %s: %v", r.Method, r.URL.Path, err)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":16,"message":"invalid or expired token"}`))
		return
	}
	authLog.Errorf("bearer verification unavailable on %s %s: %v", r.Method, r.URL.Path, err)
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"code":14,"message":"authentication temporarily unavailable"}`))
}

// writeTotpPendingReject rejects a limited totp_pending bearer used outside
// the TOTP-completion allowlist. 403 (not 401): the token is valid, but the
// second factor must be completed before it grants access here.
func writeTotpPendingReject(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"code":7,"message":"TOTP validation required"}`))
}

// buildBearerClaims verifies a raw bearer JWT and converts it into
// authware.Claims. The verified RegisteredClaims — notably ExpiresAt — are
// copied through so downstream handlers (WhoAmI in particular) can report
// the session's expiry to clients. Returns (nil, err) on verification
// failure; err wraps model.ErrUnauthorized when the token is at fault, and
// is a bare infrastructure error otherwise.
func buildBearerClaims(token string) (*authware.Claims, error) {
	ok, perms, jc, err := model.VerifyJWTClaimsDetailed(model.GetDB(), token)
	if err != nil {
		return nil, err
	}
	if !ok || perms == nil || jc == nil {
		return nil, fmt.Errorf("%w: token not valid", model.ErrUnauthorized)
	}
	cfg := hulaapp.GetConfig()
	adminUsername := ""
	if cfg != nil {
		adminUsername = cfg.Admin.Username
	}
	roles := []string{}
	// A totp_pending token is a limited credential awaiting second-factor
	// validation. It must never carry the admin role — with it, the token
	// could reach admin-gated RPCs (including RefreshToken, which would
	// upgrade it to a full session without the TOTP code).
	totpPending := jc.TotpPending || perms.HasCap("totp_pending")
	if !totpPending && perms.UserID != "" && adminUsername != "" && perms.UserID == adminUsername {
		roles = append(roles, "admin")
	}
	claims := &authware.Claims{
		Username:    perms.UserID,
		Roles:       roles,
		Permissions: perms.ListCaps(),
		// Carry the limited-token flag through so authware's middleware
		// (Claims.IsTotpPendingToken) enforces the same restrictions on
		// this path as everywhere else — a totp_pending token must not
		// reach TOTP-gated RPCs on the unified server either.
		TotpPending: totpPending,
	}
	// Carry the verified standard claims through — ExpiresAt drives
	// WhoAmI's token_expires and the mobile app's proactive refresh.
	claims.RegisteredClaims = jc.RegisteredClaims
	claims.Subject = perms.UserID
	// The login-token UUID is the server-side session handle (the
	// revocable row in login_tokens); expose it so logout/revocation
	// paths can address the session.
	claims.ID = jc.LoginToken
	claims.SessionID = jc.LoginToken
	return claims, nil
}

// AdminBearerInterceptor returns a UnaryServerInterceptor that:
//   - Looks for an "authorization: Bearer <jwt>" metadata header.
//   - If present, validates the JWT via model.VerifyJWTClaimsDetailed and
//     stores *authware.Claims (including the verified RegisteredClaims,
//     so ExpiresAt survives) on the context. A presented bearer that
//     FAILS verification is rejected with Unauthenticated on any method
//     that requires auth (per the proto `noauth` annotations) — expired
//     sessions get a clean 401 instead of an ambiguous downstream
//     "no claims in context".
//   - If absent, falls back to `Hula-Device-*` signature auth against `deviceKeys`
//     (QR-paired devices). When that also fails to produce claims the context is
//     left untouched and downstream handlers that *require* claims return
//     Unauthenticated on their own (matching the existing WhoAmI / GetMyPermissions
//     behavior).
//
// Pass `nil` (or `authware.NoopDeviceKeyStore{}`) for `deviceKeys` to disable the
// signature-auth path entirely.
func AdminBearerInterceptor(deviceKeys authware.DeviceKeyStore) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authenticateGRPC(ctx, deviceKeys, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// authenticateGRPC resolves claims for a native-gRPC request. A presented
// bearer that fails verification is rejected outright on auth-required
// methods; absent credentials keep the legacy pass-through (handlers that
// require claims return Unauthenticated themselves, and noauth RPCs run
// anonymously).
func authenticateGRPC(ctx context.Context, deviceKeys authware.DeviceKeyStore, fullMethod string) (context.Context, error) {
	token := extractBearer(ctx)
	if token != "" {
		claims, err := buildBearerClaims(token)
		if claims != nil {
			// A limited totp_pending bearer may only reach the
			// TOTP-completion endpoints; reject it everywhere else.
			if claims.TotpPending && !totpPendingAllowedMethods[fullMethod] {
				authLog.Debugf("totp_pending bearer blocked on %s", fullMethod)
				return ctx, status.Error(codes.PermissionDenied, "TOTP validation required")
			}
			return context.WithValue(ctx, authware.ClaimsKey, claims), nil
		}
		if authware.RequiresAuth(fullMethod) {
			return ctx, bearerRejectStatus(fullMethod, err)
		}
		// noauth RPC — continue unauthenticated so e.g. a login retry
		// carrying a stale bearer still works.
		return ctx, nil
	}
	if deviceKeys != nil {
		if claims := claimsFromDeviceSignature(ctx, deviceKeys); claims != nil {
			return context.WithValue(ctx, authware.ClaimsKey, claims), nil
		}
	}
	return ctx, nil
}

// bearerRejectStatus is the gRPC counterpart of writeBearerReject: token
// faults map to Unauthenticated, infrastructure faults to Unavailable.
func bearerRejectStatus(fullMethod string, err error) error {
	if err == nil || errors.Is(err, model.ErrUnauthorized) {
		authLog.Debugf("rejecting invalid bearer on %s: %v", fullMethod, err)
		return status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	authLog.Errorf("bearer verification unavailable on %s: %v", fullMethod, err)
	return status.Error(codes.Unavailable, "authentication temporarily unavailable")
}

// claimsFromDeviceSignature reads the Hula-Device-* metadata headers, verifies the
// ed25519 signature against the device's stored public key, and produces a minimal
// Claims pinned to that device's user. Returns nil when the headers are absent /
// malformed / the timestamp is stale / the signature does not verify.
//
// Nonce replay protection is enforced by the in-process nonce LRU sketched below; a
// persistent store goes in alongside the QR-pairing endpoint.
func claimsFromDeviceSignature(ctx context.Context, store authware.DeviceKeyStore) *authware.Claims {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	deviceID := mdFirst(md, authware.HeaderDeviceID)
	tsStr := mdFirst(md, authware.HeaderTimestamp)
	nonce := mdFirst(md, authware.HeaderNonce)
	sigB64 := mdFirst(md, authware.HeaderSignature)
	if deviceID == "" || tsStr == "" || nonce == "" || sigB64 == "" {
		return nil
	}
	if len(nonce) < 16 || len(nonce) > 256 {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return nil
	}
	now := time.Now()
	drift := now.Sub(ts)
	if drift < 0 {
		drift = -drift
	}
	if drift > authware.SignatureSkew {
		return nil
	}
	key, ok := store.LookupDeviceKey(ctx, deviceID)
	if !ok || len(key.PublicKey) != ed25519.PublicKeySize {
		return nil
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil
	}
	canonical := authware.CanonicalSigningBytes(tsStr, nonce, deviceID)
	if !ed25519.Verify(key.PublicKey, canonical, sig) {
		return nil
	}
	if !deviceNonces.remember(deviceID, nonce, ts) {
		// Nonce already used in the freshness window.
		return nil
	}
	claims := &authware.Claims{
		Username:    key.UserID,
		Roles:       []string{"qr_paired"},
		Permissions: nil,
	}
	claims.Subject = key.UserID
	return claims
}

func mdFirst(md metadata.MD, key string) string {
	for _, v := range md.Get(key) {
		if v != "" {
			return v
		}
	}
	return ""
}

// deviceNonces is the per-process replay-protection LRU for device-key signatures.
// Bounded so a chatty device cannot exhaust memory; entries are pruned lazily on each
// remember() call when their timestamp falls outside the freshness window.
var deviceNonces = newDeviceNonceSet(8192)

type deviceNonceSet struct {
	mu    sync.Mutex
	cap   int
	by    map[string]time.Time // key = deviceID + "\x00" + nonce
}

func newDeviceNonceSet(cap int) *deviceNonceSet {
	return &deviceNonceSet{cap: cap, by: make(map[string]time.Time)}
}

func (s *deviceNonceSet) remember(deviceID, nonce string, ts time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deviceID + "\x00" + nonce
	if _, used := s.by[key]; used {
		return false
	}
	// Lazy prune: when we hit the cap, drop everything older than the freshness window.
	if len(s.by) >= s.cap {
		cutoff := time.Now().Add(-authware.SignatureSkew * 2)
		for k, t := range s.by {
			if t.Before(cutoff) {
				delete(s.by, k)
			}
		}
		// Hard cap: if every entry was still fresh (e.g. a client/attacker
		// flooding many unique nonces within the skew window), pruning frees
		// nothing and the map would grow without bound. Evict entries (map range
		// order is pseudo-random) until we're back under cap so memory stays
		// bounded. This can weaken replay protection for the evicted nonces, but
		// only under an active flood — the bound matters more there.
		for k := range s.by {
			if len(s.by) < s.cap {
				break
			}
			delete(s.by, k)
		}
	}
	s.by[key] = ts
	return true
}

// AdminBearerStreamInterceptor mirrors AdminBearerInterceptor for streaming RPCs. gRPC
// stream interceptors can't replace the stream's context directly — we wrap the
// `grpc.ServerStream` so its `Context()` returns the augmented one. The chat-stream
// service reads claims off the stream context to authenticate the agent.
//
// Accepts both bearer and Hula-Device-* signature auth; pass nil for `deviceKeys` to
// disable the signature-auth path.
func AdminBearerStreamInterceptor(deviceKeys authware.DeviceKeyStore) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPC(ss.Context(), deviceKeys, info.FullMethod)
		if err != nil {
			return err
		}
		if ctx == ss.Context() {
			return handler(srv, ss)
		}
		return handler(srv, &claimsServerStream{ServerStream: ss, ctx: ctx})
	}
}

// claimsServerStream is a thin grpc.ServerStream wrapper that overrides Context() — the
// standard gRPC pattern for injecting per-stream values from an interceptor.
type claimsServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *claimsServerStream) Context() context.Context { return s.ctx }

func extractBearer(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	// grpc-gateway's DefaultHeaderMatcher forwards the HTTP
	// Authorization header under "grpc-metadata-authorization"; native
	// gRPC clients set it as "authorization". Check both.
	for _, key := range []string{"authorization", "grpc-metadata-authorization"} {
		for _, v := range md.Get(key) {
			if strings.HasPrefix(v, "Bearer ") {
				return strings.TrimPrefix(v, "Bearer ")
			}
		}
	}
	return ""
}
