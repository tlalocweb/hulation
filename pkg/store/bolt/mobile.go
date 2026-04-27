package bolt

// Mobile-devices + notification-prefs + notification-send-log
// persistence. All three live alongside goals/reports/alerts — one
// JSON-encoded struct per bucket row.

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ---- Devices ----------------------------------------------------

// StoredDevice is the server-side shape of an APNs / FCM device
// registration. TokenCipher holds the AES-GCM sealed push token
// (see pkg/mobile/tokenbox). Nonce is appended to the ciphertext
// by the sealer; this struct never sees plaintext.
type StoredDevice struct {
	ID                 string    `json:"id"`
	UserID             string    `json:"user_id"`
	Platform           string    `json:"platform"` // "apns" | "fcm"
	DeviceFingerprint  string    `json:"device_fingerprint"`
	Label              string    `json:"label,omitempty"`
	TokenCipher        []byte    `json:"token_cipher"` // sealed push token
	RegisteredAt       time.Time `json:"registered_at"`
	LastSeenAt         time.Time `json:"last_seen_at"`
	Active             bool      `json:"active"`
}

// PutDevice upserts. Idempotent by (user_id, device_fingerprint)
// when the caller re-uses an existing ID; otherwise a new row.
// Returns the persisted device.
func PutDevice(d StoredDevice) (StoredDevice, error) {
	if d.ID == "" || d.UserID == "" {
		return d, fmt.Errorf("device: id and user_id required")
	}
	db := Get()
	if db == nil {
		return d, ErrNotOpen
	}
	now := time.Now().UTC()
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketMobileDevices))
		if existing := b.Get([]byte(d.ID)); existing != nil {
			var prev StoredDevice
			if uerr := json.Unmarshal(existing, &prev); uerr == nil && !prev.RegisteredAt.IsZero() {
				d.RegisteredAt = prev.RegisteredAt
			}
		}
		if d.RegisteredAt.IsZero() {
			d.RegisteredAt = now
		}
		d.LastSeenAt = now
		data, merr := json.Marshal(&d)
		if merr != nil {
			return merr
		}
		return b.Put([]byte(d.ID), data)
	})
	return d, err
}

// GetDevice loads a single device. Returns nil when missing.
func GetDevice(deviceID string) (*StoredDevice, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out *StoredDevice
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketMobileDevices)).Get([]byte(deviceID))
		if v == nil {
			return nil
		}
		var d StoredDevice
		if uerr := json.Unmarshal(v, &d); uerr != nil {
			return uerr
		}
		out = &d
		return nil
	})
	return out, err
}

// DeleteDevice removes the row entirely. Used by the admin
// "forget device" flow. For the dead-token path (APNs 410 / FCM
// INVALID_ARGUMENT), callers prefer `MarkDeviceInactive` so the
// audit trail survives.
func DeleteDevice(deviceID string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketMobileDevices)).Delete([]byte(deviceID))
	})
}

// MarkDeviceInactive flips Active → false. Used when the push
// transport rejects the token (dead-token sentinel). Keeps the row
// so the admin UI can still show "device last seen X, provider
// rejected token".
func MarkDeviceInactive(deviceID string) error {
	d, err := GetDevice(deviceID)
	if err != nil || d == nil {
		return err
	}
	d.Active = false
	_, err = PutDevice(*d)
	return err
}

// ListDevicesForUser returns every active + inactive device for the
// given user_id. Use `activeOnly` at the caller for filtering.
func ListDevicesForUser(userID string) ([]StoredDevice, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredDevice
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketMobileDevices)).ForEach(func(_, v []byte) error {
			var d StoredDevice
			if uerr := json.Unmarshal(v, &d); uerr != nil {
				return nil
			}
			if d.UserID != userID {
				return nil
			}
			out = append(out, d)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

// FindDeviceByFingerprint returns the device matching (user_id,
// fingerprint) or nil. Used by RegisterDevice for idempotency.
func FindDeviceByFingerprint(userID, fingerprint string) (*StoredDevice, error) {
	if userID == "" || fingerprint == "" {
		return nil, nil
	}
	devs, err := ListDevicesForUser(userID)
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
	ID           string              `json:"id"`
	EnvelopeID   string              `json:"envelope_id"` // correlates with AlertEvent.ID when the source is an alert
	UserID       string              `json:"user_id"`
	AttemptedAt  time.Time           `json:"attempted_at"`
	Channels     map[string]string   `json:"channels"` // channel → outcome ("ok" | "failed" | "dead_token" | "unconfigured")
	Errors       map[string]string   `json:"errors,omitempty"`
}

// PutNotificationSend inserts. Not updated — each attempt is a new row.
func PutNotificationSend(s StoredNotificationSend) error {
	if s.ID == "" {
		return fmt.Errorf("notification send: id required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	if s.AttemptedAt.IsZero() {
		s.AttemptedAt = time.Now().UTC()
	}
	data, merr := json.Marshal(&s)
	if merr != nil {
		return merr
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketNotificationSends)).Put([]byte(s.ID), data)
	})
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
func PutNotificationPrefs(p StoredNotificationPrefs) (StoredNotificationPrefs, error) {
	if p.UserID == "" {
		return p, fmt.Errorf("notification prefs: user_id required")
	}
	db := Get()
	if db == nil {
		return p, ErrNotOpen
	}
	p.UpdatedAt = time.Now().UTC()
	data, merr := json.Marshal(&p)
	if merr != nil {
		return p, merr
	}
	err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketNotificationPrefs)).Put([]byte(p.UserID), data)
	})
	return p, err
}

// GetNotificationPrefs returns the user's prefs, or default-prefs
// when the row doesn't exist (caller can persist the default on
// first access if desired).
func GetNotificationPrefs(userID string) (StoredNotificationPrefs, error) {
	db := Get()
	if db == nil {
		return DefaultPrefs(userID), ErrNotOpen
	}
	var out StoredNotificationPrefs
	found := false
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketNotificationPrefs)).Get([]byte(userID))
		if v == nil {
			return nil
		}
		if uerr := json.Unmarshal(v, &out); uerr != nil {
			return uerr
		}
		found = true
		return nil
	})
	if err != nil {
		return DefaultPrefs(userID), err
	}
	if !found {
		return DefaultPrefs(userID), nil
	}
	return out, nil
}

// ListNotificationPrefs returns every prefs row. Used by the admin
// UI's `/admin/notifications` table.
func ListNotificationPrefs() ([]StoredNotificationPrefs, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []StoredNotificationPrefs
	err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketNotificationPrefs)).ForEach(func(_, v []byte) error {
			var p StoredNotificationPrefs
			if uerr := json.Unmarshal(v, &p); uerr != nil {
				return nil
			}
			out = append(out, p)
			return nil
		})
	})
	return out, err
}
