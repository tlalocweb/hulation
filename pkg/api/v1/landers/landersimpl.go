// Package landers is the gRPC LandersService implementation.
package landers

import (
	"context"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	landersspec "github.com/tlalocweb/hulation/pkg/apispec/v1/landers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var lLog = log.GetTaggedLogger("landers-impl", "gRPC LandersService implementation")

type Server struct {
	landersspec.UnimplementedLandersServiceServer
}

func New() *Server { return &Server{} }

func validateServerID(serverID string) error {
	if serverID == "" {
		return status.Error(codes.InvalidArgument, "server_id is required")
	}
	cfg := config.GetConfig()
	if cfg == nil {
		return status.Error(codes.FailedPrecondition, "server not configured")
	}
	for _, srv := range cfg.Servers {
		if srv.ID == serverID {
			return nil
		}
	}
	return status.Errorf(codes.NotFound, "server %q not configured", serverID)
}

func landerToProto(l *model.Lander) *landersspec.Lander {
	if l == nil {
		return nil
	}
	return &landersspec.Lander{
		Id:          l.ID,
		Name:        l.Name,
		Description: l.Description,
		Server:      l.Server,
		UrlPostfix:  l.UrlPostfix,
		Redirect:    l.Redirect,
		Hits:        l.Hits,
		IgnorePort:  l.IgnorePort,
		NoServe:     l.NoServe,
		CreatedAt:   timestamppb.New(l.CreatedAt),
		UpdatedAt:   timestamppb.New(l.UpdatedAt),
	}
}

// protoToLander copies non-zero proto fields onto an existing model
// lander. Used by Modify.
func protoToLander(p *landersspec.Lander, dst *model.Lander) {
	if p == nil || dst == nil {
		return
	}
	if p.GetName() != "" {
		dst.Name = p.GetName()
	}
	if p.GetDescription() != "" {
		dst.Description = p.GetDescription()
	}
	if p.GetServer() != "" {
		dst.Server = p.GetServer()
	}
	if p.GetUrlPostfix() != "" {
		dst.UrlPostfix = p.GetUrlPostfix()
	}
	if p.GetRedirect() != "" {
		dst.Redirect = p.GetRedirect()
	}
	dst.IgnorePort = p.GetIgnorePort()
	dst.NoServe = p.GetNoServe()
}

func (s *Server) CreateLander(ctx context.Context, req *landersspec.CreateLanderRequest) (*landersspec.CreateLanderResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	p := req.GetLander()
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "lander is required")
	}
	if p.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if p.GetUrlPostfix() == "" {
		return nil, status.Error(codes.InvalidArgument, "url_postfix is required")
	}

	l := model.NewLander()
	l.Name = p.GetName()
	l.Description = p.GetDescription()
	l.Server = p.GetServer()
	l.UrlPostfix = p.GetUrlPostfix()
	l.Redirect = p.GetRedirect()
	l.IgnorePort = p.GetIgnorePort()
	l.NoServe = p.GetNoServe()

	if err := l.Validate(l.UrlPostfix, model.GetDB()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate lander: %v", err)
	}
	if _, err := l.Commit(l.UrlPostfix, model.GetDB()); err != nil {
		lLog.Errorf("commit lander %q: %v", l.ID, err)
		return nil, status.Errorf(codes.Internal, "commit lander: %v", err)
	}
	return &landersspec.CreateLanderResponse{Lander: landerToProto(l)}, nil
}

func (s *Server) ModifyLander(ctx context.Context, req *landersspec.ModifyLanderRequest) (*landersspec.ModifyLanderResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetLanderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "lander_id is required")
	}
	l, _, err := model.GetLanderById(model.GetDB(), req.GetLanderId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup lander: %v", err)
	}
	if l == nil {
		return nil, status.Errorf(codes.NotFound, "lander %q not found", req.GetLanderId())
	}
	protoToLander(req.GetLander(), l)
	if _, err := l.Update(model.GetDB()); err != nil {
		return nil, status.Errorf(codes.Internal, "update lander: %v", err)
	}
	return &landersspec.ModifyLanderResponse{Lander: landerToProto(l)}, nil
}

func (s *Server) DeleteLander(ctx context.Context, req *landersspec.DeleteLanderRequest) (*landersspec.DeleteLanderResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetLanderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "lander_id is required")
	}
	if err := model.DeleteLander(model.GetDB(), req.GetLanderId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete lander: %v", err)
	}
	return &landersspec.DeleteLanderResponse{Ok: true}, nil
}

func (s *Server) ListLanders(ctx context.Context, req *landersspec.ListLandersRequest) (*landersspec.ListLandersResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	all, err := model.ListLanders(model.GetDB())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list landers: %v", err)
	}
	out := make([]*landersspec.Lander, 0, len(all))
	for _, l := range all {
		out = append(out, landerToProto(l))
	}
	return &landersspec.ListLandersResponse{Landers: out}, nil
}

func (s *Server) GetLander(ctx context.Context, req *landersspec.GetLanderRequest) (*landersspec.GetLanderResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetLanderId() == "" {
		return nil, status.Error(codes.InvalidArgument, "lander_id is required")
	}
	l, _, err := model.GetLanderById(model.GetDB(), req.GetLanderId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup lander: %v", err)
	}
	if l == nil {
		l, _, err = model.GetLanderByName(model.GetDB(), req.GetLanderId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "lookup lander by name: %v", err)
		}
	}
	if l == nil {
		return nil, status.Errorf(codes.NotFound, "lander %q not found", req.GetLanderId())
	}
	return &landersspec.GetLanderResponse{Lander: landerToProto(l)}, nil
}
