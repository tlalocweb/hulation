package config

// AnalyticsConfig holds tunables for the visitor-analytics subsystem.
// Exposed under `analytics:` in config.yaml.
type AnalyticsConfig struct {
	// EventsTTLDays is the retention window for raw events in ClickHouse.
	// Default: 395 (approx. 13 months). Materialized views
	// (mv_events_hourly, mv_events_daily, mv_sessions) are not TTL'd —
	// they retain aggregates indefinitely.
	EventsTTLDays int `yaml:"events_ttl_days,omitempty"`
}
