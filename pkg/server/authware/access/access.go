// Package access provides server-access ACL helpers that operate over
// the RoleAssignment model. Hulation's scope hierarchy is (system, server);
// a RoleAssignment with scope_type="server" and tenant_uuid=<server_id>
// grants that user access to one virtual server. The helpers here wrap
// that representation so callers (JWT factory, auth-service impl, OPA
// policy input) don't have to reason about the raw assignments.
package access

import (
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
)

// Scope value stored on RoleAssignment.scope_type for server-scoped grants.
const ScopeServer = "server"

// Role name strings. Kept as strings (not enum) because RoleAssignment's
// role_name is a string for custom-role support. The two built-ins match
// the ServerAccessRole enum in auth.proto.
const (
	RoleViewer  = "viewer"
	RoleManager = "manager"
)

// AllowedServerIDs returns the set of server IDs a user has any role on.
// Superadmin users (u.SysAdmin) bypass this — callers should check that
// separately.
func AllowedServerIDs(u *apiobjects.User) []string {
	if u == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, ra := range u.RoleAssignments {
		if ra == nil || ra.ScopeType != ScopeServer || ra.TenantUuid == "" {
			continue
		}
		seen[ra.TenantUuid] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// RoleOnServer returns the user's role name for a specific server, or
// empty if the user has no access. When a user has multiple role
// assignments on the same server, the "highest" (manager > viewer) wins.
func RoleOnServer(u *apiobjects.User, serverID string) string {
	if u == nil || serverID == "" {
		return ""
	}
	best := ""
	for _, ra := range u.RoleAssignments {
		if ra == nil || ra.ScopeType != ScopeServer || ra.TenantUuid != serverID {
			continue
		}
		if ra.RoleName == RoleManager {
			return RoleManager
		}
		if ra.RoleName == RoleViewer && best == "" {
			best = RoleViewer
		}
	}
	return best
}

// HasServerAccess is a convenience boolean.
func HasServerAccess(u *apiobjects.User, serverID string) bool {
	if u == nil {
		return false
	}
	if u.SysAdmin {
		return true
	}
	return RoleOnServer(u, serverID) != ""
}

// IntersectRequested takes a list of server IDs the caller asked to
// operate on and returns the subset the user is authorized for. Admin
// users pass through unchanged. An empty requested list means "all I can
// see" and returns the user's full allowed set.
func IntersectRequested(u *apiobjects.User, requested []string) []string {
	if u == nil {
		return nil
	}
	if u.SysAdmin {
		return requested
	}
	allowed := AllowedServerIDs(u)
	if len(requested) == 0 {
		return allowed
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, s := range allowed {
		allowedSet[s] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, r := range requested {
		if _, ok := allowedSet[r]; ok {
			out = append(out, r)
		}
	}
	return out
}

// NewServerRoleAssignment builds a RoleAssignment for a server-scope
// grant. The UUID, granted_at, and granted_by fields are set by the
// caller (the auth-service impl) before persisting.
func NewServerRoleAssignment(serverID, roleName string) *apiobjects.RoleAssignment {
	return &apiobjects.RoleAssignment{
		ScopeType:  ScopeServer,
		TenantUuid: serverID,
		RoleName:   roleName,
	}
}
