// Package visitorid implements the deterministic visitor-id
// derivation used by Phase-4c.3 cookieless mode.
//
// In cookieless mode hula does not set a visitor cookie. The
// visitor id is derived at the server from a per-server secret salt
// + the current day + (IP, User-Agent). Same visitor on the same
// day → same id; same visitor next day → different id.
//
// This makes cross-day visitor stitching impossible by design —
// that's the privacy property and exactly what France's CNIL
// cleared Matomo cookieless mode on. Same-day stitching is still
// possible (so within-session funnel reports still work).
//
// Derivation:
//
//	id_bytes = HMAC-SHA256(salt, dayKey || 0x00 || ip || 0x00 || ua)[:16]
//	id_uuid  = UUIDv4-format(id_bytes)
//
// The output is shaped as a v4-style UUID string so downstream
// consumers (GA4 MP client_id, ClickHouse joins on belongs_to)
// see a familiar identifier shape.
package visitorid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SaltLen is the required salt length for Derive.
const SaltLen = 32

// ErrSaltLen is returned when the salt isn't exactly SaltLen bytes.
var ErrSaltLen = errors.New("visitorid: salt must be 32 bytes")

// DayKey turns a time into the YYYYMMDD-shaped string used in the
// HMAC input. The zone is forced to UTC so we don't get a global
// midnight skew across DST boundaries.
func DayKey(t time.Time) string {
	return t.UTC().Format("20060102")
}

// Derive computes the cookieless visitor id for (ip, ua) on the
// given day, using the per-server salt. Empty inputs are tolerated
// — they degrade the diversity of the id space but don't error.
//
// Returns a v4-shape UUID string suitable for use as a visitor_id
// in the events table.
func Derive(salt []byte, day, ip, ua string) (string, error) {
	if len(salt) != SaltLen {
		return "", ErrSaltLen
	}
	mac := hmac.New(sha256.New, salt)
	// Domain-separated concatenation: dayKey || 0x00 || ip || 0x00 || ua.
	// Using NUL separators makes (ip="ab", ua="c") and (ip="a", ua="bc")
	// produce different ids — a small but real defense against
	// pathological collisions.
	mac.Write([]byte(day))
	mac.Write([]byte{0x00})
	mac.Write([]byte(ip))
	mac.Write([]byte{0x00})
	mac.Write([]byte(ua))
	sum := mac.Sum(nil)

	// Truncate to 16 bytes and format as v4-shape UUID.
	var b [16]byte
	copy(b[:], sum[:16])
	// Set version 4 + variant 10x.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	out := make([]byte, 36)
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out), nil
}

// DeriveNow is a convenience wrapper that uses time.Now() for the
// day key. The same call twice within the same UTC day returns the
// same id (assuming identical ip+ua). The same call on consecutive
// days returns different ids — that is the privacy property.
func DeriveNow(salt []byte, ip, ua string) (string, error) {
	return Derive(salt, DayKey(time.Now()), ip, ua)
}

// FormatID exists only so test fixtures can build expected ids
// from raw 16-byte outputs without recomputing the HMAC. Production
// code should use Derive.
func FormatID(b [16]byte) string {
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	out := make([]byte, 36)
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out)
}

// errFmt is a tiny helper that keeps `fmt` import live and lets
// callers write `return errFmt("salt: %w", err)` style errors
// without re-importing fmt at every call site.
func errFmt(s string, args ...interface{}) error {
	return fmt.Errorf(s, args...)
}
