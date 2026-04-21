// Package auth is the gRPC AuthService implementation.
//
// Phase 0 stage 0.7d lands a skeleton: the full AuthService interface is
// registered so unified-server routing works end-to-end, but most RPC
// methods return codes.Unimplemented. The live ones are the handful
// needed for a bootable system:
//
//	ListAuthProviders   — returns the configured provider list (from config).
//	WhoAmI              — echoes the authware-injected Claims back.
//	GetMyPermissions    — returns the caller's permissions from Claims.
//
// The remaining RPCs (LoginAdmin, LoginOIDC, TOTP, user CRUD, invite,
// password-reset, RefreshToken, CheckUserPermission, GetUserPermissions,
// GrantServerAccess family) land in a focused follow-up that wires Bolt
// user storage end-to-end. See PHASE_0_STATUS.md for the current plan.
package auth

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var aLog = log.GetTaggedLogger("auth-impl", "gRPC AuthService implementation")

// Server embeds UnimplementedAuthServiceServer so we get forward-compat
// defaults (Unimplemented) for every method we don't override.
type Server struct {
	authspec.UnimplementedAuthServiceServer
}

// New returns an AuthService implementation.
func New() *Server { return &Server{} }

// ListAuthProviders returns the configured auth providers so the UI can
// render login buttons. Public (noauth annotation in the proto).
func (s *Server) ListAuthProviders(ctx context.Context, req *authspec.ListAuthProvidersRequest) (*authspec.ListAuthProvidersResponse, error) {
	cfg := config.GetConfig()
	out := &authspec.ListAuthProvidersResponse{}
	if cfg == nil || cfg.Auth == nil {
		return out, nil
	}
	for _, p := range cfg.Auth.Providers {
		info := &authspec.AuthProviderInfo{
			Name: p.Name,
			Type: p.Provider,
		}
		// Optional display fields live inside the provider's
		// type-specific config blob.
		if v, ok := p.Config["display_name"].(string); ok {
			info.DisplayName = v
		}
		if v, ok := p.Config["icon_url"].(string); ok {
			info.IconUrl = v
		}
		out.Providers = append(out.Providers, info)
	}
	return out, nil
}

// WhoAmI validates the caller's token (already done by authware) and
// returns identity info from the Claims in context.
func (s *Server) WhoAmI(ctx context.Context, req *authspec.WhoAmIRequest) (*authspec.WhoAmIResponse, error) {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		return nil, status.Error(codes.Unauthenticated, "no claims in context")
	}
	resp := &authspec.WhoAmIResponse{
		Status:         "ok",
		Username:       claims.Username,
		Roles:          claims.Roles,
		TokenExpires:   "",
		EmailValidated: true, // email-validation flow not yet ported; default-accept for skeleton
		IsAdmin:        hasRole(claims.Roles, "admin") || hasRole(claims.Roles, "root"),
	}
	if claims.ExpiresAt != nil {
		resp.TokenExpires = claims.ExpiresAt.Time.Format("2006-01-02T15:04:05Z07:00")
	}
	return resp, nil
}

// GetMyPermissions returns the authenticated user's permissions — just
// echoes them back from the JWT claim. No Bolt read required.
func (s *Server) GetMyPermissions(ctx context.Context, req *authspec.GetMyPermissionsRequest) (*authspec.GetUserPermissionsResponse, error) {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		return nil, status.Error(codes.Unauthenticated, "no claims in context")
	}
	return &authspec.GetUserPermissionsResponse{
		AllowPermissions: claims.Permissions,
		// Deny permissions and PermissionSources would require a Bolt
		// lookup; deferred.
	}, nil
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
