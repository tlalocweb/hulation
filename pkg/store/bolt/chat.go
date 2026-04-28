package bolt

// Chat — the only Bolt-side state the Phase-4b chat subsystem holds:
// per-server agent-notification rosters. Chat live state lives in
// process memory (the Hub + Router); chat history lives in
// ClickHouse. The single Bolt bucket below stores who hula should
// email when a new chat opens on a given server.
//
// Bucket: chat_acl
//   key   = server_id
//   value = JSON-encoded StoredChatRoster
//
// Authorization for the admin chat RPCs is **not** read from this
// bucket — that flows through `server_access` like every other
// admin RPC. This bucket is a notification-target list; an admin
// can have access to chats without being on the email roster, and
// an email recipient might not have admin access at all.

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// StoredChatRoster is the per-server notification-target list.
// Plain struct so this package stays free of generated-proto
// imports (mirrors the alerts.go / goals.go / reports.go pattern).
type StoredChatRoster struct {
	ServerID  string    `json:"server_id"`
	// Recipients is the (deduped, sorted) list of email addresses
	// that get a "new chat opened" notification for ServerID.
	// Notifications themselves wire up in a later phase; the bucket
	// is here in 4b.1 so stage 4b.6 can ship the routing/notify
	// path without another schema bump.
	Recipients []string  `json:"recipients"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PutChatRoster upserts the roster for ServerID. Empty Recipients
// is allowed and clears the list for that server. Recipients are
// deduped + sorted on write.
func PutChatRoster(r StoredChatRoster) (StoredChatRoster, error) {
	if r.ServerID == "" {
		return r, fmt.Errorf("chat roster: server_id required")
	}
	db := Get()
	if db == nil {
		return r, ErrNotOpen
	}
	r.Recipients = dedupSorted(r.Recipients)
	r.UpdatedAt = time.Now().UTC()
	buf, err := json.Marshal(r)
	if err != nil {
		return r, fmt.Errorf("chat roster: marshal: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketChatACL))
		if b == nil {
			return fmt.Errorf("chat roster: bucket missing")
		}
		return b.Put([]byte(r.ServerID), buf)
	})
	return r, err
}

// GetChatRoster returns the roster for ServerID, or a zero-value
// StoredChatRoster (with the supplied ServerID and empty Recipients)
// when nothing is on file. Distinguishing "no entry" from "empty
// list" is not useful for the calling code so we collapse both.
func GetChatRoster(serverID string) (StoredChatRoster, error) {
	out := StoredChatRoster{ServerID: serverID}
	if serverID == "" {
		return out, fmt.Errorf("chat roster: server_id required")
	}
	db := Get()
	if db == nil {
		return out, ErrNotOpen
	}
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketChatACL))
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(serverID))
		if raw == nil {
			return nil
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}

// DeleteChatRoster removes the roster entry for ServerID. Idempotent.
func DeleteChatRoster(serverID string) error {
	if serverID == "" {
		return fmt.Errorf("chat roster: server_id required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketChatACL))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(serverID))
	})
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
