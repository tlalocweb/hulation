package analytics

import (
	"context"
	"database/sql"
	"sync"
	"time"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Visitors returns the paginated visitor directory.
func (s *Server) Visitors(ctx context.Context, req *analyticsspec.VisitorsRequest) (*analyticsspec.VisitorsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildVisitors(req.Filters, req.ServerId, req.Limit, req.Offset)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build visitors: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}
	rows, err := db.QueryContext(ctx, built.SQL, built.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "visitors query: %s", err)
	}
	defer rows.Close()

	resp := &analyticsspec.VisitorsResponse{}
	for rows.Next() {
		var (
			visitorID            sql.NullString
			firstSeen, lastSeen  sql.NullTime
			sessions, pageviews  sql.NullInt64
			events               sql.NullInt64
			topCountry, topDevice sql.NullString
		)
		if err := rows.Scan(&visitorID, &firstSeen, &lastSeen, &sessions, &pageviews, &events, &topCountry, &topDevice); err != nil {
			return nil, status.Errorf(codes.Internal, "visitors scan: %s", err)
		}
		vs := &analyticsspec.VisitorSummary{
			VisitorId:  visitorID.String,
			Sessions:   int32(sessions.Int64),
			Pageviews:  int32(pageviews.Int64),
			Events:     int32(events.Int64),
			TopCountry: topCountry.String,
			TopDevice:  topDevice.String,
		}
		if firstSeen.Valid {
			vs.FirstSeen = firstSeen.Time.UTC().Format(time.RFC3339)
		}
		if lastSeen.Valid {
			vs.LastSeen = lastSeen.Time.UTC().Format(time.RFC3339)
		}
		resp.Visitors = append(resp.Visitors, vs)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "visitors rows: %s", err)
	}
	resp.Total = int32(len(resp.Visitors))
	return resp, nil
}

// Visitor returns the per-visitor profile header + event timeline.
func (s *Server) Visitor(ctx context.Context, req *analyticsspec.VisitorRequest) (*analyticsspec.VisitorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	if req.VisitorId == "" {
		return nil, status.Error(codes.InvalidArgument, "visitor_id required")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	// BuildVisitor requires a Filters message with from/to; the profile
	// page ignores the filter window anyway (the builder widens to
	// retention) — but resolve() still needs a valid range. Synthesize
	// a 400-day window ending now.
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -400)
	summaryQ, timelineQ, err := b.BuildVisitor(&analyticsspec.Filters{
		From: from.Format(time.RFC3339),
		To:   to.Format(time.RFC3339),
	}, req.ServerId, req.VisitorId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build visitor: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}

	var resp analyticsspec.VisitorResponse

	// Summary scan.
	var (
		vid                   sql.NullString
		firstSeen, lastSeen   sql.NullTime
		sessions, pageviews   sql.NullInt64
		events                sql.NullInt64
		topCountry, topDevice sql.NullString
		ipsRaw                any // groupUniqArray returns []string; clickhouse-go decodes into any
	)
	srow := db.QueryRowContext(ctx, summaryQ.SQL, summaryQ.Params...)
	if err := srow.Scan(&vid, &firstSeen, &lastSeen, &sessions, &pageviews, &events, &topCountry, &topDevice, &ipsRaw); err != nil && err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "visitor summary: %s", err)
	}
	if vid.String == "" {
		return nil, status.Error(codes.NotFound, "visitor not found")
	}
	vs := &analyticsspec.VisitorSummary{
		VisitorId:  vid.String,
		Sessions:   int32(sessions.Int64),
		Pageviews:  int32(pageviews.Int64),
		Events:     int32(events.Int64),
		TopCountry: topCountry.String,
		TopDevice:  topDevice.String,
	}
	if firstSeen.Valid {
		vs.FirstSeen = firstSeen.Time.UTC().Format(time.RFC3339)
	}
	if lastSeen.Valid {
		vs.LastSeen = lastSeen.Time.UTC().Format(time.RFC3339)
	}
	resp.Visitor = vs
	if ips, ok := ipsRaw.([]string); ok {
		resp.Ips = ips
	}

	// Timeline scan.
	trows, err := db.QueryContext(ctx, timelineQ.SQL, timelineQ.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "visitor timeline: %s", err)
	}
	defer trows.Close()
	for trows.Next() {
		var (
			ts                        sql.NullTime
			eventCode, url, referrer  sql.NullString
			country, device, ip       sql.NullString
		)
		if err := trows.Scan(&ts, &eventCode, &url, &referrer, &country, &device, &ip); err != nil {
			return nil, status.Errorf(codes.Internal, "visitor timeline scan: %s", err)
		}
		ev := &analyticsspec.VisitorEvent{
			EventCode: eventCode.String,
			Url:       url.String,
			Referrer:  referrer.String,
			Country:   country.String,
			Device:    device.String,
			Ip:        ip.String,
		}
		if ts.Valid {
			ev.Ts = ts.Time.UTC().Format(time.RFC3339)
		}
		resp.Timeline = append(resp.Timeline, ev)
	}
	if err := trows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "visitor timeline rows: %s", err)
	}

	return &resp, nil
}

// --- Realtime (with a short-lived cache) -----------------------------

type realtimeCacheEntry struct {
	expires time.Time
	resp    *analyticsspec.RealtimeResponse
}

var (
	realtimeMu    sync.Mutex
	realtimeCache = make(map[string]*realtimeCacheEntry)
	realtimeTTL   = 5 * time.Second
)

// Realtime returns active visitors + recent events + top pages/sources
// in the last 5 minutes. Results are cached per server_id for 5
// seconds to absorb polling load — multiple dashboards open on the
// same server_id share one query.
func (s *Server) Realtime(ctx context.Context, req *analyticsspec.RealtimeRequest) (*analyticsspec.RealtimeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}

	cacheKey := req.ServerId
	realtimeMu.Lock()
	if entry, ok := realtimeCache[cacheKey]; ok && time.Now().Before(entry.expires) {
		resp := entry.resp
		realtimeMu.Unlock()
		return resp, nil
	}
	realtimeMu.Unlock()

	activeQ, recentQ, topPagesQ, topSourcesQ, err := b.BuildRealtime(nil, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build realtime: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}

	var resp analyticsspec.RealtimeResponse

	// active visitors scalar
	var active sql.NullInt64
	if err := db.QueryRowContext(ctx, activeQ.SQL, activeQ.Params...).Scan(&active); err != nil && err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "realtime active: %s", err)
	}
	resp.ActiveVisitors_5M = int32(active.Int64)

	// recent events
	rows, err := db.QueryContext(ctx, recentQ.SQL, recentQ.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "realtime recent: %s", err)
	}
	for rows.Next() {
		var (
			ts                        sql.NullTime
			eventCode, url, referrer  sql.NullString
			country, device, ip       sql.NullString
		)
		if err := rows.Scan(&ts, &eventCode, &url, &referrer, &country, &device, &ip); err != nil {
			rows.Close()
			return nil, status.Errorf(codes.Internal, "realtime recent scan: %s", err)
		}
		ev := &analyticsspec.VisitorEvent{
			EventCode: eventCode.String,
			Url:       url.String,
			Referrer:  referrer.String,
			Country:   country.String,
			Device:    device.String,
			Ip:        ip.String,
		}
		if ts.Valid {
			ev.Ts = ts.Time.UTC().Format(time.RFC3339)
		}
		resp.Recent = append(resp.Recent, ev)
	}
	rows.Close()

	// top pages
	pRows, err := db.QueryContext(ctx, topPagesQ.SQL, topPagesQ.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "realtime top pages: %s", err)
	}
	for pRows.Next() {
		var (
			key       sql.NullString
			visitors  sql.NullInt64
			pageviews sql.NullInt64
		)
		if err := pRows.Scan(&key, &visitors, &pageviews); err != nil {
			pRows.Close()
			return nil, status.Errorf(codes.Internal, "realtime top pages scan: %s", err)
		}
		resp.TopPages = append(resp.TopPages, &analyticsspec.TableRow{
			Key:       key.String,
			Visitors:  visitors.Int64,
			Pageviews: pageviews.Int64,
		})
	}
	pRows.Close()

	// top sources
	sRows, err := db.QueryContext(ctx, topSourcesQ.SQL, topSourcesQ.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "realtime top sources: %s", err)
	}
	for sRows.Next() {
		var (
			key       sql.NullString
			visitors  sql.NullInt64
			pageviews sql.NullInt64
		)
		if err := sRows.Scan(&key, &visitors, &pageviews); err != nil {
			sRows.Close()
			return nil, status.Errorf(codes.Internal, "realtime top sources scan: %s", err)
		}
		resp.TopSources = append(resp.TopSources, &analyticsspec.TableRow{
			Key:       key.String,
			Visitors:  visitors.Int64,
			Pageviews: pageviews.Int64,
		})
	}
	sRows.Close()

	// Cache + return.
	realtimeMu.Lock()
	realtimeCache[cacheKey] = &realtimeCacheEntry{
		expires: time.Now().Add(realtimeTTL),
		resp:    &resp,
	}
	realtimeMu.Unlock()

	return &resp, nil
}
