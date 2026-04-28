package internal

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tlalocweb/hulation/pkg/server/authware/tokens"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"
	"github.com/tlalocweb/hulation/pkg/utils"
)

// IzcrProvider implements local password authentication for internal users
type IzcrProvider struct {
	baseprovider.BaseProvider
	jwtFac *tokens.JWTFactory
}

func NewIzcrProvider(cfg *baseprovider.AuthProviderConfig) (*IzcrProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil provider config")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = "Izcr"
	}
	return &IzcrProvider{
		BaseProvider: baseprovider.BaseProvider{Config: cfg},
	}, nil
}

func (p *IzcrProvider) Initialize(ctx context.Context) error {
	v := ctx.Value(baseprovider.CtxKeyJWTFactory)
	if fac, ok := v.(*tokens.JWTFactory); ok && fac != nil {
		p.jwtFac = fac
		return nil
	}
	return fmt.Errorf("jwt factory not provided")
}

func (p *IzcrProvider) Shutdown(ctx context.Context) error { return nil }
func (p *IzcrProvider) IsHealthy(ctx context.Context) bool { return true }

// LoginWithSecret authenticates against stored internal users
func (p *IzcrProvider) LoginWithSecret(ctx context.Context, req *authspec.LoginWithSecretRequest) (resp *authspec.LoginWithSecretResponse, err error) {
	if req == nil || strings.TrimSpace(req.Identity) == "" || strings.TrimSpace(req.Secret) == "" {
		return nil, fmt.Errorf("missing credentials")
	}

	// Use the provider from the request, or fall back to this provider's name
	providerName := req.Provider
	if providerName == "" {
		providerName = p.Name()
	}

	user, err := apiobjects.FindUserByIdentity(req.Identity, providerName)
	if err != nil || user == nil {
		// If not found by identity, try searching by username or email
		users, err := apiobjects.ListUsers("")
		if err == nil {
			for _, u := range users {
				if u.AuthProvider != nil && u.AuthProvider.ProviderName == providerName {
					// Match by username or email
					if u.Username == req.Identity || u.Email == req.Identity {
						user = u
						break
					}
				}
			}
		}
	}

	// Check if user was found and has a password hash
	if user == nil || user.PasswordHash == nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	match, err := utils.Argon2CompareHashAndSecret(req.Secret, *user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("error comparing secret: %v", err)
	}
	if !match {
		return nil, fmt.Errorf("invalid credentials")
	}
	// no, with internal provider - the password must be sha256 hashed before it hits the wire
	// if !match {
	// 	// also accept sha256(password) pre-hash compatibility
	// 	networkHash := utils.GenerateizcrNetworkPassHash(req.Secret)
	// 	match, err = utils.Argon2CompareHashAndSecret(networkHash, *user.PasswordHash)
	// 	if err != nil {
	// 		return nil, fmt.Errorf("error comparing secret: %v", err)
	// 	}
	// 	if !match {
	// 		return nil, fmt.Errorf("invalid credentials")
	// 	}
	// }

	// If first login (last_auth_change_at is zero), update it to mark auth setup complete
	if user.LastAuthChangeAt == nil || user.LastAuthChangeAt.AsTime().IsZero() {
		err = apiobjects.CreateOrOverwriteUserByIdentity(user.ExternalIdentity, providerName, func(u *apiobjects.User, isnew bool) (error, bool) {
			u.LastAuthChangeAt = timestamppb.Now()
			return nil, false
		})
		if err != nil {
			// Log but don't fail login
			fmt.Printf("Warning: failed to update last_auth_change_at for user %s: %v\n", user.Username, err)
		}
		// Re-fetch user to get updated state for JWT
		user, err = apiobjects.FindUserByIdentity(user.ExternalIdentity, providerName)
		if err != nil || user == nil {
			return nil, fmt.Errorf("error fetching user after update")
		}
	}

	// Check if TOTP is enabled for this user
	if user.TotpEnabled {
		// Issue a TOTP pending token instead of a full token
		limitedToken, _, err := p.jwtFac.CreateTotpPendingToken(user)
		if err != nil {
			return nil, fmt.Errorf("error creating TOTP pending token: %v", err)
		}
		resp = &authspec.LoginWithSecretResponse{
			Provider:     p.Name(),
			Providertype: p.Type(),
			Username:     user.Username,
			Email:        user.Email,
			Token:        limitedToken,
			TotpRequired: true,
		}
		return resp, nil
	}

	// Check if any tenant requires TOTP but user hasn't set it up yet
	if totpRequiredByUserTenants(user) && !user.TotpEnabled {
		// Issue a TOTP pending token so user can set up TOTP
		limitedToken, _, err := p.jwtFac.CreateTotpPendingToken(user)
		if err != nil {
			return nil, fmt.Errorf("error creating TOTP pending token: %v", err)
		}
		resp = &authspec.LoginWithSecretResponse{
			Provider:     p.Name(),
			Providertype: p.Type(),
			Username:     user.Username,
			Email:        user.Email,
			Token:        limitedToken,
			TotpRequired: true,
		}
		return resp, nil
	}

	token, _, err := p.jwtFac.CreateToken(user)
	if err != nil {
		return nil, fmt.Errorf("error creating token: %v", err)
	}
	resp = &authspec.LoginWithSecretResponse{
		Provider:     p.Name(),
		Providertype: p.Type(),
		Username:     user.Username,
		Email:        user.Email,
		Token:        token,
	}
	return resp, nil
}

// totpRequiredByUserTenants: hulation v1 is single-tenant with no tenant
// TOTP policy. Returns false unconditionally; a later multi-tenant phase
// can reintroduce FindTenantByUUID-based policy lookup.
func totpRequiredByUserTenants(user *apiobjects.User) bool {
	_ = user
	return false
}

// LoginOIDC is not supported for local provider
func (p *IzcrProvider) LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (resp *authspec.LoginOIDCResponse, err error) {
	return nil, fmt.Errorf("not implemented")
}

// ValidateToken: tokens for this provider are JWTs issued by the core JWTFactory and validated upstream
func (p *IzcrProvider) ValidateToken(token string) (user *apiobjects.User, valid bool, err error) {
	return nil, false, nil
}

// UpdatePassword updates the password for an internal user
func (p *IzcrProvider) UpdatePassword(ctx context.Context, req *authspec.UpdatePasswordRequest) (resp *authspec.UpdatePasswordResponse, err error) {
	if req == nil || strings.TrimSpace(req.Identity) == "" {
		return nil, fmt.Errorf("missing identity")
	}
	if strings.TrimSpace(req.OldPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
		return nil, fmt.Errorf("missing password")
	}

	// Use the provider from the request, or fall back to this provider's name
	providerName := req.Provider
	if providerName == "" {
		providerName = p.Name()
	}

	// Find the user
	user, err := apiobjects.FindUserByIdentity(req.Identity, providerName)
	if err != nil || user == nil {
		// If not found by identity, try searching by username or email
		users, err := apiobjects.ListUsers("")
		if err == nil {
			for _, u := range users {
				if u.AuthProvider != nil && u.AuthProvider.ProviderName == providerName {
					// Match by username or email
					if u.Username == req.Identity || u.Email == req.Identity {
						user = u
						break
					}
				}
			}
		}
	}

	// Check if user was found
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	// Verify the user has a password hash
	if user.PasswordHash == nil || *user.PasswordHash == "" {
		return nil, fmt.Errorf("user has no password set")
	}

	// Verify old password
	match, err := utils.Argon2CompareHashAndSecret(req.OldPassword, *user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("error verifying password: %v", err)
	}
	if !match {
		return nil, fmt.Errorf("invalid old password")
	}

	// Hash the new password
	newHash, err := utils.Argon2GenerateFromSecretDefaults(req.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to hash new password: %v", err)
	}

	// Update the user with the new password hash
	err = apiobjects.CreateOrOverwriteUserByIdentity(user.ExternalIdentity, providerName, func(u *apiobjects.User, isnew bool) (error, bool) {
		u.PasswordHash = &newHash
		return nil, false
	})

	if err != nil {
		return nil, fmt.Errorf("failed to update password: %v", err)
	}

	resp = &authspec.UpdatePasswordResponse{
		Status: "success",
	}
	return resp, nil
}
