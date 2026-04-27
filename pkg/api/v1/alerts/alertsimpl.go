// Package alerts implements the AlertsService gRPC API — threshold-
// alert CRUD + fire-history listing.
//
// The actual rule evaluator lives in pkg/alerts/evaluator. This
// package is the storage-and-contract surface only; the evaluator
// reads/writes the same Bolt buckets.

package alerts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	alertsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/alerts"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements alertsspec.AlertsServiceServer.
type Server struct {
	alertsspec.UnimplementedAlertsServiceServer
}

// New constructs an AlertsService implementation.
func New() *Server { return &Server{} }

// --- helpers ---------------------------------------------------------

func protoToStored(a *alertsspec.Alert) hulabolt.StoredAlert {
	return hulabolt.StoredAlert{
		ID:            a.GetId(),
		ServerID:      a.GetServerId(),
		Name:          a.GetName(),
		Description:   a.GetDescription(),
		Kind:          kindEnumToString(a.GetKind()),
		Threshold:     a.GetThreshold(),
		WindowMinutes: a.GetWindowMinutes(),
		TargetGoalID:  a.GetTargetGoalId(),
		TargetPath:    a.GetTargetPath(),
		TargetFormID:  a.GetTargetFormId(),
		Recipients:    append([]string(nil), a.GetRecipients()...),
		CooldownMins:  a.GetCooldownMinutes(),
		Enabled:       a.GetEnabled(),
	}
}

func storedToProto(a hulabolt.StoredAlert) *alertsspec.Alert {
	out := &alertsspec.Alert{
		Id:              a.ID,
		ServerId:        a.ServerID,
		Name:            a.Name,
		Description:     a.Description,
		Kind:            kindStringToEnum(a.Kind),
		Threshold:       a.Threshold,
		WindowMinutes:   a.WindowMinutes,
		TargetGoalId:    a.TargetGoalID,
		TargetPath:      a.TargetPath,
		TargetFormId:    a.TargetFormID,
		Recipients:      append([]string(nil), a.Recipients...),
		CooldownMinutes: a.CooldownMins,
		Enabled:         a.Enabled,
	}
	if !a.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(a.UpdatedAt)
	}
	if !a.LastFiredAt.IsZero() {
		out.LastFiredAt = timestamppb.New(a.LastFiredAt)
	}
	return out
}

func kindEnumToString(k alertsspec.AlertKind) string {
	switch k {
	case alertsspec.AlertKind_ALERT_KIND_GOAL_COUNT_ABOVE:
		return "goal_count_above"
	case alertsspec.AlertKind_ALERT_KIND_PAGE_TRAFFIC_DELTA:
		return "page_traffic_delta"
	case alertsspec.AlertKind_ALERT_KIND_FORM_SUBMISSION_RATE:
		return "form_submission_rate"
	case alertsspec.AlertKind_ALERT_KIND_BAD_ACTOR_RATE:
		return "bad_actor_rate"
	case alertsspec.AlertKind_ALERT_KIND_BUILD_FAILED:
		return "build_failed"
	}
	return ""
}

func kindStringToEnum(s string) alertsspec.AlertKind {
	switch s {
	case "goal_count_above":
		return alertsspec.AlertKind_ALERT_KIND_GOAL_COUNT_ABOVE
	case "page_traffic_delta":
		return alertsspec.AlertKind_ALERT_KIND_PAGE_TRAFFIC_DELTA
	case "form_submission_rate":
		return alertsspec.AlertKind_ALERT_KIND_FORM_SUBMISSION_RATE
	case "bad_actor_rate":
		return alertsspec.AlertKind_ALERT_KIND_BAD_ACTOR_RATE
	case "build_failed":
		return alertsspec.AlertKind_ALERT_KIND_BUILD_FAILED
	}
	return alertsspec.AlertKind_ALERT_KIND_UNSPECIFIED
}

func deliveryStatusToEnum(s string) alertsspec.DeliveryStatus {
	switch s {
	case "success":
		return alertsspec.DeliveryStatus_DELIVERY_STATUS_SUCCESS
	case "retrying":
		return alertsspec.DeliveryStatus_DELIVERY_STATUS_RETRYING
	case "failed":
		return alertsspec.DeliveryStatus_DELIVERY_STATUS_FAILED
	case "mailer_unconfigured":
		return alertsspec.DeliveryStatus_DELIVERY_STATUS_MAILER_UNCONFIGURED
	}
	return alertsspec.DeliveryStatus_DELIVERY_STATUS_UNSPECIFIED
}

func eventToProto(e hulabolt.StoredAlertEvent) *alertsspec.AlertEvent {
	out := &alertsspec.AlertEvent{
		Id:             e.ID,
		AlertId:        e.AlertID,
		ObservedValue:  e.ObservedValue,
		Threshold:      e.Threshold,
		Recipients:     append([]string(nil), e.Recipients...),
		DeliveryStatus: deliveryStatusToEnum(e.DeliveryStatus),
		Error:          e.Error,
	}
	if !e.FiredAt.IsZero() {
		out.FiredAt = timestamppb.New(e.FiredAt)
	}
	return out
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- RPCs ------------------------------------------------------------

func (s *Server) CreateAlert(ctx context.Context, req *alertsspec.CreateAlertRequest) (*alertsspec.Alert, error) {
	if req == nil || req.GetServerId() == "" || req.GetAlert() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id and alert required")
	}
	a := protoToStored(req.GetAlert())
	a.ServerID = req.GetServerId()
	if a.ID == "" {
		a.ID = newID()
	}
	a.CreatedAt = time.Time{}
	saved, err := hulabolt.PutAlert(a)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create alert: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) UpdateAlert(ctx context.Context, req *alertsspec.UpdateAlertRequest) (*alertsspec.Alert, error) {
	if req == nil || req.GetServerId() == "" || req.GetAlertId() == "" || req.GetAlert() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id, alert_id, and alert required")
	}
	existing, err := hulabolt.GetAlert(req.GetAlertId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get alert: %s", err)
	}
	if existing == nil || existing.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "alert not found")
	}
	a := protoToStored(req.GetAlert())
	a.ID = req.GetAlertId()
	a.ServerID = req.GetServerId()
	a.LastFiredAt = existing.LastFiredAt // preserve
	saved, err := hulabolt.PutAlert(a)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update alert: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) DeleteAlert(ctx context.Context, req *alertsspec.DeleteAlertRequest) (*alertsspec.DeleteAlertResponse, error) {
	if req == nil || req.GetAlertId() == "" {
		return nil, status.Error(codes.InvalidArgument, "alert_id required")
	}
	if err := hulabolt.DeleteAlert(req.GetAlertId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete alert: %s", err)
	}
	return &alertsspec.DeleteAlertResponse{Ok: true}, nil
}

func (s *Server) ListAlerts(ctx context.Context, req *alertsspec.ListAlertsRequest) (*alertsspec.ListAlertsResponse, error) {
	serverID := ""
	if req != nil {
		serverID = req.GetServerId()
	}
	rows, err := hulabolt.ListAlerts(serverID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list alerts: %s", err)
	}
	out := make([]*alertsspec.Alert, 0, len(rows))
	for _, r := range rows {
		out = append(out, storedToProto(r))
	}
	return &alertsspec.ListAlertsResponse{Alerts: out}, nil
}

func (s *Server) GetAlert(ctx context.Context, req *alertsspec.GetAlertRequest) (*alertsspec.Alert, error) {
	if req == nil || req.GetAlertId() == "" {
		return nil, status.Error(codes.InvalidArgument, "alert_id required")
	}
	a, err := hulabolt.GetAlert(req.GetAlertId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get alert: %s", err)
	}
	if a == nil || a.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "alert not found")
	}
	return storedToProto(*a), nil
}

func (s *Server) ListAlertEvents(ctx context.Context, req *alertsspec.ListAlertEventsRequest) (*alertsspec.ListAlertEventsResponse, error) {
	if req == nil || req.GetAlertId() == "" {
		return nil, status.Error(codes.InvalidArgument, "alert_id required")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 25
	}
	rows, err := hulabolt.ListAlertEvents(req.GetAlertId(), limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list alert events: %s", err)
	}
	out := make([]*alertsspec.AlertEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventToProto(r))
	}
	return &alertsspec.ListAlertEventsResponse{Events: out}, nil
}
