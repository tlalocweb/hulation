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
	"crypto/tls"
	"fmt"

	"github.com/tlalocweb/hulation/config"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/server/unified"
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
	unifiedLog.Infof("HA internal channel enabled (team_id=%s node_id=%s)", cfg.Team.TeamID, cfg.Team.NodeID)
	return nil
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
