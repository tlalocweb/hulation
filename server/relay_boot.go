package server

// Analytics-relay boot wiring (HA_PLAN3 §6 / sub-stage 3.6).
//
// One side of the relay runs on every team node:
//   - CH-connected node hosts RelayService.RecordEventBatch on the
//     internal mTLS gRPC channel. Writes events into CH via the
//     existing model.Event commit path.
//   - Non-CH nodes host an Outbox + Drainer goroutine. Visitor
//     events get enqueued and shipped to the configured CH peer.
//
// Both sides are no-ops when team.pki is unset (solo deployments).

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	relayimpl "github.com/tlalocweb/hulation/pkg/api/v1/relay"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/relay"
	"github.com/tlalocweb/hulation/pkg/server/unified"
	"github.com/tlalocweb/hulation/pkg/team/pki"
)

var relayBootLog = log.GetTaggedLogger("relay-boot", "Analytics relay bootstrap")

// globalOutbox is the per-process Outbox used by the visitor
// handlers. Set by registerLocalRelayDrainer when this node is
// non-CH. nil-safe — handler/visitor.go's relay-aware path checks
// before using.
var globalOutbox *relay.Outbox

// GlobalOutbox returns the process-wide Outbox or nil. handler/
// visitor.go uses this for the relay-aware commit path.
func GlobalOutbox() *relay.Outbox { return globalOutbox }

// registerLiveRelay is called from bootEnableInternalChannel after
// the internal mTLS channel is up. It picks one of two paths
// based on team.ch_connected.
func registerLiveRelay(srv *unified.Server, bundle *pki.Bundle, cfg *config.Config) {
	if cfg.Team == nil {
		return
	}
	if cfg.Team.CHConnected {
		registerLiveRelayServer(srv)
		return
	}
	registerLocalRelayDrainer(bundle, cfg)
}

// registerLiveRelayServer mounts the receive-side impl on the
// internal gRPC channel and points it at the local *gorm.DB.
func registerLiveRelayServer(srv *unified.Server) {
	g := srv.GetInternalGRPCServer()
	if g == nil {
		return
	}
	db := model.GetDB()
	if db == nil {
		relayBootLog.Warnf("RelayService stays at Unimplemented — model.GetDB() is nil")
		return
	}
	internalspec.RegisterRelayServiceServer(g, relayimpl.NewWithDB(db))
	relayBootLog.Infof("RelayService receiver online (CH-connected node)")
}

// registerLocalRelayDrainer stands up the per-process Outbox and
// kicks off the drainer goroutine. The drainer holds a gRPC
// client to the configured CH peer and ships batches there on a
// loop. handler/visitor.go consumes globalOutbox to enqueue events.
func registerLocalRelayDrainer(bundle *pki.Bundle, cfg *config.Config) {
	if cfg.Team.CHRelayPeer == "" {
		relayBootLog.Warnf("non-CH node has no team.ch_relay_peer set — visitor events will buffer locally and evict")
	}

	outboxPath := cfg.Team.OutboxPath
	if outboxPath == "" && cfg.Team.DataDir != "" {
		outboxPath = filepath.Join(cfg.Team.DataDir, "outbox", "events.queue")
	}

	o, err := relay.New(relay.Config{DiskOverflowPath: outboxPath})
	if err != nil {
		relayBootLog.Warnf("relay outbox init failed (relay disabled): %v", err)
		return
	}
	globalOutbox = o

	if cfg.Team.CHRelayPeer == "" {
		relayBootLog.Infof("relay outbox online (drainer disabled — no peer configured)")
		return
	}

	creds, err := buildRelayDialCreds(bundle)
	if err != nil {
		relayBootLog.Warnf("relay dial creds: %v (drainer disabled)", err)
		return
	}
	sender := buildRelaySender(creds, cfg.Team.CHRelayPeer, cfg.Team.NodeID)
	relay.Start(context.Background(), o, sender, relay.DrainerConfig{})
	relayBootLog.Infof("relay drainer online (peer=%s outbox=%s)", cfg.Team.CHRelayPeer, outboxPath)
}

func buildRelayDialCreds(bundle *pki.Bundle) (credentials.TransportCredentials, error) {
	cfg, err := pki.PeerDialTLSConfig(bundle)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// buildRelaySender returns a relay.Sender that opens a fresh gRPC
// connection per batch. Connection-pool reuse is a follow-up; for
// today's traffic shape (a handful of events/s on test stacks)
// this is fine and the failure semantics are clean (no stale
// pooled conns).
func buildRelaySender(creds credentials.TransportCredentials, peerAddr, sourceNodeID string) relay.Sender {
	return func(ctx context.Context, batch []*model.Event) error {
		conn, err := grpc.DialContext(ctx, peerAddr,
			grpc.WithTransportCredentials(creds),
			grpc.WithBlock(),
		)
		if err != nil {
			return fmt.Errorf("dial relay peer %s: %w", peerAddr, err)
		}
		defer conn.Close()

		encoded := make([][]byte, 0, len(batch))
		for _, ev := range batch {
			b, err := relay.EncodeEvent(ev)
			if err != nil {
				return fmt.Errorf("encode event: %w", err)
			}
			encoded = append(encoded, b)
		}

		client := internalspec.NewRelayServiceClient(conn)
		_, err = client.RecordEventBatch(ctx, &internalspec.RecordEventBatchRequest{
			Events:       encoded,
			SourceNodeId: sourceNodeID,
		})
		return err
	}
}
