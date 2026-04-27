package bolt

// Goals — per-server conversion rule persistence. One bucket row
// per goal; value is a JSON-marshalled Goal proto message. Keyed by
// goalID (generated at create-time) so the admin UI can round-trip
// without collisions.

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// StoredGoal is the server-side shape. Mirrors the Goal proto but
// kept as a plain struct so the store package stays decoupled from
// the generated .pb.go types (which would pull every proto dep in).
type StoredGoal struct {
	ID            string    `json:"id"`
	ServerID      string    `json:"server_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Kind          string    `json:"kind"` // "url_visit" | "event" | "form" | "lander"
	RuleURLRegex  string    `json:"rule_url_regex,omitempty"`
	RuleEventCode int64     `json:"rule_event_code,omitempty"`
	RuleFormID    string    `json:"rule_form_id,omitempty"`
	RuleLanderID  string    `json:"rule_lander_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PutGoal upserts the goal. ID + ServerID must be set; CreatedAt is
// preserved on update. Returns the persisted goal.
func PutGoal(g StoredGoal) (StoredGoal, error) {
	if g.ID == "" || g.ServerID == "" {
		return g, fmt.Errorf("goal: id and server_id required")
	}
	db := Get()
	if db == nil {
		return g, ErrNotOpen
	}
	now := time.Now().UTC()
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketGoals))
		// Preserve CreatedAt when updating.
		if existing := b.Get([]byte(g.ID)); existing != nil {
			var prev StoredGoal
			if uerr := json.Unmarshal(existing, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				g.CreatedAt = prev.CreatedAt
			}
		}
		if g.CreatedAt.IsZero() {
			g.CreatedAt = now
		}
		g.UpdatedAt = now
		data, merr := json.Marshal(&g)
		if merr != nil {
			return merr
		}
		return b.Put([]byte(g.ID), data)
	})
	return g, err
}

// GetGoal loads one goal. Returns nil when not found (not an error).
func GetGoal(goalID string) (*StoredGoal, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out *StoredGoal
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketGoals)).Get([]byte(goalID))
		if v == nil {
			return nil
		}
		var g StoredGoal
		if uerr := json.Unmarshal(v, &g); uerr != nil {
			return uerr
		}
		out = &g
		return nil
	})
	return out, err
}

// DeleteGoal removes the goal. Idempotent.
func DeleteGoal(goalID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketGoals)).Delete([]byte(goalID))
	})
}

// ListGoals returns every goal scoped to the given server_id. Empty
// server_id returns every goal (admin view).
func ListGoals(serverID string) ([]StoredGoal, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredGoal
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketGoals)).ForEach(func(_, v []byte) error {
			var g StoredGoal
			if uerr := json.Unmarshal(v, &g); uerr != nil {
				return nil // skip malformed rows; don't fail the whole list
			}
			if serverID != "" && g.ServerID != serverID {
				return nil
			}
			out = append(out, g)
			return nil
		})
	})
	return out, err
}
