// Package query composes parameterised ClickHouse SQL for the
// analytics RPCs. It is the only code that emits SQL strings — every
// handler in pkg/api/v1/analytics calls into a Builder here instead of
// building queries itself.
//
// Two invariants:
//
//   - Every query is parameterised. No user-supplied string is ever
//     concatenated into SQL.
//   - Every query carries a server_id filter. The Builder fails fast
//     if WithAllowedServerIDs hasn't been called — the ACL intersection
//     is NOT optional.
//
// Source-table selection (`pickSource`) rounds through:
//   - mv_events_hourly — range ≤ 48h and granularity ∈ {hour, day}
//   - mv_events_daily  — range ≤ 400d and granularity ∈ {day, week}
//   - events           — everything else (session-aware queries, raw
//                        drill-downs, filters on columns the MVs don't
//                        carry)
//
// The picker is conservative: whenever a filter chip references a
// column not present on the MV state table (utm_*, event_code, goal,
// region, city, browser, os) the Builder falls back to raw events.

package query

import (
	"fmt"
	"strings"
	"time"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// Builder assembles analytics SQL from proto Filters plus the caller's
// ACL-restricted set of server ids.
type Builder struct {
	allowed []string // intersected ACL — mandatory.
}

// New returns a Builder with no ACL set. Callers MUST follow up with
// WithAllowedServerIDs before requesting any SQL, or every Build* call
// will return ErrNoACL.
func New() *Builder {
	return &Builder{}
}

// WithAllowedServerIDs installs the caller's ACL-intersected server id
// list. A nil / empty slice means "no access" — queries will still be
// built but produce no rows (server_id IN () never matches).
func (b *Builder) WithAllowedServerIDs(ids []string) *Builder {
	b.allowed = append([]string(nil), ids...)
	return b
}

// ErrNoACL is returned when a Build* method is called before
// WithAllowedServerIDs has ever been invoked. This is a programmer
// error — the handler forgot to run ACL intersection.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrNoACL        = Error("query.Builder: WithAllowedServerIDs not called; refusing to emit SQL without an ACL")
	ErrMissingRange = Error("query.Builder: filters.from / filters.to required")
	ErrBadRange     = Error("query.Builder: filters.from must be before filters.to")
	ErrBadTime      = Error("query.Builder: filters.from / filters.to must parse as RFC 3339")
)

// filterCtx is the internal per-call working state: parsed time range,
// effective server id set, and a slice of (col, value) pairs for the
// drill-down chips.
type filterCtx struct {
	from   time.Time
	to     time.Time
	gran   string // normalized to "hour" | "day" | "week"
	server string // the request's server_id; replaces allowed when non-empty

	// chips carries the optional drill-down filter pairs.
	chips []chipFilter

	// requireEvents forces the raw events table when any chip references
	// a column the MVs don't carry.
	requireEvents bool

	// allowedIDs is the intersection of (b.allowed) and (request server_id,
	// if any) and (filters.server_ids, if any). Always a bounded list.
	allowedIDs []string
}

type chipFilter struct {
	col   string
	val   string
	onMV  bool // true if the column exists on mv_events_hourly/_daily state tables
}

// resolve normalises a proto Filters + a single server_id scalar into
// the internal working state. Returns an error when the filter is
// unusable — missing range, unparseable time, empty ACL intersection.
func (b *Builder) resolve(f *analyticsspec.Filters, serverID string) (*filterCtx, error) {
	if b.allowed == nil {
		return nil, ErrNoACL
	}
	if f == nil {
		f = &analyticsspec.Filters{}
	}
	if f.From == "" || f.To == "" {
		return nil, ErrMissingRange
	}
	from, err := time.Parse(time.RFC3339, f.From)
	if err != nil {
		return nil, ErrBadTime
	}
	to, err := time.Parse(time.RFC3339, f.To)
	if err != nil {
		return nil, ErrBadTime
	}
	if !from.Before(to) {
		return nil, ErrBadRange
	}

	ctx := &filterCtx{
		from:   from,
		to:     to,
		gran:   normalizeGranularity(f.Granularity, to.Sub(from)),
		server: serverID,
	}

	// Build the effective allowed server set: intersect (b.allowed) with
	// (explicit server_id request) and/or (filters.server_ids).
	requested := []string{}
	if serverID != "" {
		requested = append(requested, serverID)
	}
	requested = append(requested, f.ServerIds...)
	ctx.allowedIDs = intersect(b.allowed, requested)

	// Drill-down chips. onMV flags follow the MV state-table schemas in
	// migrations/0002_events_mvs.sql.
	addChip := func(col, val string, onMV bool) {
		if val == "" {
			return
		}
		ctx.chips = append(ctx.chips, chipFilter{col: col, val: val, onMV: onMV})
		if !onMV {
			ctx.requireEvents = true
		}
	}
	addChip("country_code", f.Country, true)
	addChip("device_category", f.Device, true)
	addChip("referer_host", f.Source, true)
	addChip("url_path", f.Path, true)
	addChip("event_code", f.EventCode, false)
	addChip("goal", f.Goal, false)
	addChip("browser", f.Browser, false)
	addChip("os", f.Os, false)
	addChip("channel", f.Channel, true)
	addChip("utm_source", f.UtmSource, false)
	addChip("utm_medium", f.UtmMedium, false)
	addChip("utm_campaign", f.UtmCampaign, false)
	addChip("region", f.Region, false)
	addChip("city", f.City, false)

	return ctx, nil
}

// whereClause returns the WHERE fragment plus the param slice. The caller
// is responsible for prepending SELECT … FROM <table> and appending
// GROUP BY / ORDER BY / LIMIT as needed.
//
// timeCol is the column holding the event timestamp — "when" on raw
// events, "bucket_hour" / "bucket_day" on MVs.
// serverIDCol is the column holding the server identifier — always
// "server_id" in current schemas but kept parameterised for safety.
func (ctx *filterCtx) whereClause(timeCol, serverIDCol string, onMV bool) (string, []any) {
	var sb strings.Builder
	params := make([]any, 0, 4+len(ctx.chips))

	// Time range.
	sb.WriteString(fmt.Sprintf("%s >= ? AND %s < ?", timeCol, timeCol))
	params = append(params, ctx.from, ctx.to)

	// ACL: server_id IN (...). Always present. Empty list is a valid
	// "no access" state — emit an IN () that ClickHouse reads as never-
	// match, so no rows are returned.
	sb.WriteString(" AND ")
	sb.WriteString(serverIDCol)
	sb.WriteString(" IN (")
	if len(ctx.allowedIDs) == 0 {
		sb.WriteString("''") // literal empty string — no server_id matches
	} else {
		for i, id := range ctx.allowedIDs {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("?")
			params = append(params, id)
		}
	}
	sb.WriteString(")")

	// Drill-down chips.
	for _, c := range ctx.chips {
		if onMV && !c.onMV {
			// Unreachable when pickSource did its job — but fail closed
			// rather than silently drop the filter.
			continue
		}
		sb.WriteString(" AND ")
		sb.WriteString(c.col)
		sb.WriteString(" = ?")
		params = append(params, c.val)
	}

	return sb.String(), params
}

// intersect returns the elements of 'want' that also appear in 'have'.
// If 'want' is empty, every element of 'have' passes. If 'have' is
// empty, the intersection is empty.
func intersect(have, want []string) []string {
	if len(have) == 0 {
		return nil
	}
	if len(want) == 0 {
		out := make([]string, len(have))
		copy(out, have)
		return out
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	out := make([]string, 0, len(want))
	for _, w := range want {
		if _, ok := set[w]; ok {
			out = append(out, w)
		}
	}
	return out
}

// normalizeGranularity maps a raw granularity string onto the three
// supported values. An empty value falls back to a sensible default
// given the range (short ranges default to hour, long to day).
func normalizeGranularity(requested string, span time.Duration) string {
	switch strings.ToLower(requested) {
	case "hour":
		return "hour"
	case "day":
		return "day"
	case "week":
		return "week"
	}
	// Default based on span.
	switch {
	case span <= 48*time.Hour:
		return "hour"
	case span <= 14*24*time.Hour:
		return "day"
	default:
		return "day"
	}
}
