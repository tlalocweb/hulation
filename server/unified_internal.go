package server

// Internal-channel boot wiring for HA Stage 3. When the operator
// drops a Team CA + per-node cert bundle on disk and points the
// team.pki block at it, this code path enables the second gRPC
// server on the unified listener — the one that hosts internal-
// only services (Membership, Raft transport, Storage proxy, Relay,
// Gossip, Chat lookup).
//
// Solo deployments without a team.pki block skip this entirely;
// `bootEnableInternalChannel` is a no-op when no PKI material is
// configured. The hula binary still boots and serves traffic
// exactly as it did before HA Stage 3 landed.

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/tlalocweb/hulation/config"
	membershipimpl "github.com/tlalocweb/hulation/pkg/api/v1/membership"
	storageproxyimpl "github.com/tlalocweb/hulation/pkg/api/v1/storageproxy"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/pkg/team/pki"
)

// bootEnableInternalChannel optionally turns on the team-only
// internal mTLS gRPC channel on the unified listener. Returns nil
// when the deployment is solo (no team.pki block) — that's the
// expected path for everyone who hasn't yet rolled out genteamcerts.
func bootEnableInternalChannel(srv *unified.Server, cfg *config.Config) error {
	if srv == nil || cfg == nil || cfg.Team == nil || cfg.Team.PKI == nil {
		return nil
	}
	p := cfg.Team.PKI
	if p.CACert == "" && p.NodeCert == "" && p.NodeKey == "" {
		return nil
	}
	if p.CACert == "" || p.NodeCert == "" || p.NodeKey == "" {
		return fmt.Errorf("team.pki is partial — ca_cert, node_cert, and node_key are all required")
	}

	bundle, err := pki.LoadBundle(p.CACert, p.NodeCert, p.NodeKey)
	if err != nil {
		return fmt.Errorf("load team pki bundle: %w", err)
	}
	leaf, err := tls.X509KeyPair(bundle.Cert, bundle.Key)
	if err != nil {
		return fmt.Errorf("parse node cert+key: %w", err)
	}

	if err := srv.EnableInternalChannel(bundle.CAPool, &leaf); err != nil {
		return fmt.Errorf("enable internal channel: %w", err)
	}
	registerInternalServiceStubs(srv)
	registerLiveMembership(srv, cfg)
	registerLiveStorageProxy(srv, bundle, cfg)
	unifiedLog.Infof("HA internal channel enabled (team_id=%s node_id=%s)", cfg.Team.TeamID, cfg.Team.NodeID)
	return nil
}

// registerLiveStorageProxy wires the receive side of HA Stage 3.5
// (StorageProxyService.Apply on the leader) AND the dial side
// (RaftStorage.SetLeaderForwarder when running as a follower).
// Both sides reuse the same pkg/team/pki bundle that gated the
// internal channel — peers must already have a Team-CA-signed cert
// to even reach this RPC, so identity-of-the-peer is established
// before any of this runs.
func registerLiveStorageProxy(srv *unified.Server, bundle *pki.Bundle, cfg *config.Config) {
	g := srv.GetInternalGRPCServer()
	if g == nil {
		return
	}
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		unifiedLog.Warnf("StorageProxyService stays at Unimplemented — storage.Global is not RaftStorage")
		return
	}

	internalspec.RegisterStorageProxyServiceServer(g, storageproxyimpl.New(rs))

	creds, err := storageProxyDialCreds(bundle, cfg)
	if err != nil {
		unifiedLog.Warnf("forwardToLeader will fail closed: %v", err)
		return
	}
	rs.SetLeaderForwarder(buildLeaderForwarder(creds))
}

func storageProxyDialCreds(bundle *pki.Bundle, _ *config.Config) (credentials.TransportCredentials, error) {
	cfg, err := pki.PeerDialTLSConfig(bundle)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

func buildLeaderForwarder(creds credentials.TransportCredentials) raftbackend.LeaderForwarder {
	return func(ctx context.Context, _, leaderAddr string, encoded []byte) error {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, err := grpc.DialContext(dialCtx, leaderAddr,
			grpc.WithTransportCredentials(creds),
			grpc.WithBlock(),
		)
		if err != nil {
			return fmt.Errorf("dial leader %s: %w", leaderAddr, err)
		}
		defer conn.Close()

		client := internalspec.NewStorageProxyServiceClient(conn)
		_, err = client.Apply(ctx, &internalspec.ApplyRequest{Command: encoded})
		return err
	}
}

// registerLiveMembership swaps the stub MembershipService for the
// real one when a Raft-backed Storage is available. preloadFast-
// Subsystems runs before BootUnifiedServer, so storage.Global() is
// already populated by the time we hit this path. When the global
// is nil (storage init failed; degraded mode) the stub stays in
// place — Join calls return Unimplemented rather than crashing.
func registerLiveMembership(srv *unified.Server, cfg *config.Config) {
	g := srv.GetInternalGRPCServer()
	if g == nil {
		return
	}
	rs, ok := storagepkg.Global().(*raftbackend.RaftStorage)
	if !ok || rs == nil {
		unifiedLog.Warnf("MembershipService stays at Unimplemented — storage.Global is not RaftStorage")
		return
	}
	svc := membershipimpl.New(raftClusterAdapter{rs}, rs, cfg.Team.TeamID)
	internalspec.RegisterMembershipServiceServer(g, svc)
}

// raftClusterAdapter bridges *raftbackend.RaftStorage's accessor
// methods (which return raftbackend.MemberInfo) to membership-
// package's Cluster interface (which uses ClusterMember). Avoids a
// pkg/api/v1/membership → pkg/store/storage/raft import.
type raftClusterAdapter struct{ rs *raftbackend.RaftStorage }

func (a raftClusterAdapter) IsLeader() bool                   { return a.rs.IsLeader() }
func (a raftClusterAdapter) LeaderInfo() (string, string)     { return a.rs.LeaderInfo() }
func (a raftClusterAdapter) LastIndex() uint64                { return a.rs.LastIndex() }
func (a raftClusterAdapter) AppliedIndex() uint64             { return a.rs.AppliedIndex() }
func (a raftClusterAdapter) AddVoter(id, addr string, prevIndex uint64, t time.Duration) error {
	return a.rs.AddVoter(id, addr, prevIndex, t)
}
func (a raftClusterAdapter) RemoveServer(id string, prevIndex uint64, t time.Duration) error {
	return a.rs.RemoveServer(id, prevIndex, t)
}
func (a raftClusterAdapter) Members() []membershipimpl.ClusterMember {
	src := a.rs.Members()
	out := make([]membershipimpl.ClusterMember, 0, len(src))
	for _, m := range src {
		out = append(out, membershipimpl.ClusterMember{
			NodeID:   m.NodeID,
			RaftAddr: m.RaftAddr,
			Suffrage: m.Suffrage,
			IsLeader: m.IsLeader,
		})
	}
	return out
}

// registerInternalServiceStubs registers UnimplementedXxxServer for
// every internal service. Subsequent stages (3.3 RaftTransport, 3.4
// Membership, 3.5 StorageProxy, 3.6 Relay, 3.7 Gossip, 3.8
// ChatLookup) replace these stubs with real impls. The stubs
// guarantee a clean Unimplemented response for any peer that ends
// up calling a not-yet-built service.
func registerInternalServiceStubs(srv *unified.Server) {
	g := srv.GetInternalGRPCServer()
	if g == nil {
		return
	}
	internalspec.RegisterMembershipServiceServer(g, internalspec.UnimplementedMembershipServiceServer{})
	internalspec.RegisterRaftTransportServiceServer(g, internalspec.UnimplementedRaftTransportServiceServer{})
	internalspec.RegisterStorageProxyServiceServer(g, internalspec.UnimplementedStorageProxyServiceServer{})
	internalspec.RegisterRelayServiceServer(g, internalspec.UnimplementedRelayServiceServer{})
	internalspec.RegisterGossipServiceServer(g, internalspec.UnimplementedGossipServiceServer{})
	internalspec.RegisterChatLookupServiceServer(g, internalspec.UnimplementedChatLookupServiceServer{})
}
