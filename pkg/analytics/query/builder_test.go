package query

import (
	"errors"
	"strings"
	"testing"
	"time"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// helpers ---------------------------------------------------------------

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func baseFilters(span time.Duration, gran string) *analyticsspec.Filters {
	to := time.Now().UTC().Truncate(time.Second)
	from := to.Add(-span)
	return &analyticsspec.Filters{
		From:        iso(from),
		To:          iso(to),
		Granularity: gran,
	}
}

func builderFor(allowed ...string) *Builder {
	return New().WithAllowedServerIDs(allowed)
}

// resolve / ACL / error paths -----------------------------------------

func TestResolve_NoACL(t *testing.T) {
	b := New() // no WithAllowedServerIDs
	_, err := b.BuildSummary(baseFilters(time.Hour, "hour"), "s1")
	if !errors.Is(err, ErrNoACL) {
		t.Fatalf("expected ErrNoACL, got %v", err)
	}
}

func TestResolve_MissingRange(t *testing.T) {
	b := builderFor("s1")
	_, err := b.BuildSummary(&analyticsspec.Filters{}, "s1")
	if !errors.Is(err, ErrMissingRange) {
		t.Fatalf("expected ErrMissingRange, got %v", err)
	}
}

func TestResolve_BadTime(t *testing.T) {
	b := builderFor("s1")
	_, err := b.BuildSummary(&analyticsspec.Filters{From: "not-a-time", To: "also-not"}, "s1")
	if !errors.Is(err, ErrBadTime) {
		t.Fatalf("expected ErrBadTime, got %v", err)
	}
}

func TestResolve_BadRange(t *testing.T) {
	b := builderFor("s1")
	now := iso(time.Now().UTC())
	_, err := b.BuildSummary(&analyticsspec.Filters{From: now, To: now}, "s1")
	if !errors.Is(err, ErrBadRange) {
		t.Fatalf("expected ErrBadRange, got %v", err)
	}
}

func TestACL_IntersectsServerID(t *testing.T) {
	b := builderFor("allowed-1", "allowed-2")
	// Request targets an un-allowed id → intersection is empty →
	// query still builds but filter serves an IN ('') clause.
	got, err := b.BuildSummary(baseFilters(time.Hour, "hour"), "other-server")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(got.SQL, "IN (''") {
		t.Fatalf("expected empty-ACL marker IN (''), got:\n%s", got.SQL)
	}
}

func TestACL_PassesThroughAllowed(t *testing.T) {
	b := builderFor("allowed-1", "allowed-2")
	got, err := b.BuildSummary(baseFilters(time.Hour, "hour"), "allowed-1")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// server_id parameterised, not inlined.
	if strings.Contains(got.SQL, "'allowed-1'") {
		t.Fatalf("server id leaked into SQL literal:\n%s", got.SQL)
	}
	// First params are from/to (2), next should be the ACL server id.
	if len(got.Params) < 3 {
		t.Fatalf("expected ≥3 params, got %d", len(got.Params))
	}
	if got.Params[2] != "allowed-1" {
		t.Fatalf("expected allowed-1 as 3rd param, got %v", got.Params[2])
	}
}

// source-table picker -------------------------------------------------

func TestPickSource_ShortHourlyGoesRaw(t *testing.T) {
	b := builderFor("s1")
	// 1h range, hour granularity → raw events.
	got, _ := b.BuildTimeseries(baseFilters(time.Hour, "hour"), "s1")
	if !strings.Contains(got.SQL, "FROM events") || strings.Contains(got.SQL, "FROM mv_") {
		t.Fatalf("expected raw events table, got:\n%s", got.SQL)
	}
}

func TestPickSource_TenDayDayGoesHourlyMV(t *testing.T) {
	b := builderFor("s1")
	got, _ := b.BuildTimeseries(baseFilters(10*24*time.Hour, "day"), "s1")
	if !strings.Contains(got.SQL, "mv_events_hourly_state") {
		t.Fatalf("expected mv_events_hourly_state, got:\n%s", got.SQL)
	}
}

func TestPickSource_LongGoesDailyMV(t *testing.T) {
	b := builderFor("s1")
	got, _ := b.BuildTimeseries(baseFilters(90*24*time.Hour, "day"), "s1")
	if !strings.Contains(got.SQL, "mv_events_daily_state") {
		t.Fatalf("expected mv_events_daily_state, got:\n%s", got.SQL)
	}
}

func TestPickSource_WeekGoesDailyMV(t *testing.T) {
	b := builderFor("s1")
	got, _ := b.BuildTimeseries(baseFilters(90*24*time.Hour, "week"), "s1")
	if !strings.Contains(got.SQL, "mv_events_daily_state") {
		t.Fatalf("expected mv_events_daily_state for week granularity, got:\n%s", got.SQL)
	}
	if !strings.Contains(got.SQL, "toStartOfWeek") {
		t.Fatalf("expected toStartOfWeek bucket expr, got:\n%s", got.SQL)
	}
}

func TestPickSource_MVDroppedWhenChipIsOffMV(t *testing.T) {
	b := builderFor("s1")
	f := baseFilters(10*24*time.Hour, "day")
	f.UtmSource = "newsletter" // off-MV column
	got, _ := b.BuildTimeseries(f, "s1")
	if !strings.Contains(got.SQL, "FROM events") || strings.Contains(got.SQL, "FROM mv_") {
		t.Fatalf("expected raw events when utm_source filter is set, got:\n%s", got.SQL)
	}
}

// table builder -------------------------------------------------------

func TestBuildTable_CapsLimit(t *testing.T) {
	b := builderFor("s1")
	got, err := b.BuildTable(DimPath, baseFilters(24*time.Hour, "day"), "s1", 9999, 0)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(got.SQL, "LIMIT 1000") {
		t.Fatalf("limit should cap at 1000; got:\n%s", got.SQL)
	}
}

func TestBuildTable_OffMVDimForcesEvents(t *testing.T) {
	b := builderFor("s1")
	got, _ := b.BuildTable(DimBrowser, baseFilters(30*24*time.Hour, "day"), "s1", 10, 0)
	if !strings.Contains(got.SQL, "FROM events") {
		t.Fatalf("browser dim should force raw events, got:\n%s", got.SQL)
	}
}

// visitors ------------------------------------------------------------

func TestBuildVisitor_NeedsID(t *testing.T) {
	b := builderFor("s1")
	_, _, err := b.BuildVisitor(baseFilters(time.Hour, "hour"), "s1", "")
	if err == nil {
		t.Fatalf("expected error on empty visitor_id")
	}
}

func TestBuildVisitor_TwoQueries(t *testing.T) {
	b := builderFor("s1")
	summary, timeline, err := b.BuildVisitor(baseFilters(time.Hour, "hour"), "s1", "v-42")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(summary.SQL, "GROUP BY belongs_to") {
		t.Fatalf("summary query missing GROUP BY belongs_to:\n%s", summary.SQL)
	}
	if !strings.Contains(timeline.SQL, "ORDER BY when DESC") {
		t.Fatalf("timeline query missing ORDER BY when:\n%s", timeline.SQL)
	}
	// Both queries take the visitor_id as final param.
	if summary.Params[len(summary.Params)-1] != "v-42" {
		t.Fatalf("summary last param should be visitor_id, got %v", summary.Params[len(summary.Params)-1])
	}
}

// realtime ------------------------------------------------------------

func TestBuildRealtime_FourQueries(t *testing.T) {
	b := builderFor("s1")
	active, recent, pages, sources, err := b.BuildRealtime(nil, "s1")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for i, q := range []*Built{active, recent, pages, sources} {
		if q == nil || q.SQL == "" {
			t.Fatalf("realtime query %d nil/empty", i)
		}
		if !strings.Contains(q.SQL, "FROM events") {
			t.Fatalf("realtime query %d should be raw events, got:\n%s", i, q.SQL)
		}
	}
}

// no-SQL-injection smoke — grep the package for fmt.Sprintf mixed with
// SELECT that concatenates an unknown-origin identifier. This is a
// coarse signal; the real protection is the params slice.

func TestNoUserInputInSQL(t *testing.T) {
	b := builderFor("s1")
	f := baseFilters(time.Hour, "hour")
	f.Country = "'; DROP TABLE events; --"
	got, err := b.BuildSummary(f, "s1")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if strings.Contains(got.SQL, "DROP TABLE") {
		t.Fatalf("user input leaked into SQL:\n%s", got.SQL)
	}
	// The payload should appear in params, not the SQL string.
	found := false
	for _, p := range got.Params {
		if s, ok := p.(string); ok && strings.Contains(s, "DROP TABLE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("malicious payload should have become a bound param")
	}
}
