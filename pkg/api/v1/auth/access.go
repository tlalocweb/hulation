package auth

// ServerAccess RPCs — GrantServerAccess / RevokeServerAccess /
// ListServerAccess. Back-ends the admin UI "Manage access" sheet.
// Reads/writes go through pkg/store/bolt; roles come from
// authspec.ServerAccessRole.

import (
	"context"

	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	authspec "github.com/tlalocweb/hulation/pkg/apispec/v1/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func roleEnumToString(r authspec.ServerAccessRole) string {
	switch r {
	case authspec.ServerAccessRole_SERVER_ACCESS_ROLE_VIEWER:
		return "viewer"
	case authspec.ServerAccessRole_SERVER_ACCESS_ROLE_MANAGER:
		return "manager"
	}
	return ""
}

func roleStringToEnum(s string) authspec.ServerAccessRole {
	switch s {
	case "viewer":
		return authspec.ServerAccessRole_SERVER_ACCESS_ROLE_VIEWER
	case "manager":
		return authspec.ServerAccessRole_SERVER_ACCESS_ROLE_MANAGER
	}
	return authspec.ServerAccessRole_SERVER_ACCESS_ROLE_UNSPECIFIED
}

// GrantServerAccess upserts a (user, server, role) grant. Requires
// admin auth; enforced upstream by the method's permission
// annotation in auth.proto.
func (s *Server) GrantServerAccess(ctx context.Context, req *authspec.GrantServerAccessRequest) (*authspec.GrantServerAccessResponse, error) {
	if req == nil || req.UserId == "" || req.ServerId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id and server_id required")
	}
	role := roleEnumToString(req.Role)
	if role == "" {
		return nil, status.Error(codes.InvalidArgument, "role must be viewer or manager")
	}
	if err := hulabolt.GrantServerAccess(ctx, storage.Global(), req.UserId, req.ServerId, role); err != nil {
		return nil, status.Errorf(codes.Internal, "grant: %s", err)
	}
	return &authspec.GrantServerAccessResponse{
		Status: "ok",
		Entry: &authspec.ServerAccessEntry{
			UserId:   req.UserId,
			ServerId: req.ServerId,
			Role:     req.Role,
		},
	}, nil
}

// RevokeServerAccess drops the (user, server) grant. Missing grants
// are not an error — the UI "revoke" button is idempotent.
func (s *Server) RevokeServerAccess(ctx context.Context, req *authspec.RevokeServerAccessRequest) (*authspec.RevokeServerAccessResponse, error) {
	if req == nil || req.UserId == "" || req.ServerId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id and server_id required")
	}
	if err := hulabolt.RevokeServerAccess(ctx, storage.Global(), req.UserId, req.ServerId); err != nil {
		return nil, status.Errorf(codes.Internal, "revoke: %s", err)
	}
	return &authspec.RevokeServerAccessResponse{Status: "ok"}, nil
}

// ListServerAccess lists every grant. Optional user / server
// filters narrow the result set.
func (s *Server) ListServerAccess(ctx context.Context, req *authspec.ListServerAccessRequest) (*authspec.ListServerAccessResponse, error) {
	if req == nil {
		req = &authspec.ListServerAccessRequest{}
	}
	rows, err := hulabolt.ListServerAccess(ctx, storage.Global(), req.UserId, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list server access: %s", err)
	}
	out := make([]*authspec.ServerAccessEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, &authspec.ServerAccessEntry{
			UserId:   r.UserID,
			ServerId: r.ServerID,
			Role:     roleStringToEnum(r.Role),
		})
	}
	return &authspec.ListServerAccessResponse{Status: "ok", Entries: out}, nil
}
