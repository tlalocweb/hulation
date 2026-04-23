package server

// analyticsACLLookup binds the analytics-service ACL hook to the
// current Hulation config. Admin callers (any authware Claim with role
// "admin" or "root") see every server_id defined in config.Servers;
// non-admin callers fall through to pkg/server/authware/access for
// their per-user grant list.
//
// Kept in its own file so the unified_boot wire-up stays tidy, and so
// this helper can be extended independently of the service
// registration block when Phase-2 starts using pkg/server/authware
// with real Bolt-backed users.

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	apiobjects "github.com/tlalocweb/hulation/pkg/apiobjects/v1"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"github.com/tlalocweb/hulation/pkg/server/authware/access"
)

// analyticsACLLookup returns a function the analytics service can call
// per-RPC to find the caller's allowed server ids.
func analyticsACLLookup(cfg *config.Config) func(ctx context.Context) []string {
	return func(ctx context.Context) []string {
		claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil {
			return nil
		}
		// Admin + root roles bypass the per-user ACL — they see every
		// configured server. Same check the Phase-0 JWT factory uses
		// when minting admin tokens.
		if hasRole(claims.Roles, "admin") || hasRole(claims.Roles, "root") {
			return allConfiguredServerIDs(cfg)
		}
		// Non-admin: the authware/access helpers would walk this user's
		// RoleAssignments — but Phase-0 left the Bolt user store
		// unwired, so we synthesise a User object carrying the Claims
		// permissions list and let access.AllowedServerIDs do its job.
		// When Phase-1 deploys with populated grants this still works;
		// until then non-admin callers see nothing (by design).
		user := &apiobjects.User{
			Uuid:     claims.Subject,
			Username: claims.Username,
		}
		allowed := access.AllowedServerIDs(user)
		// Intersect with configured servers so stale grants can't
		// reference servers that no longer exist.
		return intersectStrings(allowed, allConfiguredServerIDs(cfg))
	}
}

func allConfiguredServerIDs(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s != nil && s.ID != "" {
			out = append(out, s.ID)
		}
	}
	return out
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func intersectStrings(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(b))
	for _, v := range b {
		set[v] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if _, ok := set[v]; ok {
			out = append(out, v)
		}
	}
	return out
}
