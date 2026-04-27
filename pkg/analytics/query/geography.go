package query

import analyticsspec "github.com/tlalocweb/hulation/pkg/apispec/v1/analytics"

// BuildGeography emits the Geography-report query. When filters.country
// is set, the report drills into regions within that country; otherwise
// it groups by country_code.
func (b *Builder) BuildGeography(f *analyticsspec.Filters, serverID string) (*Built, error) {
	// Drill decision: if country chip set → group by region. Else by country.
	dim := DimCountry
	if f != nil && f.Country != "" {
		dim = DimRegion
	}
	return b.BuildTable(dim, f, serverID, 100, 0)
}

// BuildSources emits the Sources-report query. group_by selects
// between channel (default — aggregated source type) and the raw
// referer_host, mirroring the UI's "group_by" chip.
func (b *Builder) BuildSources(f *analyticsspec.Filters, serverID, groupBy string, limit, offset int32) (*Built, error) {
	dim := DimChannel
	switch groupBy {
	case "referer_host", "host":
		dim = DimRefererHost
	case "utm_source":
		dim = Dimension("utm_source")
	case "channel", "":
		dim = DimChannel
	}
	return b.BuildTable(dim, f, serverID, limit, offset)
}
