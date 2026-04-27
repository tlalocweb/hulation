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
	analyticsimpl "github.com/tlalocweb/hulation/pkg/api/v1/analytics"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

// analyticsACLLookup returns a function the analytics service can call
// per-RPC to find the caller's ACL resolution.
func analyticsACLLookup(cfg *config.Config) func(ctx context.Context) analyticsimpl.ACLResolution {
	return func(ctx context.Context) analyticsimpl.ACLResolution {
		claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil {
			return analyticsimpl.ACLResolution{}
		}
		// Admin + root roles are superadmin — they can query any
		// server_id (including seed fixtures outside the configured
		// set). The builder still scopes each query to the requested
		// server_id(s); superadmin just skips the intersection step.
		if hasRole(claims.Roles, "admin") || hasRole(claims.Roles, "root") {
			return analyticsimpl.ACLResolution{
				Allowed:    allConfiguredServerIDs(cfg),
				Superadmin: true,
			}
		}
		// Non-admin: read per-user grants from the Phase-3 Bolt store.
		// Until a given user has been granted something via the admin
		// UI or hulactl, they see no servers — by design, matches the
		// "silent empty result" contract from Phase 1.
		allowed, err := hulabolt.AllowedServerIDsForUser(claims.Subject)
		if err != nil {
			// Bolt unavailable or read error: log via the query builder
			// (it degrades to an empty ACL) but don't leak details.
			return analyticsimpl.ACLResolution{}
		}
		// Intersect with configured servers so stale grants can't
		// reference servers that no longer exist.
		return analyticsimpl.ACLResolution{
			Allowed: intersectStrings(allowed, allConfiguredServerIDs(cfg)),
		}
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
