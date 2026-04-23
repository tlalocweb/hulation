package query

import (
	"fmt"

	analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"
)

// Dimension identifies the grouping column for a table-style report.
type Dimension string

const (
	DimPath           Dimension = "url_path"          // Pages
	DimRefererHost    Dimension = "referer_host"      // Sources (default group)
	DimChannel        Dimension = "channel"           // Sources (group_by=channel)
	DimCountry        Dimension = "country_code"      // Geography
	DimRegion         Dimension = "region"            // Geography drill
	DimDeviceCategory Dimension = "device_category"   // Devices
	DimBrowser        Dimension = "browser"           // Devices
	DimOS             Dimension = "os"                // Devices
	DimEventCode      Dimension = "code"              // Events
	DimFormID         Dimension = "form_id"           // FormsReport — populated from 'data' JSON
)

// isOnMV reports whether the dimension column exists on the MV state
// tables. When false, pickSource falls back to raw events.
func (d Dimension) isOnMV() bool {
	switch d {
	case DimPath, DimRefererHost, DimChannel, DimCountry, DimDeviceCategory:
		return true
	}
	return false
}

// BuildTable emits a grouped query for one dimension. Limit caps the
// row count; offset is honoured so the caller can paginate (Pages,
// Sources, Visitors). Offset=0/limit=0 means "return all up to an
// internal safety cap of 1000".
func (b *Builder) BuildTable(dim Dimension, f *analyticsspec.Filters, serverID string, limit, offset int32) (*Built, error) {
	ctx, err := b.resolve(f, serverID)
	if err != nil {
		return nil, err
	}
	if !dim.isOnMV() {
		ctx.requireEvents = true
	}
	src := pickSource(ctx)
	where, params := ctx.whereClause(src.timeCol, "server_id", src.onMV)

	var visitors, pageviews string
	if src.onMV {
		visitors = "uniqMerge(visitors_hll)"
		pageviews = "sum(pageviews)"
	} else {
		visitors = "uniqExact(belongs_to)"
		pageviews = "countIf(code = 1)"
	}

	if limit <= 0 {
		limit = 1000
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}

	sql := fmt.Sprintf(`
SELECT
    %s AS key,
    %s AS visitors,
    %s AS pageviews
FROM %s
WHERE %s
GROUP BY key
ORDER BY pageviews DESC
LIMIT %d OFFSET %d`, string(dim), visitors, pageviews, src.table, where, limit, offset)

	return &Built{SQL: sql, Params: params}, nil
}
