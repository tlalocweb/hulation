package access

import (
	"sort"
	"testing"

	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
)

func userWithAccess(grants map[string]string, sysadmin bool) *apiobjects.User {
	u := &apiobjects.User{SysAdmin: sysadmin}
	for server, role := range grants {
		u.RoleAssignments = append(u.RoleAssignments, &apiobjects.RoleAssignment{
			ScopeType:  ScopeServer,
			TenantUuid: server,
			RoleName:   role,
		})
	}
	return u
}

func TestAllowedServerIDs(t *testing.T) {
	u := userWithAccess(map[string]string{"s1": "viewer", "s2": "manager"}, false)
	got := AllowedServerIDs(u)
	sort.Strings(got)
	want := []string{"s1", "s2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}

	if len(AllowedServerIDs(nil)) != 0 {
		t.Error("nil user should yield empty slice")
	}

	empty := &apiobjects.User{}
	if len(AllowedServerIDs(empty)) != 0 {
		t.Error("user with no assignments should yield empty slice")
	}
}

func TestRoleOnServerManagerWins(t *testing.T) {
	u := &apiobjects.User{
		RoleAssignments: []*apiobjects.RoleAssignment{
			{ScopeType: ScopeServer, TenantUuid: "s1", RoleName: RoleViewer},
			{ScopeType: ScopeServer, TenantUuid: "s1", RoleName: RoleManager},
		},
	}
	if r := RoleOnServer(u, "s1"); r != RoleManager {
		t.Errorf("got %q, want %q (manager should win)", r, RoleManager)
	}
}

func TestRoleOnServerIgnoresOtherScopes(t *testing.T) {
	u := &apiobjects.User{
		RoleAssignments: []*apiobjects.RoleAssignment{
			{ScopeType: "system", RoleName: "admin"},
			{ScopeType: "tenant", TenantUuid: "t1", RoleName: RoleManager},
			{ScopeType: ScopeServer, TenantUuid: "s1", RoleName: RoleViewer},
		},
	}
	if r := RoleOnServer(u, "s1"); r != RoleViewer {
		t.Errorf("got %q, want viewer", r)
	}
	if r := RoleOnServer(u, "s2"); r != "" {
		t.Errorf("got %q, want empty for unknown server", r)
	}
}

func TestHasServerAccess_SysAdmin(t *testing.T) {
	u := userWithAccess(nil, true)
	if !HasServerAccess(u, "anything") {
		t.Error("sysadmin should have access to any server")
	}
}

func TestIntersectRequested(t *testing.T) {
	u := userWithAccess(map[string]string{"s1": "viewer", "s2": "manager"}, false)

	// No request → returns full allowed set.
	got := IntersectRequested(u, nil)
	sort.Strings(got)
	if len(got) != 2 {
		t.Errorf("expected all 2, got %v", got)
	}

	// Request subset → intersection.
	got = IntersectRequested(u, []string{"s1", "s3"})
	if len(got) != 1 || got[0] != "s1" {
		t.Errorf("expected [s1], got %v", got)
	}

	// Sysadmin → pass-through (auth check happens elsewhere, not here).
	admin := userWithAccess(nil, true)
	got = IntersectRequested(admin, []string{"s99", "s100"})
	if len(got) != 2 {
		t.Errorf("sysadmin should pass through requested list; got %v", got)
	}
}

func TestNewServerRoleAssignment(t *testing.T) {
	ra := NewServerRoleAssignment("srv1", RoleManager)
	if ra.ScopeType != ScopeServer {
		t.Errorf("scope_type: got %q, want %q", ra.ScopeType, ScopeServer)
	}
	if ra.TenantUuid != "srv1" {
		t.Errorf("tenant_uuid: got %q, want srv1", ra.TenantUuid)
	}
	if ra.RoleName != RoleManager {
		t.Errorf("role: got %q, want manager", ra.RoleName)
	}
}
