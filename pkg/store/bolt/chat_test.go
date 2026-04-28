package bolt_test

import (
	"context"
	"reflect"
	"testing"

	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
)

// TestChatRosterCRUD covers the Put/Get/Delete contract for the
// chat_acl bucket. Empty server id rejected, dedup+sort applied on
// write, GetChatRoster on a missing entry returns a zero-value
// roster (not an error).
func TestChatRosterCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStorage(t)

	// Empty server_id rejected on both writes and reads.
	if _, err := hulabolt.PutChatRoster(ctx, s, hulabolt.StoredChatRoster{}); err == nil {
		t.Fatal("PutChatRoster: want error on empty server_id")
	}
	if _, err := hulabolt.GetChatRoster(ctx, s, ""); err == nil {
		t.Fatal("GetChatRoster: want error on empty server_id")
	}

	// Missing entry → zero-value roster, no error.
	got, err := hulabolt.GetChatRoster(ctx, s, "missing")
	if err != nil {
		t.Fatalf("GetChatRoster missing: %v", err)
	}
	if got.ServerID != "missing" || len(got.Recipients) != 0 {
		t.Fatalf("want zero roster, got %+v", got)
	}

	// Put with duplicates + unsorted input → stored deduped + sorted.
	in := hulabolt.StoredChatRoster{
		ServerID:   "site-a",
		Recipients: []string{"bob@x", "alice@x", "bob@x", ""},
	}
	saved, err := hulabolt.PutChatRoster(ctx, s, in)
	if err != nil {
		t.Fatalf("PutChatRoster: %v", err)
	}
	wantRecipients := []string{"alice@x", "bob@x"}
	if !reflect.DeepEqual(saved.Recipients, wantRecipients) {
		t.Fatalf("recipients: want %v, got %v", wantRecipients, saved.Recipients)
	}
	if saved.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set on Put")
	}

	// Round-trip through Get.
	got, err = hulabolt.GetChatRoster(ctx, s, "site-a")
	if err != nil {
		t.Fatalf("GetChatRoster: %v", err)
	}
	if !reflect.DeepEqual(got.Recipients, wantRecipients) {
		t.Fatalf("recipients (read): want %v, got %v", wantRecipients, got.Recipients)
	}

	// Empty Recipients on update clears the list.
	if _, err := hulabolt.PutChatRoster(ctx, s, hulabolt.StoredChatRoster{ServerID: "site-a"}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = hulabolt.GetChatRoster(ctx, s, "site-a")
	if len(got.Recipients) != 0 {
		t.Fatalf("want empty after clear, got %v", got.Recipients)
	}

	// Delete is idempotent.
	if err := hulabolt.DeleteChatRoster(ctx, s, "site-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := hulabolt.DeleteChatRoster(ctx, s, "site-a"); err != nil {
		t.Fatalf("delete idempotent: %v", err)
	}
}
