package permissions

// SystemRolePermissions defines permissions granted when a role is assigned at system level.
// These are templates; placeholders are not used at system level (wildcards granted instead).
// NOTE: operator, viewer, auditor roles have been removed - use role_assignments with appropriate
// permissions instead.
var SystemRolePermissions = map[string][]string{
	"root": {
		"*", // Root has all permissions
	},
	"admin": {
		"superadmin.*", // All superadmin operations
	},
}

// TenantRolePermissions defines permissions granted when a role is assigned to a tenant.
// The {tenant_uuid} placeholder is replaced with the actual tenant UUID at assignment time.
var TenantRolePermissions = map[string][]string{
	"tenant_admin": {
		"tenant.{tenant_uuid}.*", // Full control over tenant
	},
	// tenant_superadmin_owner: First owner (creator) and delegated superadmins
	// Has all tenant permissions PLUS superadmin-specific permissions for:
	// - Modifying tenant details (display name, description, badge icon)
	// - Adding/removing owners
	// - Granting/revoking superadmin status to other owners
	"tenant_superadmin_owner": {
		"tenant.{tenant_uuid}.*",
		"tenant.{tenant_uuid}.superadmin.*",
	},
	// tenant_owner: Regular owner without superadmin privileges
	// Has full tenant permissions but cannot modify tenant details or manage owners
	"tenant_owner": {
		"tenant.{tenant_uuid}.*",
	},
	"tenant_operator": {
		"tenant.{tenant_uuid}.project.*",
		"tenant.{tenant_uuid}.user.list",
	},
	"tenant_viewer": {
		"tenant.{tenant_uuid}.*.list",
		"tenant.{tenant_uuid}.*.read",
	},
	"tenant_member": {
		"tenant.{tenant_uuid}.project.list",
	},
}

// ProjectRolePermissions defines permissions granted when a role is assigned to a project.
// Both {tenant_uuid} and {project_uuid} are replaced at assignment time.
var ProjectRolePermissions = map[string][]string{
	"project_admin": {
		"tenant.{tenant_uuid}.project.{project_uuid}.*", // Full control over project
	},
	"project_maintainer": {
		"tenant.{tenant_uuid}.project.{project_uuid}.modify",
		"tenant.{tenant_uuid}.project.{project_uuid}.member.*",
		"tenant.{tenant_uuid}.project.{project_uuid}.registry.*",
		"tenant.{tenant_uuid}.project.{project_uuid}.fleet.*",
		"tenant.{tenant_uuid}.project.{project_uuid}.edgenode.*",
		"tenant.{tenant_uuid}.project.{project_uuid}.rollout.*",
		"tenant.{tenant_uuid}.project.{project_uuid}.backing.*",
	},
	"project_developer": {
		"tenant.{tenant_uuid}.project.{project_uuid}.list",
		"tenant.{tenant_uuid}.project.{project_uuid}.registry.read",
		"tenant.{tenant_uuid}.project.{project_uuid}.registry.write",
		"tenant.{tenant_uuid}.project.{project_uuid}.fleet.list",
		"tenant.{tenant_uuid}.project.{project_uuid}.edgenode.list",
		"tenant.{tenant_uuid}.project.{project_uuid}.rollout.list",
	},
	"project_guest": {
		"tenant.{tenant_uuid}.project.{project_uuid}.list",
		"tenant.{tenant_uuid}.project.{project_uuid}.registry.read",
	},
	"project_limited_guest": {
		"tenant.{tenant_uuid}.project.{project_uuid}.registry.read",
	},
}

// BuiltInRoles lists all built-in role names
// NOTE: operator, viewer, auditor have been removed from system roles
var BuiltInRoles = map[string]bool{
	// System roles
	"root":  true,
	"admin": true,
	// Tenant roles
	"tenant_admin":           true,
	"tenant_superadmin_owner": true, // First owner / delegated superadmins
	"tenant_owner":           true,  // Regular owners without superadmin
	"tenant_operator":        true,
	"tenant_viewer":          true,
	"tenant_member":          true,
	// Project roles
	"project_admin":         true,
	"project_maintainer":    true,
	"project_developer":     true,
	"project_guest":         true,
	"project_limited_guest": true,
}

// IsBuiltInRole checks if a role name is a built-in role
func IsBuiltInRole(roleName string) bool {
	return BuiltInRoles[roleName]
}

// GetRolePermissions returns the permission templates for a role at a given scope
func GetRolePermissions(roleName, scopeType string) []string {
	switch scopeType {
	case ScopeSystem:
		if perms, ok := SystemRolePermissions[roleName]; ok {
			return perms
		}
	case ScopeTenant:
		if perms, ok := TenantRolePermissions[roleName]; ok {
			return perms
		}
	case ScopeProject:
		if perms, ok := ProjectRolePermissions[roleName]; ok {
			return perms
		}
	}
	return nil
}
