package query

import (
	"fmt"
	"time"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// BuildRealtime emits the three queries that back /api/v1/analytics/realtime:
//
//  1. active visitors in the last 5 minutes (scalar count)
//  2. the 50 most recent events (for the live feed)
//  3. top pages + top sources in the last 5 minutes
//
// All three read raw events with a synthetic 5-minute window regardless
// of filters.from/filters.to. The caller should cache the result for
// ~5 seconds (see stage 1.5 notes in PLAN_1.md) to absorb polling
// load.
func (b *Builder) BuildRealtime(f *analyticsspec.Filters, serverID string) (active, recent, topPages, topSources *Built, err error) {
	// We need a resolved ACL but don't require the caller to send a
	// valid from/to for realtime — substitute a canned 5-minute window.
	if b.allowed == nil {
		return nil, nil, nil, nil, ErrNoACL
	}
	if f == nil {
		f = &analyticsspec.Filters{}
	}
	now := time.Now().UTC()
	syn := &analyticsspec.Filters{
		ServerIds: f.ServerIds,
		From:      now.Add(-5 * time.Minute).Format(time.RFC3339),
		To:        now.Format(time.RFC3339),
	}
	ctx, err := b.resolve(syn, serverID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ctx.requireEvents = true
	src := pickSource(ctx)
	where, params := ctx.whereClause(src.timeCol, "server_id", src.onMV)

	activeSQL := fmt.Sprintf(`
SELECT uniqExact(belongs_to) AS active_visitors_5m
FROM events
WHERE %s`, where)

	recentSQL := fmt.Sprintf(`
SELECT
    when AS ts,
    toString(code) AS event_code,
    url,
    referer AS referrer,
    country_code AS country,
    device_category AS device,
    from_ip AS ip
FROM events
WHERE %s
ORDER BY when DESC
LIMIT 50`, where)

	topPagesSQL := fmt.Sprintf(`
SELECT
    url_path AS key,
    uniqExact(belongs_to) AS visitors,
    countIf(code = 1) AS pageviews
FROM events
WHERE %s
GROUP BY key
ORDER BY pageviews DESC
LIMIT 10`, where)

	topSourcesSQL := fmt.Sprintf(`
SELECT
    referer_host AS key,
    uniqExact(belongs_to) AS visitors,
    countIf(code = 1) AS pageviews
FROM events
WHERE %s
GROUP BY key
ORDER BY pageviews DESC
LIMIT 10`, where)

	clone := func(p []any) []any { out := make([]any, len(p)); copy(out, p); return out }
	active = &Built{SQL: activeSQL, Params: clone(params)}
	recent = &Built{SQL: recentSQL, Params: clone(params)}
	topPages = &Built{SQL: topPagesSQL, Params: clone(params)}
	topSources = &Built{SQL: topSourcesSQL, Params: clone(params)}
	return active, recent, topPages, topSources, nil
}
