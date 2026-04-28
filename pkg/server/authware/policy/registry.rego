package izcr.authz

# Registry-specific authorization policy for OCI Registry v2 API
# This policy is used for requests to /v2/* endpoints (Docker Registry HTTP API V2)

# Default deny - all registry requests are denied unless explicitly allowed
default allow = false

# Registry endpoint patterns
# The OCI Registry v2 API uses paths like:
# - /v2/ - base endpoint (version check)
# - /v2/_catalog - catalog listing
# - /v2/<name>/manifests/<reference> - manifest operations
# - /v2/<name>/blobs/<digest> - blob operations
# - /v2/<name>/blobs/uploads/ - blob upload operations
# - /v2/<name>/tags/list - tag listing

# Helper function to check if this is a registry endpoint
is_registry_endpoint {
    startswith(input.path, "/v2/")
}

is_registry_endpoint {
    input.path == "/v2"
}

# Helper function to determine if the request is read-only
is_read_operation {
    input.method == "GET"
}

is_read_operation {
    input.method == "HEAD"
}

# Helper function to determine if the request is a write operation
is_write_operation {
    input.method == "PUT"
}

is_write_operation {
    input.method == "POST"
}

is_write_operation {
    input.method == "PATCH"
}

# Helper function to determine if the request is a delete operation
is_delete_operation {
    input.method == "DELETE"
}

# Helper function to check if user has a specific role
has_role(role) {
    input.roles[_] == role
}

# Helper function to check if user has a specific permission
has_permission(perm) {
    input.permissions[_] == perm
}

# CRITICAL: Parse the project name from the registry path
# Registry paths follow pattern: /v2/<project>/<repository>/...
# In Harbor, the first path segment after /v2/ is the project name
parse_project_from_path(path) = project {
    is_registry_endpoint
    path_parts := split(substring(path, 4, -1), "/")  # Remove "/v2/" prefix
    count(path_parts) > 0
    path_parts[0] != "_catalog"  # Skip catalog endpoint
    path_parts[0] != ""
    project := path_parts[0]
}

# Check if this is a project-specific request (not global endpoints)
is_project_request {
    is_registry_endpoint
    input.path != "/v2/"
    input.path != "/v2"
    input.path != "/v2/_catalog"
    parse_project_from_path(input.path)
}

# Check if user has access to a specific project
has_project_access(project) {
    input.project_access[project]
}

# Check if user has specific permission for a project
has_project_permission(project, perm) {
    project_info := input.project_access[project]
    project_info.permissions[_] == perm
}

# Check if user has specific role for a project
has_project_role(project, role) {
    project_info := input.project_access[project]
    project_info.role == role
}

# ALLOW RULES WITH PROJECT-SCOPED ACCESS CONTROL

# Rule 1: Allow version check endpoint for authenticated users
allow {
    input.path == "/v2/"
    input.roles
}

allow {
    input.path == "/v2"
    input.roles
}

# Rule 2: Allow catalog listing for authenticated users with any registry permission
allow {
    input.path == "/v2/_catalog"
    is_read_operation
    input.permissions[_] == "registry:read"
}

# Rule 3: Project-scoped read access
allow {
    is_project_request
    is_read_operation
    project := parse_project_from_path(input.path)
    has_project_permission(project, "registry:read")
}

# Rule 4: Project-scoped write access
allow {
    is_project_request
    is_write_operation
    project := parse_project_from_path(input.path)
    has_project_permission(project, "registry:write")
}

# Rule 5: Project-scoped delete access
allow {
    is_project_request
    is_delete_operation
    project := parse_project_from_path(input.path)
    has_project_permission(project, "registry:delete")
}

# Rule 6: Project-scoped pull-only access (read-only)
allow {
    is_project_request
    is_read_operation
    project := parse_project_from_path(input.path)
    has_project_permission(project, "registry_pull_only")
}

# Rule 7: Admin and root users have full registry access (backward compatibility)
allow {
    is_registry_endpoint
    has_role("admin")
}

allow {
    is_registry_endpoint
    has_role("root")
}

# Rule 8: Legacy role-based access (for backward compatibility only)
# These rules apply only if project_access is not defined or empty
allow {
    is_registry_endpoint
    not input.project_access
    has_role("registry_user")
}

allow {
    is_registry_endpoint
    not input.project_access
    has_role("registry_pull_only")
    is_read_operation
}

allow {
    is_registry_endpoint
    not input.project_access
    has_permission("registry:read")
    is_read_operation
}

allow {
    is_registry_endpoint
    not input.project_access
    has_permission("registry:write")
    is_write_operation
}

allow {
    is_registry_endpoint
    not input.project_access
    has_permission("registry:write")
    is_read_operation
}

allow {
    is_registry_endpoint
    not input.project_access
    has_permission("registry:delete")
    is_delete_operation
}

# DENY REASONS (for debugging and better error messages)

deny_reason := "not a registry endpoint" {
    not is_registry_endpoint
}

deny_reason := "no authentication provided" {
    is_registry_endpoint
    not input.roles
}

deny_reason := msg {
    is_project_request
    project := parse_project_from_path(input.path)
    not has_project_access(project)
    not has_role("admin")
    not has_role("root")
    msg := sprintf("no access to project '%s'", [project])
}

deny_reason := msg {
    is_project_request
    is_read_operation
    project := parse_project_from_path(input.path)
    has_project_access(project)
    not has_project_permission(project, "registry:read")
    not has_project_permission(project, "registry:write")
    not has_project_permission(project, "registry:delete")
    not has_project_permission(project, "registry_pull_only")
    not has_role("admin")
    not has_role("root")
    msg := sprintf("no read permission for project '%s'", [project])
}

deny_reason := msg {
    is_project_request
    is_write_operation
    project := parse_project_from_path(input.path)
    has_project_access(project)
    not has_project_permission(project, "registry:write")
    not has_project_permission(project, "registry:delete")
    not has_role("admin")
    not has_role("root")
    msg := sprintf("no write permission for project '%s'", [project])
}

deny_reason := msg {
    is_project_request
    is_delete_operation
    project := parse_project_from_path(input.path)
    has_project_access(project)
    not has_project_permission(project, "registry:delete")
    not has_role("admin")
    not has_role("root")
    msg := sprintf("no delete permission for project '%s'", [project])
}

deny_reason := msg {
    is_project_request
    is_write_operation
    project := parse_project_from_path(input.path)
    has_project_permission(project, "registry_pull_only")
    msg := sprintf("pull-only access cannot perform write operations in project '%s'", [project])
}

deny_reason := "insufficient permissions for registry access" {
    is_registry_endpoint
    input.roles
    not allow
}