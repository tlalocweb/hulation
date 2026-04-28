// Package reports implements the ReportsService gRPC API — CRUD +
// Preview for scheduled email reports. SendNow + the dispatcher
// pipeline + ListRuns body land in stage 3.5.

package reports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	reportsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/reports"
	"github.com/tlalocweb/hulation/pkg/reports/dispatch"
	"github.com/tlalocweb/hulation/pkg/reports/render"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements reportsspec.ReportsServiceServer.
type Server struct {
	reportsspec.UnimplementedReportsServiceServer
}

// New constructs a ReportsService implementation.
func New() *Server { return &Server{} }

// --- helpers ---------------------------------------------------------

func variantEnumToString(v reportsspec.TemplateVariant) string {
	switch v {
	case reportsspec.TemplateVariant_TEMPLATE_VARIANT_DETAILED:
		return "detailed"
	case reportsspec.TemplateVariant_TEMPLATE_VARIANT_SUMMARY:
		return "summary"
	}
	return "summary"
}

func variantStringToEnum(s string) reportsspec.TemplateVariant {
	switch s {
	case "detailed":
		return reportsspec.TemplateVariant_TEMPLATE_VARIANT_DETAILED
	case "summary":
		return reportsspec.TemplateVariant_TEMPLATE_VARIANT_SUMMARY
	}
	return reportsspec.TemplateVariant_TEMPLATE_VARIANT_UNSPECIFIED
}

// filtersProtoToMap flattens the proto Filters into a
// map<string,string> for BoltDB round-tripping.
func filtersProtoToMap(f *reportsspec.Filters) map[string]string {
	if f == nil {
		return nil
	}
	out := make(map[string]string, 20)
	if f.From != "" {
		out["from"] = f.From
	}
	if f.To != "" {
		out["to"] = f.To
	}
	if f.Granularity != "" {
		out["granularity"] = f.Granularity
	}
	if f.Compare != "" {
		out["compare"] = f.Compare
	}
	if f.Country != "" {
		out["country"] = f.Country
	}
	if f.Device != "" {
		out["device"] = f.Device
	}
	if f.Source != "" {
		out["source"] = f.Source
	}
	if f.Path != "" {
		out["path"] = f.Path
	}
	if f.EventCode != "" {
		out["event_code"] = f.EventCode
	}
	if f.Goal != "" {
		out["goal"] = f.Goal
	}
	if f.Browser != "" {
		out["browser"] = f.Browser
	}
	if f.Os != "" {
		out["os"] = f.Os
	}
	if f.Channel != "" {
		out["channel"] = f.Channel
	}
	if f.UtmSource != "" {
		out["utm_source"] = f.UtmSource
	}
	if f.UtmMedium != "" {
		out["utm_medium"] = f.UtmMedium
	}
	if f.UtmCampaign != "" {
		out["utm_campaign"] = f.UtmCampaign
	}
	if f.Region != "" {
		out["region"] = f.Region
	}
	if f.City != "" {
		out["city"] = f.City
	}
	return out
}

func filtersMapToProto(m map[string]string) *reportsspec.Filters {
	if len(m) == 0 {
		return nil
	}
	return &reportsspec.Filters{
		From:        m["from"],
		To:          m["to"],
		Granularity: m["granularity"],
		Compare:     m["compare"],
		Country:     m["country"],
		Device:      m["device"],
		Source:      m["source"],
		Path:        m["path"],
		EventCode:   m["event_code"],
		Goal:        m["goal"],
		Browser:     m["browser"],
		Os:          m["os"],
		Channel:     m["channel"],
		UtmSource:   m["utm_source"],
		UtmMedium:   m["utm_medium"],
		UtmCampaign: m["utm_campaign"],
		Region:      m["region"],
		City:        m["city"],
	}
}

func protoToStored(r *reportsspec.ScheduledReport) hulabolt.StoredReport {
	recipients := append([]string(nil), r.GetRecipients()...)
	return hulabolt.StoredReport{
		ID:              r.GetId(),
		ServerID:        r.GetServerId(),
		Name:            r.GetName(),
		Cron:            r.GetCron(),
		Timezone:        r.GetTimezone(),
		Recipients:      recipients,
		TemplateVariant: variantEnumToString(r.GetTemplateVariant()),
		Filters:         filtersProtoToMap(r.GetFilters()),
		Enabled:         r.GetEnabled(),
	}
}

func storedToProto(r hulabolt.StoredReport) *reportsspec.ScheduledReport {
	out := &reportsspec.ScheduledReport{
		Id:              r.ID,
		ServerId:        r.ServerID,
		Name:            r.Name,
		Cron:            r.Cron,
		Timezone:        r.Timezone,
		Recipients:      append([]string(nil), r.Recipients...),
		TemplateVariant: variantStringToEnum(r.TemplateVariant),
		Filters:         filtersMapToProto(r.Filters),
		Enabled:         r.Enabled,
	}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(r.UpdatedAt)
	}
	if !r.NextFireAt.IsZero() {
		out.NextFireAt = timestamppb.New(r.NextFireAt)
	}
	return out
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- RPCs ------------------------------------------------------------

func (s *Server) CreateReport(ctx context.Context, req *reportsspec.CreateReportRequest) (*reportsspec.ScheduledReport, error) {
	if req == nil || req.GetServerId() == "" || req.GetReport() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id and report required")
	}
	r := protoToStored(req.GetReport())
	r.ServerID = req.GetServerId()
	if r.ID == "" {
		r.ID = newID()
	}
	r.CreatedAt = time.Time{}
	saved, err := hulabolt.PutReport(ctx, storage.Global(), r)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create report: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) UpdateReport(ctx context.Context, req *reportsspec.UpdateReportRequest) (*reportsspec.ScheduledReport, error) {
	if req == nil || req.GetServerId() == "" || req.GetReportId() == "" || req.GetReport() == nil {
		return nil, status.Error(codes.InvalidArgument, "server_id, report_id, report required")
	}
	existing, err := hulabolt.GetReport(ctx, storage.Global(), req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %s", err)
	}
	if existing == nil || existing.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "report not found")
	}
	r := protoToStored(req.GetReport())
	r.ID = req.GetReportId()
	r.ServerID = req.GetServerId()
	saved, err := hulabolt.PutReport(ctx, storage.Global(), r)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update report: %s", err)
	}
	return storedToProto(saved), nil
}

func (s *Server) DeleteReport(ctx context.Context, req *reportsspec.DeleteReportRequest) (*reportsspec.DeleteReportResponse, error) {
	if req == nil || req.GetReportId() == "" {
		return nil, status.Error(codes.InvalidArgument, "report_id required")
	}
	if err := hulabolt.DeleteReport(ctx, storage.Global(), req.GetReportId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete report: %s", err)
	}
	return &reportsspec.DeleteReportResponse{Ok: true}, nil
}

func (s *Server) ListReports(ctx context.Context, req *reportsspec.ListReportsRequest) (*reportsspec.ListReportsResponse, error) {
	serverID := ""
	if req != nil {
		serverID = req.GetServerId()
	}
	rows, err := hulabolt.ListReports(ctx, storage.Global(), serverID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list reports: %s", err)
	}
	out := make([]*reportsspec.ScheduledReport, 0, len(rows))
	for _, r := range rows {
		out = append(out, storedToProto(r))
	}
	return &reportsspec.ListReportsResponse{Reports: out}, nil
}

func (s *Server) GetReport(ctx context.Context, req *reportsspec.GetReportRequest) (*reportsspec.ScheduledReport, error) {
	if req == nil || req.GetReportId() == "" {
		return nil, status.Error(codes.InvalidArgument, "report_id required")
	}
	r, err := hulabolt.GetReport(ctx, storage.Global(), req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %s", err)
	}
	if r == nil || r.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "report not found")
	}
	return storedToProto(*r), nil
}

// PreviewReport renders the report body using the render package.
// The data population (calling analytics.Summary under the hood) is
// scoped for stage 3.5 — for now we hand static zeros so the UI can
// validate the template layout before the dispatcher is live.
func (s *Server) PreviewReport(ctx context.Context, req *reportsspec.PreviewReportRequest) (*reportsspec.PreviewReportResponse, error) {
	if req == nil || req.GetReportId() == "" {
		return nil, status.Error(codes.InvalidArgument, "report_id required")
	}
	r, err := hulabolt.GetReport(ctx, storage.Global(), req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %s", err)
	}
	if r == nil || r.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "report not found")
	}
	variant := render.VariantSummary
	if r.TemplateVariant == "detailed" {
		variant = render.VariantDetailed
	}
	now := time.Now().UTC()
	in := render.SummaryInput{
		ReportName:                r.Name,
		ServerID:                  r.ServerID,
		From:                      now.Add(-7 * 24 * time.Hour),
		To:                        now,
		TimezoneLabel:             r.Timezone,
		Visitors:                  0, // stubbed pending 3.5 data fetch
		Pageviews:                 0,
		BounceRate:                0,
		AvgSessionDurationSeconds: 0,
	}
	html, subject, rerr := render.Render(variant, in)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "render preview: %s", rerr)
	}
	return &reportsspec.PreviewReportResponse{Html: html, Subject: subject}, nil
}

// SendNow enqueues an immediate render+send via the Phase-3.5
// dispatcher. Returns a run_id the caller can poll via ListRuns.
func (s *Server) SendNow(ctx context.Context, req *reportsspec.SendNowRequest) (*reportsspec.SendNowResponse, error) {
	if req == nil || req.GetReportId() == "" {
		return nil, status.Error(codes.InvalidArgument, "report_id required")
	}
	r, err := hulabolt.GetReport(ctx, storage.Global(), req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %s", err)
	}
	if r == nil || r.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "report not found")
	}
	d := dispatch.Get()
	if d == nil {
		return nil, status.Error(codes.FailedPrecondition, "dispatcher not running")
	}
	runID, err := d.Enqueue(req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.ResourceExhausted, "enqueue: %s", err)
	}
	return &reportsspec.SendNowResponse{RunId: runID}, nil
}

// ListRuns returns the N most-recent dispatch attempts for a report.
// Default limit 50.
func (s *Server) ListRuns(ctx context.Context, req *reportsspec.ListRunsRequest) (*reportsspec.ListRunsResponse, error) {
	if req == nil || req.GetReportId() == "" {
		return nil, status.Error(codes.InvalidArgument, "report_id required")
	}
	r, err := hulabolt.GetReport(ctx, storage.Global(), req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %s", err)
	}
	if r == nil || r.ServerID != req.GetServerId() {
		return nil, status.Error(codes.NotFound, "report not found")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	rows, err := hulabolt.ListReportRuns(ctx, storage.Global(), req.GetReportId(), limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list runs: %s", err)
	}
	out := make([]*reportsspec.ReportRun, 0, len(rows))
	for _, run := range rows {
		rr := &reportsspec.ReportRun{
			Id:         run.ID,
			ReportId:   run.ReportID,
			Status:     run.Status,
			Attempt:    run.Attempt,
			Error:      run.Error,
			Recipients: append([]string(nil), run.Recipients...),
		}
		if !run.StartedAt.IsZero() {
			rr.StartedAt = timestamppb.New(run.StartedAt)
		}
		if !run.FinishedAt.IsZero() {
			rr.FinishedAt = timestamppb.New(run.FinishedAt)
		}
		out = append(out, rr)
	}
	return &reportsspec.ListRunsResponse{Runs: out}, nil
}
