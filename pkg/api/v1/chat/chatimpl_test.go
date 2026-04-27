package chat

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

// authorize() is the gate every admin RPC runs at entry. The 4b.2
// smoke test caught a wide-open variant of this where no-auth
// requests were returning 200 — these tests pin the contract:
//
//   - empty server_id  → InvalidArgument
//   - no claims        → Unauthenticated
//   - claims, no ACL   → PermissionDenied
//   - claims, allowed  → nil
//   - claims, super    → nil even without explicit allowlist
func TestAuthorizeGate(t *testing.T) {
	withClaims := context.WithValue(context.Background(), authware.ClaimsKey,
		&authware.Claims{Username: "alice"})

	cases := []struct {
		name     string
		ctx      context.Context
		serverID string
		acl      ACLLookup
		wantCode codes.Code
	}{
		{
			name:     "empty server_id rejected",
			ctx:      withClaims,
			serverID: "",
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "no claims",
			ctx:      context.Background(),
			serverID: "tlaloc",
			acl:      func(context.Context) ACLResolution { return ACLResolution{} },
			wantCode: codes.Unauthenticated,
		},
		{
			name:     "claims, empty allowlist",
			ctx:      withClaims,
			serverID: "tlaloc",
			acl:      func(context.Context) ACLResolution { return ACLResolution{} },
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "claims, mismatched allowlist",
			ctx:      withClaims,
			serverID: "tlaloc",
			acl: func(context.Context) ACLResolution {
				return ACLResolution{Allowed: []string{"otherserver"}}
			},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "claims, server in allowlist",
			ctx:      withClaims,
			serverID: "tlaloc",
			acl: func(context.Context) ACLResolution {
				return ACLResolution{Allowed: []string{"tlaloc"}}
			},
			wantCode: codes.OK,
		},
		{
			name:     "superadmin still constrained by Allowed list",
			ctx:      withClaims,
			serverID: "does-not-exist",
			acl: func(context.Context) ACLResolution {
				return ACLResolution{Superadmin: true, Allowed: []string{"tlaloc"}}
			},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "superadmin allowed when server_id is configured",
			ctx:      withClaims,
			serverID: "tlaloc",
			acl: func(context.Context) ACLResolution {
				return ACLResolution{Superadmin: true, Allowed: []string{"tlaloc"}}
			},
			wantCode: codes.OK,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(nil, nil, c.acl)
			err := s.authorize(c.ctx, c.serverID)
			got := status.Code(err)
			if got != c.wantCode {
				t.Errorf("got %s, want %s (err=%v)", got, c.wantCode, err)
			}
		})
	}
}

// nil ACLLookup defaults to "deny all" — fail-closed, not fail-open.
// Regression: 4b.2 first wrote the chat service without an ACL hook
// and the live smoke test caught no-auth bypass.
func TestNilACLLookupIsDenyAll(t *testing.T) {
	s := New(nil, nil, nil)
	ctx := context.WithValue(context.Background(), authware.ClaimsKey,
		&authware.Claims{Username: "alice"})
	if err := s.authorize(ctx, "tlaloc"); status.Code(err) != codes.PermissionDenied {
		t.Errorf("nil ACLLookup should deny: got %v", err)
	}
}

// Verify the chatspec package symbol surface the impl depends on
// hasn't drifted (catches a stale generated chat.pb.go).
var _ chatspec.ChatServiceServer = (*Server)(nil)
