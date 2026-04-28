package chat

import (
	"reflect"
	"testing"
	"time"
)

// Pure-function tests for the chat store helpers. Live-DB
// behaviour is covered separately by the e2e harness (suite 29)
// which spins ClickHouse for real.

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"pricing", []string{"pricing"}},
		{"  Pricing  ", []string{"pricing"}},
		{"two words", []string{"two", "words"}},
		{"\tTab\nNewline", []string{"tab", "newline"}},
		{"MIXED Case Foo", []string{"mixed", "case", "foo"}},
	}
	for _, c := range cases {
		got := tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q): want %v, got %v", c.in, c.want, got)
		}
	}
}

// nullableTime returns nil for *time.Time(nil) and the
// dereferenced time.Time otherwise — the ClickHouse driver expects
// either a SQL NULL placeholder or a concrete time value for
// Nullable(DateTime64) columns.
func TestNullableTime(t *testing.T) {
	if v := nullableTime(nil); v != nil {
		t.Errorf("nil pointer → %v, want nil", v)
	}
	now := time.Now().UTC()
	v := nullableTime(&now)
	got, ok := v.(time.Time)
	if !ok {
		t.Fatalf("non-nil pointer → %T, want time.Time", v)
	}
	if !got.Equal(now) {
		t.Errorf("time mismatch: want %v, got %v", now, got)
	}
}

// checkDB returns an error when the store has no DB handle. The
// admin RPCs map this to a graceful "DB unavailable" response
// rather than crashing.
func TestCheckDB(t *testing.T) {
	if err := (*Store)(nil).checkDB(); err == nil {
		t.Error("nil receiver: want error")
	}
	if err := (&Store{}).checkDB(); err == nil {
		t.Error("nil db: want error")
	}
}
