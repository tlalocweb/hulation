package query

import (
	"fmt"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// BuildVisitors emits the paginated visitor directory query. The
// visitor directory always reads raw events — it needs per-visitor
// first/last-seen timestamps, counts of distinct sessions, and
// top-country / top-device lookups the MVs don't carry.
func (b *Builder) BuildVisitors(f *analyticsspec.Filters, serverID string, limit, offset int32) (*Built, error) {
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	ctx.requireEvents = true // always raw
	src := pickSource(ctx)
	where, params := ctx.whereClause(src.timeCol, "server_id", src.onMV)

	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	sql := fmt.Sprintf(`
SELECT
    belongs_to AS visitor_id,
    min(when) AS first_seen,
    max(when) AS last_seen,
    uniqExact(session_id) AS sessions,
    countIf(code = 1) AS pageviews,
    count() AS events,
    anyHeavy(country_code) AS top_country,
    anyHeavy(device_category) AS top_device,
    anyHeavy(asn) AS top_asn,
    anyHeavy(isp) AS top_isp
FROM events
WHERE %s
GROUP BY belongs_to
ORDER BY last_seen DESC
LIMIT %d OFFSET %d`, where, limit, offset)

	return &Built{SQL: sql, Params: params}, nil
}

// BuildVisitor emits the per-visitor profile summary + timeline queries.
// Returns TWO queries: one for the summary header, one for the event
// timeline (ordered by time). Caller issues them back-to-back.
//
// The visitor query intentionally widens the time range beyond
// filters.{from,to} — a profile page wants the visitor's *full*
// history, not just the current filter window. We clamp to the last
// 400 days to stay within the TTL.
func (b *Builder) BuildVisitor(f *analyticsspec.Filters, serverID, visitorID string) (summary, timeline *Built, err error) {
	if visitorID == "" {
		return nil, nil, Error("query.Builder: BuildVisitor needs a non-empty visitor_id")
	}
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, nil, err
	}
	// Force raw events; widen time range to the whole retention window.
	ctx.requireEvents = true
	src := pickSource(ctx)

	// Override from/to for the profile query. Keep the ACL and the
	// server_id clause; drop drill-down chips (profile is visitor-
	// scoped, not filter-scoped).
	profileCtx := &filterCtx{
		from:       ctx.to.AddDate(0, 0, -400),
		to:         ctx.to,
		gran:       ctx.gran,
		allowedIDs: ctx.allowedIDs,
	}
	whereBase, paramsBase := profileCtx.whereClause(src.timeCol, "server_id", false)
	where := whereBase + " AND belongs_to = ?"
	params := append(append([]any(nil), paramsBase...), visitorID)

	summarySQL := fmt.Sprintf(`
SELECT
    belongs_to AS visitor_id,
    min(when) AS first_seen,
    max(when) AS last_seen,
    uniqExact(session_id) AS sessions,
    countIf(code = 1) AS pageviews,
    count() AS events,
    anyHeavy(country_code) AS top_country,
    anyHeavy(device_category) AS top_device,
    anyHeavy(asn) AS top_asn,
    anyHeavy(isp) AS top_isp,
    groupUniqArray(from_ip) AS ips
FROM events
WHERE %s
GROUP BY belongs_to`, where)

	timelineSQL := fmt.Sprintf(`
SELECT
    when AS ts,
    toString(code) AS event_code,
    url,
    referer AS referrer,
    country_code AS country,
    device_category AS device,
    from_ip AS ip,
    asn,
    isp
FROM events
WHERE %s
ORDER BY when DESC
LIMIT 1000`, where)

	summary = &Built{SQL: summarySQL, Params: append([]any(nil), params...)}
	timeline = &Built{SQL: timelineSQL, Params: append([]any(nil), params...)}
	return summary, timeline, nil
}

// BuildVisitorIPs emits the per-IP enrichment query for a visitor's
// detail page. One row per distinct IP the visitor has used, with
// the most-frequently-seen ASN / ISP / Org / country_code for that
// IP. Same widened time range as BuildVisitor.
func (b *Builder) BuildVisitorIPs(f *analyticsspec.Filters, serverID, visitorID string) (*Built, error) {
	if visitorID == "" {
		return nil, Error("query.Builder: BuildVisitorIPs needs a non-empty visitor_id")
	}
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	ctx.requireEvents = true
	src := pickSource(ctx)

	profileCtx := &filterCtx{
		from:       ctx.to.AddDate(0, 0, -400),
		to:         ctx.to,
		gran:       ctx.gran,
		allowedIDs: ctx.allowedIDs,
	}
	whereBase, paramsBase := profileCtx.whereClause(src.timeCol, "server_id", false)
	where := whereBase + " AND belongs_to = ?"
	params := append(append([]any(nil), paramsBase...), visitorID)

	// from_ip is high-cardinality; LowCardinality columns (asn/isp/org)
	// resolve fast inside anyHeavy. Sorting by recency keeps the most
	// relevant IP on top.
	sql := fmt.Sprintf(`
SELECT
    from_ip AS ip,
    anyHeavy(asn) AS asn,
    anyHeavy(isp) AS isp,
    anyHeavy(org) AS org,
    anyHeavy(country_code) AS country_code,
    max(when) AS last_seen
FROM events
WHERE %s AND from_ip != ''
GROUP BY from_ip
ORDER BY last_seen DESC
LIMIT 200`, where)

	return &Built{SQL: sql, Params: params}, nil
}
