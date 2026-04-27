package query

import "time"

// source identifies which physical table a query should read from.
type source struct {
	table string // "events" | "mv_events_hourly" | "mv_events_daily"
	// timeCol is the name of the timestamp column on this table. Raw
	// events uses "when" (DateTime64); MV state tables use
	// "bucket_hour" or "bucket_day".
	timeCol string
	// onMV is true iff the source is one of the MV state tables.
	onMV bool
}

var (
	srcRawEvents = source{table: "events", timeCol: "when", onMV: false}
	srcMVHourly  = source{table: "mv_events_hourly_state", timeCol: "bucket_hour", onMV: true}
	srcMVDaily   = source{table: "mv_events_daily_state", timeCol: "bucket_day", onMV: true}
)

// pickSource selects the source table given the effective range,
// granularity, and whether any chip filter forces us to raw events.
//
// Rules (conservative — prefer raw for edge cases):
//   - requireEvents set → raw.
//   - week granularity  → daily MV (week buckets are assembled in the
//     SELECT via toStartOfWeek on the bucket_day column).
//   - range ≤ 48h and granularity hour → raw (freshness + avoid the
//     ~second of hourly-MV lag during ingest).
//   - range ≤ 14d and granularity day or hour → hourly MV.
//   - range > 14d → daily MV.
//
// Rationale: the hourly MV aggregates by hour; at long ranges that's
// 24× more rows than the daily MV and costs more. The daily MV's cap
// is 400 days (rough year of retention) which matches EventsTTLDays.
func pickSource(ctx *filterCtx) source {
	if ctx.requireEvents {
		return srcRawEvents
	}
	span := ctx.to.Sub(ctx.from)
	switch ctx.gran {
	case "week":
		return srcMVDaily
	case "hour":
		if span <= 48*time.Hour {
			return srcRawEvents
		}
		return srcMVHourly
	case "day":
		if span <= 14*24*time.Hour {
			return srcMVHourly
		}
		return srcMVDaily
	}
	// Unreachable — normalizeGranularity restricts to the three above.
	return srcRawEvents
}
