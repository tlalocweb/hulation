package authware

// PairCode infrastructure — backs the QR-pairing endpoints
// (`POST /api/v1/pair/{issue,redeem}`). An admin issues a short-lived
// single-use code via the SPA; the mobile app reads it from a QR, calls
// `/api/v1/pair/redeem` with its freshly-generated ed25519 public key,
// and the redemption stores the device in the surrounding
// [`DeviceKeyStore`]. Subsequent requests signed with that device key
// satisfy `claimsFromDeviceSignature`.
//
// The code format is human-friendly with a checksum so an operator
// reading off the QR can dictate it over the phone:
//
//	HULA-PAIR-XXXX-YYYY-ZZZZ
//
// 12 base32-ish alphabet chars (no ambiguous I / O / 0 / 1), grouped 4-4-4.
// One byte of CRC8 checksum sits inside the trailing group so a typo
// fails fast at redeem time instead of resolving to someone else's code.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"
	"time"
)

// Default lifetime — long enough to walk the operator's laptop over to the
// marketer's phone, short enough that a leaked screenshot stops mattering soon.
const DefaultPairCodeTTL = 15 * time.Minute

// PairCodePrefix is the literal prefix every code starts with. Keeping the
// product name in the wire format means an accidentally-pasted code in the
// wrong app rejects fast.
const PairCodePrefix = "HULA-PAIR-"

// Base32-ish alphabet — Crockford-style without the visually ambiguous
// I/O/0/1. 32 chars exactly so each char is 5 bits.
const pairCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// ErrPairCodeNotFound is returned by [`PairCodeStore.Consume`] when the code
// is unknown, expired, or already redeemed.
var ErrPairCodeNotFound = errors.New("paircode: not found or expired")

// ErrPairCodeChecksum is returned by [`ParsePairCode`] when the embedded
// checksum doesn't validate — typo, transcription error, or tampering.
var ErrPairCodeChecksum = errors.New("paircode: bad checksum")

// PairCode captures what the issuer's intent was — which user the redeemed
// device belongs to, which server it can chat for, an optional label.
type PairCode struct {
	// Code in canonical wire form: `HULA-PAIR-XXXX-YYYY-ZZZZ`.
	Code string
	// UserID is the hulation user the redeemed device acts on behalf of.
	// Carries through into [`DeviceKey.UserID`] at redemption.
	UserID string
	// ServerID is the chat server_id this device is scoped to. Empty means
	// "all servers the user has access to" — the redeem handler resolves
	// that later.
	ServerID string
	// Label is a human-readable hint the operator chose ("Maria's iPhone").
	// Surfaced in admin lists after redemption.
	Label string
	// CreatedAt + ExpiresAt are UTC.
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IsExpired reports whether `now` is past ExpiresAt.
func (p *PairCode) IsExpired(now time.Time) bool {
	return !now.Before(p.ExpiresAt)
}

// PairCodeStore is the read/write interface the handlers consult.
// Production implementations back this with bolt / sqlite / postgres.
type PairCodeStore interface {
	// Put stores a freshly-minted code. Overwrites any prior entry for the
	// same code (collisions are astronomically unlikely but the contract is
	// "last writer wins" to avoid surprising the operator).
	Put(code PairCode)
	// Consume atomically reads and deletes the entry. Returns
	// `ErrPairCodeNotFound` when the code is unknown or expired. Single-use
	// by design — calling Consume twice with the same code yields the entry
	// the first time and the error the second.
	Consume(code string, now time.Time) (PairCode, error)
}

// InMemoryPairCodeStore is a sync-safe map used by tests + the v1 boot path
// until a persistent store lands. Per-process — restarting hulation
// invalidates any unredeemed codes, which is acceptable given the 15-minute
// TTL.
type InMemoryPairCodeStore struct {
	mu      sync.Mutex
	byCode  map[string]PairCode
}

func NewInMemoryPairCodeStore() *InMemoryPairCodeStore {
	return &InMemoryPairCodeStore{byCode: make(map[string]PairCode)}
}

func (s *InMemoryPairCodeStore) Put(code PairCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byCode[code.Code] = code
}

func (s *InMemoryPairCodeStore) Consume(code string, now time.Time) (PairCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byCode[code]
	if !ok {
		return PairCode{}, ErrPairCodeNotFound
	}
	// Always delete on lookup — even expired entries — so the map doesn't
	// grow unbounded under the issue-but-never-redeem pattern.
	delete(s.byCode, code)
	if entry.IsExpired(now) {
		return PairCode{}, ErrPairCodeNotFound
	}
	return entry, nil
}

// PurgeExpired drops entries whose ExpiresAt is past `now`. Optional — Consume
// already deletes on read, so this only matters for codes that get issued and
// never redeemed. Wired as an opt-in janitor in tests; the production boot
// path can ignore it and let the map grow at one entry per unredeemed issue.
func (s *InMemoryPairCodeStore) PurgeExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for code, entry := range s.byCode {
		if entry.IsExpired(now) {
			delete(s.byCode, code)
			dropped++
		}
	}
	return dropped
}

// GeneratePairCode mints a fresh code with embedded CRC8-ish checksum.
// `rng` is optional (nil ⇒ crypto/rand). Returns the canonical wire form,
// ready to drop into a [`PairCode.Code`] field.
func GeneratePairCode(rng io.Reader) (string, error) {
	if rng == nil {
		rng = rand.Reader
	}
	// 11 alphabet chars of entropy + 1 char of checksum = 12 total payload.
	// 11 × 5 bits = 55 bits of entropy, plenty for a 15-minute window.
	const payloadLen = 11
	var raw [payloadLen]byte
	if _, err := io.ReadFull(rng, raw[:]); err != nil {
		return "", fmt.Errorf("paircode: read entropy: %w", err)
	}
	payload := make([]byte, payloadLen)
	for i, b := range raw {
		payload[i] = pairCodeAlphabet[int(b)%len(pairCodeAlphabet)]
	}
	check := checksumChar(payload)
	body := append(payload, check)
	// Group as XXXX-YYYY-ZZZZ.
	grouped := fmt.Sprintf(
		"%s%s-%s-%s",
		PairCodePrefix,
		string(body[0:4]),
		string(body[4:8]),
		string(body[8:12]),
	)
	return grouped, nil
}

// ParsePairCode validates the wire form (prefix, length, checksum). Returns
// the canonical 12-char body (stripped of prefix and dashes, uppercased) on
// success. The operator types the value into a recovery flow on failure;
// this is where we catch typos before touching the store.
func ParsePairCode(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, PairCodePrefix) {
		return "", ErrPairCodeChecksum
	}
	rest := strings.ReplaceAll(s[len(PairCodePrefix):], "-", "")
	if len(rest) != 12 {
		return "", ErrPairCodeChecksum
	}
	for _, c := range rest {
		if !strings.ContainsRune(pairCodeAlphabet, c) {
			return "", ErrPairCodeChecksum
		}
	}
	body := []byte(rest)
	want := checksumChar(body[:11])
	if body[11] != want {
		return "", ErrPairCodeChecksum
	}
	return rest, nil
}

// checksumChar derives a single alphabet character from the CRC-32 of the
// payload, modulo the alphabet length. Not cryptographic — just a typo
// detector.
func checksumChar(payload []byte) byte {
	crc := crc32.ChecksumIEEE(payload)
	return pairCodeAlphabet[int(crc%uint32(len(pairCodeAlphabet)))]
}
