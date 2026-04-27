// Package clickhouse ties together the ClickHouse schema and migration
// files and exposes a convenient Apply() entry point for server startup.
//
// This package is optional — hula can run without ClickHouse (ClickHouse
// is reserved for analytics, badactor, and web-traffic data). When it IS
// available, call clickhouse.Apply(ctx, db, cfg) once on startup to
// ensure the schema is current.

package clickhouse

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"

	"github.com/tlalocweb/hulation/pkg/store/clickhouse/migrations"
)

//go:embed schema/*.sql migrations/*.sql
var sqlFiles embed.FS

// DefaultEventsTTLDays is the fallback retention for the raw events table
// when config.Analytics.EventsTTLDays is unset (approx. 13 months).
const DefaultEventsTTLDays = 395

// Apply runs all schema and pending migrations against the given ClickHouse
// connection. Idempotent on repeat invocation. Returns nil when up to
// date.
//
// eventsTTLDays controls the events TTL in the events_v1 DDL. Passing 0
// substitutes DefaultEventsTTLDays.
func Apply(ctx context.Context, db *sql.DB, eventsTTLDays int) error {
	if eventsTTLDays <= 0 {
		eventsTTLDays = DefaultEventsTTLDays
	}
	return migrations.Apply(ctx, db, sqlFiles, migrations.TemplateVars{
		EventsTTLDays: eventsTTLDays,
	})
}

// Files exposes the embedded SQL files for testing and inspection.
func Files() fs.FS { return sqlFiles }
