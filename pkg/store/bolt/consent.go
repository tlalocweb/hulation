package bolt

// Phase 4c.1 — consent log. Append-only audit trail capturing the
// (server_id, visitor_id, ts, analytics, marketing, source) tuple
// at the moment a visitor's consent state was recorded. ForgetVisitor
// purges per-visitor entries.
//
// Source taxonomy (one of):
//
//   "gpc_header"      — Sec-GPC: 1 was present; we treated it as a
//                       binding marketing opt-out.
//   "cmp_payload"     — the page's CMP supplied an explicit consent
//                       object via /v/hello body.
//   "default_off"     — no consent signal; defaults applied per the
//                       per-server consent_mode config.
//   "default_optin"   — opt_in mode; nothing was processed yet.
//   "default_optout"  — opt_out mode; everything was processed.

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// StoredConsent is one consent-log row.
type StoredConsent struct {
	ServerID  string    `json:"server_id"`
	VisitorID string    `json:"visitor_id"`
	At        time.Time `json:"at"`
	Analytics bool      `json:"analytics"`
	Marketing bool      `json:"marketing"`
	Source    string    `json:"source"`
}

// consentKey builds the full Storage key for a consent row.
// Layout: "consent_log/<server_id>|<visitor_id>|<ts>".
func consentKey(serverID, visitorID, ts string) string {
	return "consent_log/" + serverID + "|" + visitorID + "|" + ts
}

// consentVisitorPrefix is the Storage prefix that scopes a List/Keys
// scan to one visitor on one server.
func consentVisitorPrefix(serverID, visitorID string) string {
	return "consent_log/" + serverID + "|" + visitorID + "|"
}

// PutConsent appends a row to the consent_log bucket.
func PutConsent(ctx context.Context, s storage.Storage, c StoredConsent) error {
	if s == nil {
		return ErrNotOpen
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	data, err := json.Marshal(&c)
	if err != nil {
		return err
	}
	key := consentKey(c.ServerID, c.VisitorID, c.At.UTC().Format(time.RFC3339Nano))
	return s.Put(ctx, key, data)
}

// ListConsentForVisitor returns every recorded consent state for the
// given (server_id, visitor_id), oldest-first. Bounded scan; consent
// log isn't a high-volume bucket so unpaged lookup is fine.
func ListConsentForVisitor(ctx context.Context, s storage.Storage, serverID, visitorID string) ([]StoredConsent, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, consentVisitorPrefix(serverID, visitorID))
	if err != nil {
		return nil, err
	}
	out := make([]StoredConsent, 0, len(rows))
	// Sort by key (which sorts by timestamp because the timestamp
	// is the trailing segment).
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var sc StoredConsent
		if err := json.Unmarshal(rows[k], &sc); err != nil {
			continue
		}
		out = append(out, sc)
	}
	return out, nil
}

// DeleteConsentForVisitor removes every row keyed by
// (server_id, visitor_id). Used by ForgetVisitor; idempotent.
func DeleteConsentForVisitor(ctx context.Context, s storage.Storage, serverID, visitorID string) error {
	if s == nil {
		return ErrNotOpen
	}
	keys, err := s.Keys(ctx, consentVisitorPrefix(serverID, visitorID))
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	ops := make([]storage.BatchOp, 0, len(keys))
	for _, k := range keys {
		ops = append(ops, storage.BatchOp{Op: storage.OpDelete, Key: k})
	}
	return s.Batch(ctx, ops)
}

