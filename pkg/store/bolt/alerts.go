package bolt

// Alerts — threshold-alert rule persistence + fire-history. Matches
// the shape pattern of goals.go and reports.go: one JSON-encoded
// struct per bucket row.

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// StoredAlert is the server-side shape. Deliberately a plain struct
// so pkg/store/bolt stays free of generated-proto imports.
type StoredAlert struct {
	ID             string    `json:"id"`
	ServerID       string    `json:"server_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Kind           string    `json:"kind"` // "goal_count_above" | "page_traffic_delta" | "form_submission_rate" | "bad_actor_rate" | "build_failed"
	Threshold      float64   `json:"threshold"`
	WindowMinutes  int32     `json:"window_minutes"`
	TargetGoalID   string    `json:"target_goal_id,omitempty"`
	TargetPath     string    `json:"target_path,omitempty"`
	TargetFormID   string    `json:"target_form_id,omitempty"`
	Recipients     []string  `json:"recipients"`
	CooldownMins   int32     `json:"cooldown_minutes"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastFiredAt    time.Time `json:"last_fired_at"`
}

// StoredAlertEvent is one fire-history row.
type StoredAlertEvent struct {
	ID             string    `json:"id"`
	AlertID        string    `json:"alert_id"`
	ServerID       string    `json:"server_id"` // denormalised so ListAlertEvents can filter without a join
	FiredAt        time.Time `json:"fired_at"`
	ObservedValue  float64   `json:"observed_value"`
	Threshold      float64   `json:"threshold"`
	Recipients     []string  `json:"recipients"`
	DeliveryStatus string    `json:"delivery_status"` // "success" | "retrying" | "failed" | "mailer_unconfigured"
	Error          string    `json:"error,omitempty"`
}

// PutAlert upserts. Preserves CreatedAt on update.
func PutAlert(a StoredAlert) (StoredAlert, error) {
	if a.ID == "" || a.ServerID == "" {
		return a, fmt.Errorf("alert: id and server_id required")
	}
	db := Get()
	if db == nil {
		return a, ErrNotOpen
	}
	now := time.Now().UTC()
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketAlerts))
		if existing := b.Get([]byte(a.ID)); existing != nil {
			var prev StoredAlert
			if uerr := json.Unmarshal(existing, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				a.CreatedAt = prev.CreatedAt
			}
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		a.UpdatedAt = now
		data, merr := json.Marshal(&a)
		if merr != nil {
			return merr
		}
		return b.Put([]byte(a.ID), data)
	})
	return a, err
}

// GetAlert loads one alert. Returns nil when not found.
func GetAlert(alertID string) (*StoredAlert, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out *StoredAlert
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketAlerts)).Get([]byte(alertID))
		if v == nil {
			return nil
		}
		var a StoredAlert
		if uerr := json.Unmarshal(v, &a); uerr != nil {
			return uerr
		}
		out = &a
		return nil
	})
	return out, err
}

// DeleteAlert removes the alert. Idempotent. Does not remove the
// associated AlertEvent rows — those stay as historical record.
func DeleteAlert(alertID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketAlerts)).Delete([]byte(alertID))
	})
}

// ListAlerts returns every alert scoped to server_id (empty = all).
func ListAlerts(serverID string) ([]StoredAlert, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredAlert
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketAlerts)).ForEach(func(_, v []byte) error {
			var a StoredAlert
			if uerr := json.Unmarshal(v, &a); uerr != nil {
				return nil
			}
			if serverID != "" && a.ServerID != serverID {
				return nil
			}
			out = append(out, a)
			return nil
		})
	})
	return out, err
}

// PutAlertEvent appends a fire-history row.
func PutAlertEvent(e StoredAlertEvent) error {
	if e.ID == "" || e.AlertID == "" {
		return fmt.Errorf("alert event: id and alert_id required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	if e.FiredAt.IsZero() {
		e.FiredAt = time.Now().UTC()
	}
	data, merr := json.Marshal(&e)
	if merr != nil {
		return merr
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketAlertEvents)).Put([]byte(e.ID), data)
	})
}

// ListAlertEvents returns the most recent N events for one alert,
// ordered by FiredAt DESC.
func ListAlertEvents(alertID string, limit int) ([]StoredAlertEvent, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	if limit <= 0 {
		limit = 25
	}
	var out []StoredAlertEvent
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketAlertEvents)).ForEach(func(_, v []byte) error {
			var e StoredAlertEvent
			if uerr := json.Unmarshal(v, &e); uerr != nil {
				return nil
			}
			if e.AlertID != alertID {
				return nil
			}
			out = append(out, e)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FiredAt.After(out[j].FiredAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- GDPR audit trail (stage 4.9 uses this) -----------------------

// StoredForgetAudit records a GDPR visitor-forget action for
// compliance audit. Written by pkg/api/v1/analytics.ForgetVisitor.
type StoredForgetAudit struct {
	VisitorID string    `json:"visitor_id"`
	ServerID  string    `json:"server_id"`
	AdminUser string    `json:"admin_user"`
	At        time.Time `json:"at"`
	// RowsDeleted is best-effort; ClickHouse DELETE is async so the
	// number may be "mutation scheduled, N rows at start of run".
	RowsDeleted int64 `json:"rows_deleted"`
}

// PutForgetAudit appends to the audit bucket. Key is
// "<visitor_id>|<RFC3339Nano timestamp>" so callers can re-create
// a scrollable timeline without juggling uuids.
func PutForgetAudit(a StoredForgetAudit) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	data, merr := json.Marshal(&a)
	if merr != nil {
		return merr
	}
	key := a.VisitorID + "|" + a.At.UTC().Format(time.RFC3339Nano)
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketAuditForget)).Put([]byte(key), data)
	})
}
