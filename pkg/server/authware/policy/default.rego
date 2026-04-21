package izcr.authz

import future.keywords.if
import future.keywords.in

# Default deny - all requests are denied unless explicitly allowed
default allow = false

# Public endpoints that don't require authentication
public_endpoints := {
    "/izcr.v1.auth.AuthService/LoginAdmin",
    "/izcr.v1.auth.AuthService/LoginWithSecret",
    "/izcr.v1.auth.AuthService/LoginOIDC",
    "/izcr.v1.auth.AuthService/LoginWithCode",
    "/izcr.v1.HealthService/Check",
    "/api/v1/auth/admin",
    "/api/v1/auth/secret",
    "/api/v1/auth/oidc",
    "/api/v1/auth/code",
    "/api/v1/health"
}

# Allow access to public endpoints without authentication
allow if {
    public_endpoints[input.path]
}

# ============================================================================
# Tenant membership validation
# ============================================================================

# User is a member of the requested tenant (or has wildcard "*")
user_is_tenant_member if {
    input.tenants[_] == "*"  # Superadmin wildcard - member of all tenants
}

user_is_tenant_member if {
    input.request_params.tenant_uuid
    input.tenants[_] == input.request_params.tenant_uuid
}

# Tenant membership is not required if no tenant_uuid in request
tenant_check_passed if {
    not input.request_params.tenant_uuid
}

# Tenant membership check passes if user is a member
tenant_check_passed if {
    input.request_params.tenant_uuid
    user_is_tenant_member
}

# Add deny reason for tenant membership failure
deny_reason := "not a member of the requested tenant" if {
    input.request_params.tenant_uuid
    not user_is_tenant_member
}

# ============================================================================
# Permission-based authorization (NEW - primary authorization path)
# ============================================================================

# Allow if permission check passed (computed in Go middleware)
# The Go middleware resolves placeholders, checks allow/deny, and sets this flag
# Also requires tenant membership check for tenant-scoped requests
allow if {
    input.permission_check_passed == true
    tenant_check_passed
}

# Permission-based access: check if user has required permissions
# This is used when the Go middleware passes required_permissions to OPA
# Also requires tenant membership check for tenant-scoped requests
allow if {
    count(input.required_permissions) > 0
    user_has_required_permissions
    tenant_check_passed
}

# Check if user has at least one of the required permissions (OR logic)
# Note: Complex wildcard matching is done in Go; this handles exact matches only
user_has_required_permissions if {
    input.require_all != true
    some req in input.required_permissions
    has_permission_allow(req)
    not has_permission_deny(req)
}

# Check if user has ALL required permissions (AND logic)
user_has_required_permissions if {
    input.require_all == true
    every req in input.required_permissions {
        has_permission_allow(req)
        not has_permission_deny(req)
    }
}

# Check if any allow permission matches (exact match for OPA, wildcard done in Go)
has_permission_allow(required) if {
    some allowed in input.allow_permissions
    allowed == required
}

# Check if any deny permission matches
has_permission_deny(required) if {
    some denied in input.deny_permissions
    denied == required
}

# ============================================================================
# Role-based authorization (fallback for unannotated endpoints)
# ============================================================================

# Root users have full access to everything
allow if {
    input.roles[_] == "root"
}

# Admin users have full access to everything
allow if {
    input.roles[_] == "admin"
}

# User-level permissions
# Users can access KV APIs for their own data
allow if {
    input.roles[_] == "user"
    startswith(input.path, "/izcr.v1.kv.KVService")
}

allow if {
    input.roles[_] == "user"
    startswith(input.path, "/api/v1/kv")
}

# Users can read their own user information (future enhancement)
allow if {
    input.roles[_] == "user"
    input.path == "/izcr.v1.auth.AuthService/GetUser"
    input.user == input.target_user
}

# Helper function to check if user has a specific role
has_role(role) if {
    input.roles[_] == role
}

# Helper function to check if user is admin or root
is_admin if {
    has_role("admin")
}

is_admin if {
    has_role("root")
}

# Admin-only endpoints
admin_only_endpoints := {
    "/izcr.v1.auth.AuthService/ListUsers",
    "/izcr.v1.auth.AuthService/CreateUser",
    "/izcr.v1.auth.AuthService/PatchUser",
    "/izcr.v1.auth.AuthService/DeleteUser",
    "/api/v1/auth/users/list",
    "/api/v1/auth/users",
    "/api/v1/auth/user"
}

# Explicitly deny non-admin access to admin endpoints
allow if {
    admin_only_endpoints[input.path]
    is_admin
}

# Deny reasons for debugging
deny_reason := "no authentication provided" if {
    not input.roles
}

deny_reason := "insufficient permissions" if {
    input.roles
    not allow
}

deny_reason := "endpoint requires admin role" if {
    admin_only_endpoints[input.path]
    not is_admin
}

deny_reason := "permission denied by explicit deny rule" if {
    count(input.required_permissions) > 0
    some req in input.required_permissions
    has_permission_deny(req)
}

deny_reason := "no matching allow permission" if {
    count(input.required_permissions) > 0
    not user_has_required_permissions
}

# Password setup/reset endpoints - users can set their own password
# The middleware validates that limited tokens (auth_setup_only) are only allowed for this endpoint
allow if {
    input.roles[_] == "user"
    input.path == "/api/v1/auth/set-password"
}

allow if {
    input.roles[_] == "user"
    input.path == "/izcr.v1.auth.AuthService/SetInitialPassword"
}

# RegistryUser API endpoints - users can manage their own registry users
# Ownership check is done in the Go handler since it requires database lookup
allow if {
    input.roles[_] == "user"
    startswith(input.path, "/izcr.v1.reguser.RegUserService")
}

allow if {
    input.roles[_] == "user"
    startswith(input.path, "/api/v1/reguser")
}

# Internal agent endpoints (require certificate-based authentication)
# These are accessed by izcragent via mTLS with client certificates
agent_endpoints := {
    "/agentctl.v1.AgentControlService/Register",
    "/agentctl.v1.AgentControlService/Status",
    "/agentctl.v1.AgentControlService/HeartBeat"
}

# Allow agents with certificate-based internal authentication
# The "internal_service" role is set by the AgentAuthInterceptor when a valid
# client certificate is presented
allow if {
    agent_endpoints[input.path]
    input.roles[_] == "internal_service"
}
