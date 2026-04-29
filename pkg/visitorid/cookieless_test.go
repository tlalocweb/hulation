package visitorid

import (
	"strings"
	"testing"
	"time"
)

func TestDerive_Determinism(t *testing.T) {
	salt := make([]byte, SaltLen)
	for i := range salt {
		salt[i] = byte(i)
	}
	day := "20260428"
	a, err := Derive(salt, day, "1.2.3.4", "Mozilla/Test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Derive(salt, day, "1.2.3.4", "Mozilla/Test")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same input → different output: %s vs %s", a, b)
	}
}

func TestDerive_DayRotation(t *testing.T) {
	salt := make([]byte, SaltLen)
	a, _ := Derive(salt, "20260428", "1.2.3.4", "ua")
	b, _ := Derive(salt, "20260429", "1.2.3.4", "ua")
	if a == b {
		t.Errorf("different day → same id (privacy property broken): %s", a)
	}
}

func TestDerive_IPSeparation(t *testing.T) {
	salt := make([]byte, SaltLen)
	a, _ := Derive(salt, "20260428", "1.2.3.4", "ua")
	b, _ := Derive(salt, "20260428", "1.2.3.5", "ua")
	if a == b {
		t.Errorf("different IP → same id: %s", a)
	}
}

func TestDerive_UASeparation(t *testing.T) {
	salt := make([]byte, SaltLen)
	a, _ := Derive(salt, "20260428", "1.2.3.4", "Chrome")
	b, _ := Derive(salt, "20260428", "1.2.3.4", "Firefox")
	if a == b {
		t.Errorf("different UA → same id")
	}
}

func TestDerive_DomainSeparation(t *testing.T) {
	// (ip="ab", ua="c") vs (ip="a", ua="bc") must NOT collide.
	salt := make([]byte, SaltLen)
	a, _ := Derive(salt, "20260428", "ab", "c")
	b, _ := Derive(salt, "20260428", "a", "bc")
	if a == b {
		t.Errorf("NUL-separator missing? %q == %q", a, b)
	}
}

func TestDerive_SaltSeparation(t *testing.T) {
	s1 := make([]byte, SaltLen)
	s2 := make([]byte, SaltLen)
	s2[0] = 1
	a, _ := Derive(s1, "20260428", "ip", "ua")
	b, _ := Derive(s2, "20260428", "ip", "ua")
	if a == b {
		t.Errorf("different salt → same id (rotating salt would have no effect): %s", a)
	}
}

func TestDerive_BadSaltLen(t *testing.T) {
	_, err := Derive([]byte{0, 1, 2}, "20260428", "ip", "ua")
	if err == nil {
		t.Error("expected error on short salt")
	}
}

func TestDerive_UUIDShape(t *testing.T) {
	salt := make([]byte, SaltLen)
	id, err := Derive(salt, "20260428", "ip", "ua")
	if err != nil {
		t.Fatal(err)
	}
	// Length 36 + dashes at correct positions.
	if len(id) != 36 {
		t.Fatalf("length: got %d want 36", len(id))
	}
	for _, pos := range []int{8, 13, 18, 23} {
		if id[pos] != '-' {
			t.Errorf("dash at %d: got %c", pos, id[pos])
		}
	}
	// v4 marker.
	if id[14] != '4' {
		t.Errorf("v4 marker at pos 14: got %c", id[14])
	}
	// variant marker — first nibble of pos 19 must be 8/9/a/b.
	v := id[19]
	if !strings.ContainsRune("89ab", rune(v)) {
		t.Errorf("variant marker at pos 19: got %c want 8/9/a/b", v)
	}
}

func TestDayKey_UTC(t *testing.T) {
	// 2026-04-28 14:00 UTC → "20260428".
	tm := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	if got := DayKey(tm); got != "20260428" {
		t.Errorf("DayKey: got %s want 20260428", got)
	}
	// Far-east-zone times that are still on UTC's previous day.
	loc, _ := time.LoadLocation("Asia/Tokyo") // UTC+9
	tmJP := time.Date(2026, 4, 29, 1, 30, 0, 0, loc) // 2026-04-28 16:30 UTC
	if got := DayKey(tmJP); got != "20260428" {
		t.Errorf("DayKey JP: got %s want 20260428 (UTC date)", got)
	}
}
