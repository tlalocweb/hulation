package server

// chatACLLookup is the ACL hook the chat service consults per-RPC.
// Mirrors analytics_acl.go's resolution rules: admin / root roles
// see every configured server_id; everyone else falls through to
// pkg/store/bolt's per-user grants.
//
// Kept in its own file so unified_boot.go's wiring stays tidy and
// so this helper can evolve independently when (later) we add a
// dedicated chat-agent permission scope.

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	chatimpl "github.com/tlalocweb/hulation/pkg/api/v1/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

func chatACLLookup(cfg *config.Config) chatimpl.ACLLookup {
	return func(ctx context.Context) chatimpl.ACLResolution {
		claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
		if claims == nil {
			return chatimpl.ACLResolution{}
		}
		if hasRole(claims.Roles, "admin") || hasRole(claims.Roles, "root") {
			return chatimpl.ACLResolution{
				Allowed:    allConfiguredServerIDs(cfg),
				Superadmin: true,
			}
		}
		allowed, err := hulabolt.AllowedServerIDsForUser(claims.Subject)
		if err != nil {
			return chatimpl.ACLResolution{}
		}
		return chatimpl.ACLResolution{Allowed: allowed}
	}
}
