package analytics

import (
	"context"
	"database/sql"

	"github.com/tlalocweb/hulation/pkg/analytics/query"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Pages ---

// Pages returns top paths by pageviews, honouring limit/offset.
func (s *Server) Pages(ctx context.Context, req *analyticsspec.PagesRequest) (*analyticsspec.PagesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildTable(query.DimPath, req.Filters, req.ServerId, req.Limit, req.Offset)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build pages: %s", err)
	}
	rows, err := s.queryTable(ctx, built)
	if err != nil {
		return nil, err
	}
	return &analyticsspec.PagesResponse{Rows: rows}, nil
}

// --- Sources ---

// Sources returns channel / referer_host / utm_source aggregates per
// the group_by request field. Defaults to channel.
func (s *Server) Sources(ctx context.Context, req *analyticsspec.SourcesRequest) (*analyticsspec.SourcesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildSources(req.Filters, req.ServerId, req.GroupBy, req.Limit, req.Offset)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build sources: %s", err)
	}
	rows, err := s.queryTable(ctx, built)
	if err != nil {
		return nil, err
	}
	return &analyticsspec.SourcesResponse{Rows: rows}, nil
}

// --- Geography ---

// Geography returns country (or region, when filters.country is set)
// aggregates with percent-of-total populated.
func (s *Server) Geography(ctx context.Context, req *analyticsspec.GeographyRequest) (*analyticsspec.GeographyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildGeography(req.Filters, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build geography: %s", err)
	}
	rows, err := s.queryTable(ctx, built)
	if err != nil {
		return nil, err
	}
	// Populate percent as fraction of total pageviews in this response.
	var total int64
	for _, r := range rows {
		total += r.Pageviews
	}
	if total > 0 {
		for _, r := range rows {
			r.Percent = float64(r.Pageviews) * 100.0 / float64(total)
		}
	}
	return &analyticsspec.GeographyResponse{Rows: rows}, nil
}

// --- Devices ---

// Devices returns three parallel tables — device_category, browser,
// and os — so the UI can render all three on one page without three
// round-trips.
func (s *Server) Devices(ctx context.Context, req *analyticsspec.DevicesRequest) (*analyticsspec.DevicesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	dc, br, os, err := b.BuildDevices(req.Filters, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build devices: %s", err)
	}
	deviceRows, err := s.queryTable(ctx, dc)
	if err != nil {
		return nil, err
	}
	browserRows, err := s.queryTable(ctx, br)
	if err != nil {
		return nil, err
	}
	osRows, err := s.queryTable(ctx, os)
	if err != nil {
		return nil, err
	}
	return &analyticsspec.DevicesResponse{
		DeviceCategory: deviceRows,
		Browser:        browserRows,
		Os:             osRows,
	}, nil
}

// --- Events ---

// Events returns per-event-code counts with first/last-seen timestamps.
func (s *Server) Events(ctx context.Context, req *analyticsspec.EventsRequest) (*analyticsspec.EventsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildEvents(req.Filters, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build events: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}
	rows, err := db.QueryContext(ctx, built.SQL, built.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "events query: %s", err)
	}
	defer rows.Close()

	resp := &analyticsspec.EventsResponse{}
	for rows.Next() {
		var (
			key                      sql.NullString
			count                    sql.NullInt64
			uniqueVisitors           sql.NullInt64
			firstSeen, lastSeen      sql.NullTime
		)
		if err := rows.Scan(&key, &count, &uniqueVisitors, &firstSeen, &lastSeen); err != nil {
			return nil, status.Errorf(codes.Internal, "events scan: %s", err)
		}
		row := &analyticsspec.TableRow{
			Key:            key.String,
			Count:          count.Int64,
			UniqueVisitors: uniqueVisitors.Int64,
		}
		if firstSeen.Valid {
			row.FirstSeen = firstSeen.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		if lastSeen.Valid {
			row.LastSeen = lastSeen.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp.Rows = append(resp.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "events rows: %s", err)
	}
	return resp, nil
}

// --- FormsReport ---

// FormsReport is currently a stub — form submissions are not yet
// persisted as distinct events with extractable form_id in the data
// column (handler/form.go writes FormSubmission events but the data
// field is not parsed into a dedicated form_id). Returns an empty row
// set so the UI doesn't 404. Full implementation deferred to a
// Phase-1 follow-up.
func (s *Server) FormsReport(ctx context.Context, req *analyticsspec.FormsReportRequest) (*analyticsspec.FormsReportResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	// Still run the ACL gate — refuses when the caller can't see the
	// server at all.
	if _, err := s.builder(ctx); err != nil {
		return nil, err
	}
	return &analyticsspec.FormsReportResponse{}, nil
}

// --- helpers ---

// queryTable is the shared scan path for reports that all return
// TableRow shapes (Pages, Sources, Geography, Devices). Each selects
// three columns: key, visitors, pageviews.
func (s *Server) queryTable(ctx context.Context, built *query.Built) ([]*analyticsspec.TableRow, error) {
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}
	rows, err := db.QueryContext(ctx, built.SQL, built.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "table query: %s", err)
	}
	defer rows.Close()
	var out []*analyticsspec.TableRow
	for rows.Next() {
		var (
			key       sql.NullString
			visitors  sql.NullInt64
			pageviews sql.NullInt64
		)
		if err := rows.Scan(&key, &visitors, &pageviews); err != nil {
			return nil, status.Errorf(codes.Internal, "table scan: %s", err)
		}
		out = append(out, &analyticsspec.TableRow{
			Key:       key.String,
			Visitors:  visitors.Int64,
			Pageviews: pageviews.Int64,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "table rows: %s", err)
	}
	return out, nil
}
