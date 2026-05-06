package server

// Bridge between preloadFastSubsystems (where raftbackend gets
// constructed) and BootUnifiedServer (where the internal gRPC
// server gets registered). The StreamLayer is the only object both
// sides need — raftbackend uses it as a raft.StreamLayer; the gRPC
// server registers it as a RaftTransportServiceServer.
//
// HA Stage 3: when team.pki is configured we run the Raft transport
// over the unified listener instead of a separate TCP socket
// (HA_PLAN3 §4.2). The two boot phases share a process-wide handle
// here so the layer outlives the function that created it.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/grpc/credentials"

	"github.com/tlalocweb/hulation/config"
	hraft "github.com/hashicorp/raft"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	raftbackend "github.com/tlalocweb/hulation/pkg/store/storage/raft"
	"github.com/tlalocweb/hulation/pkg/team/pki"
)

var (
	teamStreamMu     sync.Mutex
	teamStreamLayer  *raftbackend.GRPCStreamLayer
	teamPKIBundleEnv *pki.Bundle
)

// buildTeamStreamLayer is called from preloadFastSubsystems. Returns
// (nil, nil) when the deployment is solo (no team.pki block) — the
// raftbackend then falls back to its TCP transport. Returns
// (layer, nil) when PKI is configured and the StreamLayer is ready
// to plug into raftbackend.Config.StreamLayer.
func buildTeamStreamLayer(cfg *config.Config, advertiseAddr string) (*raftbackend.GRPCStreamLayer, error) {
	if cfg == nil || cfg.Team == nil || cfg.Team.PKI == nil {
		return nil, nil
	}
	p := cfg.Team.PKI
	if p.CACert == "" || p.NodeCert == "" || p.NodeKey == "" {
		return nil, errors.New("team.pki is partial — ca_cert, node_cert, node_key all required")
	}
	bundle, err := pki.LoadBundle(p.CACert, p.NodeCert, p.NodeKey)
	if err != nil {
		return nil, fmt.Errorf("load team pki: %w", err)
	}

	dialTLS, err := pki.PeerDialTLSConfig(bundle)
	if err != nil {
		return nil, fmt.Errorf("peer dial tls: %w", err)
	}
	if advertiseAddr == "" {
		advertiseAddr = cfg.Team.AdvertiseAddr
	}
	if advertiseAddr == "" {
		return nil, errors.New("team.advertise_addr required when team.pki is set")
	}

	layer, err := raftbackend.NewGRPCStreamLayer(raftbackend.GRPCStreamLayerConfig{
		AdvertiseAddr: advertiseAddr,
		Credentials:   credentials.NewTLS(dialTLS),
	})
	if err != nil {
		return nil, fmt.Errorf("new stream layer: %w", err)
	}

	teamStreamMu.Lock()
	teamPKIBundleEnv = bundle
	teamStreamMu.Unlock()
	return layer, nil
}

func saveTeamStreamLayer(sl *raftbackend.GRPCStreamLayer) {
	teamStreamMu.Lock()
	teamStreamLayer = sl
	teamStreamMu.Unlock()
}

// teamStreamLayerHandle returns the StreamLayer + bundle saved by
// preloadFastSubsystems. nil-safe — solo deployments return zero.
func teamStreamLayerHandle() (*raftbackend.GRPCStreamLayer, *pki.Bundle) {
	teamStreamMu.Lock()
	defer teamStreamMu.Unlock()
	return teamStreamLayer, teamPKIBundleEnv
}

// mountTeamRaftTransport registers the saved StreamLayer onto the
// internal gRPC server so peer Raft traffic lands on it. Called
// from bootEnableInternalChannel right after the internal channel
// is enabled but before any other internal services register
// (gRPC disallows post-Serve registration).
func mountTeamRaftTransport(internalGRPCRegister func(svc *raftbackend.GRPCStreamLayer)) {
	sl, _ := teamStreamLayerHandle()
	if sl == nil {
		return
	}
	internalGRPCRegister(sl)
}

// _ keeps imports stable across edit churn while we wire pieces in.
var _ = hraft.ServerAddress("")
var _ tls.Config
var _ internalspec.RaftTransportServiceServer
