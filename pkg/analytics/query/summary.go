package query

import (
	"fmt"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// Built is the return shape for every Build* method: parameterised SQL
// and the positional params to hand to the ClickHouse driver. SQL uses
// '?' placeholders (ClickHouse's native positional syntax).
type Built struct {
	SQL    string
	Params []any
}

// BuildSummary emits SELECT for KPI cards: visitors, pageviews,
// bounce-rate, avg session duration. Ignores granularity.
//
// The bounce + duration metrics require per-session aggregation —
// which the MVs don't carry in the aggregate state. For now this
// query targets raw events and derives session stats client-side in
// the handler (sum of uniqExact over session_id, etc). The query
// shape is kept simple so the handler can swap to mv_sessions once
// its schema is finalised.
func (b *Builder) BuildSummary(f *analyticsspec.Filters, serverID string) (*Built, error) {
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	// KPI computations need session-level aggregation; raw events is
	// the only source that carries it today. Once mv_sessions exposes
	// bounce / duration as aggregate functions this can switch over.
	where, params := ctx.whereClause("when", "server_id", false)
	sql := fmt.Sprintf(`
SELECT
    uniqExact(belongs_to) AS visitors,
    countIf(code = 1) AS pageviews,
    countIf(session_pageviews = 1) / nullIf(sessions, 0) AS bounce_rate,
    avg(session_duration_seconds) AS avg_session_duration_seconds
FROM (
    SELECT
        belongs_to,
        session_id,
        code,
        count() OVER (PARTITION BY session_id) AS session_pageviews,
        dateDiff('second', min(when) OVER (PARTITION BY session_id), max(when) OVER (PARTITION BY session_id)) AS session_duration_seconds,
        count(DISTINCT session_id) OVER () AS sessions
    FROM events
    WHERE %s
)`, where)
	return &Built{SQL: sql, Params: params}, nil
}
