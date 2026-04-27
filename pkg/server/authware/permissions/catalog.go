// Package permissions provides permission definitions and resolution logic for RBAC.
package permissions

// Permission constants - hierarchical dotted lowercase strings.
// These are used as documentation and for type-safety when referencing permissions.
// The actual permission strings used in APIs may include scope placeholders.
const (
	// Superadmin permissions (system-wide operations)
	SuperadminKVRead           = "superadmin.kv.read"
	SuperadminKVWrite          = "superadmin.kv.write"
	SuperadminUserCreate       = "superadmin.user.create"
	SuperadminUserDelete       = "superadmin.user.delete"
	SuperadminUserModify       = "superadmin.user.modify"
	SuperadminUserList         = "superadmin.user.list"
	SuperadminPermissionsRead  = "superadmin.permissions.read"
	SuperadminPermissionsCheck = "superadmin.permissions.check"
	SuperadminRoleCreate       = "superadmin.role.create"
	SuperadminRoleDelete       = "superadmin.role.delete"
	SuperadminRoleModify       = "superadmin.role.modify"
	SuperadminRoleList         = "superadmin.role.list"
	SuperadminGroupCreate      = "superadmin.group.create"
	SuperadminGroupDelete      = "superadmin.group.delete"
	SuperadminGroupModify      = "superadmin.group.modify"
	SuperadminGroupList        = "superadmin.group.list"
	SuperadminAll = "superadmin.*"

	// Superadmin tenant management permissions (system-level operations on tenants)
	SuperadminTenantCreate = "superadmin.tenant.create"
	SuperadminTenantDelete = "superadmin.tenant.delete"
	SuperadminTenantList   = "superadmin.tenant.list"
	SuperadminTenantRead   = "superadmin.tenant.read"

	// Tenant-scoped permissions (these use {tenant_uuid} placeholder in API annotations)
	TenantAdmin            = "tenant.{tenant_uuid}.admin"
	TenantUserCreate       = "tenant.{tenant_uuid}.user.create"
	TenantUserDelete       = "tenant.{tenant_uuid}.user.delete"
	TenantUserModify       = "tenant.{tenant_uuid}.user.modify"
	TenantUserList         = "tenant.{tenant_uuid}.user.list"
	TenantProjectList      = "tenant.{tenant_uuid}.project.list"
	TenantPermissionsRead  = "tenant.{tenant_uuid}.permissions.read"
	TenantPermissionsCheck = "tenant.{tenant_uuid}.permissions.check"

	// Tenant read permission (for viewing tenant details)
	TenantRead = "tenant.{tenant_uuid}.read"

	// Tenant superadmin permissions (only for superadmin owners)
	// These control tenant-level administration: changing tenant details, managing owners
	TenantSuperadminAll          = "tenant.{tenant_uuid}.superadmin.*"
	TenantSuperadminModify       = "tenant.{tenant_uuid}.superadmin.modify"
	TenantSuperadminOwnerAdd     = "tenant.{tenant_uuid}.superadmin.owner.add"
	TenantSuperadminOwnerRemove  = "tenant.{tenant_uuid}.superadmin.owner.remove"
	TenantSuperadminOwnerGrant   = "tenant.{tenant_uuid}.superadmin.owner.grant-superadmin"
	TenantSuperadminOwnerRevoke  = "tenant.{tenant_uuid}.superadmin.owner.revoke-superadmin"

	// Tenant owner permissions (for all owners - read-only owner list)
	TenantOwnerAll  = "tenant.{tenant_uuid}.*"
	TenantOwnerList = "tenant.{tenant_uuid}.owner.list"

	// Tenant member management permissions (for owners/admins)
	TenantMemberAdd    = "tenant.{tenant_uuid}.member.add"
	TenantMemberModify = "tenant.{tenant_uuid}.member.modify"
	TenantMemberRemove = "tenant.{tenant_uuid}.member.remove"
	TenantMemberList   = "tenant.{tenant_uuid}.member.list"

	// Project-scoped permissions (these use {tenant_uuid} and {project_uuid} placeholders)
	ProjectAdmin        = "tenant.{tenant_uuid}.project.{project_uuid}.admin"
	ProjectCreate       = "tenant.{tenant_uuid}.project.{project_uuid}.create"
	ProjectDelete       = "tenant.{tenant_uuid}.project.{project_uuid}.delete"
	ProjectModify       = "tenant.{tenant_uuid}.project.{project_uuid}.modify"
	ProjectList         = "tenant.{tenant_uuid}.project.list"
	ProjectMemberAdd    = "tenant.{tenant_uuid}.project.{project_uuid}.member.add"
	ProjectMemberRemove = "tenant.{tenant_uuid}.project.{project_uuid}.member.remove"
	ProjectMemberList   = "tenant.{tenant_uuid}.project.{project_uuid}.member.list"

	// Registry permissions (project-scoped)
	RegistryRead   = "registry.{project_uuid}.read"
	RegistryWrite  = "registry.{project_uuid}.write"
	RegistryDelete = "registry.{project_uuid}.delete"

	// Fleet permissions (tenant-scoped)
	FleetAdminCreate = "tenant.{tenant_uuid}.fleet.create"
	FleetAdminDelete  = "tenant.{tenant_uuid}.fleet.delete"
	FleetAdminModify      = "tenant.{tenant_uuid}.fleet.modify"
	FleetList   = "tenant.{tenant_uuid}.fleet.list"


	// EdgeNode permissions (project-scoped)
	EdgeNodeCreate = "tenant.{tenant_uuid}.fleet.{fleet_uuid}.edgenode.create"
	EdgeNodeDelete = "tenant.{tenant_uuid}.fleet.{fleet_uuid}.edgenode.delete"
	EdgeNodeModify = "tenant.{tenant_uuid}.fleet.{fleet_uuid}.edgenode.modify"
	EdgeNodeList   = "tenant.{tenant_uuid}.fleet.{fleet_uuid}.edgenode.list"

	// Rollout permissions (project-scoped)
	RolloutCreate = "tenant.{tenant_uuid}.rollout.create"
	RolloutDelete = "tenant.{tenant_uuid}.rollout.delete"
	RolloutModify = "tenant.{tenant_uuid}.rollout.modify"
	RolloutList   = "tenant.{tenant_uuid}.rollout.list"

	// Backing provider permissions (tenant-scoped)
	BackingCreate = "tenant.{tenant_uuid}.backing.create"
	BackingDelete = "tenant.{tenant_uuid}.backing.delete"
	BackingModify = "tenant.{tenant_uuid}.backing.modify"
	BackingList   = "tenant.{tenant_uuid}.backing.list"

	// Audit permissions (tenant-scoped)
	AuditRead = "tenant.{tenant_uuid}.audit.read"
	AuditList = "tenant.{tenant_uuid}.audit.list"
)

// Scope types for role assignments
const (
	ScopeSystem  = "system"
	ScopeTenant  = "tenant"
	ScopeProject = "project"
)

// Placeholders used in permission strings
const (
	PlaceholderTenantUUID  = "{tenant_uuid}"
	PlaceholderProjectUUID = "{project_uuid}"
)
