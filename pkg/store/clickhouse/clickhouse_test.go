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
		"migrations/0001_events_v1.sql",
		"migrations/0002_events_mvs.sql",
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
