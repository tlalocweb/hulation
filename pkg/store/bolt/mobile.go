package bolt

// Mobile-devices + notification-prefs + notification-send-log
// persistence. All three live alongside goals/reports/alerts — one
// JSON-encoded struct per bucket row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// ---- Devices ----------------------------------------------------

// StoredDevice is the server-side shape of an APNs / FCM device
// registration. TokenCipher holds the AES-GCM sealed push token
// (see pkg/mobile/tokenbox). Nonce is appended to the ciphertext
// by the sealer; this struct never sees plaintext.
type StoredDevice struct {
	ID                string    `json:"id"`
	UserID            string    `json:"user_id"`
	Platform          string    `json:"platform"` // "apns" | "fcm"
	DeviceFingerprint string    `json:"device_fingerprint"`
	Label             string    `json:"label,omitempty"`
	TokenCipher       []byte    `json:"token_cipher"` // sealed push token
	RegisteredAt      time.Time `json:"registered_at"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	Active            bool      `json:"active"`
}

func deviceKey(id string) string                 { return "mobile_devices/" + id }
func notifSendKey(id string) string              { return "notification_sends/" + id }
func notifPrefsKey(userID string) string         { return "notification_prefs/" + userID }

// PutDevice upserts. Idempotent by (user_id, device_fingerprint)
// when the caller re-uses an existing ID; otherwise a new row.
func PutDevice(ctx context.Context, s storage.Storage, d StoredDevice) (StoredDevice, error) {
	if s == nil {
		return d, ErrNotOpen
	}
	if d.ID == "" || d.UserID == "" {
		return d, fmt.Errorf("device: id and user_id required")
	}
	now := time.Now().UTC()
	err := s.Mutate(ctx, deviceKey(d.ID), func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			var prev StoredDevice
			if uerr := json.Unmarshal(current, &prev); uerr == nil && !prev.RegisteredAt.IsZero() {
				d.RegisteredAt = prev.RegisteredAt
			}
		}
		if d.RegisteredAt.IsZero() {
			d.RegisteredAt = now
		}
		d.LastSeenAt = now
		return json.Marshal(&d)
	})
	return d, err
}

// GetDevice loads a single device. Returns nil when missing.
func GetDevice(ctx context.Context, s storage.Storage, deviceID string) (*StoredDevice, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, deviceKey(deviceID))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d StoredDevice
	if uerr := json.Unmarshal(v, &d); uerr != nil {
		return nil, uerr
	}
	return &d, nil
}

// DeleteDevice removes the row entirely. Used by the admin
// "forget device" flow. For the dead-token path, callers prefer
// MarkDeviceInactive so the audit trail survives.
func DeleteDevice(ctx context.Context, s storage.Storage, deviceID string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, deviceKey(deviceID))
}

// MarkDeviceInactive flips Active → false. Used when the push
// transport rejects the token (dead-token sentinel).
func MarkDeviceInactive(ctx context.Context, s storage.Storage, deviceID string) error {
	if s == nil {
		return ErrNotOpen
	}
	d, err := GetDevice(ctx, s, deviceID)
	if err != nil || d == nil {
		return err
	}
	d.Active = false
	_, err = PutDevice(ctx, s, *d)
	return err
}

// ListDevicesForUser returns every active + inactive device for the
// given user_id, most recently-seen first.
func ListDevicesForUser(ctx context.Context, s storage.Storage, userID string) ([]StoredDevice, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "mobile_devices/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredDevice, 0, len(rows))
	for _, v := range rows {
		var d StoredDevice
		if uerr := json.Unmarshal(v, &d); uerr != nil {
			continue
		}
		if d.UserID != userID {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

// FindDeviceByFingerprint returns the device matching (user_id,
// fingerprint) or nil. Used by RegisterDevice for idempotency.
func FindDeviceByFingerprint(ctx context.Context, s storage.Storage, userID, fingerprint string) (*StoredDevice, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	if userID == "" || fingerprint == "" {
		return nil, nil
	}
	devs, err := ListDevicesForUser(ctx, s, userID)
	if err != nil {
		return nil, err
	}
	for i := range devs {
		if devs[i].DeviceFingerprint == fingerprint {
			return &devs[i], nil
		}
	}
	return nil, nil
}

// ---- Notification sends ----------------------------------------

// StoredNotificationSend is the audit row for one cross-channel
// delivery attempt. One row per Envelope, summarising the fan-out.
type StoredNotificationSend struct {
	ID          string            `json:"id"`
	EnvelopeID  string            `json:"envelope_id"` // correlates with AlertEvent.ID when the source is an alert
	UserID      string            `json:"user_id"`
	AttemptedAt time.Time         `json:"attempted_at"`
	Channels    map[string]string `json:"channels"` // channel → outcome ("ok" | "failed" | "dead_token" | "unconfigured")
	Errors      map[string]string `json:"errors,omitempty"`
}

// PutNotificationSend inserts. Not updated — each attempt is a new row.
func PutNotificationSend(ctx context.Context, s storage.Storage, ns StoredNotificationSend) error {
	if s == nil {
		return ErrNotOpen
	}
	if ns.ID == "" {
		return fmt.Errorf("notification send: id required")
	}
	if ns.AttemptedAt.IsZero() {
		ns.AttemptedAt = time.Now().UTC()
	}
	data, merr := json.Marshal(&ns)
	if merr != nil {
		return merr
	}
	return s.Put(ctx, notifSendKey(ns.ID), data)
}

// ---- Notification preferences ----------------------------------

// StoredNotificationPrefs carries the per-user channel routing.
type StoredNotificationPrefs struct {
	UserID          string    `json:"user_id"`
	EmailEnabled    bool      `json:"email_enabled"`
	PushEnabled     bool      `json:"push_enabled"`
	Timezone        string    `json:"timezone,omitempty"`
	QuietHoursStart string    `json:"quiet_hours_start,omitempty"` // "HH:MM"
	QuietHoursEnd   string    `json:"quiet_hours_end,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// DefaultPrefs returns sensible defaults for a newly-seen user:
// email on, push off until a device registers, no quiet hours.
func DefaultPrefs(userID string) StoredNotificationPrefs {
	return StoredNotificationPrefs{
		UserID:       userID,
		EmailEnabled: true,
		PushEnabled:  false,
	}
}

// PutNotificationPrefs upserts keyed on user_id.
func PutNotificationPrefs(ctx context.Context, s storage.Storage, p StoredNotificationPrefs) (StoredNotificationPrefs, error) {
	if s == nil {
		return p, ErrNotOpen
	}
	if p.UserID == "" {
		return p, fmt.Errorf("notification prefs: user_id required")
	}
	p.UpdatedAt = time.Now().UTC()
	data, merr := json.Marshal(&p)
	if merr != nil {
		return p, merr
	}
	if err := s.Put(ctx, notifPrefsKey(p.UserID), data); err != nil {
		return p, err
	}
	return p, nil
}

// GetNotificationPrefs returns the user's prefs, or default-prefs
// when the row doesn't exist.
func GetNotificationPrefs(ctx context.Context, s storage.Storage, userID string) (StoredNotificationPrefs, error) {
	if s == nil {
		return DefaultPrefs(userID), ErrNotOpen
	}
	v, err := s.Get(ctx, notifPrefsKey(userID))
	if errors.Is(err, storage.ErrNotFound) {
		return DefaultPrefs(userID), nil
	}
	if err != nil {
		return DefaultPrefs(userID), err
	}
	var out StoredNotificationPrefs
	if uerr := json.Unmarshal(v, &out); uerr != nil {
		return DefaultPrefs(userID), uerr
	}
	return out, nil
}

// ListNotificationPrefs returns every prefs row. Used by the admin
// UI's `/admin/notifications` table.
func ListNotificationPrefs(ctx context.Context, s storage.Storage) ([]StoredNotificationPrefs, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "notification_prefs/")
	if err != nil {
		return nil, err
	}
	out := make([]StoredNotificationPrefs, 0, len(rows))
	for _, v := range rows {
		var p StoredNotificationPrefs
		if uerr := json.Unmarshal(v, &p); uerr != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}
