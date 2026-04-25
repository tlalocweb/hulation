package bolt

// OPAQUE password-file persistence — one row per (provider, username)
// pair. The blob is the bytemare/opaque RegistrationRecord that the
// server's LoginInit needs to construct an AKE response.
//
// Layout: bucket `opaque_records`, key = "<provider>|<username>",
// value = JSON-encoded StoredOpaqueRecord. The Envelope []byte
// carries the OPAQUE wire-format bytes (~192 bytes for the default
// suite); the rest is metadata.

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// SuiteIDDefault names the cipher suite this row was registered
// under. If we ever rotate suites, this lets us refuse a record
// whose suite no longer matches the running server.
const SuiteIDDefault = "ristretto255-sha512-argon2id-v1"

// StoredOpaqueRecord is the per-user OPAQUE password file.
type StoredOpaqueRecord struct {
	Username         string    `json:"username"`
	Provider         string    `json:"provider"` // "admin" | "internal"
	SuiteID          string    `json:"suite_id"`
	Envelope         []byte    `json:"envelope"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	LastSuccessLogin time.Time `json:"last_success_login,omitempty"`
}

func opaqueKey(provider, username string) []byte {
	return []byte(provider + "|" + username)
}

// PutOpaqueRecord upserts. Preserves CreatedAt; refreshes UpdatedAt.
func PutOpaqueRecord(rec StoredOpaqueRecord) (StoredOpaqueRecord, error) {
	if rec.Username == "" || rec.Provider == "" {
		return rec, fmt.Errorf("opaque record: username and provider required")
	}
	if len(rec.Envelope) == 0 {
		return rec, fmt.Errorf("opaque record: envelope required")
	}
	if rec.SuiteID == "" {
		rec.SuiteID = SuiteIDDefault
	}
	db := Get()
	if db == nil {
		return rec, ErrNotOpen
	}
	now := time.Now().UTC()
	err := db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketOpaqueRecords))
		key := opaqueKey(rec.Provider, rec.Username)
		if existing := b.Get(key); existing != nil {
			var prev StoredOpaqueRecord
			if uerr := json.Unmarshal(existing, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				rec.CreatedAt = prev.CreatedAt
			}
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = now
		}
		rec.UpdatedAt = now
		data, merr := json.Marshal(&rec)
		if merr != nil {
			return merr
		}
		return b.Put(key, data)
	})
	return rec, err
}

// GetOpaqueRecord loads the record. Returns nil when missing.
func GetOpaqueRecord(provider, username string) (*StoredOpaqueRecord, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out *StoredOpaqueRecord
	err := db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(BucketOpaqueRecords)).Get(opaqueKey(provider, username))
		if v == nil {
			return nil
		}
		var rec StoredOpaqueRecord
		if uerr := json.Unmarshal(v, &rec); uerr != nil {
			return uerr
		}
		out = &rec
		return nil
	})
	return out, err
}

// DeleteOpaqueRecord removes the record. Idempotent. Used for the
// rollback path (operator wants to revert to legacy auth).
func DeleteOpaqueRecord(provider, username string) error {
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(BucketOpaqueRecords)).Delete(opaqueKey(provider, username))
	})
}

// MarkOpaqueLoginSuccess updates LastSuccessLogin on a successful
// OPAQUE login. Best-effort — failures are not fatal to the login
// flow itself.
func MarkOpaqueLoginSuccess(provider, username string) error {
	rec, err := GetOpaqueRecord(provider, username)
	if err != nil || rec == nil {
		return err
	}
	rec.LastSuccessLogin = time.Now().UTC()
	_, err = PutOpaqueRecord(*rec)
	return err
}
