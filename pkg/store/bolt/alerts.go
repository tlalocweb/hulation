package bolt

// Alerts — threshold-alert rule persistence + fire-history. Matches
// the shape pattern of goals.go and reports.go: one JSON-encoded
// struct per bucket row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// StoredAlert is the server-side shape. Deliberately a plain struct
// so pkg/store/bolt stays free of generated-proto imports.
type StoredAlert struct {
	ID            string    `json:"id"`
	ServerID      string    `json:"server_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Kind          string    `json:"kind"` // "goal_count_above" | "page_traffic_delta" | "form_submission_rate" | "bad_actor_rate" | "build_failed"
	Threshold     float64   `json:"threshold"`
	WindowMinutes int32     `json:"window_minutes"`
	TargetGoalID  string    `json:"target_goal_id,omitempty"`
	TargetPath    string    `json:"target_path,omitempty"`
	TargetFormID  string    `json:"target_form_id,omitempty"`
	Recipients    []string  `json:"recipients"`
	CooldownMins  int32     `json:"cooldown_minutes"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastFiredAt   time.Time `json:"last_fired_at"`
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

func alertKey(id string) string                { return "alerts/" + id }
func alertEventKey(id string) string           { return "alert_events/" + id }
func auditForgetKey(visitorID, ts string) string {
	return "audit_forget/" + visitorID + "|" + ts
}

// PutAlert upserts. Preserves CreatedAt on update by reading the
// existing row first and copying the field forward.
func PutAlert(ctx context.Context, s storage.Storage, a StoredAlert) (StoredAlert, error) {
	if s == nil {
		return a, ErrNotOpen
	}
	if a.ID == "" || a.ServerID == "" {
		return a, fmt.Errorf("alert: id and server_id required")
	}
	now := time.Now().UTC()

	err := s.Mutate(ctx, alertKey(a.ID), func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			var prev StoredAlert
			if uerr := json.Unmarshal(current, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				a.CreatedAt = prev.CreatedAt
			}
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		a.UpdatedAt = now
		return json.Marshal(&a)
	})
	return a, err
}

// GetAlert loads one alert. Returns nil when not found.
func GetAlert(ctx context.Context, s storage.Storage, alertID string) (*StoredAlert, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, alertKey(alertID))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a StoredAlert
	if uerr := json.Unmarshal(v, &a); uerr != nil {
		return nil, uerr
	}
	return &a, nil
}

// DeleteAlert removes the alert. Idempotent. Does not remove the
// associated AlertEvent rows — those stay as historical record.
func DeleteAlert(ctx context.Context, s storage.Storage, alertID string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, alertKey(alertID))
}

// ListAlerts returns every alert scoped to server_id (empty = all).
func ListAlerts(ctx context.Context, s storage.Storage, serverID string) ([]StoredAlert, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "alerts/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredAlert, 0, len(rows))
	for _, v := range rows {
		var a StoredAlert
		if uerr := json.Unmarshal(v, &a); uerr != nil {
			continue
		}
		if serverID != "" && a.ServerID != serverID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// PutAlertEvent appends a fire-history row.
func PutAlertEvent(ctx context.Context, s storage.Storage, e StoredAlertEvent) error {
	if s == nil {
		return ErrNotOpen
	}
	if e.ID == "" || e.AlertID == "" {
		return fmt.Errorf("alert event: id and alert_id required")
	}
	if e.FiredAt.IsZero() {
		e.FiredAt = time.Now().UTC()
	}
	data, merr := json.Marshal(&e)
	if merr != nil {
		return merr
	}
	return s.Put(ctx, alertEventKey(e.ID), data)
}

// ListAlertEvents returns the most recent N events for one alert,
// ordered by FiredAt DESC.
func ListAlertEvents(ctx context.Context, s storage.Storage, alertID string, limit int) ([]StoredAlertEvent, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.List(ctx, "alert_events/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredAlertEvent, 0, len(rows))
	for _, v := range rows {
		var e StoredAlertEvent
		if uerr := json.Unmarshal(v, &e); uerr != nil {
			continue
		}
		if e.AlertID != alertID {
			continue
		}
		out = append(out, e)
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
func PutForgetAudit(ctx context.Context, s storage.Storage, a StoredForgetAudit) error {
	if s == nil {
		return ErrNotOpen
	}
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	data, merr := json.Marshal(&a)
	if merr != nil {
		return merr
	}
	return s.Put(ctx, auditForgetKey(a.VisitorID, a.At.UTC().Format(time.RFC3339Nano)), data)
}
