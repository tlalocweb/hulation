// Package storageproxy implements the internal-only RPC followers
// use to forward Raft writes to the current leader (HA_PLAN3 §9).
// Mounted on the unified listener's internal gRPC channel, so peers
// must present a Team-CA-signed cert before any method here runs.
package storageproxy

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tlalocweb/hulation/log"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
)

var proxyLog = log.GetTaggedLogger("storageproxy", "Internal StorageProxyService")

// LeaderApplier is the slice of *raftbackend.RaftStorage methods
// this service consumes. Defining it here makes the impl trivial
// to mock.
type LeaderApplier interface {
	IsLeader() bool
	LeaderInfo() (id, addr string)
	ApplyEncodedAsLeader(ctx context.Context, encoded []byte) (uint64, error)
}

type Service struct {
	internalspec.UnimplementedStorageProxyServiceServer
	applier LeaderApplier
}

func New(applier LeaderApplier) *Service {
	return &Service{applier: applier}
}

// Apply runs the follower-supplied Raft command on this node's
// raft.Apply. Fails fast with FailedPrecondition + the current
// leader's address when we aren't leader so the caller can re-dial.
func (s *Service) Apply(ctx context.Context, req *internalspec.ApplyRequest) (*internalspec.ApplyResponse, error) {
	if req == nil || len(req.GetCommand()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "command bytes are required")
	}
	if !s.applier.IsLeader() {
		_, addr := s.applier.LeaderInfo()
		if addr == "" {
			return nil, status.Error(codes.Unavailable, "no leader yet")
		}
		return nil, status.Errorf(codes.FailedPrecondition, "not leader; retry against %s", addr)
	}
	idx, err := s.applier.ApplyEncodedAsLeader(ctx, req.GetCommand())
	if err != nil {
		if errors.Is(err, raftbackend.ErrNotLeader) {
			_, addr := s.applier.LeaderInfo()
			return nil, status.Errorf(codes.FailedPrecondition, "not leader; retry against %s", addr)
		}
		proxyLog.Warnf("Apply rejected: %v", err)
		return nil, status.Errorf(codes.Internal, "apply: %v", err)
	}
	return &internalspec.ApplyResponse{AppliedIndex: idx}, nil
}
