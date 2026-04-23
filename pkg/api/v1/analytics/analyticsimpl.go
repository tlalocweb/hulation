// Package analytics implements the AnalyticsService gRPC API. All
// handlers route through pkg/analytics/query to build ClickHouse SQL,
// execute it on the same *sql.DB the model package uses, and project
// the result into proto response messages.
package analytics

import (
	"context"
	"database/sql"

	"github.com/tlalocweb/hulation/model"
	"github.com/tlalocweb/hulation/pkg/analytics/query"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ACLResolution is what the handler's ACL hook returns: the allowed
// set of server ids + a flag for "this caller is superadmin, let them
// query anything". Superadmin bypasses the intersection check so
// server ids outside the configured set (e.g., seed fixtures) still
// resolve for admin users.
type ACLResolution struct {
	Allowed    []string
	Superadmin bool
}

// Server implements analyticsspec.AnalyticsServiceServer.
type Server struct {
	analyticsspec.UnimplementedAnalyticsServiceServer

	// aclLookup returns the caller's ACL resolution. Admin callers get
	// Superadmin=true; non-admin callers get their per-user ACL list.
	aclLookup func(ctx context.Context) ACLResolution

	// dbFn returns the ClickHouse *sql.DB. Injected so tests can pass a
	// mock.
	dbFn func() *sql.DB
}

// New constructs an AnalyticsService implementation.
//
//	aclLookup   — returns the caller's ACL resolution from ctx.
//	db          — returns the ClickHouse *sql.DB; typically model.GetSQLDB.
func New(aclLookup func(ctx context.Context) ACLResolution, db func() *sql.DB) *Server {
	return &Server{aclLookup: aclLookup, dbFn: db}
}

// builder produces a query.Builder pre-loaded with the caller's ACL.
// Returns a gRPC Unauthenticated error when the context carries no
// claims at all — that's the authware contract and the indicator that
// upstream middleware didn't populate identity.
func (s *Server) builder(ctx context.Context) (*query.Builder, error) {
	if _, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); !ok {
		return nil, status.Error(codes.Unauthenticated, "no claims in context")
	}
	res := s.aclLookup(ctx)
	b := query.New()
	if res.Superadmin {
		b = b.WithSuperadmin()
	}
	b = b.WithAllowedServerIDs(res.Allowed)
	return b, nil
}

// Summary returns visitor + pageview totals plus bounce rate and
// average session duration for the requested window.
func (s *Server) Summary(ctx context.Context, req *analyticsspec.SummaryRequest) (*analyticsspec.SummaryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildSummary(req.Filters, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build summary: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}
	var resp analyticsspec.SummaryResponse
	row := db.QueryRowContext(ctx, built.SQL, built.Params...)
	var (
		visitors                  sql.NullInt64
		pageviews                 sql.NullInt64
		bounceRate                sql.NullFloat64
		avgSessionDurationSeconds sql.NullFloat64
	)
	if err := row.Scan(&visitors, &pageviews, &bounceRate, &avgSessionDurationSeconds); err != nil && err != sql.ErrNoRows {
		return nil, status.Errorf(codes.Internal, "summary query: %s", err)
	}
	resp.Visitors = visitors.Int64
	resp.Pageviews = pageviews.Int64
	resp.BounceRate = bounceRate.Float64
	resp.AvgSessionDurationSeconds = avgSessionDurationSeconds.Float64
	// Compare deltas are not yet computed; return -1 sentinel per proto
	// doc comment.
	resp.VisitorsDeltaPct = -1
	resp.PageviewsDeltaPct = -1
	resp.BounceRateDeltaPct = -1
	resp.AvgSessionDurationDeltaPct = -1
	return &resp, nil
}

// Timeseries returns a bucketed list of (ts, visitors, pageviews) rows
// aligned to the requested granularity.
func (s *Server) Timeseries(ctx context.Context, req *analyticsspec.TimeseriesRequest) (*analyticsspec.TimeseriesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	b, err := s.builder(ctx)
	if err != nil {
		return nil, err
	}
	built, err := b.BuildTimeseries(req.Filters, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "build timeseries: %s", err)
	}
	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}
	rows, err := db.QueryContext(ctx, built.SQL, built.Params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "timeseries query: %s", err)
	}
	defer rows.Close()

	var resp analyticsspec.TimeseriesResponse
	for rows.Next() {
		var (
			ts        sql.NullTime
			visitors  sql.NullInt64
			pageviews sql.NullInt64
		)
		if err := rows.Scan(&ts, &visitors, &pageviews); err != nil {
			return nil, status.Errorf(codes.Internal, "timeseries scan: %s", err)
		}
		bucket := &analyticsspec.TimeseriesBucket{
			Visitors:  visitors.Int64,
			Pageviews: pageviews.Int64,
		}
		if ts.Valid {
			bucket.Ts = ts.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp.Buckets = append(resp.Buckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "timeseries rows: %s", err)
	}
	return &resp, nil
}

// DefaultDB is the ACL-less convenience DB accessor most callers want:
// the global ClickHouse connection opened by model.ConnectDB.
func DefaultDB() *sql.DB { return model.GetSQLDB() }

// ensure the concrete type satisfies the interface at compile time.
var _ analyticsspec.AnalyticsServiceServer = (*Server)(nil)
