package authware

import (
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/tlalocweb/hulation/config"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
)

const (
	// basic role stuff for now
	ROLE_ROOT     = "root"     // root has the ability to create new admins
	ROLE_ADMIN    = "admin"    // admin can do full admin on izcr, but cant create new admins
	ROLE_OPERATOR = "operator" // operator can manage resources but not admin settings
	ROLE_VIEWER   = "viewer"   // viewer has read-only access
	ROLE_AUDITOR  = "auditor"  // auditor has audit/compliance access
	ROLE_USER     = "user"

	// Registry-specific roles
	ROLE_REGISTRY_USER      = "registry_user"      // full registry access (push/pull/delete)
	ROLE_REGISTRY_PULL_ONLY = "registry_pull_only" // read-only registry access (pull only)

	// Auth provider names - these are used for user identity indexing
	AUTH_PROVIDER_PENDING  = "pending"  // User invited but hasn't chosen auth method yet
	AUTH_PROVIDER_INTERNAL = "internal" // User authenticates with password stored in izcr

	// brainstorming here:
	// TENANT_ADMIN = "tenant_admin" // can create tenant users
	// TENANT_USER = "tenant_user" // can do stuff in a tenant
	// TENANT_OPERATOR = "tenant_operator" // can do add / delete pods in a tenant
	// TENANT_VIEWER = "tenant_viewer" // can observe applications in a tenant, but cant change anything
	// TENANT_APP_OPERATOR = "tenant_app_operator" // can do add / delete pods for a specific deployment in a tenant
	// TENANT_APP_VIEWER = "tenant_app_viewer" // can observe a specific deployment in a tenant, but cant change anything
)

// appendRoleIfMissing appends a role to the slice if it's not already present
func appendRoleIfMissing(roles []string, role string) []string {
	for _, r := range roles {
		if r == role {
			return roles
		}
	}
	return append(roles, role)
}

// these are the claims that are stored in the JWT token
// this custom claims here are the izcr specific claims
type Claims struct {
	jwt.RegisteredClaims          // standard claims (RFC 7519)
	Username             string   `json:"username"`
	Roles                []string `json:"roles"`
	// TenantRoles is a map of tenant name to role.
	// this enable fast lookup of role
	TenantRoles map[string]map[string]struct{} `json:"tenant_roles"`
	// Tenants is the list of tenant UUIDs the user is a member of.
	// Used for fast membership validation in OPA - actual permissions come from RBAC cache.
	// For superadmins, this will contain ["*"] indicating membership in all tenants.
	Tenants      []string `json:"tenants"`
	Permissions  []string `json:"permissions"`
	SessionID    string   `json:"session_id"`
	IPAddress    string   `json:"ip_address"`
	TokenVersion int64    `json:"token_version"`
	// a token provided by an identity provider if applicable
	IDProviderToken string `json:"id_provider_token"`
	// if using an identity provider, this is the name of the provider
	// (this name will match an entry in the izcr config under auth_providers)
	IDProviderName string `json:"id_provider_name"`
	// For registry-only users, this identifies the RegistryUser record
	RegistryUserUUID string `json:"registry_user_uuid,omitempty"`
	// Project-scoped access control for registry authentication
	// Maps project UUID to specific permissions for that project
	ProjectAccess map[string]ProjectAccessInfo `json:"project_access,omitempty"`
	// Email validation fields
	Email            string    `json:"email,omitempty"`
	EmailValidatedAt time.Time `json:"email_validated_at,omitempty"`
	// Auth setup fields
	LastAuthChangeAt time.Time `json:"last_auth_change_at,omitempty"`
	AuthSetupOnly    bool      `json:"auth_setup_only,omitempty"` // Limited token for password setup
	TotpPending      bool      `json:"totp_pending,omitempty"`    // Limited token pending TOTP validation

	// ============================================================================
	// Permission checksum (replaces AllowPermissions/DenyPermissions arrays)
	// ============================================================================

	// PermissionsChecksum is a checksum of the user's resolved permissions.
	// The full permission set is stored server-side in the PermissionCacheStore.
	// This checksum allows the middleware to look up the permissions without
	// storing potentially large permission arrays in the JWT.
	// A value of 0 indicates no permissions have been computed (e.g., admin tokens).
	PermissionsChecksum uint64 `json:"permissions_checksum,omitempty"`
}

// MapForOpaWithPermissions creates a map used by OPA with the given permissions.
// The allowPerms and denyPerms should be loaded from PermissionCacheStore using
// the PermissionsChecksum from the claims.
func (claims *Claims) MapForOpaWithPermissions(allowPerms, denyPerms []string) (m map[string]interface{}) {
	// Ensure project access is never nil (for backward compatibility)
	projectAccess := claims.ProjectAccess
	if projectAccess == nil {
		projectAccess = make(map[string]ProjectAccessInfo)
	}

	// Ensure permission slices are never nil
	if allowPerms == nil {
		allowPerms = []string{}
	}
	if denyPerms == nil {
		denyPerms = []string{}
	}

	// Ensure tenants is never nil
	tenants := claims.Tenants
	if tenants == nil {
		tenants = []string{}
	}

	// Convert claims to map[string]interface{}
	m = map[string]interface{}{
		"roles":             claims.Roles,
		"permissions":       claims.Permissions,
		"allow_permissions": allowPerms,
		"deny_permissions":  denyPerms,
		"project_access":    projectAccess,
		"tenants":           tenants,
		"user":              claims.RegisteredClaims.Subject,
		"exp":               claims.RegisteredClaims.ExpiresAt,
		"iat":               claims.RegisteredClaims.IssuedAt,
		"nbf":               claims.RegisteredClaims.NotBefore,
		"aud":               claims.RegisteredClaims.Audience,
		"iss":               claims.RegisteredClaims.Issuer,
	}
	return
}

// MapForOpa creates a map used by OPA with empty permissions.
// Deprecated: Use MapForOpaWithPermissions with permissions loaded from cache.
func (claims *Claims) MapForOpa() (m map[string]interface{}) {
	return claims.MapForOpaWithPermissions(nil, nil)
}

// IsEmailValidated returns true if the user's email has been validated
// (i.e., EmailValidatedAt is set and not zero)
func (c *Claims) IsEmailValidated() bool {
	return !c.EmailValidatedAt.IsZero()
}

// NeedsAuthSetup returns true if the user has never completed authentication setup
// (i.e., never set a password or logged in via IdP)
func (c *Claims) NeedsAuthSetup() bool {
	// If LastAuthChangeAt is set, setup is definitely complete
	if !c.LastAuthChangeAt.IsZero() {
		return false
	}
	// If user authenticated via OIDC (not "pending"/"internal"), setup is complete
	// This handles edge cases where browser has stale cookies with old claims
	if c.IDProviderName != "" &&
		c.IDProviderName != AUTH_PROVIDER_PENDING &&
		c.IDProviderName != AUTH_PROVIDER_INTERNAL &&
		c.IDProviderName != "izcr" {
		return false
	}
	return true
}

// IsAuthSetupToken returns true if this is a limited token that can only be used
// for authentication setup (setting password, linking IdP)
func (c *Claims) IsAuthSetupToken() bool {
	return c.AuthSetupOnly
}

// IsTotpPendingToken returns true if this is a limited token pending TOTP validation
func (c *Claims) IsTotpPendingToken() bool {
	return c.TotpPending
}

// getJWTIssuer returns the first hostname from registry_hostnames, falling back to hostname if not set
func getJWTIssuer() string {
	cfg := config.GetConfig()
	if cfg.RegistryHostnames != "" {
		// Split comma-separated hostnames and return the first one
		hostnames := strings.Split(cfg.RegistryHostnames, ",")
		if len(hostnames) > 0 {
			return strings.TrimSpace(hostnames[0])
		}
	}
	// Fallback to Hostname if RegistryHostnames is empty
	return cfg.Hostname
}

func GenClaimsForRoot(tokenid string, tokenversion int64, tokenduration time.Duration, sess *apiobjects.SessionAdapter, ip string) (claims *Claims) {
	claims = &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:  getJWTIssuer(),
			Subject: "admin",
			//			Audience:  jwt.ClaimStrings{"your-app"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenduration)),
			NotBefore: jwt.NewNumericDate(time.Now()),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        tokenid,
		},
		Roles: []string{ROLE_ADMIN, ROLE_ROOT},
		// Superadmins are members of all tenants - wildcard "*" indicates this
		Tenants: []string{"*"},
		//		Roles:        user.Roles,
		//		Permissions:  user.Permissions,
		SessionID: sess.GetSessionId(),
		IPAddress: ip,
		//		TenantID:     "tenant123",
		TokenVersion: tokenversion,
	}
	return
}
func GenClaimsForUser(user *apiobjects.User, tokenid string, tokenversion int64, tokenduration time.Duration, sess *apiobjects.SessionAdapter, ip string) (claims *Claims) {
	// Build roles list - all authenticated users get the base user role
	roles := []string{ROLE_USER}

	// NEW: Read from role_assignments (the new RBAC system)
	for _, ra := range user.RoleAssignments {
		if ra.ScopeType == "system" {
			switch ra.RoleName {
			case "admin":
				roles = appendRoleIfMissing(roles, ROLE_ADMIN)
			case "root":
				roles = appendRoleIfMissing(roles, ROLE_ROOT)
			// NOTE: operator, viewer, auditor are deprecated and not mapped
			}
		}
	}

	// LEGACY: Also check old SystemRoles for backward compatibility
	// This allows existing users with SystemRoles to continue working
	// DEPRECATED: SystemRoles field is deprecated, use RoleAssignments instead
	// for _, sysRole := range user.SystemRoles {
	// 	if sysRole == apiobjects.SystemRole_SYSTEM_ROLE_ADMIN {
	// 		roles = appendRoleIfMissing(roles, ROLE_ADMIN)
	// 	}
	// 	// NOTE: SYSTEM_ROLE_OPERATOR, SYSTEM_ROLE_VIEWER, SYSTEM_ROLE_AUDITOR are deprecated
	// 	// and no longer mapped to roles
	// }

	// LEGACY: Also check if user is marked as sysadmin (another legacy field)
	if user.SysAdmin {
		roles = appendRoleIfMissing(roles, ROLE_ADMIN)
	}

	// Build tenants list from role assignments
	// If user has admin/root role, use "*" wildcard for all tenants
	tenants := []string{}
	isSuperAdmin := false
	for _, r := range roles {
		if r == ROLE_ADMIN || r == ROLE_ROOT {
			isSuperAdmin = true
			break
		}
	}
	if isSuperAdmin {
		tenants = []string{"*"}
	} else {
		// Collect unique tenant UUIDs from role assignments with tenant scope
		tenantSet := make(map[string]struct{})
		for _, ra := range user.RoleAssignments {
			if ra.ScopeType == "tenant" && ra.TenantUuid != "" {
				tenantSet[ra.TenantUuid] = struct{}{}
			}
		}
		for t := range tenantSet {
			tenants = append(tenants, t)
		}
	}

	claims = &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    getJWTIssuer(),
			Subject:   user.Uuid,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenduration)),
			NotBefore: jwt.NewNumericDate(time.Now()),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        tokenid,
		},
		Roles:        roles,
		Username:     user.ExternalIdentity,
		TenantRoles:  make(map[string]map[string]struct{}),
		Tenants:      tenants,
		SessionID:    sess.GetSessionId(),
		IPAddress:    ip,
		TokenVersion: tokenversion,
		Email:        user.Email,
	}

	// Set email validation time if present
	if user.ValidatedAt != nil && user.ValidatedAt.IsValid() {
		claims.EmailValidatedAt = user.ValidatedAt.AsTime()
	}

	// Set last auth change time if present
	if user.LastAuthChangeAt != nil && user.LastAuthChangeAt.IsValid() {
		claims.LastAuthChangeAt = user.LastAuthChangeAt.AsTime()
	}

	// Extract provider name from AuthProviderRef
	if user.AuthProvider != nil {
		claims.IDProviderName = user.AuthProvider.ProviderName
	} else {
		claims.IDProviderName = "izcr" // default provider
	}

	// TODO: Map ProjectRoles to TenantRoles/Permissions if needed for project-level access control

	return
}

// GenClaimsForRegistryUser (izcr-only) intentionally omitted: hulation has
// no OCI RegistryUser concept. The RegistryUserUUID field on Claims remains
// for JSON compatibility but is never populated.
