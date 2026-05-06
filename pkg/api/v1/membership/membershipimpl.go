// Package membership implements the internal MembershipService
// (HA_PLAN3 §3). The service is mounted on the unified listener's
// internal gRPC channel — every caller has already terminated mTLS
// with a Team-CA-signed cert, so identity-of-the-peer is established
// before any RPC method runs. The bootstrap_token verified inside
// Join is a layered second factor.
package membership

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tlalocweb/hulation/log"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

var membershipLog = log.GetTaggedLogger("membership", "Internal MembershipService")

// BootstrapTokenKey is the Raft FSM key under which the team's
// pre-shared join secret lives. The leader writes it once at
// team-init (or rotates it via team-rotate-bootstrap-token); every
// node reads it during Join verification.
const BootstrapTokenKey = "_team/bootstrap_token"

// CHConnectedPrefix is the FSM key prefix where each node refreshes
// its CH-connectivity flag every 60s. HA_PLAN3 §10 — used by the
// leader-priority loop and surfaced in Status responses.
const CHConnectedPrefix = "_team/ch_connected/"

// Cluster is the slice of *raftbackend.RaftStorage methods this
// service consumes. Defining the interface here decouples the
// implementation from the concrete Raft backend so tests can pass
// a mock and so the interface matches HA_PLAN3 §3 verbatim.
type Cluster interface {
	IsLeader() bool
	LeaderInfo() (id, addr string)
	Members() []ClusterMember
	LastIndex() uint64
	AppliedIndex() uint64
	AddVoter(nodeID, raftAddr string, prevIndex uint64, timeout time.Duration) error
	RemoveServer(nodeID string, prevIndex uint64, timeout time.Duration) error
}

// ClusterMember mirrors raftbackend.MemberInfo. Repeated here so
// pkg/api/v1/membership doesn't import the raft backend directly
// (avoids an import cycle with the storage layer in tests).
type ClusterMember struct {
	NodeID   string
	RaftAddr string
	Suffrage string
	IsLeader bool
}

// Service implements internalspec.MembershipServiceServer. The
// stub embedded base returns Unimplemented for any future RPC we
// add to the proto without touching this file.
type Service struct {
	internalspec.UnimplementedMembershipServiceServer

	cluster Cluster
	storage storage.Storage
	teamID  string

	// joinTimeout caps the AddVoter future. Default 10s — matches
	// hashicorp/raft's leadership-transfer suggestion under healthy
	// quorum; under partition-stress this returns a clear error and
	// the joiner retries.
	joinTimeout time.Duration
}

// New builds a Service. cluster MUST be non-nil; passing nil and
// expecting graceful Unimplemented behaviour is a programmer error
// — solo deployments should not register this service at all.
func New(cluster Cluster, store storage.Storage, teamID string) *Service {
	return &Service{
		cluster:     cluster,
		storage:     store,
		teamID:      teamID,
		joinTimeout: 10 * time.Second,
	}
}

// Join is HA_PLAN3 §3.2. Followers forward to the leader (Stage 3.5
// will wire a transparent retry interceptor; for now we surface
// FailedPrecondition + leader hint and let the caller retry).
func (s *Service) Join(ctx context.Context, req *internalspec.JoinRequest) (*internalspec.JoinResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	if req.GetTeamId() == "" || req.GetNodeId() == "" || req.GetRaftAddr() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id, node_id, raft_addr are required")
	}
	if req.GetTeamId() != s.teamID {
		membershipLog.Warnf("Join refused: team_id mismatch (caller=%q local=%q)", req.GetTeamId(), s.teamID)
		return nil, status.Error(codes.FailedPrecondition, "team_id mismatch")
	}
	if !s.cluster.IsLeader() {
		leaderID, leaderAddr := s.cluster.LeaderInfo()
		if leaderAddr == "" {
			return nil, status.Error(codes.Unavailable, "no leader yet")
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"not leader; retry against %s (%s)", leaderID, leaderAddr)
	}

	stored, err := s.storage.Get(ctx, BootstrapTokenKey)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && len(stored) == 0) {
		return nil, status.Error(codes.FailedPrecondition, "team has no bootstrap_token configured")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read bootstrap token: %v", err)
	}
	// Wire form is base64 (proto string fields require valid UTF-8;
	// raw 32-byte tokens almost never qualify). Decode here; the
	// FSM-stored value is the raw bytes.
	caller, err := base64.StdEncoding.DecodeString(req.GetBootstrapToken())
	if err != nil {
		// Fall back to comparing the raw string — legacy callers
		// might still ship undecoded bytes if they happen to be
		// UTF-8 safe.
		caller = []byte(req.GetBootstrapToken())
	}
	if subtle.ConstantTimeCompare(caller, stored) != 1 {
		membershipLog.Warnf("Join refused: bad bootstrap_token from node_id=%q", req.GetNodeId())
		return nil, status.Error(codes.Unauthenticated, "bad bootstrap token")
	}

	if err := s.cluster.AddVoter(req.GetNodeId(), req.GetRaftAddr(), 0, s.joinTimeout); err != nil {
		return nil, status.Errorf(codes.Internal, "AddVoter: %v", err)
	}

	if req.GetChConnected() {
		if err := s.storage.Put(ctx, CHConnectedPrefix+req.GetNodeId(), []byte("1")); err != nil {
			membershipLog.Warnf("ch_connected hint write failed for %s: %v", req.GetNodeId(), err)
		}
	}

	leaderID, leaderAddr := s.cluster.LeaderInfo()
	membershipLog.Infof("Join accepted: node=%s addr=%s ch=%t", req.GetNodeId(), req.GetRaftAddr(), req.GetChConnected())
	return &internalspec.JoinResponse{
		LeaderId:   leaderID,
		LeaderAddr: leaderAddr,
		LastIndex:  s.cluster.LastIndex(),
	}, nil
}

// Leave is HA_PLAN3 §3 — the inverse of Join. The leader removes
// the named node from the configuration; the leaver's local Raft
// will see the change via its existing transport before shutting
// down.
func (s *Service) Leave(ctx context.Context, req *internalspec.LeaveRequest) (*internalspec.LeaveResponse, error) {
	if req == nil || req.GetTeamId() == "" || req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id and node_id are required")
	}
	if req.GetTeamId() != s.teamID {
		return nil, status.Error(codes.FailedPrecondition, "team_id mismatch")
	}
	if !s.cluster.IsLeader() {
		_, leaderAddr := s.cluster.LeaderInfo()
		return nil, status.Errorf(codes.FailedPrecondition, "not leader; retry against %s", leaderAddr)
	}

	if err := s.cluster.RemoveServer(req.GetNodeId(), 0, s.joinTimeout); err != nil {
		return nil, status.Errorf(codes.Internal, "RemoveServer: %v", err)
	}
	_ = s.storage.Delete(ctx, CHConnectedPrefix+req.GetNodeId())
	membershipLog.Infof("Leave accepted: node=%s", req.GetNodeId())
	return &internalspec.LeaveResponse{LeadershipTransferred: false}, nil
}

// Status returns membership info. mTLS-only; no team_id check
// (operators sometimes call this against an unknown node to figure
// out which team it belongs to).
func (s *Service) Status(ctx context.Context, _ *internalspec.StatusRequest) (*internalspec.StatusResponse, error) {
	leaderID, leaderAddr := s.cluster.LeaderInfo()
	members := s.cluster.Members()
	out := &internalspec.StatusResponse{
		LeaderId:     leaderID,
		LeaderAddr:   leaderAddr,
		LastIndex:    s.cluster.LastIndex(),
		AppliedIndex: s.cluster.AppliedIndex(),
		HasQuorum:    leaderAddr != "",
	}
	for _, m := range members {
		ch := false
		if v, _ := s.storage.Get(ctx, CHConnectedPrefix+m.NodeID); len(v) > 0 {
			ch = true
		}
		out.Members = append(out.Members, &internalspec.MemberInfo{
			NodeId:      m.NodeID,
			RaftAddr:    m.RaftAddr,
			Suffrage:    m.Suffrage,
			ChConnected: ch,
			IsLeader:    m.IsLeader,
		})
	}
	return out, nil
}

// SetBootstrapToken writes the team's join secret. Called from
// `hulactl team-rotate-bootstrap-token` via the admin gRPC surface
// (which already authenticates the operator). Returns an error if
// the local node is not the leader — token rotation must be a
// coordinated event, not racy.
func (s *Service) SetBootstrapToken(ctx context.Context, token []byte) error {
	if len(token) == 0 {
		return errors.New("token must be non-empty")
	}
	if !s.cluster.IsLeader() {
		return errors.New("not leader; rotate from the current leader")
	}
	return s.storage.Put(ctx, BootstrapTokenKey, token)
}
