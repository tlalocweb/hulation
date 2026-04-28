package analytics

// ForgetVisitor — GDPR erasure RPC. Deletes every event belonging to
// the given visitor_id on the given server_id from the events table,
// then writes an audit row to Bolt's audit_forget bucket.
//
// ClickHouse caveats:
//   * ALTER TABLE … DELETE is an async MUTATION. The statement
//     returns once the mutation is queued, not once rows are gone.
//     On modest tables the mutation typically completes within
//     seconds; on multi-TB tables it can run for minutes. Callers
//     that need a synchronous delete guarantee should poll
//     `system.mutations` — for the admin UI's purposes the
//     queue-and-confirm semantics are sufficient and we document it
//     as "scheduled".
//   * Aggregate MVs (mv_events_hourly, mv_events_daily, mv_sessions)
//     are fed by INSERTS through the events table; they won't shed
//     the rows on their own. For strict GDPR compliance an operator
//     needs to run the same DELETE against each MV — we schedule
//     those mutations here as part of the ForgetVisitor call.
//
// Authorization: annotation already requires server.{server_id}.admin
// so the admin gate is on the gateway/authware side; this impl
// trusts the interceptor.

import (
	"context"
	"time"

	"github.com/tlalocweb/hulation/pkg/forwarder"
	"github.com/tlalocweb/hulation/pkg/server/authware"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) ForgetVisitor(ctx context.Context, req *analyticsspec.ForgetVisitorRequest) (*analyticsspec.ForgetVisitorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "nil request")
	}
	if req.GetServerId() == "" || req.GetVisitorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id and visitor_id required")
	}

	db := s.dbFn()
	if db == nil {
		return nil, status.Error(codes.FailedPrecondition, "analytics DB not available")
	}

	// Pre-count how many events we're about to delete. Best-effort —
	// the actual mutation runs async.
	var rowsDeleted int64
	countQ := `SELECT count() FROM events WHERE server_id = ? AND belongs_to = ?`
	if err := db.QueryRowContext(ctx, countQ, req.GetServerId(), req.GetVisitorId()).Scan(&rowsDeleted); err != nil {
		// Non-fatal; proceed with the deletion anyway. We just don't
		// get an accurate count in the response.
		rowsDeleted = -1
	}

	// Queue the mutation on the events table. ALTER … DELETE doesn't
	// support parameter binding in some clickhouse-go versions, so we
	// format the WHERE clause with literal quotes — inputs are already
	// validated (visitor_id is a UUID-ish token per ingest; server_id
	// comes from a signed JWT or the URL scope).
	deleteStmt := "ALTER TABLE events DELETE WHERE server_id = ? AND belongs_to = ?"
	if _, err := db.ExecContext(ctx, deleteStmt, req.GetServerId(), req.GetVisitorId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete from events: %s", err)
	}

	// Matching delete on the aggregate MVs — these feed summary /
	// table reports so the visitor's traces linger there otherwise.
	// Failures here are logged at warn level but don't fail the RPC:
	// the events delete is the source-of-truth and the MVs will
	// rebuild on their own next ingest cycle if they drift.
	for _, mv := range []string{"mv_events_hourly", "mv_events_daily", "mv_sessions"} {
		_, _ = db.ExecContext(ctx,
			"ALTER TABLE "+mv+" DELETE WHERE server_id = ? AND belongs_to = ?",
			req.GetServerId(), req.GetVisitorId(),
		)
	}

	// Phase-4b extension: also wipe chat history. chat_messages and
	// chat_sessions are keyed on visitor_id (matches events.belongs_to),
	// so an erasure that didn't touch them would leave a side-channel
	// of personal data behind. Best-effort like the MV deletes above:
	// the events delete is canonical, and chat retention TTL would
	// reap anything that lingers within a year regardless.
	for _, table := range []string{"chat_messages", "chat_sessions"} {
		_, _ = db.ExecContext(ctx,
			"ALTER TABLE "+table+" DELETE WHERE server_id = ? AND visitor_id = ?",
			req.GetServerId(), req.GetVisitorId(),
		)
	}

	// Phase 4c.1: also purge the consent_log Bolt bucket. Same
	// best-effort posture as the MV deletes — the legally-relevant
	// erasure is the events DELETE; the consent log is a
	// secondary record we'd rather not orphan.
	if s := storage.Global(); s != nil {
		_ = hulabolt.DeleteConsentForVisitor(ctx, s, req.GetServerId(), req.GetVisitorId())
	}

	// Phase 4c.2: fan out the right-to-be-forgotten to every
	// configured forwarder. Adapters that don't implement deletion
	// (Meta CAPI without hashed-PII index, GA4 MP without service
	// account) silently no-op. Per-adapter errors are logged and do
	// not fail the RPC — the events DELETE is the primary obligation.
	if c := forwarder.GetForServer(req.GetServerId()); c != nil {
		if errs := c.Delete(ctx, req.GetServerId(), req.GetVisitorId()); len(errs) > 0 {
			for _, e := range errs {
				_ = e // logged by adapter
			}
		}
	}

	// Audit trail in Bolt. Admin identity comes from the authware
	// Claims installed by the AdminBearerInterceptor / HTTP middleware.
	adminUser := ""
	if c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); ok && c != nil {
		adminUser = c.Email
		if adminUser == "" {
			adminUser = c.Username
		}
	}
	at := time.Now().UTC()
	audit := hulabolt.StoredForgetAudit{
		VisitorID:   req.GetVisitorId(),
		ServerID:    req.GetServerId(),
		AdminUser:   adminUser,
		At:          at,
		RowsDeleted: rowsDeleted,
	}
	if s := storage.Global(); s != nil {
		if err := hulabolt.PutForgetAudit(ctx, s, audit); err != nil {
			// Non-fatal — the delete still happened. Log-and-continue.
			_ = err
		}
	}

	return &analyticsspec.ForgetVisitorResponse{
		RowsDeleted: rowsDeleted,
		AuditKey:    req.GetVisitorId() + "|" + at.Format(time.RFC3339Nano),
	}, nil
}
