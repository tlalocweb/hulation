package bolt

// ServerAccess — per-user, per-server role grants. Backs the
// GrantServerAccess / RevokeServerAccess / ListServerAccess RPCs
// and feeds pkg/server/authware/access for non-admin callers.
//
// Storage shape: key = "server_access/<userID>|<serverID>"; value =
// role name ("viewer" | "manager"). One row per (user, server) pair;
// re-granting overwrites.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// ServerAccessEntry is one (user, server, role) triple.
type ServerAccessEntry struct {
	UserID   string
	ServerID string
	Role     string
}

func accessKey(userID, serverID string) string {
	return "server_access/" + userID + "|" + serverID
}

func splitAccessSubKey(subKey string) (userID, serverID string, ok bool) {
	parts := strings.SplitN(subKey, "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// GrantServerAccess upserts a (user, server, role) row.
func GrantServerAccess(ctx context.Context, s storage.Storage, userID, serverID, role string) error {
	if s == nil {
		return ErrNotOpen
	}
	if userID == "" || serverID == "" || role == "" {
		return fmt.Errorf("grant: userID, serverID, role required")
	}
	return s.Put(ctx, accessKey(userID, serverID), []byte(role))
}

// RevokeServerAccess removes a (user, server) row. Missing rows are
// not an error.
func RevokeServerAccess(ctx context.Context, s storage.Storage, userID, serverID string) error {
	if s == nil {
		return ErrNotOpen
	}
	if userID == "" || serverID == "" {
		return fmt.Errorf("revoke: userID and serverID required")
	}
	return s.Delete(ctx, accessKey(userID, serverID))
}

// ListServerAccess returns every row. Callers filter in-memory; the
// rowcount is expected to be low (O(users × servers)) so a full
// scan is cheaper than maintaining secondary indexes.
//
// Optional user / server filters — empty string matches all.
func ListServerAccess(ctx context.Context, s storage.Storage, userIDFilter, serverIDFilter string) ([]ServerAccessEntry, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	rows, err := s.List(ctx, "server_access/")
	if err != nil {
		return nil, err
	}
	out := make([]ServerAccessEntry, 0, len(rows))
	for fullKey, v := range rows {
		// Strip the "server_access/" prefix to get the subKey.
		subKey := strings.TrimPrefix(fullKey, "server_access/")
		userID, serverID, ok := splitAccessSubKey(subKey)
		if !ok {
			continue
		}
		if userIDFilter != "" && userIDFilter != userID {
			continue
		}
		if serverIDFilter != "" && serverIDFilter != serverID {
			continue
		}
		out = append(out, ServerAccessEntry{
			UserID:   userID,
			ServerID: serverID,
			Role:     string(v),
		})
	}
	return out, nil
}

// AllowedServerIDsForUser returns the set of server IDs the user has
// any role on. Used by the analytics ACL hook for non-admin callers.
func AllowedServerIDsForUser(ctx context.Context, s storage.Storage, userID string) ([]string, error) {
	if s == nil {
		return nil, ErrNotOpen
	}
	if userID == "" {
		return nil, nil
	}
	rows, err := ListServerAccess(ctx, s, userID, "")
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
func RoleOnServer(ctx context.Context, s storage.Storage, userID, serverID string) (string, error) {
	if s == nil {
		return "", ErrNotOpen
	}
	if userID == "" || serverID == "" {
		return "", nil
	}
	v, err := s.Get(ctx, accessKey(userID, serverID))
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(v), nil
}
