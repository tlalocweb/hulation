package bolt

// ServerAccess — per-user, per-server role grants. Backs the
// GrantServerAccess / RevokeServerAccess / ListServerAccess RPCs
// and feeds pkg/server/authware/access for non-admin callers.
//
// Storage shape: key = "<userID>|<serverID>"; value = role name
// ("viewer" | "manager"). One row per (user, server) pair; re-
// granting overwrites.

import (
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// ServerAccessEntry is one (user, server, role) triple.
type ServerAccessEntry struct {
	UserID   string
	ServerID string
	Role     string
}

func accessKey(userID, serverID string) []byte {
	return []byte(userID + "|" + serverID)
}

func splitAccessKey(key []byte) (userID, serverID string, ok bool) {
	parts := strings.SplitN(string(key), "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// GrantServerAccess upserts a (user, server, role) row.
func GrantServerAccess(userID, serverID, role string) error {
	if userID == "" || serverID == "" || role == "" {
		return fmt.Errorf("grant: userID, serverID, role required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketServerAccess))
		return b.Put(accessKey(userID, serverID), []byte(role))
	})
}

// RevokeServerAccess removes a (user, server) row. Missing rows are
// not an error.
func RevokeServerAccess(userID, serverID string) error {
	if userID == "" || serverID == "" {
		return fmt.Errorf("revoke: userID and serverID required")
	}
	db := Get()
	if db == nil {
		return ErrNotOpen
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketServerAccess))
		return b.Delete(accessKey(userID, serverID))
	})
}

// ListServerAccess returns every row. Callers filter in-memory; the
// rowcount is expected to be low (O(users × servers)) so a full
// scan is cheaper than maintaining secondary indexes.
//
// Optional user / server filters — empty string matches all.
func ListServerAccess(userIDFilter, serverIDFilter string) ([]ServerAccessEntry, error) {
	db := Get()
	if db == nil {
		return nil, ErrNotOpen
	}
	var out []ServerAccessEntry
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketServerAccess))
		return b.ForEach(func(k, v []byte) error {
			userID, serverID, ok := splitAccessKey(k)
			if !ok {
				return nil
			}
			if userIDFilter != "" && userIDFilter != userID {
				return nil
			}
			if serverIDFilter != "" && serverIDFilter != serverID {
				return nil
			}
			out = append(out, ServerAccessEntry{
				UserID:   userID,
				ServerID: serverID,
				Role:     string(v),
			})
			return nil
		})
	})
	return out, err
}

// AllowedServerIDsForUser returns the set of server IDs the user has
// any role on. Used by the analytics ACL hook for non-admin callers.
func AllowedServerIDsForUser(userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	rows, err := ListServerAccess(userID, "")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ServerID)
	}
	return out, nil
}

// RoleOnServer returns the user's role on a single server, or empty
// when the user has no grant.
func RoleOnServer(userID, serverID string) (string, error) {
	if userID == "" || serverID == "" {
		return "", nil
	}
	db := Get()
	if db == nil {
		return "", ErrNotOpen
	}
	var role string
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BucketServerAccess))
		v := b.Get(accessKey(userID, serverID))
		if v != nil {
			role = string(v)
		}
		return nil
	})
	return role, err
}
