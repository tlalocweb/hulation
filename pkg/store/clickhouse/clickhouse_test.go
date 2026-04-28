package clickhouse

import (
	"io/fs"
	"strings"
	"testing"
)

// Smoke test: the embedded FS surfaces the expected files. Catches
// broken embed directives early (they'd otherwise fail only at server
// startup, after a ClickHouse connect).
func TestEmbeddedFilesPresent(t *testing.T) {
	expected := []string{
		"schema/events_v1.sql",
		"schema/chat_v1.sql",
		"migrations/0001_events_v1.sql",
		"migrations/0002_events_mvs.sql",
		"migrations/0003_chat_v1.sql",
	}
	for _, path := range expected {
		data, err := fs.ReadFile(sqlFiles, path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", path)
		}
		if !strings.Contains(string(data), "CREATE") && !strings.Contains(string(data), "INSERT") && !strings.Contains(string(data), "RENAME") {
			t.Errorf("%s does not contain expected DDL/DML keywords", path)
		}
	}
}

// Verify the TTL template placeholder is present in events_v1.sql so the
// runner knows to substitute it.
func TestEventsTTLTemplatePresent(t *testing.T) {
	data, err := fs.ReadFile(sqlFiles, "schema/events_v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "{{ .EventsTTLDays }}") {
		t.Error("events_v1.sql missing {{ .EventsTTLDays }} placeholder")
	}
}

// chat_v1.sql: shape + template placeholder + FTS index presence.
// We don't run the SQL (no live CH in unit tests), but we verify
// the file declares the two tables, references both skip-index
// types (the FTS plan in PLAN_4B.md §9), and uses the
// {{ .ChatRetentionDays }} placeholder so the runner substitutes
// the TTL.
func TestChatSchemaShape(t *testing.T) {
	data, err := fs.ReadFile(sqlFiles, "schema/chat_v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS chat_sessions",
		"CREATE TABLE IF NOT EXISTS chat_messages",
		"INDEX idx_content_token",
		"tokenbf_v1",
		"INDEX idx_content_ngram",
		"ngrambf_v1",
		"{{ .ChatRetentionDays }}",
		"assigned_agent_id",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chat_v1.sql missing %q", want)
		}
	}
}
