package bolt

// Goals — per-server conversion rule persistence. One bucket row
// per goal; value is a JSON-marshalled Goal proto message. Keyed by
// goalID (generated at create-time) so the admin UI can round-trip
// without collisions.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
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

func goalKey(id string) string { return "goals/" + id }

// PutGoal upserts the goal. ID + ServerID must be set; CreatedAt is
// preserved on update. Returns the persisted goal.
func PutGoal(ctx context.Context, s storage.Storage, g StoredGoal) (StoredGoal, error) {
	if s == nil {
		return g, ErrNotOpen
	}
	if g.ID == "" || g.ServerID == "" {
		return g, fmt.Errorf("goal: id and server_id required")
	}
	now := time.Now().UTC()
	err := s.Mutate(ctx, goalKey(g.ID), func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			var prev StoredGoal
			if uerr := json.Unmarshal(current, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				g.CreatedAt = prev.CreatedAt
			}
		}
		if g.CreatedAt.IsZero() {
			g.CreatedAt = now
		}
		g.UpdatedAt = now
		return json.Marshal(&g)
	})
	return g, err
}

// GetGoal loads one goal. Returns nil when not found (not an error).
func GetGoal(ctx context.Context, s storage.Storage, goalID string) (*StoredGoal, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, goalKey(goalID))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g StoredGoal
	if uerr := json.Unmarshal(v, &g); uerr != nil {
		return nil, uerr
	}
	return &g, nil
}

// DeleteGoal removes the goal. Idempotent.
func DeleteGoal(ctx context.Context, s storage.Storage, goalID string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, goalKey(goalID))
}

// ListGoals returns every goal scoped to the given server_id. Empty
// server_id returns every goal (admin view).
func ListGoals(ctx context.Context, s storage.Storage, serverID string) ([]StoredGoal, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "goals/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredGoal, 0, len(rows))
	for _, v := range rows {
		var g StoredGoal
		if uerr := json.Unmarshal(v, &g); uerr != nil {
			continue
		}
		if serverID != "" && g.ServerID != serverID {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}
