package config

import (
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestBuildHSTSHeader_DefaultsOnly(t *testing.T) {
	got := BuildHSTSHeader(nil, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
	})
	want := "max-age=31536000; includeSubDomains"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildHSTSHeader_GloballyDisabled(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{
		MaxAge:            "1h",
		IncludeSubDomains: ptr(true),
		Preload:           ptr(true),
	}, HSTSDefaults{Enabled: false, MaxAge: 8760 * time.Hour})
	if got != "" {
		t.Errorf("globally disabled should return empty, got %q", got)
	}
}

func TestBuildHSTSHeader_VhostHardDisable(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{Disable: true}, HSTSDefaults{
		Enabled: true,
		MaxAge:  8760 * time.Hour,
	})
	if got != "" {
		t.Errorf("vhost Disable=true should return empty, got %q", got)
	}
}

func TestBuildHSTSHeader_VhostMaxAgeOverride(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{MaxAge: "1h"}, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
	})
	want := "max-age=3600; includeSubDomains"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildHSTSHeader_VhostMaxAgeMalformedFallsBack(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{MaxAge: "not-a-duration"}, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
	})
	want := "max-age=31536000; includeSubDomains"
	if got != want {
		t.Errorf("malformed should fall back to default; got %q want %q", got, want)
	}
}

func TestBuildHSTSHeader_VhostDisablesIncludeSubDomains(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{IncludeSubDomains: ptr(false)}, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
	})
	if got != "max-age=31536000" {
		t.Errorf("got %q, want max-age=31536000 (no includeSubDomains)", got)
	}
}

func TestBuildHSTSHeader_VhostEnablesPreload(t *testing.T) {
	got := BuildHSTSHeader(&HSTSConfig{Preload: ptr(true)}, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
	})
	if !strings.Contains(got, "preload") {
		t.Errorf("expected preload directive; got %q", got)
	}
}

func TestBuildHSTSHeader_TriStateNilInherits(t *testing.T) {
	// IncludeSubDomains nil pointer should NOT flip the global default.
	got := BuildHSTSHeader(&HSTSConfig{MaxAge: "1h"}, HSTSDefaults{
		Enabled:           true,
		MaxAge:            8760 * time.Hour,
		IncludeSubDomains: true,
		Preload:           false,
	})
	if !strings.Contains(got, "includeSubDomains") {
		t.Errorf("nil IncludeSubDomains should inherit true; got %q", got)
	}
	if strings.Contains(got, "preload") {
		t.Errorf("nil Preload should inherit false; got %q", got)
	}
}

func TestBuildHSTSHeader_AllOff(t *testing.T) {
	// No subs, no preload, just max-age.
	got := BuildHSTSHeader(&HSTSConfig{
		IncludeSubDomains: ptr(false),
	}, HSTSDefaults{Enabled: true, MaxAge: time.Hour})
	if got != "max-age=3600" {
		t.Errorf("got %q, want max-age=3600", got)
	}
}
