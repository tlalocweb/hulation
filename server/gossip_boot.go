package server

// Gossip + CRDT boot wiring (HA Stage 3.7).
//
// Every team node — CH-connected or not — opens a local crdt.db
// and registers GossipService on the internal mTLS gRPC channel.
// The Engine then drives push-on-write fan-out + a 10s anti-
// entropy ticker against the live Raft membership.

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	gossipimpl "github.com/tlalocweb/hulation/pkg/api/v1/gossip"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/gossip"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/pkg/team/pki"
)

var gossipLog = log.GetTaggedLogger("gossip-boot", "Gossip + CRDT bootstrap")

// globalGossipStore is the per-process CRDT store. handler/visitor.go
// will read/write through this in a follow-up; for now visitor data
// still flows through the Raft FSM and the gossip layer is
// operational + tested but mostly idle.
var globalGossipStore *gossip.Store

// GlobalGossipStore returns the per-process CRDT store or nil.
func GlobalGossipStore() *gossip.Store { return globalGossipStore }

// registerLiveGossip stands up the CRDT store + GossipService +
// the engine. Idempotent in the sense that a partial init logs +
// returns gracefully so the rest of boot continues.
func registerLiveGossip(srv *unified.Server, bundle *pki.Bundle, cfg *config.Config) {
	g := srv.GetInternalGRPCServer()
	if g == nil {
		return
	}
	if cfg == nil || cfg.Team == nil {
		return
	}

	dataDir := cfg.Team.DataDir
	if dataDir == "" {
		gossipLog.Warnf("team.data_dir empty — gossip layer disabled")
		return
	}
	storePath := filepath.Join(dataDir, "crdt.db")
	store, err := gossip.OpenStore(storePath, cfg.Team.NodeID)
	if err != nil {
		gossipLog.Warnf("gossip store init failed (%v) — CRDT layer disabled", err)
		return
	}
	globalGossipStore = store
	internalspec.RegisterGossipServiceServer(g, gossipimpl.New(store))

	creds, err := pki.PeerDialTLSConfig(bundle)
	if err != nil {
		gossipLog.Warnf("gossip dial creds: %v — engine disabled, server side still active", err)
		return
	}
	tlsCreds := credentials.NewTLS(creds)
	sender := newGRPCSender(tlsCreds)
	peers := raftPeerProvider{}

	engine, err := gossip.NewEngine(store, peers, sender, gossip.EngineConfig{})
	if err != nil {
		gossipLog.Warnf("gossip engine: %v — CRDT writes will not fan out", err)
		return
	}
	engine.Start(context.Background())
	gossipLog.Infof("gossip engine online (store=%s anti_entropy=10s)", storePath)
}

// raftPeerProvider returns the team's voter set MINUS the local
// node by reading storage.Global() at request time. Decoupled from
// the gossip package via the PeerProvider interface so we don't
// import raftbackend there.
type raftPeerProvider struct{}

func (raftPeerProvider) Peers(_ context.Context) ([]gossip.Peer, error) {
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		return nil, nil
	}
	members := rs.Members()
	out := make([]gossip.Peer, 0, len(members))
	leaderID, _ := rs.LeaderInfo()
	for _, m := range members {
		// Skip self — the local clock maintained by Store.Clock()
		// already speaks for the local node.
		if m.NodeID == leaderID && m.IsLeader && rs.IsLeader() {
			continue
		}
		out = append(out, gossip.Peer{NodeID: m.NodeID, RaftAddr: m.RaftAddr})
	}
	return out, nil
}

// grpcSender is the production gossip.Sender implementation.
// Connection-pool reuse is a follow-up; for typical traffic shapes
// (handful of writes/s) the per-call dial is fine and produces
// clean failure semantics.
type grpcSender struct {
	creds credentials.TransportCredentials
}

func newGRPCSender(creds credentials.TransportCredentials) *grpcSender {
	return &grpcSender{creds: creds}
}

func (g *grpcSender) Push(ctx context.Context, peer gossip.Peer, deltas []gossip.Delta) error {
	if peer.RaftAddr == "" {
		return fmt.Errorf("gossip: peer %s has no raft addr", peer.NodeID)
	}
	conn, err := grpc.DialContext(ctx, peer.RaftAddr,
		grpc.WithTransportCredentials(g.creds),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("gossip: dial %s: %w", peer.RaftAddr, err)
	}
	defer conn.Close()

	req := &internalspec.PushRequest{
		Deltas: make([]*internalspec.Delta, 0, len(deltas)),
	}
	for _, d := range deltas {
		req.Deltas = append(req.Deltas, &internalspec.Delta{
			Bucket:  d.Bucket,
			Key:     d.Key,
			Payload: d.Payload,
			Hlc:     hlcToProto(d.HLC),
		})
	}
	_, err = internalspec.NewGossipServiceClient(conn).Push(ctx, req)
	return err
}

func (g *grpcSender) SyncDigest(ctx context.Context, peer gossip.Peer, digest map[string]gossip.HLC) ([]gossip.Delta, error) {
	if peer.RaftAddr == "" {
		return nil, fmt.Errorf("gossip: peer %s has no raft addr", peer.NodeID)
	}
	conn, err := grpc.DialContext(ctx, peer.RaftAddr,
		grpc.WithTransportCredentials(g.creds),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("gossip: dial %s: %w", peer.RaftAddr, err)
	}
	defer conn.Close()

	req := &internalspec.SyncDigestRequest{
		Digests: make([]*internalspec.BucketDigest, 0, len(digest)),
	}
	for bucket, hw := range digest {
		req.Digests = append(req.Digests, &internalspec.BucketDigest{
			Bucket:    bucket,
			HighWater: hlcToProto(hw),
		})
	}
	resp, err := internalspec.NewGossipServiceClient(conn).SyncDigest(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]gossip.Delta, 0, len(resp.GetDeltas()))
	for _, d := range resp.GetDeltas() {
		out = append(out, gossip.Delta{
			Bucket:  d.GetBucket(),
			Key:     d.GetKey(),
			Payload: d.GetPayload(),
			HLC:     hlcFromProto(d.GetHlc()),
		})
	}
	return out, nil
}

func hlcToProto(h gossip.HLC) *internalspec.HLC {
	return &internalspec.HLC{
		WallMs:  h.WallMs,
		Counter: h.Counter,
		NodeId:  h.NodeID,
	}
}

func hlcFromProto(p *internalspec.HLC) gossip.HLC {
	if p == nil {
		return gossip.HLC{}
	}
	return gossip.HLC{
		WallMs:  p.GetWallMs(),
		Counter: p.GetCounter(),
		NodeID:  p.GetNodeId(),
	}
}
