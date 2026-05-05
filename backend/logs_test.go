package backend

import (
	"strings"
	"testing"
)

func TestBuildPrefix_NoColor(t *testing.T) {
	got := buildPrefix("api", false)
	want := "[api] "
	if got != want {
		t.Fatalf("buildPrefix(api, false) = %q, want %q", got, want)
	}
}

func TestBuildPrefix_Colored(t *testing.T) {
	got := buildPrefix("api", true)
	if !strings.Contains(got, "[api]") {
		t.Fatalf("colored prefix missing bracket name: %q", got)
	}
	if !strings.HasPrefix(got, "\x1b[") || !strings.Contains(got, ansiReset) {
		t.Fatalf("colored prefix missing ANSI codes: %q", got)
	}
}

func TestBuildPrefix_StablePerName(t *testing.T) {
	// Same name → same color, every call.
	a := buildPrefix("svc-x", true)
	b := buildPrefix("svc-x", true)
	if a != b {
		t.Fatalf("color for same name not stable: %q vs %q", a, b)
	}
}

func TestPrefixingWriter_BuffersPartialLines(t *testing.T) {
	// We can't directly capture log output from the package logger,
	// so we test the buffer behavior by reaching into the writer's
	// state. Specifically: a write without a newline should leave
	// the data buffered; a subsequent newline should flush.
	w := newPrefixingWriter("[t] ", false)
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if string(w.buf) != "partial" {
		t.Fatalf("buf after partial write = %q, want %q", w.buf, "partial")
	}
	if _, err := w.Write([]byte(" line\n")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if len(w.buf) != 0 {
		t.Fatalf("buf after newline = %q, want empty", w.buf)
	}
}

func TestPrefixingWriter_HandlesCRLF(t *testing.T) {
	w := newPrefixingWriter("[t] ", false)
	if _, err := w.Write([]byte("line one\r\nline two\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(w.buf) != 0 {
		t.Fatalf("buf should be empty after two complete lines, got %q", w.buf)
	}
}

func TestPrefixingWriter_FlushEmitsPartial(t *testing.T) {
	w := newPrefixingWriter("[t] ", false)
	if _, err := w.Write([]byte("trailing no newline")); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Flush()
	if len(w.buf) != 0 {
		t.Fatalf("buf after flush = %q, want empty", w.buf)
	}
}

func TestLogConfig_EffectiveDefaults(t *testing.T) {
	enabled, colored := (*LogConfig)(nil).Effective()
	if !enabled || !colored {
		t.Fatalf("nil LogConfig.Effective() = (%v,%v), want (true,true)", enabled, colored)
	}
	enabled, colored = (&LogConfig{Disabled: true}).Effective()
	if enabled {
		t.Fatalf("Disabled=true should yield enabled=false")
	}
	if !colored {
		t.Fatalf("Disabled=true alone shouldn't change colored")
	}
	enabled, colored = (&LogConfig{NoColor: true}).Effective()
	if !enabled {
		t.Fatalf("NoColor=true alone shouldn't change enabled")
	}
	if colored {
		t.Fatalf("NoColor=true should yield colored=false")
	}
}

func TestMergeLogConfig(t *testing.T) {
	g := &LogConfig{NoColor: true}
	o := &LogConfig{Disabled: true}
	if mergeLogConfig(g, nil) != g {
		t.Fatalf("nil override should fall through to global")
	}
	if mergeLogConfig(nil, o) != o {
		t.Fatalf("nil global should fall through to override")
	}
	if mergeLogConfig(g, o) != o {
		t.Fatalf("override should win when both set")
	}
}
