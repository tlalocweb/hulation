package mobile

import (
	"testing"
	"time"
)

func TestPresetToRange(t *testing.T) {
	cases := []struct {
		preset    string
		wantDur   time.Duration
		wantGrain string
	}{
		{"24h", 24 * time.Hour, "hour"},
		{"7d", 7 * 24 * time.Hour, "day"},
		{"30d", 30 * 24 * time.Hour, "day"},
		{"90d", 90 * 24 * time.Hour, "day"},
	}
	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			from, to, grain := presetToRange(tc.preset)
			if got := to.Sub(from); got != tc.wantDur {
				t.Errorf("preset %q: duration = %s, want %s", tc.preset, got, tc.wantDur)
			}
			if grain != tc.wantGrain {
				t.Errorf("preset %q: grain = %q, want %q", tc.preset, grain, tc.wantGrain)
			}
		})
	}
}
