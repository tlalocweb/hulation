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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// StoredChatRoster is the per-server notification-target list.
type StoredChatRoster struct {
	ServerID string `json:"server_id"`
	// Recipients is the (deduped, sorted) list of email addresses
	// that get a "new chat opened" notification for ServerID.
	Recipients []string  `json:"recipients"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func chatRosterKey(serverID string) string { return "chat_acl/" + serverID }

// PutChatRoster upserts the roster for ServerID. Empty Recipients
// is allowed and clears the list for that server. Recipients are
// deduped + sorted on write.
func PutChatRoster(ctx context.Context, s storage.Storage, r StoredChatRoster) (StoredChatRoster, error) {
	if r.ServerID == "" {
		return r, fmt.Errorf("chat roster: server_id required")
	}
	r.Recipients = dedupSorted(r.Recipients)
	r.UpdatedAt = time.Now().UTC()
	buf, err := json.Marshal(r)
	if err != nil {
		return r, fmt.Errorf("chat roster: marshal: %w", err)
	}
	if err := s.Put(ctx, chatRosterKey(r.ServerID), buf); err != nil {
		return r, err
	}
	return r, nil
}

// GetChatRoster returns the roster for ServerID, or a zero-value
// StoredChatRoster (with the supplied ServerID and empty Recipients)
// when nothing is on file.
func GetChatRoster(ctx context.Context, s storage.Storage, serverID string) (StoredChatRoster, error) {
	out := StoredChatRoster{ServerID: serverID}
	if serverID == "" {
		return out, fmt.Errorf("chat roster: server_id required")
	}
	v, err := s.Get(ctx, chatRosterKey(serverID))
	if errors.Is(err, storage.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if uerr := json.Unmarshal(v, &out); uerr != nil {
		return out, uerr
	}
	return out, nil
}

// DeleteChatRoster removes the roster entry for ServerID. Idempotent.
func DeleteChatRoster(ctx context.Context, s storage.Storage, serverID string) error {
	if serverID == "" {
		return fmt.Errorf("chat roster: server_id required")
	}
	return s.Delete(ctx, chatRosterKey(serverID))
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, str := range in {
		if str == "" {
			continue
		}
		if _, ok := seen[str]; ok {
			continue
		}
		seen[str] = struct{}{}
		out = append(out, str)
	}
	sort.Strings(out)
	return out
}
