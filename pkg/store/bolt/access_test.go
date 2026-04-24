package bolt

import (
	"path/filepath"
	"testing"
)

// openTestDB opens a fresh Bolt in a t.TempDir and registers a
// cleanup that closes it. Uses direct package state so the Grant/
// Revoke/List helpers (which read from the package-global handle)
// exercise the real code path.
func openTestDB(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.bolt")
	// Reset the package global so a previous test's handle doesn't
	// leak into this one.
	_ = Close()
	if _, err := Open(path); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = Close() })
}

func TestGrantListRevoke(t *testing.T) {
	openTestDB(t)

	if err := GrantServerAccess("u1", "site-a", "viewer"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := GrantServerAccess("u1", "site-b", "manager"); err != nil {
		t.Fatalf("grant b: %v", err)
	}
	if err := GrantServerAccess("u2", "site-a", "viewer"); err != nil {
		t.Fatalf("grant u2: %v", err)
	}

	// AllowedServerIDsForUser — user u1 has access to site-a + site-b.
	ids, err := AllowedServerIDsForUser("u1")
	if err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 ids for u1, got %d: %v", len(ids), ids)
	}

	// RoleOnServer — exact role read.
	role, err := RoleOnServer("u1", "site-b")
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if role != "manager" {
		t.Fatalf("want manager, got %q", role)
	}

	// Re-grant overwrites.
	if err := GrantServerAccess("u1", "site-a", "manager"); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	role, _ = RoleOnServer("u1", "site-a")
	if role != "manager" {
		t.Fatalf("re-grant didn't overwrite, got %q", role)
	}

	// ListServerAccess with user filter.
	rows, err := ListServerAccess("u1", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 u1 rows, got %d", len(rows))
	}

	// Revoke is idempotent: second call doesn't error even though the
	// row is already gone.
	if err := RevokeServerAccess("u1", "site-a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := RevokeServerAccess("u1", "site-a"); err != nil {
		t.Fatalf("revoke idempotent: %v", err)
	}
	role, _ = RoleOnServer("u1", "site-a")
	if role != "" {
		t.Fatalf("revoke didn't delete, got %q", role)
	}
}

func TestNotOpen(t *testing.T) {
	_ = Close()
	if err := GrantServerAccess("u", "s", "viewer"); err != ErrNotOpen {
		t.Fatalf("want ErrNotOpen, got %v", err)
	}
}
