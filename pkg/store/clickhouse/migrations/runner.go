// Package migrations applies versioned SQL files against ClickHouse on
// startup. Idempotent — the runner tracks applied migrations in a
// `schema_migrations` table and skips anything already seen.
//
// Usage:
//
//	import "github.com/tlalocweb/hulation/pkg/store/clickhouse/migrations"
//	if err := migrations.Apply(ctx, db, fs); err != nil {
//	    log.Fatalf("apply migrations: %v", err)
//	}
//
// The `fs` argument carries the embed.FS containing both the schema
// files (pkg/store/clickhouse/schema/*.sql) and the migration files
// (pkg/store/clickhouse/migrations/*.sql). Schema files are applied
// first (idempotent CREATE TABLE IF NOT EXISTS); migrations run in
// lexicographic order of filename.

package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"text/template"

	"github.com/tlalocweb/hulation/log"
)

var migrationLog = log.GetTaggedLogger("ch-migrations", "ClickHouse migration runner")

// TemplateVars are substituted into schema/migration SQL before execution.
// Keep this tight — everything here becomes a public knob.
type TemplateVars struct {
	// EventsTTLDays is the retention window for raw events. Default 395
	// (~13 months); operators can tune in config.yaml.
	EventsTTLDays int
}

// Apply runs all pending schema + migration files. Returns nil when the
// database is at the latest version.
//
// filesFS should be the embedded filesystem containing `schema/` and
// `migrations/` directories. Most callers pass a package-level
// //go:embed FS.
func Apply(ctx context.Context, db *sql.DB, filesFS fs.FS, vars TemplateVars) error {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	// Step 1: apply all schema files (idempotent CREATE TABLE IF NOT
	// EXISTS / CREATE MV IF NOT EXISTS). These are always applied; they
	// do not leave entries in schema_migrations.
	schemaFiles, err := collectFiles(filesFS, "schema")
	if err != nil {
		return fmt.Errorf("collect schema files: %w", err)
	}
	for _, f := range schemaFiles {
		if err := runFile(ctx, db, filesFS, f, vars); err != nil {
			return fmt.Errorf("apply schema %s: %w", f, err)
		}
		migrationLog.Infof("schema applied: %s", f)
	}

	// Step 2: apply migrations in lexicographic order, tracked.
	migFiles, err := collectFiles(filesFS, "migrations")
	if err != nil {
		return fmt.Errorf("collect migrations: %w", err)
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return fmt.Errorf("load applied set: %w", err)
	}
	for _, f := range migFiles {
		base := strings.TrimPrefix(f, "migrations/")
		if applied[base] {
			continue
		}
		if err := runFile(ctx, db, filesFS, f, vars); err != nil {
			return fmt.Errorf("apply migration %s: %w", base, err)
		}
		if err := recordApplied(ctx, db, base); err != nil {
			return fmt.Errorf("record %s: %w", base, err)
		}
		migrationLog.Infof("migration applied: %s", base)
	}

	return nil
}

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations
(
    name       String,
    applied_at DateTime64(3) DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(applied_at)
ORDER BY name;`

func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, createMigrationsTable)
	return err
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM schema_migrations FINAL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	return set, rows.Err()
}

func recordApplied(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (name) VALUES (?)", name)
	return err
}

func collectFiles(filesFS fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(filesFS, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, dir+"/"+e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func runFile(ctx context.Context, db *sql.DB, filesFS fs.FS, path string, vars TemplateVars) error {
	raw, err := fs.ReadFile(filesFS, path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Template-expand {{ .EventsTTLDays }} etc.
	tpl, err := template.New(path).Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, vars); err != nil {
		return fmt.Errorf("execute template %s: %w", path, err)
	}

	// ClickHouse driver doesn't accept multiple statements per Exec;
	// split on semicolons. We assume no semicolons inside string
	// literals here, which is true for our DDL.
	stmts := splitStatements(buf.String())
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("exec %s: %w\n---\n%s", path, err, s)
		}
	}
	return nil
}

// splitStatements splits on top-level semicolons. Comments (lines starting
// with --) are stripped.
func splitStatements(s string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// Embedded is a placeholder FS — consumers should construct their own
// embed.FS anchored at this package directory to pick up schema/ and
// migrations/ subdirs. We provide this here purely for type convenience.
type Embedded struct {
	FS embed.FS
}
