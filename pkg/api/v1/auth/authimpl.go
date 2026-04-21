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
	"github.com/tlalocweb/hulation/model"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var aLog = log.GetTaggedLogger("auth-impl", "gRPC AuthService implementation")

// Server embeds UnimplementedAuthServiceServer so we get forward-compat
// defaults (Unimplemented) for every method we don't override.
type Server struct {
	authspec.UnimplementedAuthServiceServer
}

// New returns an AuthService implementation.
func New() *Server { return &Server{} }

// LoginAdmin authenticates the built-in admin (root) user against the
// argon2id hash stored in config.Admin.Hash. Issues a JWT via
// model.NewJWTClaimsCommit on success. Mirrors handler.Login from the
// legacy Fiber route.
func (s *Server) LoginAdmin(ctx context.Context, req *authspec.LoginAdminRequest) (*authspec.LoginAdminResponse, error) {
	cfg := config.GetConfig()
	if cfg == nil || cfg.Admin == nil {
		return nil, status.Error(codes.FailedPrecondition, "admin not configured")
	}
	if req.GetUsername() != cfg.Admin.Username {
		return &authspec.LoginAdminResponse{
			Error: ptr("invalid credentials"),
		}, nil
	}
	match, err := utils.Argon2CompareHashAndSecret(req.GetHash(), cfg.Admin.Hash)
	if err != nil {
		aLog.Errorf("LoginAdmin: argon2 compare: %v", err)
		return nil, status.Error(codes.Internal, "hash function error")
	}
	if !match {
		return &authspec.LoginAdminResponse{Error: ptr("invalid credentials")}, nil
	}

	// TODO: TOTP gate for admin logins that have TOTP enabled. The
	// existing handler.CheckTotpRequired logic lives in handler/totp.go;
	// skipped here because the full TOTP flow (totp_pending → validate
	// RPCs) is still Unimplemented. Admin logins with TOTP enabled
	// should continue to use the legacy /api/auth/login route until
	// the TOTP RPCs land.

	jwt, err := model.NewJWTClaimsCommit(model.GetDB(), req.GetUsername(), &model.LoginOpts{
		IsAdmin: true,
	})
	if err != nil {
		aLog.Errorf("LoginAdmin: JWT issue: %v", err)
		return nil, status.Errorf(codes.Internal, "issue jwt: %v", err)
	}
	return &authspec.LoginAdminResponse{Admintoken: jwt}, nil
}

func ptr(s string) *string { return &s }

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

// userModelToProto maps a legacy model.User into the apiobjects User
// proto. Only the fields hula's current (ClickHouse) schema actually
// populates — the Bolt-native fields (role_assignments, TOTP state,
// email validation, etc.) stay zero-valued until that migration lands.
func userModelToProto(u *model.User) *apiobjects.User {
	if u == nil {
		return nil
	}
	return &apiobjects.User{
		Uuid:      u.ID,
		Username:  u.Email,
		Email:     u.Email,
		CreatedAt: timestamppb.New(u.CreatedAt),
		UpdatedAt: timestamppb.New(u.UpdatedAt),
	}
}

// Logout revokes the login token. Mirrors the legacy handler.Logout.
// No-op if the claim token is empty.
func (s *Server) logoutInternal(ctx context.Context) error {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil || claims.ID == "" {
		return nil
	}
	// Delete the login token from the DB. The JWT itself is
	// stateless, but the `login_tokens` table is what stamps it as
	// still-valid; removing the row invalidates subsequent requests.
	return model.GetDB().Delete(&model.LoginToken{}, "id = ?", claims.ID).Error
}

// ListUsers returns every user row in the DB. Requires
// superadmin.user.list (enforced by authware upstream).
func (s *Server) ListUsers(ctx context.Context, req *authspec.ListUsersRequest) (*authspec.ListUsersResponse, error) {
	var users []model.User
	if err := model.GetDB().Find(&users).Error; err != nil {
		aLog.Errorf("ListUsers: %v", err)
		return nil, status.Errorf(codes.Internal, "list users: %v", err)
	}
	out := &authspec.ListUsersResponse{Users: make([]*apiobjects.User, 0, len(users))}
	for i := range users {
		out.Users = append(out.Users, userModelToProto(&users[i]))
	}
	return out, nil
}

// CreateUser creates a user with the given email. First/last name come
// from the proto User payload. Requires superadmin.user.create.
func (s *Server) CreateUser(ctx context.Context, req *authspec.CreateUserRequest) (*authspec.CreateUserResponse, error) {
	u := req.GetUser()
	if u == nil {
		return nil, status.Error(codes.InvalidArgument, "user is required")
	}
	if u.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	// Split display_name into first/last if provided. hula's legacy
	// CreateNewUser takes (email, first, last).
	first, last := splitName(u.GetDisplayName())
	created, err := model.CreateNewUser(model.GetDB(), u.GetEmail(), first, last)
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "create user: %v", err)
	}
	return &authspec.CreateUserResponse{
		Status: "ok",
		User:   userModelToProto(created),
	}, nil
}

// GetUser looks up a user by UUID or email. Requires
// superadmin.user.read.
func (s *Server) GetUser(ctx context.Context, req *authspec.GetUserRequest) (*authspec.GetUserResponse, error) {
	id := req.GetUserId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	var u *model.User
	var err error
	if looksLikeUUID(id) {
		u, err = model.GetUserById(model.GetDB(), id)
	} else {
		u, err = model.GetUserByEmail(model.GetDB(), id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup user: %v", err)
	}
	if u == nil {
		return nil, status.Errorf(codes.NotFound, "user %q not found", id)
	}
	return &authspec.GetUserResponse{Status: "ok", User: userModelToProto(u)}, nil
}

// DeleteUser removes a user by UUID or email. Requires
// superadmin.user.delete.
func (s *Server) DeleteUser(ctx context.Context, req *authspec.DeleteUserRequest) (*authspec.DeleteUserResponse, error) {
	id := req.GetIdentity()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}
	var err error
	if looksLikeUUID(id) {
		err = model.DeleteUser(model.GetDB(), id)
	} else {
		err = model.DeleteUserByEmail(model.GetDB(), id)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete user: %v", err)
	}
	return &authspec.DeleteUserResponse{Status: "ok"}, nil
}

func splitName(full string) (first, last string) {
	for i := 0; i < len(full); i++ {
		if full[i] == ' ' {
			return full[:i], full[i+1:]
		}
	}
	return full, ""
}

func looksLikeUUID(s string) bool {
	// Rough UUID shape check: 36 chars with dashes at 8/13/18/23.
	if len(s) != 36 {
		return false
	}
	for _, i := range []int{8, 13, 18, 23} {
		if s[i] != '-' {
			return false
		}
	}
	return true
}
