package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tlalocweb/hulation/pkg/server/authware/tokens"
	"github.com/tlalocweb/hulation/log"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	baseprovider "github.com/tlalocweb/hulation/pkg/server/authware/provider/base"
)

var logger *log.Sublogger

func init() {
	logger = log.GetSubLogger("oidcprov")
}

// OIDCConfig holds the OIDC provider-specific configuration
type OIDCConfig struct {
	Issuer        string   `yaml:"issuer"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  string   `yaml:"client_secret"`
	RedirectURI   string   `yaml:"redirect_uri"`
	Scopes        []string `yaml:"scopes"`
	NewUserStatus string   `yaml:"new_user_status"` // "pending" or "active"
	DisplayName   string   `yaml:"display_name"`
	IconURL       string   `yaml:"icon_url"`
	// Claim mappings
	ClaimMappings struct {
		Identity    string `yaml:"identity"`     // Which claim to use as identity (default: "email")
		DisplayName string `yaml:"display_name"` // Which claim to use as display name (default: "name")
		Email       string `yaml:"email"`        // Which claim to use as email (default: "email")
	} `yaml:"claim_mappings"`
}

// pendingAuth stores pending OIDC auth flows
type pendingAuth struct {
	state     string
	nonce     string
	createdAt time.Time
}

// OIDCProvider implements authentication using generic OIDC
type OIDCProvider struct {
	baseprovider.BaseProvider
	config       OIDCConfig
	provider     *oidc.Provider
	oauth2Config *oauth2.Config
	verifier     *oidc.IDTokenVerifier
	jwtFac       *tokens.JWTFactory

	// Pending auth flows (state -> pendingAuth)
	pendingMu sync.RWMutex
	pending   map[string]*pendingAuth
}

func NewOIDCProvider(cfg *baseprovider.AuthProviderConfig) (*OIDCProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil provider config")
	}

	p := &OIDCProvider{
		BaseProvider: baseprovider.BaseProvider{Config: cfg},
		pending:      make(map[string]*pendingAuth),
	}

	// Decode provider-specific config
	if err := cfg.DecodeConfig(&p.config); err != nil {
		return nil, fmt.Errorf("failed to decode OIDC config: %w", err)
	}

	// Validate required fields
	if p.config.Issuer == "" {
		return nil, fmt.Errorf("OIDC issuer is required")
	}
	if p.config.ClientID == "" {
		return nil, fmt.Errorf("OIDC client_id is required")
	}
	if p.config.ClientSecret == "" {
		return nil, fmt.Errorf("OIDC client_secret is required")
	}
	if p.config.RedirectURI == "" {
		return nil, fmt.Errorf("OIDC redirect_uri is required")
	}

	// Set defaults
	if len(p.config.Scopes) == 0 {
		p.config.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if p.config.NewUserStatus == "" {
		p.config.NewUserStatus = "pending"
	}
	if p.config.ClaimMappings.Identity == "" {
		p.config.ClaimMappings.Identity = "email"
	}
	if p.config.ClaimMappings.DisplayName == "" {
		p.config.ClaimMappings.DisplayName = "name"
	}
	if p.config.ClaimMappings.Email == "" {
		p.config.ClaimMappings.Email = "email"
	}

	return p, nil
}

func (p *OIDCProvider) Initialize(ctx context.Context) error {
	// Get JWT factory from context
	v := ctx.Value(baseprovider.CtxKeyJWTFactory)
	if fac, ok := v.(*tokens.JWTFactory); ok && fac != nil {
		p.jwtFac = fac
	} else {
		return fmt.Errorf("jwt factory not provided")
	}

	// Initialize OIDC provider with discovery
	provider, err := oidc.NewProvider(ctx, p.config.Issuer)
	if err != nil {
		return fmt.Errorf("failed to initialize OIDC provider for %s: %w", p.config.Issuer, err)
	}
	p.provider = provider

	// Configure OAuth2
	p.oauth2Config = &oauth2.Config{
		ClientID:     p.config.ClientID,
		ClientSecret: p.config.ClientSecret,
		RedirectURL:  p.config.RedirectURI,
		Endpoint:     provider.Endpoint(),
		Scopes:       p.config.Scopes,
	}

	// Configure ID token verifier
	p.verifier = provider.Verifier(&oidc.Config{
		ClientID: p.config.ClientID,
	})

	logger.Infof("OIDC provider '%s' initialized with issuer: %s", p.Name(), p.config.Issuer)

	// Start cleanup goroutine for expired pending auths
	go p.cleanupPendingAuths()

	return nil
}

func (p *OIDCProvider) Shutdown(ctx context.Context) error {
	return nil
}

func (p *OIDCProvider) IsHealthy(ctx context.Context) bool {
	return p.provider != nil
}

// generateState creates a random state string for CSRF protection
func generateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// generateNonce creates a random nonce for replay protection
func generateNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// LoginOIDC initiates the OIDC login flow
func (p *OIDCProvider) LoginOIDC(ctx context.Context, req *authspec.LoginOIDCRequest) (resp *authspec.LoginOIDCResponse, err error) {
	if p.oauth2Config == nil {
		return nil, fmt.Errorf("OIDC provider not initialized")
	}

	randomState, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	nonce, err := generateNonce()
	if err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encode provider name in state: "providerName:randomState"
	state := p.Name() + ":" + randomState

	// Store pending auth using full state as key
	p.pendingMu.Lock()
	p.pending[state] = &pendingAuth{
		state:     state,
		nonce:     nonce,
		createdAt: time.Now(),
	}
	p.pendingMu.Unlock()

	// Generate auth URL with nonce
	authURL := p.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce))

	resp = &authspec.LoginOIDCResponse{
		Url:          authURL,
		Instructions: fmt.Sprintf("Please visit the URL to authenticate with %s", p.config.DisplayName),
	}

	logger.Debugf("OIDC login initiated for provider '%s', state: %s", p.Name(), state[:16]+"...")

	return resp, nil
}

// GetAuthURLForCallback returns the authorization URL for one-click login (used by ListAuthProviders)
func (p *OIDCProvider) GetAuthURLForCallback() (string, error) {
	if p.oauth2Config == nil {
		return "", fmt.Errorf("OIDC provider not initialized")
	}

	randomState, err := generateState()
	if err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}

	nonce, err := generateNonce()
	if err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encode provider name in state: "providerName:randomState"
	state := p.Name() + ":" + randomState

	// Store pending auth
	p.pendingMu.Lock()
	p.pending[state] = &pendingAuth{
		state:     state,
		nonce:     nonce,
		createdAt: time.Now(),
	}
	p.pendingMu.Unlock()

	return p.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce)), nil
}

// ExchangeCode exchanges an authorization code for tokens and creates/updates a user
func (p *OIDCProvider) ExchangeCode(ctx context.Context, code string, state string) (token string, err error) {
	if p.oauth2Config == nil || p.verifier == nil {
		return "", fmt.Errorf("OIDC provider not initialized")
	}

	// Verify state and get pending auth
	p.pendingMu.Lock()
	pending, ok := p.pending[state]
	if ok {
		delete(p.pending, state)
	}
	p.pendingMu.Unlock()

	if !ok {
		return "", fmt.Errorf("invalid or expired state")
	}

	// Exchange code for tokens
	oauth2Token, err := p.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code: %w", err)
	}

	// Extract ID token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		return "", fmt.Errorf("no id_token in token response")
	}

	// Verify ID token
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return "", fmt.Errorf("failed to verify ID token: %w", err)
	}

	// Verify nonce
	if idToken.Nonce != pending.nonce {
		return "", fmt.Errorf("nonce mismatch")
	}

	// Extract claims
	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return "", fmt.Errorf("failed to extract claims: %w", err)
	}

	// Get identity from claims
	identity, _ := claims[p.config.ClaimMappings.Identity].(string)
	if identity == "" {
		return "", fmt.Errorf("identity claim '%s' not found or empty", p.config.ClaimMappings.Identity)
	}

	displayName, _ := claims[p.config.ClaimMappings.DisplayName].(string)
	email, _ := claims[p.config.ClaimMappings.Email].(string)

	// Find or create user
	user, err := apiobjects.FindUserByIdentity(identity, p.Name())
	if err != nil || user == nil {
		// User not found by OIDC identity - check if there's an invited user with matching email
		if email != "" {
			// First, check for a user with "pending" provider (invited users who haven't picked auth method)
			existingUser, findErr := apiobjects.FindUserByIdentity(email, authware.AUTH_PROVIDER_PENDING)
			if findErr != nil || existingUser == nil {
				// Also check by email as fallback
				existingUser, findErr = apiobjects.FindUserByEmail(email)
			}
			if findErr == nil && existingUser != nil {
				// Found user - check if it's an invited user pending auth setup
				oldProviderName := authware.AUTH_PROVIDER_PENDING
				if existingUser.AuthProvider != nil && existingUser.AuthProvider.ProviderName != "" {
					oldProviderName = existingUser.AuthProvider.ProviderName
				}

				if existingUser.LastAuthChangeAt == nil || existingUser.LastAuthChangeAt.AsTime().IsZero() {
					// User was invited and hasn't completed auth setup - link OIDC to this account
					logger.Infof("Linking OIDC identity '%s' to invited user with email '%s' (old provider: %s, old identity: %s)", identity, email, oldProviderName, existingUser.ExternalIdentity)

					// Update the existing user using OLD provider name so we find it by the existing index
					// The user's ExternalIdentity will change to the OIDC identity, and the AuthProvider
					// will change to the OIDC provider - these changes will update the index automatically
					err = apiobjects.CreateOrOverwriteUserByIdentity(existingUser.ExternalIdentity, oldProviderName, func(u *apiobjects.User, isnew bool) (error, bool) {
						// Update identity to OIDC identity
						u.ExternalIdentity = identity
						u.Username = existingUser.Username
						u.Email = email
						if displayName != "" {
							u.DisplayName = displayName
						} else {
							u.DisplayName = existingUser.DisplayName
						}
						// Link OIDC provider info
						u.AuthProvider = &apiobjects.AuthProviderRef{
							ProviderName: p.Name(),
							ProviderType: "oidc",
						}
						// Mark auth setup as complete
						u.LastAuthChangeAt = timestamppb.Now()
						// Mark email as validated
						u.ValidatedAt = timestamppb.Now()
						// Enable the user
						disabledFalse := false
						u.Disabled = &disabledFalse
						// Preserve roles
						// DEPRECATED: SystemRoles and ProjectRoles fields are deprecated, use RoleAssignments instead
						// u.SystemRoles = existingUser.SystemRoles
						// u.ProjectRoles = existingUser.ProjectRoles
						// Clear pending validations
						u.PendingValidation = nil
						return nil, false
					})
					if err != nil {
						return "", fmt.Errorf("failed to link OIDC to invited user: %w", err)
					}

					// Delete the old provider index entry (it's now stale since identity and/or provider changed)
					if oldProviderName != p.Name() || existingUser.ExternalIdentity != identity {
						deleteErr := apiobjects.DeleteUserByIdentity(existingUser.ExternalIdentity, oldProviderName)
						if deleteErr != nil {
							logger.Warnf("Failed to delete old '%s' provider index for user %s (identity: %s): %v", oldProviderName, email, existingUser.ExternalIdentity, deleteErr)
						} else {
							logger.Debugf("Deleted old '%s' provider index for user %s (identity: %s)", oldProviderName, email, existingUser.ExternalIdentity)
						}
					}

					// Fetch the updated user
					user, err = apiobjects.FindUserByIdentity(identity, p.Name())
					if err != nil || user == nil {
						return "", fmt.Errorf("failed to fetch user after OIDC linking")
					}

					logger.Infof("Successfully linked OIDC identity '%s' to invited user '%s'", identity, existingUser.Username)
				} else {
					// User already has auth setup with different identity - reject
					return "", fmt.Errorf("account with email %s already exists with different authentication method", email)
				}
			}
		}

		// If still no user, create a new one
		if user == nil {
			err = apiobjects.CreateOrOverwriteUserByIdentity(identity, p.Name(), func(u *apiobjects.User, isnew bool) (error, bool) {
				u.ExternalIdentity = identity
				u.Username = identity
				u.Email = email
				u.DisplayName = displayName
				u.AuthProvider = &apiobjects.AuthProviderRef{
					ProviderName: p.Name(),
					ProviderType: "oidc",
				}
				// Set disabled status based on config
				// "active" means not disabled, "pending" means disabled until approved
				disabledFalse := false
				disabledTrue := true
				if p.config.NewUserStatus == "active" {
					u.Disabled = &disabledFalse
				} else {
					u.Disabled = &disabledTrue // Pending users are disabled until approved
				}
				// New OIDC users have completed auth setup
				u.LastAuthChangeAt = timestamppb.Now()
				return nil, false
			})
			if err != nil {
				return "", fmt.Errorf("failed to create user: %w", err)
			}

			// Fetch the created user
			user, err = apiobjects.FindUserByIdentity(identity, p.Name())
			if err != nil || user == nil {
				return "", fmt.Errorf("failed to fetch created user")
			}

			logger.Infof("Created new user '%s' via OIDC provider '%s'", identity, p.Name())
		}
	} else {
		// Update existing user info
		err = apiobjects.CreateOrOverwriteUserByIdentity(identity, p.Name(), func(u *apiobjects.User, isnew bool) (error, bool) {
			u.Email = email
			u.DisplayName = displayName
			// If first OIDC login (last_auth_change_at is zero), mark auth setup complete
			if u.LastAuthChangeAt == nil || u.LastAuthChangeAt.AsTime().IsZero() {
				u.LastAuthChangeAt = timestamppb.Now()
			}
			return nil, false
		})
		if err != nil {
			logger.Warnf("Failed to update user info for '%s': %v", identity, err)
		}

		// Re-fetch user to get current state (including any admin changes like enabling/disabling)
		user, err = apiobjects.FindUserByIdentity(identity, p.Name())
		if err != nil || user == nil {
			return "", fmt.Errorf("failed to fetch user after update: %v", err)
		}

		logger.Infof("User '%s' logged in via OIDC provider '%s'", identity, p.Name())
	}

	// Check if user is disabled
	if user.GetDisabled() {
		return "", fmt.Errorf("user account is disabled or pending approval")
	}

	// Generate JWT token
	jwtToken, _, err := p.jwtFac.CreateToken(user)
	if err != nil {
		return "", fmt.Errorf("failed to create token: %w", err)
	}

	return jwtToken, nil
}

// LoginWithSecret is not the primary method for OIDC but can be used for resource owner password grant
func (p *OIDCProvider) LoginWithSecret(ctx context.Context, req *authspec.LoginWithSecretRequest) (resp *authspec.LoginWithSecretResponse, err error) {
	// OIDC providers typically don't support direct password login
	// This could be implemented using resource owner password credentials grant if the IdP supports it
	return nil, fmt.Errorf("direct password login not supported for OIDC provider '%s', use LoginOIDC instead", p.Name())
}

// ValidateToken validates an ID token from this provider
func (p *OIDCProvider) ValidateToken(token string) (user *apiobjects.User, valid bool, err error) {
	if p.verifier == nil {
		return nil, false, fmt.Errorf("provider not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	idToken, err := p.verifier.Verify(ctx, token)
	if err != nil {
		return nil, false, nil // Invalid token
	}

	// Extract identity claim
	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, false, fmt.Errorf("failed to extract claims: %w", err)
	}

	identity, _ := claims[p.config.ClaimMappings.Identity].(string)
	if identity == "" {
		return nil, false, nil
	}

	// Find user
	user, err = apiobjects.FindUserByIdentity(identity, p.Name())
	if err != nil || user == nil {
		return nil, false, nil
	}

	return user, true, nil
}

// UpdatePassword is not supported for OIDC providers
func (p *OIDCProvider) UpdatePassword(ctx context.Context, req *authspec.UpdatePasswordRequest) (resp *authspec.UpdatePasswordResponse, err error) {
	return nil, fmt.Errorf("password management not supported for OIDC provider '%s'", p.Name())
}

// cleanupPendingAuths removes expired pending auth flows
func (p *OIDCProvider) cleanupPendingAuths() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		p.pendingMu.Lock()
		now := time.Now()
		for state, pending := range p.pending {
			// Remove entries older than 10 minutes
			if now.Sub(pending.createdAt) > 10*time.Minute {
				delete(p.pending, state)
			}
		}
		p.pendingMu.Unlock()
	}
}

// GetAuthURL returns the authorization URL for initiating login (convenience method)
func (p *OIDCProvider) GetAuthURL(state, nonce string) string {
	if p.oauth2Config == nil {
		return ""
	}
	return p.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce))
}

// GetDisplayName returns the display name for this provider
func (p *OIDCProvider) GetDisplayName() string {
	if p.config.DisplayName != "" {
		return p.config.DisplayName
	}
	// Capitalize first letter
	name := p.Name()
	if len(name) > 0 {
		return strings.ToUpper(name[:1]) + name[1:]
	}
	return name
}

// GetIconURL returns the icon URL for this provider
func (p *OIDCProvider) GetIconURL() string {
	return p.config.IconURL
}
