package bolt_test

import (
	"context"
	"path/filepath"
	"testing"

	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
)

// newTestStorage spins up a fresh LocalStorage backed by a temp
// bbolt file. Returns the Storage handle plus a Cleanup that
// closes it at test end.
func newTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	s, err := local.Open(local.Options{Path: filepath.Join(t.TempDir(), "test.bolt")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestGrantListRevoke(t *testing.T) {
	ctx := context.Background()
	s := newTestStorage(t)

	if err := hulabolt.GrantServerAccess(ctx, s, "u1", "site-a", "viewer"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := hulabolt.GrantServerAccess(ctx, s, "u1", "site-b", "manager"); err != nil {
		t.Fatalf("grant b: %v", err)
	}
	if err := hulabolt.GrantServerAccess(ctx, s, "u2", "site-a", "viewer"); err != nil {
		t.Fatalf("grant u2: %v", err)
	}

	// AllowedServerIDsForUser — user u1 has access to site-a + site-b.
	ids, err := hulabolt.AllowedServerIDsForUser(ctx, s, "u1")
	if err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 ids for u1, got %d: %v", len(ids), ids)
	}

	// RoleOnServer — exact role read.
	role, err := hulabolt.RoleOnServer(ctx, s, "u1", "site-b")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if role != "manager" {
		t.Fatalf("want manager, got %q", role)
	}

	// Re-grant overwrites.
	if err := hulabolt.GrantServerAccess(ctx, s, "u1", "site-a", "manager"); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	role, _ = hulabolt.RoleOnServer(ctx, s, "u1", "site-a")
	if role != "manager" {
		t.Fatalf("re-grant didn't overwrite, got %q", role)
	}

	// ListServerAccess with user filter.
	rows, err := hulabolt.ListServerAccess(ctx, s, "u1", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 u1 rows, got %d", len(rows))
	}

	// Revoke is idempotent: second call doesn't error even though the
	// row is already gone.
	if err := hulabolt.RevokeServerAccess(ctx, s, "u1", "site-a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := hulabolt.RevokeServerAccess(ctx, s, "u1", "site-a"); err != nil {
		t.Fatalf("revoke idempotent: %v", err)
	}
	role, _ = hulabolt.RoleOnServer(ctx, s, "u1", "site-a")
	if role != "" {
		t.Fatalf("revoke didn't delete, got %q", role)
	}
}
