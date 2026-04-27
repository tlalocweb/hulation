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
	"net/http"
	"strings"

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
//   - If absent or invalid, leaves the context untouched — downstream
//     handlers that *require* claims will return Unauthenticated on
//     their own (matching the existing WhoAmI / GetMyPermissions
//     behavior).
//
// This is the smallest thing that makes WhoAmI & GetMyPermissions work
// end-to-end for the admin user without pulling in the whole Bolt user
// store. Once the Phase-1 authware rewrite lands it can replace this.
func AdminBearerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		token := extractBearer(ctx)
		if token == "" {
			return handler(ctx, req)
		}
		ok, perms, err := model.VerifyJWTClaims(model.GetDB(), token)
		if err != nil || !ok || perms == nil {
			return handler(ctx, req)
		}
		cfg := hulaapp.GetConfig()
		adminUsername := ""
		if cfg != nil {
			adminUsername = cfg.Admin.Username
		}
		// The admin user isn't in the users DB table — build a minimal
		// Claims from the known-good config + JWT permissions.
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
		ctx = context.WithValue(ctx, authware.ClaimsKey, claims)
		return handler(ctx, req)
	}
}

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
