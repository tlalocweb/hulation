package authware

// ProjectAccessInfo contains project-specific permissions for a user
// This structure is used to enforce project-scoped access control
// in the registry authentication system
type ProjectAccessInfo struct {
	ProjectUUID  string   `json:"project_uuid"`
	ProjectName  string   `json:"project_name"`
	Role         string   `json:"role"`          // guest, developer, maintainer, admin
	Permissions  []string `json:"permissions"`   // registry:read, registry:write, registry:delete
}