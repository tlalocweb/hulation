// Package gossip implements the internal-only RPC server side of
// the CRDT replication protocol (HA_PLAN3 §7.4). Lives on the
// internal mTLS gRPC channel; peers must present a Team-CA-signed
// cert before any of these methods runs.
package gossip

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/tlalocweb/hulation/log"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	gossippkg "github.com/tlalocweb/hulation/pkg/gossip"
)

var gossipLog = log.GetTaggedLogger("gossiprpc", "Internal GossipService")

// Backend is the slice of *gossippkg.Store methods the service
// consumes. Defining the interface here keeps the impl mock-
// friendly and avoids handing out the full Store surface.
type Backend interface {
	ApplyDelta(ctx context.Context, d gossippkg.Delta) (bool, gossippkg.HLC, error)
	SyncFromDigest(ctx context.Context, digest map[string]gossippkg.HLC, max int) ([]gossippkg.Delta, error)
	Clock() *gossippkg.Clock
}

// Service implements internalspec.GossipServiceServer.
type Service struct {
	internalspec.UnimplementedGossipServiceServer
	backend  Backend
	maxDelta int
}

// New builds a Service.
func New(b Backend) *Service {
	return &Service{backend: b, maxDelta: 256}
}

// Push merges incoming deltas into the local CRDT store. Per
// HA_PLAN3 §7.4 this is fire-and-forget from the sender's POV;
// our reply carries the local clock's current HLC so the peer
// can pull its clock forward.
func (s *Service) Push(ctx context.Context, req *internalspec.PushRequest) (*internalspec.PushResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	if s.backend == nil {
		return nil, status.Error(codes.FailedPrecondition, "gossip backend not configured")
	}
	var accepted uint32
	for _, d := range req.GetDeltas() {
		hlc := protoHLCToGossip(d.GetHlc())
		_, _, err := s.backend.ApplyDelta(ctx, gossippkg.Delta{
			Bucket:  d.GetBucket(),
			Key:     d.GetKey(),
			Payload: d.GetPayload(),
			HLC:     hlc,
		})
		if err != nil {
			if errors.Is(err, gossippkg.ErrKeyNotFound) {
				continue
			}
			gossipLog.Warnf("Push from %s: apply delta %s/%s: %v",
				req.GetSourceNodeId(), d.GetBucket(), d.GetKey(), err)
			continue
		}
		accepted++
	}
	clock := s.backend.Clock()
	now := clock.Now()
	return &internalspec.PushResponse{
		Accepted:  accepted,
		RemoteHlc: gossipHLCToProto(now),
	}, nil
}

// SyncDigest is the anti-entropy endpoint. Caller ships its
// per-bucket high-water HLCs; we reply with everything we have
// that's strictly newer.
func (s *Service) SyncDigest(ctx context.Context, req *internalspec.SyncDigestRequest) (*internalspec.SyncDigestResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	if s.backend == nil {
		return nil, status.Error(codes.FailedPrecondition, "gossip backend not configured")
	}
	digest := make(map[string]gossippkg.HLC, len(req.GetDigests()))
	for _, d := range req.GetDigests() {
		digest[d.GetBucket()] = protoHLCToGossip(d.GetHighWater())
	}
	out, err := s.backend.SyncFromDigest(ctx, digest, s.maxDelta)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "sync: %v", err)
	}
	resp := &internalspec.SyncDigestResponse{
		Deltas:    make([]*internalspec.Delta, 0, len(out)),
		RemoteHlc: gossipHLCToProto(s.backend.Clock().Now()),
	}
	for _, d := range out {
		resp.Deltas = append(resp.Deltas, &internalspec.Delta{
			Bucket:  d.Bucket,
			Key:     d.Key,
			Payload: d.Payload,
			Hlc:     gossipHLCToProto(d.HLC),
		})
	}
	return resp, nil
}

func protoHLCToGossip(p *internalspec.HLC) gossippkg.HLC {
	if p == nil {
		return gossippkg.HLC{}
	}
	return gossippkg.HLC{
		WallMs:  p.GetWallMs(),
		Counter: p.GetCounter(),
		NodeID:  p.GetNodeId(),
	}
}

func gossipHLCToProto(h gossippkg.HLC) *internalspec.HLC {
	return &internalspec.HLC{
		WallMs:  h.WallMs,
		Counter: h.Counter,
		NodeId:  h.NodeID,
	}
}
