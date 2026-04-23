package query

import (
	"fmt"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// BuildEvents emits the Events-report query: one row per distinct
// event code, with count, unique visitors, and first/last-seen
// timestamps. Always reads raw events (code-level detail isn't carried
// on the MVs).
func (b *Builder) BuildEvents(f *analyticsspec.Filters, serverID string) (*Built, error) {
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	ctx.requireEvents = true
	src := pickSource(ctx)
	where, params := ctx.whereClause(src.timeCol, "server_id", src.onMV)

	sql := fmt.Sprintf(`
SELECT
    toString(code) AS key,
    count() AS count,
    uniqExact(belongs_to) AS unique_visitors,
    min(when) AS first_seen,
    max(when) AS last_seen
FROM events
WHERE %s
GROUP BY key
ORDER BY count DESC
LIMIT 100`, where)
	return &Built{SQL: sql, Params: params}, nil
}
