package query

import (
	"fmt"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// BuildTimeseries emits a time-bucketed series of (ts, visitors,
// pageviews) rows aligned to the requested granularity.
func (b *Builder) BuildTimeseries(f *analyticsspec.Filters, serverID string) (*Built, error) {
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	src := pickSource(ctx)
	where, params := ctx.whereClause(src.timeCol, "server_id", src.onMV)

	var tsExpr, visitorsExpr, pageviewsExpr string
	if src.onMV {
		tsExpr = src.timeCol
		visitorsExpr = "uniqMerge(visitors_hll)"
		pageviewsExpr = "sum(pageviews)"
	} else {
		visitorsExpr = "uniqExact(belongs_to)"
		pageviewsExpr = "countIf(code = 1)"
		switch ctx.gran {
		case "hour":
			tsExpr = fmt.Sprintf("toStartOfHour(%s)", src.timeCol)
		case "week":
			tsExpr = fmt.Sprintf("toStartOfWeek(%s)", src.timeCol)
		default:
			tsExpr = fmt.Sprintf("toStartOfDay(%s)", src.timeCol)
		}
	}
	// Align MV day buckets to the requested granularity (hour or week).
	// The hourly MV already aligns to hour; the daily MV is day-aligned
	// and only needs re-bucketing for week.
	if src.onMV && ctx.gran == "week" {
		tsExpr = fmt.Sprintf("toStartOfWeek(%s)", src.timeCol)
	}

	sql := fmt.Sprintf(`
SELECT
    %s AS ts,
    %s AS visitors,
    %s AS pageviews
FROM %s
WHERE %s
GROUP BY ts
ORDER BY ts`, tsExpr, visitorsExpr, pageviewsExpr, src.table, where)

	return &Built{SQL: sql, Params: params}, nil
}
