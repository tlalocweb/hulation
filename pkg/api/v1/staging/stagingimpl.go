// Package staging is the gRPC StagingService implementation.
//
// Only the StagingBuild RPC lives here. The WebDAV endpoints
// (staging-update, staging-mount, custom PATCH) remain HTTP — they're
// served via the unified server's ServeMux fallback, not through gRPC.
package staging

import (
	"context"
	"errors"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	stagingspec "github.com/tlalocweb/hulation/pkg/apispec/v1/staging"
	"github.com/tlalocweb/hulation/sitedeploy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var stLog = log.GetTaggedLogger("staging-impl", "gRPC StagingService implementation")

type Server struct {
	stagingspec.UnimplementedStagingServiceServer
}

func New() *Server { return &Server{} }

func (s *Server) StagingBuild(ctx context.Context, req *stagingspec.StagingBuildRequest) (*stagingspec.StagingBuildResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	cfg := config.GetConfig()
	if cfg == nil {
		return nil, status.Error(codes.FailedPrecondition, "server not configured")
	}
	if cfg.GetServerByID(req.GetServerId()) == nil {
		return nil, status.Errorf(codes.NotFound, "server %q not configured", req.GetServerId())
	}

	sm := sitedeploy.GetStagingManager()
	if sm == nil {
		return nil, status.Error(codes.Unavailable, "staging manager not initialized")
	}
	if sm.GetStagingContainer(req.GetServerId()) == nil {
		return nil, status.Errorf(codes.NotFound, "no staging container for server %q", req.GetServerId())
	}

	bs, err := sm.RebuildStaging(req.GetServerId())
	if errors.Is(err, sitedeploy.ErrBuildInProgress) {
		return nil, status.Errorf(codes.AlreadyExists, "build already in progress for server %q", req.GetServerId())
	}
	if err != nil {
		stLog.Errorf("RebuildStaging %s: %v", req.GetServerId(), err)
		// Return the failing BuildInfo so the client can surface logs.
		bi := &sitespec.BuildInfo{ServerId: req.GetServerId(), Status: "failed", LogExcerpt: err.Error()}
		if bs != nil {
			snap := bs.Snapshot()
			bi.BuildId = snap.BuildID
			bi.CompletedAt = timestamppb.New(snap.StartedAt)
		}
		return nil, status.Errorf(codes.Internal, "rebuild staging: %v", err)
	}

	snap := bs.Snapshot()
	bi := &sitespec.BuildInfo{
		BuildId:   snap.BuildID,
		ServerId:  snap.ServerID,
		Status:    "complete",
		StartedAt: timestamppb.New(snap.StartedAt),
	}
	if snap.EndedAt != nil {
		bi.CompletedAt = timestamppb.New(*snap.EndedAt)
	}
	return &stagingspec.StagingBuildResponse{Build: bi}, nil
}
