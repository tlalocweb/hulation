package permissions

import (
	"strings"

	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
)

// ResolveEffectivePermissions computes the allow and deny permission lists for a user.
// This is called at token generation time to populate the JWT claims.
func ResolveEffectivePermissions(
	roleAssignments []*apiobjects.RoleAssignment,
	directPermissions []*apiobjects.PermissionGrant,
	directDenyPermissions []*apiobjects.PermissionGrant,
	groupPermissions []string, // Allow permissions from groups
	groupDenyPermissions []string, // Deny permissions from groups
) (allowPerms []string, denyPerms []string) {
	allowSet := make(map[string]struct{})
	denySet := make(map[string]struct{})

	// 1. Add permissions from role assignments
	for _, ra := range roleAssignments {
		perms := GeneratePermissionsForRoleAssignment(ra.RoleName, ra.ScopeType, ra.TenantUuid, ra.ProjectUuid)
		for _, p := range perms {
			allowSet[p] = struct{}{}
		}
	}

	// 2. Add permissions from groups
	for _, p := range groupPermissions {
		allowSet[p] = struct{}{}
	}
	for _, p := range groupDenyPermissions {
		denySet[p] = struct{}{}
	}

	// 3. Add direct allow permissions
	for _, pg := range directPermissions {
		if !pg.IsDeny {
			allowSet[pg.Permission] = struct{}{}
		}
	}

	// 4. Add direct deny permissions
	for _, pg := range directDenyPermissions {
		if pg.IsDeny {
			denySet[pg.Permission] = struct{}{}
		}
	}

	// Convert sets to slices
	for p := range allowSet {
		allowPerms = append(allowPerms, p)
	}
	for p := range denySet {
		denyPerms = append(denyPerms, p)
	}

	return allowPerms, denyPerms
}

// GeneratePermissionsForRoleAssignment generates actual permission strings
// when a role is assigned to a user at a specific scope.
// Placeholders like {tenant_uuid} are replaced with actual values.
func GeneratePermissionsForRoleAssignment(roleName, scopeType, tenantUUID, projectUUID string) []string {
	var templates []string

	switch scopeType {
	case ScopeSystem:
		templates = SystemRolePermissions[roleName]
	case ScopeTenant:
		templates = TenantRolePermissions[roleName]
	case ScopeProject:
		templates = ProjectRolePermissions[roleName]
	default:
		return nil
	}

	if templates == nil {
		return nil
	}

	permissions := make([]string, 0, len(templates))
	for _, template := range templates {
		perm := template
		if tenantUUID != "" {
			perm = strings.ReplaceAll(perm, PlaceholderTenantUUID, tenantUUID)
		}
		if projectUUID != "" {
			perm = strings.ReplaceAll(perm, PlaceholderProjectUUID, projectUUID)
		}
		permissions = append(permissions, perm)
	}
	return permissions
}

// ResolveRequiredPermission substitutes placeholders in an API permission requirement
// with actual values from the request parameters.
// e.g., "tenant.{tenant_uuid}.project.{project_uuid}.member.add"
//
//	-> "tenant.abc123.project.xyz789.member.add"
func ResolveRequiredPermission(permTemplate string, requestParams map[string]string) string {
	result := permTemplate
	for key, value := range requestParams {
		placeholder := "{" + key + "}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// COMMENTED OUT: The functions below use inefficient regex-based wildcard matching.
// Use permcache.CheckAccess and permcache.HasRequiredPermissions instead,
// which use HashTree.MatchWithWildcards for O(k) radix tree-based matching.
// See pkg/server/authware/permissions/cache/cache.go for the efficient implementations.

// // MatchPermission checks if a granted permission satisfies a required permission.
// // Supports wildcards:
// //   - "tenant.abc.*" matches "tenant.abc.project.xyz.member.add"
// //   - "*" matches everything
// //   - Exact match: "superadmin.kv.read" matches "superadmin.kv.read"
// func MatchPermission(granted, required string) bool {
// 	// Exact match
// 	if granted == required {
// 		return true
// 	}
//
// 	// Universal wildcard
// 	if granted == "*" {
// 		return true
// 	}
//
// 	// Wildcard matching
// 	if strings.Contains(granted, "*") {
// 		// Convert wildcard pattern to regex
// 		// "tenant.abc.*" should match "tenant.abc.project.xyz.member.add"
// 		// We need to handle both trailing .* and embedded wildcards
//
// 		// Escape regex special characters except *
// 		escaped := regexp.QuoteMeta(granted)
// 		// Replace escaped \* with .* for regex matching
// 		pattern := strings.ReplaceAll(escaped, `\*`, `.*`)
// 		pattern = "^" + pattern + "$"
//
// 		re, err := regexp.Compile(pattern)
// 		if err != nil {
// 			return false
// 		}
// 		return re.MatchString(required)
// 	}
//
// 	return false
// }
//
// // CheckAccess evaluates allow/deny permissions against a required permission.
// // Returns true only if: (1) an allow matches AND (2) no deny matches.
// func CheckAccess(allowPerms []string, denyPerms []string, required string) bool {
// 	// Step 1: Check if any allow permission matches
// 	allowed := false
// 	for _, perm := range allowPerms {
// 		if MatchPermission(perm, required) {
// 			allowed = true
// 			break
// 		}
// 	}
//
// 	if !allowed {
// 		return false // No allow matched → deny by default
// 	}
//
// 	// Step 2: Check if any deny permission matches (overrides allow)
// 	for _, perm := range denyPerms {
// 		if MatchPermission(perm, required) {
// 			return false // Explicit deny → access denied
// 		}
// 	}
//
// 	return true // Allow matched and no deny matched
// }
//
// // CheckAccessWithMatch is like CheckAccess but also returns which permission matched.
// func CheckAccessWithMatch(allowPerms []string, denyPerms []string, required string) (allowed bool, matchedAllow, matchedDeny string) {
// 	// Step 1: Check if any allow permission matches
// 	for _, perm := range allowPerms {
// 		if MatchPermission(perm, required) {
// 			matchedAllow = perm
// 			allowed = true
// 			break
// 		}
// 	}
//
// 	if !allowed {
// 		return false, "", "" // No allow matched → deny by default
// 	}
//
// 	// Step 2: Check if any deny permission matches (overrides allow)
// 	for _, perm := range denyPerms {
// 		if MatchPermission(perm, required) {
// 			return false, matchedAllow, perm // Explicit deny → access denied
// 		}
// 	}
//
// 	return true, matchedAllow, "" // Allow matched and no deny matched
// }
//
// // HasRequiredPermissions checks if user permissions satisfy API requirements.
// // The requireAll parameter controls AND vs OR logic:
// //   - requireAll=true: ALL permissions in 'required' must be satisfied (AND)
// //   - requireAll=false: ANY permission in 'required' is sufficient (OR)
// func HasRequiredPermissions(allowPerms, denyPerms, required []string, requireAll bool) bool {
// 	if len(required) == 0 {
// 		return true // No permissions required
// 	}
//
// 	if requireAll {
// 		// AND logic: all required permissions must be satisfied
// 		for _, req := range required {
// 			if !CheckAccess(allowPerms, denyPerms, req) {
// 				return false
// 			}
// 		}
// 		return true
// 	}
//
// 	// OR logic (default): any required permission is sufficient
// 	for _, req := range required {
// 		if CheckAccess(allowPerms, denyPerms, req) {
// 			return true
// 		}
// 	}
// 	return false
// }

// FormatScope formats a scope type and IDs into a human-readable string.
// e.g., "system", "tenant:abc123", "project:abc123:xyz789"
func FormatScope(scopeType, tenantUUID, projectUUID string) string {
	switch scopeType {
	case ScopeSystem:
		return "system"
	case ScopeTenant:
		return "tenant:" + tenantUUID
	case ScopeProject:
		return "project:" + tenantUUID + ":" + projectUUID
	default:
		return scopeType
	}
}
