// Package relay implements the receive side of the analytics
// relay (HA_PLAN3 §6.3). The CH-connected node hosts this; non-CH
// peers ship pre-enriched model.Event rows here in batches and
// the server inserts them into ClickHouse via the existing
// model path.
package relay

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/tlalocweb/hulation/log"
	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/pkg/relay"
)

var relayLog = log.GetTaggedLogger("relayrpc", "Internal RelayService")

// CHWriter is the seam the impl uses to write events. Production
// wires *gorm.DB directly via NewWithDB; tests substitute a fake
// that records calls.
type CHWriter interface {
	Insert(ctx context.Context, encoded [][]byte) (accepted, rejected uint32, err error)
}

// Service implements internalspec.RelayServiceServer. It accepts
// pre-enriched model.Event rows from non-CH peers and persists
// them via the configured CHWriter. The unified listener has
// already gated this on a Team-CA-signed client cert before the
// RPC dispatch, so identity-of-the-peer is established here.
type Service struct {
	internalspec.UnimplementedRelayServiceServer
	writer CHWriter
}

// New builds a Service from a CHWriter (mock-friendly).
func New(w CHWriter) *Service {
	return &Service{writer: w}
}

// NewWithDB is the production constructor. The DB is the shared
// *gorm.DB returned by model.GetDB().
func NewWithDB(db *gorm.DB) *Service {
	return &Service{writer: gormWriter{db: db}}
}

// RecordEventBatch is the receive endpoint. We intentionally accept
// the batch even when individual rows fail to decode — the failure
// is local to that row and we still want the rest to land.
// FailedPrecondition is reserved for "this node isn't CH-connected"
// which the boot wiring prevents from ever being registered.
func (s *Service) RecordEventBatch(ctx context.Context, req *internalspec.RecordEventBatchRequest) (*internalspec.RecordEventBatchResponse, error) {
	if req == nil || len(req.GetEvents()) == 0 {
		return &internalspec.RecordEventBatchResponse{}, nil
	}
	if s.writer == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"relay receiver is not CH-connected — drop the relay client")
	}
	accepted, rejected, err := s.writer.Insert(ctx, req.GetEvents())
	if err != nil {
		relayLog.Warnf("RecordEventBatch from %s: writer error: %v",
			req.GetSourceNodeId(), err)
		return nil, status.Errorf(codes.Internal, "ch insert: %v", err)
	}
	if rejected > 0 {
		relayLog.Warnf("RecordEventBatch from %s: %d events rejected during decode",
			req.GetSourceNodeId(), rejected)
	}
	return &internalspec.RecordEventBatchResponse{
		Accepted: accepted,
		Rejected: rejected,
	}, nil
}

// gormWriter is the production CHWriter. Decodes each gob blob,
// runs db.Create per event. We do single-row inserts (not bulk)
// because the events table goes through ClickHouse's events_v1
// path that has client-side enrichment hooks; bulk-inserting via
// gorm bypasses those.
type gormWriter struct{ db *gorm.DB }

func (g gormWriter) Insert(ctx context.Context, encoded [][]byte) (uint32, uint32, error) {
	if g.db == nil {
		return 0, 0, errors.New("nil db")
	}
	var accepted, rejected uint32
	tx := g.db.WithContext(ctx)
	for _, b := range encoded {
		ev, err := relay.DecodeEvent(b)
		if err != nil {
			rejected++
			continue
		}
		if err := tx.Create(ev).Error; err != nil {
			return accepted, rejected, err
		}
		accepted++
	}
	return accepted, rejected, nil
}
