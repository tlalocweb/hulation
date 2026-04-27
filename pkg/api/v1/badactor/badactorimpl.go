// Package badactor is the gRPC BadActorService implementation. Wraps
// the existing badactor.Store package; business logic is unchanged.
package badactor

import (
	"context"
	"time"

	"github.com/tlalocweb/hulation/badactor"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	badactorspec "github.com/tlalocweb/hulation/pkg/apispec/v1/badactor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var bLog = log.GetTaggedLogger("badactor-impl", "gRPC BadActorService implementation")

type Server struct {
	badactorspec.UnimplementedBadActorServiceServer
}

func New() *Server { return &Server{} }

func validateServerID(serverID string) error {
	if serverID == "" {
		// Some badactor ops are system-scoped (ListSignatures).
		return nil
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

func mustStore() (*badactor.Store, error) {
	s := badactor.GetStore()
	if s == nil {
		return nil, status.Error(codes.FailedPrecondition, "bad actor feature not enabled")
	}
	return s, nil
}

func (s *Server) ListBadActors(ctx context.Context, req *badactorspec.ListBadActorsRequest) (*badactorspec.ListBadActorsResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	entries := st.ListBlockedIPsWithDetail(limit, 0)
	out := make([]*badactorspec.BadActor, 0, len(entries))
	for _, e := range entries {
		out = append(out, &badactorspec.BadActor{
			Ip:        e.IP,
			Reason:    e.LastReason,
			HitCount:  int64(e.Score),
			FirstSeen: timestamppb.New(e.DetectedAt),
			LastSeen:  timestamppb.New(e.DetectedAt),
			// country_code / asn / org populated from ipinfo cache if/when
			// we start persisting them on the entry struct. Not in scope.
		})
	}
	return &badactorspec.ListBadActorsResponse{Actors: out}, nil
}

func (s *Server) ManualBlock(ctx context.Context, req *badactorspec.ManualBlockRequest) (*badactorspec.ManualBlockResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetIp() == "" {
		return nil, status.Error(codes.InvalidArgument, "ip is required")
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	reason := req.GetReason()
	if reason == "" {
		reason = "manually blocked"
	}
	// Persist to DB with score at/above threshold so the normal eviction
	// flow treats it as blocked. Matches the legacy handler.
	if err := badactor.InsertBadActorRecord(st.DB(), req.GetIp(), "", "", "", "", reason, "manual", "manual", st.BlockThreshold()); err != nil {
		bLog.Errorf("InsertBadActorRecord %s: %v", req.GetIp(), err)
		return nil, status.Errorf(codes.Internal, "manual block: %v", err)
	}
	// Reflect in the in-memory radix tree (identical insert as handler).
	st.ManualInsertBlocked(req.GetIp(), reason)
	return &badactorspec.ManualBlockResponse{
		Actor: &badactorspec.BadActor{
			Ip:       req.GetIp(),
			Reason:   reason,
			Manual:   true,
			LastSeen: timestamppb.New(time.Now()),
		},
	}, nil
}

func (s *Server) EvictBadActor(ctx context.Context, req *badactorspec.EvictBadActorRequest) (*badactorspec.EvictBadActorResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetIp() == "" {
		return nil, status.Error(codes.InvalidArgument, "ip is required")
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	st.EvictIPManual(req.GetIp())
	return &badactorspec.EvictBadActorResponse{Ok: true}, nil
}

func (s *Server) ListAllowlist(ctx context.Context, req *badactorspec.ListAllowlistRequest) (*badactorspec.ListAllowlistResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	ips, dbErr := badactor.LoadAllowlist(st.DB())
	if dbErr != nil {
		return nil, status.Errorf(codes.Internal, "load allowlist: %v", dbErr)
	}
	out := make([]*badactorspec.AllowlistEntry, 0, len(ips))
	for _, ip := range ips {
		out = append(out, &badactorspec.AllowlistEntry{Ip: ip})
	}
	return &badactorspec.ListAllowlistResponse{Entries: out}, nil
}

func (s *Server) AddToAllowlist(ctx context.Context, req *badactorspec.AddToAllowlistRequest) (*badactorspec.AddToAllowlistResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetIp() == "" {
		return nil, status.Error(codes.InvalidArgument, "ip is required")
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	if err := badactor.AddToAllowlistDB(st.DB(), req.GetIp(), req.GetReason(), "admin"); err != nil {
		return nil, status.Errorf(codes.Internal, "add allowlist: %v", err)
	}
	st.AddToAllowlist(req.GetIp())
	st.EvictIPManual(req.GetIp())
	return &badactorspec.AddToAllowlistResponse{
		Entry: &badactorspec.AllowlistEntry{
			Ip:      req.GetIp(),
			Reason:  req.GetReason(),
			AddedBy: "admin",
			AddedAt: timestamppb.New(time.Now()),
		},
	}, nil
}

func (s *Server) RemoveFromAllowlist(ctx context.Context, req *badactorspec.RemoveFromAllowlistRequest) (*badactorspec.RemoveFromAllowlistResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetIp() == "" {
		return nil, status.Error(codes.InvalidArgument, "ip is required")
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	if err := badactor.RemoveFromAllowlistDB(st.DB(), req.GetIp()); err != nil {
		return nil, status.Errorf(codes.Internal, "remove allowlist: %v", err)
	}
	st.RemoveFromAllowlist(req.GetIp())
	return &badactorspec.RemoveFromAllowlistResponse{Ok: true}, nil
}

func (s *Server) BadActorStats(ctx context.Context, req *badactorspec.BadActorStatsRequest) (*badactorspec.BadActorStatsResponse, error) {
	if err := validateServerID(req.GetServerId()); err != nil {
		return nil, err
	}
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	return &badactorspec.BadActorStatsResponse{
		BlockedTotal:   int64(st.GetBlockedCount()),
		AllowlistSize:  int64(st.GetAllowlistCount()),
		// BlockedLast24h and TopSources24h would require additional store
		// introspection; left zero-valued for Phase 0.
	}, nil
}

func (s *Server) ListSignatures(ctx context.Context, req *badactorspec.ListSignaturesRequest) (*badactorspec.ListSignaturesResponse, error) {
	// System-scoped; no validateServerID check.
	st, err := mustStore()
	if err != nil {
		return nil, err
	}
	sigs := st.AllSignatures()
	out := make([]*badactorspec.Signature, 0, len(sigs))
	for _, sg := range sigs {
		out = append(out, &badactorspec.Signature{
			Id:          sg.Name,
			Description: sg.Reason,
			Target:      string(sg.Type),
			Severity:    sg.Category,
		})
	}
	return &badactorspec.ListSignaturesResponse{Signatures: out}, nil
}
