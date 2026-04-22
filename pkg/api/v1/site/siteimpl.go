// Package site is the gRPC SiteService implementation. It wraps the
// existing sitedeploy.BuildManager.
package site

import (
	"context"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	sitespec "github.com/tlalocweb/hulation/pkg/apispec/v1/site"
	"github.com/tlalocweb/hulation/sitedeploy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var sLog = log.GetTaggedLogger("site-impl", "gRPC SiteService implementation")

type Server struct {
	sitespec.UnimplementedSiteServiceServer
}

func New() *Server { return &Server{} }

func snapToProto(snap sitedeploy.BuildStateSnapshot) *sitespec.BuildInfo {
	bi := &sitespec.BuildInfo{
		BuildId:    snap.BuildID,
		ServerId:   snap.ServerID,
		Status:     snap.Status.String(),
		LogExcerpt: strings.Join(snap.Logs, "\n"),
		QueuedAt:   timestamppb.New(snap.StartedAt),
		StartedAt:  timestamppb.New(snap.StartedAt),
	}
	if snap.EndedAt != nil {
		bi.CompletedAt = timestamppb.New(*snap.EndedAt)
	}
	return bi
}

func (s *Server) TriggerBuild(ctx context.Context, req *sitespec.TriggerBuildRequest) (*sitespec.TriggerBuildResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	cfg := config.GetConfig()
	if cfg == nil {
		return nil, status.Error(codes.FailedPrecondition, "server not configured")
	}
	srvCfg := cfg.GetServerByID(req.GetServerId())
	if srvCfg == nil {
		return nil, status.Errorf(codes.NotFound, "server %q not configured", req.GetServerId())
	}
	if srvCfg.GitAutoDeploy == nil {
		return nil, status.Error(codes.FailedPrecondition, "server has no root_git_autodeploy config")
	}
	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return nil, status.Error(codes.Unavailable, "site deploy manager not initialized")
	}

	// Legacy handler accepted Args []string; keep compatibility by
	// passing nothing — branch/commit are server-config driven. If a
	// later phase needs per-invocation overrides, map req.Branch /
	// req.Commit into args here.
	buildID, err := bm.TriggerBuild(srvCfg, nil)
	if err != nil {
		if bm.IsBuilding(req.GetServerId()) {
			return nil, status.Errorf(codes.AlreadyExists, "build already in progress for server %q", req.GetServerId())
		}
		sLog.Errorf("TriggerBuild %s: %v", req.GetServerId(), err)
		return nil, status.Errorf(codes.Internal, "build trigger failed: %v", err)
	}

	bs := bm.GetBuild(buildID)
	if bs == nil {
		return &sitespec.TriggerBuildResponse{
			Build: &sitespec.BuildInfo{
				BuildId:  buildID,
				ServerId: req.GetServerId(),
				Status:   "queued",
			},
		}, nil
	}
	return &sitespec.TriggerBuildResponse{Build: snapToProto(bs.Snapshot())}, nil
}

func (s *Server) GetBuildStatus(ctx context.Context, req *sitespec.GetBuildStatusRequest) (*sitespec.GetBuildStatusResponse, error) {
	if req.GetBuildId() == "" {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}
	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return nil, status.Error(codes.Unavailable, "site deploy manager not initialized")
	}
	bs := bm.GetBuild(req.GetBuildId())
	if bs == nil {
		return nil, status.Errorf(codes.NotFound, "build %q not found", req.GetBuildId())
	}
	return &sitespec.GetBuildStatusResponse{Build: snapToProto(bs.Snapshot())}, nil
}

func (s *Server) ListBuilds(ctx context.Context, req *sitespec.ListBuildsRequest) (*sitespec.ListBuildsResponse, error) {
	if req.GetServerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	bm := sitedeploy.GetBuildManager()
	if bm == nil {
		return nil, status.Error(codes.Unavailable, "site deploy manager not initialized")
	}
	builds := bm.GetBuildsForServer(req.GetServerId())
	limit := int(req.GetLimit())
	if limit > 0 && len(builds) > limit {
		builds = builds[:limit]
	}
	out := make([]*sitespec.BuildInfo, 0, len(builds))
	for _, b := range builds {
		out = append(out, snapToProto(b))
	}
	return &sitespec.ListBuildsResponse{Builds: out}, nil
}
