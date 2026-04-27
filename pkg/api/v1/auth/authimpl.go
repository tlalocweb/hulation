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
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	authprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider"
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

// LoginAdmin is the LEGACY plaintext-password admin login. Removed
// — hula now requires OPAQUE PAKE for every password-based login.
// Returns an error envelope so old clients fail loud rather than
// silently retrying.
//
// The proto RPC stays registered for compat during the cutover; new
// callers must use OpaqueLoginInit / OpaqueLoginFinish.
func (s *Server) LoginAdmin(ctx context.Context, req *authspec.LoginAdminRequest) (*authspec.LoginAdminResponse, error) {
	aLog.Warnf("legacy LoginAdmin RPC hit — rejecting; caller should use /api/v1/auth/opaque/login/*")
	return &authspec.LoginAdminResponse{
		Error: ptr("legacy LoginAdmin removed; use /api/v1/auth/opaque/login/* (run hulactl set-password to bootstrap)"),
	}, nil
}

func ptr(s string) *string { return &s }

// RequestPasswordReset acknowledges a password-reset request. Phase 0
// implementation always returns status="success" to prevent email-
// enumeration attacks. Actual email dispatch happens in a later phase
// once hula has a mail integration wired in. The user's email is
// logged at INFO for operator visibility.
func (s *Server) RequestPasswordReset(ctx context.Context, req *authspec.RequestPasswordResetRequest) (*authspec.RequestPasswordResetResponse, error) {
	email := req.GetEmail()
	if email == "" {
		// Still return success to avoid leaking that the request shape
		// is being validated. Empty-input is indistinguishable from
		// any other invalid input.
		return &authspec.RequestPasswordResetResponse{Status: "success"}, nil
	}
	// Look up whether the user exists. Result intentionally not leaked
	// in the response — the server logs it locally.
	u, err := model.GetUserByEmail(model.GetDB(), email)
	if err != nil {
		aLog.Errorf("RequestPasswordReset: lookup %s: %v", email, err)
	} else if u == nil {
		aLog.Infof("RequestPasswordReset: no user for email %q (response still success)", email)
	} else {
		// TODO: issue a password-reset token, stash it on the user
		// row, send email. Phase 0 skips the mail side; the token
		// model is part of the upcoming Bolt user-store migration.
		aLog.Infof("RequestPasswordReset: reset requested for %s (email dispatch deferred)", u.Email)
	}
	return &authspec.RequestPasswordResetResponse{Status: "success"}, nil
}

// LoginWithSecret is the username+password login path for non-OIDC
// providers (e.g., internal/local). Delegates to the named provider.
func (s *Server) LoginWithSecret(ctx context.Context, req *authspec.LoginWithSecretRequest) (*authspec.LoginWithSecretResponse, error) {
	providerName := req.GetProvider()
	if providerName == "" {
		providerName = "internal"
	}
	pm := authprovider.GetProviderManager()
	p, err := pm.GetProvider(providerName)
	if err != nil || p == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "provider %q not configured", providerName)
	}
	return p.LoginWithSecret(ctx, req)
}

// LoginOIDC starts an OIDC login. Returns a redirect URL + instructions
// for the caller to visit, plus a one-time token used to correlate
// with the subsequent LoginWithCode call.
//
// Hula picks the first configured OIDC provider when the request
// doesn't specify one (the proto doesn't carry a provider field —
// izcr's legacy shape).
func (s *Server) LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (*authspec.LoginOIDCResponse, error) {
	p := firstOIDCProvider()
	if p == nil {
		return nil, status.Error(codes.FailedPrecondition, "no OIDC provider configured")
	}
	return p.LoginOIDC(ctx, req)
}

// LoginWithCode completes an OIDC login using the code returned by the
// identity provider's callback. provider must name one of the
// configured OIDC providers.
func (s *Server) LoginWithCode(ctx context.Context, req *authspec.LoginWithCodeRequest) (*authspec.LoginWithCodeResponse, error) {
	providerName := req.GetProvider()
	if providerName == "" {
		return nil, status.Error(codes.InvalidArgument, "provider is required")
	}
	pm := authprovider.GetProviderManager()
	p, err := pm.GetProvider(providerName)
	if err != nil || p == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "provider %q not configured", providerName)
	}
	// The provider-base interface exposes the three login methods but
	// not LoginWithCode directly (izcr's OIDC provider implements it as
	// a method on its concrete type). Check if the provider implements
	// the optional interface via type assertion.
	if cp, ok := p.(loginWithCoder); ok {
		return cp.LoginWithCode(ctx, req)
	}
	return nil, status.Error(codes.Unimplemented, "provider does not support LoginWithCode")
}

// firstOIDCProvider returns the first registered provider whose type is
// "oidc". Order is registration order.
func firstOIDCProvider() authprovider.AuthProvider {
	pm := authprovider.GetProviderManager()
	for _, p := range pm.GetProviders() {
		if p.Type() == "oidc" {
			return p
		}
	}
	return nil
}

// loginWithCoder is the optional interface OIDC providers implement to
// complete the authorization-code exchange. Matches izcr's oidc
// provider's method signature.
type loginWithCoder interface {
	LoginWithCode(ctx context.Context, req *authspec.LoginWithCodeRequest) (*authspec.LoginWithCodeResponse, error)
}

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

// PatchUser applies non-empty fields from the request onto the existing
// user row. Matches the PATCH semantics of the legacy
// handler.ModifyUser: empty fields are left unchanged.
func (s *Server) PatchUser(ctx context.Context, req *authspec.PatchUserRequest) (*authspec.PatchUserResponse, error) {
	id := req.GetIdentity()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
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
		if !req.GetCreateIfNotExists() {
			return nil, status.Errorf(codes.NotFound, "user %q not found", id)
		}
		// Create from scratch.
		email := id
		if pu := req.GetUser(); pu != nil && pu.GetEmail() != "" {
			email = pu.GetEmail()
		}
		first, last := splitName(req.GetUser().GetDisplayName())
		u, err = model.CreateNewUser(model.GetDB(), email, first, last)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "create user: %v", err)
		}
	} else if pu := req.GetUser(); pu != nil {
		changed := false
		if pu.GetEmail() != "" && pu.GetEmail() != u.Email {
			u.Email = pu.GetEmail()
			changed = true
		}
		if pu.GetDisplayName() != "" {
			first, last := splitName(pu.GetDisplayName())
			if first != u.FirstName {
				u.FirstName = first
				changed = true
			}
			if last != u.LastName {
				u.LastName = last
				changed = true
			}
		}
		if changed {
			if err := model.GetDB().Save(u).Error; err != nil {
				return nil, status.Errorf(codes.Internal, "save user: %v", err)
			}
		}
	}
	return &authspec.PatchUserResponse{Status: "ok", User: userModelToProto(u)}, nil
}

// SearchUsers returns users whose email matches the given pattern.
// Simple LIKE-style substring match; no regex in Phase 0.
func (s *Server) SearchUsers(ctx context.Context, req *authspec.SearchUsersRequest) (*authspec.SearchUsersResponse, error) {
	pattern := req.GetUsername()
	var users []model.User
	db := model.GetDB()
	if pattern == "" {
		if err := db.Find(&users).Error; err != nil {
			return nil, status.Errorf(codes.Internal, "search users: %v", err)
		}
	} else {
		// email is the "username" axis for hula's legacy User.
		if err := db.Where("email LIKE ?", "%"+pattern+"%").Find(&users).Error; err != nil {
			return nil, status.Errorf(codes.Internal, "search users: %v", err)
		}
	}
	out := &authspec.SearchUsersResponse{
		Status: "ok",
		Users:  make([]*apiobjects.User, 0, len(users)),
	}
	for i := range users {
		out.Users = append(out.Users, userModelToProto(&users[i]))
	}
	return out, nil
}

// UpdateUserPassword updates a user's password. In the legacy hula
// model, user passwords are managed separately (argon2 hashes stored
// in a parallel structure, not on the User row). Phase 0 wires this to
// the same path `handler.UpdateAdminHash` exposes — but only for the
// admin user. Non-admin password updates require the Bolt user store.
func (s *Server) UpdateUserPassword(ctx context.Context, req *authspec.UpdateUserPasswordRequest) (*authspec.UpdateUserPasswordResponse, error) {
	if req.GetUserId() == "" || req.GetNewPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id and new_password are required")
	}
	// Only the admin (root) user has password storage today (in
	// cfg.Admin.Hash). The legacy flow rewrote config via hulactl, not
	// through an API endpoint. For Phase 0 we return Unimplemented for
	// non-admin users and document the migration requirement. Admin
	// password update over this RPC would require persisting back to
	// config.yaml, which is outside Phase 0's scope.
	return nil, status.Error(codes.Unimplemented, "password update requires Bolt user store (Phase 1)")
}

// SetUserSysAdmin flips the sysadmin flag on a user. In the legacy
// ClickHouse user model there's no sysadmin column — admin privilege
// is entirely configured via cfg.Admin. Deferred to the Bolt user store
// migration.
func (s *Server) SetUserSysAdmin(ctx context.Context, req *authspec.SetUserSysAdminRequest) (*authspec.SetUserSysAdminResponse, error) {
	return nil, status.Error(codes.Unimplemented, "sysadmin flag requires Bolt user store (Phase 1)")
}

// RefreshToken reissues a JWT with updated permissions. Phase 0
// implementation: verify the current token, decode its subject, and
// issue a fresh token via model.NewJWTClaimsCommit. Does not check
// the permissions-stale flag (that's a Bolt-store feature).
func (s *Server) RefreshToken(ctx context.Context, req *authspec.RefreshTokenRequest) (*authspec.RefreshTokenResponse, error) {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		return nil, status.Error(codes.Unauthenticated, "no claims in context")
	}
	var userid string
	if claims.Username != "" {
		userid = claims.Username
	} else {
		userid = claims.Subject
	}
	if userid == "" {
		return nil, status.Error(codes.Unauthenticated, "no user identity in claims")
	}
	isAdmin := hasRole(claims.Roles, "admin") || hasRole(claims.Roles, "root")
	jwt, err := model.NewJWTClaimsCommit(model.GetDB(), userid, &model.LoginOpts{IsAdmin: isAdmin})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue jwt: %v", err)
	}
	return &authspec.RefreshTokenResponse{
		Status:             "ok",
		Token:              jwt,
		PermissionsChanged: false,
	}, nil
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

// callerUsername pulls the caller's username from the authware Claims
// in context. Returns an Unauthenticated error if no claims are present.
func callerUsername(ctx context.Context) (string, error) {
	claims, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		return "", status.Error(codes.Unauthenticated, "no claims in context")
	}
	if claims.Username != "" {
		return claims.Username, nil
	}
	return claims.Subject, nil
}

// TotpStatus reports whether TOTP is enrolled / pending / enforced for
// the authenticated caller. Public: any authenticated user can ask
// about their own TOTP state.
func (s *Server) TotpStatus(ctx context.Context, req *authspec.TotpStatusRequest) (*authspec.TotpStatusResponse, error) {
	username, err := callerUsername(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "totp lookup: %v", err)
	}
	resp := &authspec.TotpStatusResponse{Status: "ok"}
	if rec != nil {
		resp.TotpEnabled = rec.TotpEnabled
		resp.TotpPendingSetup = rec.TotpPendingSetup
		resp.RecoveryCodesRemaining = int32(len(rec.TotpRecoveryCodesHashed))
		if !rec.TotpEnabledAt.IsZero() {
			resp.TotpEnabledAt = timestamppb.New(rec.TotpEnabledAt)
		}
	}
	return resp, nil
}

// TotpSetup begins TOTP enrollment for the authenticated caller.
// Returns the secret, provisioning URI, and fresh recovery codes.
// Mirrors handler.TotpSetup.
func (s *Server) TotpSetup(ctx context.Context, req *authspec.TotpSetupRequest) (*authspec.TotpSetupResponse, error) {
	conf := config.GetConfig()
	if conf == nil {
		return nil, status.Error(codes.FailedPrecondition, "config not loaded")
	}
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "TOTP not configured: %v", err)
	}
	username, err := callerUsername(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "totp lookup: %v", err)
	}
	if existing != nil && existing.TotpEnabled {
		return nil, status.Error(codes.AlreadyExists, "TOTP already enabled; disable first to re-enroll")
	}
	issuer := conf.TotpIssuer
	if issuer == "" {
		issuer = "Hulation"
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: username})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate TOTP: %v", err)
	}
	encrypted, err := utils.EncryptTOTPSecret(key.Secret(), encKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	recCodes, err := utils.GenerateRecoveryCodes(8)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "recovery codes: %v", err)
	}
	hashed, err := utils.HashRecoveryCodes(recCodes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "hash recovery codes: %v", err)
	}
	rec := &model.AdminTotpRecord{
		Username:                username,
		TotpEnabled:             false,
		TotpPendingSetup:        true,
		TotpSecretEncrypted:     encrypted,
		TotpRecoveryCodesHashed: hashed,
	}
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return nil, status.Errorf(codes.Internal, "save TOTP: %v", err)
	}
	return &authspec.TotpSetupResponse{
		Status:          "ok",
		Secret:          key.Secret(),
		ProvisioningUri: key.URL(),
		RecoveryCodes:   recCodes,
	}, nil
}

// TotpVerifySetup completes enrollment by verifying the first
// authenticator code. Mirrors handler.TotpVerifySetup.
func (s *Server) TotpVerifySetup(ctx context.Context, req *authspec.TotpVerifySetupRequest) (*authspec.TotpVerifySetupResponse, error) {
	if req.GetCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "code is required")
	}
	conf := config.GetConfig()
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "TOTP not configured: %v", err)
	}
	username, err := callerUsername(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil {
		return nil, status.Error(codes.FailedPrecondition, "no pending TOTP setup")
	}
	if !rec.TotpPendingSetup {
		return nil, status.Error(codes.FailedPrecondition, "no pending TOTP setup")
	}
	secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt secret: %v", err)
	}
	if !totp.Validate(req.GetCode(), secret) {
		return nil, status.Error(codes.Unauthenticated, "invalid code")
	}
	rec.TotpEnabled = true
	rec.TotpPendingSetup = false
	rec.TotpEnabledAt = time.Now()
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return nil, status.Errorf(codes.Internal, "save TOTP: %v", err)
	}
	aLog.Infof("TOTP enabled for %s", username)
	return &authspec.TotpVerifySetupResponse{Status: "totp_enabled"}, nil
}

// TotpDisable turns off TOTP for the authenticated caller. Requires a
// valid current TOTP code.
func (s *Server) TotpDisable(ctx context.Context, req *authspec.TotpDisableRequest) (*authspec.TotpDisableResponse, error) {
	if req.GetCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "code is required")
	}
	conf := config.GetConfig()
	encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "TOTP not configured: %v", err)
	}
	username, err := callerUsername(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil || !rec.TotpEnabled {
		return nil, status.Error(codes.FailedPrecondition, "TOTP not enabled")
	}
	secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt secret: %v", err)
	}
	if !totp.Validate(req.GetCode(), secret) {
		return nil, status.Error(codes.Unauthenticated, "invalid code")
	}
	rec.TotpEnabled = false
	rec.TotpPendingSetup = false
	rec.TotpSecretEncrypted = ""
	rec.TotpRecoveryCodesHashed = nil
	rec.TotpEnabledAt = time.Time{}
	if err := model.UpsertAdminTotp(model.GetDB(), rec); err != nil {
		return nil, status.Errorf(codes.Internal, "save TOTP: %v", err)
	}
	aLog.Infof("TOTP disabled for %s", username)
	return &authspec.TotpDisableResponse{Status: "totp_disabled"}, nil
}

// TotpValidate validates a code (or recovery code) using a totp_pending
// token. On success returns a full JWT via model.NewJWTClaimsCommit.
// Mirrors handler.TotpValidate.
func (s *Server) TotpValidate(ctx context.Context, req *authspec.TotpValidateRequest) (*authspec.TotpValidateResponse, error) {
	if req.GetTotpPendingToken() == "" || req.GetCode() == "" {
		return nil, status.Error(codes.InvalidArgument, "totp_pending_token and code are required")
	}
	ok, perms, err := model.VerifyJWTClaims(model.GetDB(), req.GetTotpPendingToken())
	if err != nil || !ok {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	if !perms.HasCap("totp_pending") {
		return nil, status.Error(codes.Unauthenticated, "not a totp-pending token")
	}
	username := perms.UserID

	rec, err := model.GetAdminTotp(model.GetDB(), username)
	if err != nil || rec == nil || !rec.TotpEnabled {
		return nil, status.Error(codes.FailedPrecondition, "TOTP not enabled")
	}

	if req.GetIsRecoveryCode() {
		matched, verr := model.VerifyRecoveryCode(model.GetDB(), rec, req.GetCode())
		if verr != nil {
			return nil, status.Errorf(codes.Internal, "verify recovery: %v", verr)
		}
		if !matched {
			return nil, status.Error(codes.Unauthenticated, "invalid recovery code")
		}
	} else {
		conf := config.GetConfig()
		encKey, err := utils.GetTOTPEncryptionKey(conf.TotpEncryptionKey)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "TOTP not configured: %v", err)
		}
		secret, err := utils.DecryptTOTPSecret(rec.TotpSecretEncrypted, encKey)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "decrypt secret: %v", err)
		}
		if !totp.Validate(req.GetCode(), secret) {
			return nil, status.Error(codes.Unauthenticated, "invalid code")
		}
	}

	// Issue a full JWT (admin flag — LoginAdmin sets it; TOTP validation
	// upgrades the totp_pending token to a full one with the same admin
	// privilege).
	jwt, err := model.NewJWTClaimsCommit(model.GetDB(), username, &model.LoginOpts{IsAdmin: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "issue jwt: %v", err)
	}
	return &authspec.TotpValidateResponse{Status: "ok", Token: jwt}, nil
}
