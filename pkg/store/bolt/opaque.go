package bolt

// OPAQUE password-file persistence — one row per (provider, username)
// pair. The blob is the bytemare/opaque RegistrationRecord that the
// server's LoginInit needs to construct an AKE response.
//
// Layout: bucket `opaque_records`, key = "<provider>|<username>",
// value = JSON-encoded StoredOpaqueRecord.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
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

func opaqueKey(provider, username string) string {
	return "opaque_records/" + provider + "|" + username
}

// PutOpaqueRecord upserts. Preserves CreatedAt; refreshes UpdatedAt.
func PutOpaqueRecord(ctx context.Context, s storage.Storage, rec StoredOpaqueRecord) (StoredOpaqueRecord, error) {
	if s == nil {
		return rec, ErrNotOpen
	}
	if rec.Username == "" || rec.Provider == "" {
		return rec, fmt.Errorf("opaque record: username and provider required")
	}
	if len(rec.Envelope) == 0 {
		return rec, fmt.Errorf("opaque record: envelope required")
	}
	if rec.SuiteID == "" {
		rec.SuiteID = SuiteIDDefault
	}
	now := time.Now().UTC()
	err := s.Mutate(ctx, opaqueKey(rec.Provider, rec.Username), func(current []byte) ([]byte, error) {
		if len(current) > 0 {
			var prev StoredOpaqueRecord
			if uerr := json.Unmarshal(current, &prev); uerr == nil && !prev.CreatedAt.IsZero() {
				rec.CreatedAt = prev.CreatedAt
			}
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = now
		}
		rec.UpdatedAt = now
		return json.Marshal(&rec)
	})
	return rec, err
}

// GetOpaqueRecord loads the record. Returns nil when missing.
func GetOpaqueRecord(ctx context.Context, s storage.Storage, provider, username string) (*StoredOpaqueRecord, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	v, err := s.Get(ctx, opaqueKey(provider, username))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec StoredOpaqueRecord
	if uerr := json.Unmarshal(v, &rec); uerr != nil {
		return nil, uerr
	}
	return &rec, nil
}

// DeleteOpaqueRecord removes the record. Idempotent.
func DeleteOpaqueRecord(ctx context.Context, s storage.Storage, provider, username string) error {
	if s == nil {
		return ErrNotOpen
	}
	return s.Delete(ctx, opaqueKey(provider, username))
}

// MarkOpaqueLoginSuccess updates LastSuccessLogin on a successful
// OPAQUE login. Best-effort — failures are not fatal to the login
// flow itself.
func MarkOpaqueLoginSuccess(ctx context.Context, s storage.Storage, provider, username string) error {
	if s == nil {
		return ErrNotOpen
	}
	rec, err := GetOpaqueRecord(ctx, s, provider, username)
	if err != nil || rec == nil {
		return err
	}
	rec.LastSuccessLogin = time.Now().UTC()
	_, err = PutOpaqueRecord(ctx, s, *rec)
	return err
}
