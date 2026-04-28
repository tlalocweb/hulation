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
	"encoding/json"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
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

// PutConsent appends a row to the consent_log bucket. Key shape is
// "<server_id>|<visitor_id>|<RFC3339Nano>" — ranged scans by visitor
// don't need a join, and sorting is implicit (server, visitor, time).
func PutConsent(c StoredConsent) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	data, err := json.Marshal(&c)
	if err != nil {
		return err
	}
	key := c.ServerID + "|" + c.VisitorID + "|" + c.At.UTC().Format(time.RFC3339Nano)
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketConsentLog)).Put([]byte(key), data)
	})
}

// ListConsentForVisitor returns every recorded consent state for the
// given (server_id, visitor_id), oldest-first. Bounded scan; consent
// log isn't a high-volume bucket so unpaged lookup is fine.
func ListConsentForVisitor(serverID, visitorID string) ([]StoredConsent, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	prefix := []byte(serverID + "|" + visitorID + "|")
	var out []StoredConsent
	err := db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(BucketConsentLog)).Cursor()
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			var sc StoredConsent
			if err := json.Unmarshal(v, &sc); err != nil {
				continue
			}
			out = append(out, sc)
		}
		return nil
	})
	return out, err
}

// DeleteConsentForVisitor removes every row keyed by
// (server_id, visitor_id). Used by ForgetVisitor; idempotent.
func DeleteConsentForVisitor(serverID, visitorID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	prefix := []byte(serverID + "|" + visitorID + "|")
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketConsentLog))
		c := b.Cursor()
		var keysToDelete [][]byte
		for k, _ := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, _ = c.Next() {
			keysToDelete = append(keysToDelete, append([]byte(nil), k...))
		}
		for _, k := range keysToDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func hasPrefix(b, prefix []byte) bool {
	return strings.HasPrefix(string(b), string(prefix))
}
