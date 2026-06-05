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
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

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
			if ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token); err == nil && ok && perms != nil {
				cfg := hulaapp.GetConfig()
				adminUsername := ""
				if cfg != nil {
					adminUsername = cfg.Admin.Username
				}
				roles := []string{}
				if perms.UserID != "" && adminUsername != "" && perms.UserID == adminUsername {
					roles = append(roles, "admin")
				}
				claims := &authware.Claims{
					Username:    perms.UserID,
					Roles:       roles,
					Permissions: perms.ListCaps(),
				}
				claims.Subject = perms.UserID
				r = r.WithContext(context.WithValue(r.Context(), authware.ClaimsKey, claims))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// AdminBearerInterceptor returns a UnaryServerInterceptor that:
//   - Looks for an "authorization: Bearer <jwt>" metadata header.
//   - If present, validates the JWT against model.VerifyJWTClaims and
//     stores a minimal *authware.Claims on the context when the token
//     belongs to the admin account.
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
		if claims := claimsFromContext(ctx, deviceKeys); claims != nil {
			ctx = context.WithValue(ctx, authware.ClaimsKey, claims)
		}
		return handler(ctx, req)
	}
}

// claimsFromContext consolidates the bearer-then-signature lookup logic so both the
// unary and stream interceptors share one source of truth.
func claimsFromContext(ctx context.Context, deviceKeys authware.DeviceKeyStore) *authware.Claims {
	if claims := claimsFromBearer(ctx); claims != nil {
		return claims
	}
	if deviceKeys != nil {
		if claims := claimsFromDeviceSignature(ctx, deviceKeys); claims != nil {
			return claims
		}
	}
	return nil
}

func claimsFromBearer(ctx context.Context) *authware.Claims {
	token := extractBearer(ctx)
	if token == "" {
		return nil
	}
	ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
	if err != nil || !ok || perms == nil {
		return nil
	}
	cfg := hulaapp.GetConfig()
	adminUsername := ""
	if cfg != nil {
		adminUsername = cfg.Admin.Username
	}
	roles := []string{}
	if perms.UserID != "" && adminUsername != "" && perms.UserID == adminUsername {
		roles = append(roles, "admin")
	}
	claims := &authware.Claims{
		Username:    perms.UserID,
		Roles:       roles,
		Permissions: perms.ListCaps(),
	}
	claims.Subject = perms.UserID
	return claims
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
		ctx := ss.Context()
		claims := claimsFromContext(ctx, deviceKeys)
		if claims == nil {
			return handler(srv, ss)
		}
		newCtx := context.WithValue(ctx, authware.ClaimsKey, claims)
		return handler(srv, &claimsServerStream{ServerStream: ss, ctx: newCtx})
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
