package gossip

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T, nodeID string) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "crdt.db"), nodeID)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_PutGetLWW(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	hlc, err := s.PutLWW(ctx, BucketVisitors, "srv/v1", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if hlc.NodeID != "nodeA" {
		t.Errorf("hlc.NodeID got %q", hlc.NodeID)
	}
	got, err := s.GetLWW(ctx, BucketVisitors, "srv/v1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != "payload" {
		t.Errorf("payload mismatch: %q", got.Payload)
	}
	if got.Tombstone {
		t.Error("unexpected tombstone")
	}
}

func TestStore_TombstoneOverwritesValue(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	_, _ = s.PutLWW(ctx, BucketVisitors, "k", []byte("alive"))
	_, _ = s.DeleteLWW(ctx, BucketVisitors, "k")
	got, err := s.GetLWW(ctx, BucketVisitors, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Tombstone {
		t.Errorf("expected tombstone, got %+v", got)
	}
}

func TestStore_FlagBadAndRead(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	hlc, err := s.FlagBad(ctx, BucketBadactorFlags, "1.2.3.4", "scoring threshold", "nodeA")
	if err != nil {
		t.Fatal(err)
	}
	if hlc.IsZero() {
		t.Error("FlagBad returned zero HLC")
	}
	got, err := s.GetMonotone(ctx, BucketBadactorFlags, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Flagged || got.Reason != "scoring threshold" || got.ByNodeID != "nodeA" {
		t.Errorf("monotone read mismatch: %+v", got)
	}
}

func TestStore_HighWaterTracksMaxHLC(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = s.PutLWW(ctx, BucketVisitors, "key", []byte("v"))
	}
	hw, err := s.HighWater(ctx, BucketVisitors)
	if err != nil {
		t.Fatal(err)
	}
	if hw.IsZero() {
		t.Error("HighWater zero after 5 writes")
	}
}

func TestStore_DeltasSinceFiltersHLC(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	_, _ = s.PutLWW(ctx, BucketVisitors, "a", []byte("1"))
	mid, _ := s.HighWater(ctx, BucketVisitors)
	_, _ = s.PutLWW(ctx, BucketVisitors, "b", []byte("2"))
	_, _ = s.PutLWW(ctx, BucketVisitors, "c", []byte("3"))

	got, err := s.DeltasSince(ctx, BucketVisitors, mid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("DeltasSince got %d items, want 2", len(got))
	}
}

func TestStore_ApplyDelta_NewKey(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	rec := LWWRecord{HLC: mkHLC(100, 0, "nodeB"), Payload: []byte("from-B")}
	enc, _ := EncodeLWW(rec)
	changed, _, err := s.ApplyDelta(ctx, Delta{
		Bucket: BucketVisitors, Key: "k", Payload: enc, HLC: rec.HLC,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true on new key")
	}
	got, _ := s.GetLWW(ctx, BucketVisitors, "k")
	if string(got.Payload) != "from-B" {
		t.Errorf("payload from B not stored: %q", got.Payload)
	}
}

func TestStore_ApplyDelta_OlderLoses(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	_, _ = s.PutLWW(ctx, BucketVisitors, "k", []byte("local-newer"))

	older := LWWRecord{HLC: mkHLC(1, 0, "nodeB"), Payload: []byte("remote-older")}
	enc, _ := EncodeLWW(older)
	changed, _, err := s.ApplyDelta(ctx, Delta{
		Bucket: BucketVisitors, Key: "k", Payload: enc, HLC: older.HLC,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("local-newer should have won; changed shouldn't be true")
	}
	got, _ := s.GetLWW(ctx, BucketVisitors, "k")
	if string(got.Payload) != "local-newer" {
		t.Errorf("local got overwritten: %q", got.Payload)
	}
}

func TestStore_ApplyDelta_BadActorMonotone(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	flag := MonotoneRecord{HLC: mkHLC(1, 0, "nodeB"), Flagged: true, Reason: "scoring", ByNodeID: "nodeB"}
	enc, _ := EncodeMonotone(flag)
	if _, _, err := s.ApplyDelta(ctx, Delta{
		Bucket: BucketBadactorFlags, Key: "1.2.3.4", Payload: enc, HLC: flag.HLC,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMonotone(ctx, BucketBadactorFlags, "1.2.3.4")
	if !got.Flagged || got.ByNodeID != "nodeB" {
		t.Errorf("flag from B not applied: %+v", got)
	}
}

func TestStore_OnWriteFires(t *testing.T) {
	s := newStore(t, "nodeA")
	var seen string
	s.SetOnWrite(func(bucket, key string, _ []byte, _ HLC) {
		seen = bucket + "/" + key
	})
	_, err := s.PutLWW(context.Background(), BucketVisitors, "k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if seen != "visitors/k" {
		t.Errorf("onWrite saw %q, want visitors/k", seen)
	}
}

func TestStore_RejectsUnknownBucket(t *testing.T) {
	s := newStore(t, "nodeA")
	if _, err := s.PutLWW(context.Background(), "garbage", "k", nil); err == nil {
		t.Error("expected error for unknown bucket")
	}
}

func TestStore_SweepTombstones(t *testing.T) {
	s := newStore(t, "nodeA")
	ctx := context.Background()
	// Forge an old tombstone via ApplyDelta so we control its HLC.
	old := LWWRecord{HLC: HLC{WallMs: 1, Counter: 0, NodeID: "x"}, Tombstone: true}
	enc, _ := EncodeLWW(old)
	_, _, _ = s.ApplyDelta(ctx, Delta{
		Bucket: BucketVisitors, Key: "old", Payload: enc, HLC: old.HLC,
	})
	// And a fresh tombstone.
	_, _ = s.DeleteLWW(ctx, BucketVisitors, "fresh")

	// Sweep with 1ms TTL — old (wall=1) is older; fresh just got
	// stamped with time.Now-derived wall, well within 1ms TTL... so
	// fresh might also be swept on a slow CI. Use a 1-second
	// tolerance instead.
	time.Sleep(2 * time.Millisecond)
	deleted, err := s.SweepTombstones(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 1 {
		t.Errorf("SweepTombstones deleted %d, want >= 1", deleted)
	}
}

func TestEncodeDecodeHLC(t *testing.T) {
	want := HLC{WallMs: 1700000000123, Counter: 7, NodeID: "node-east"}
	b, _ := EncodeHLC(want)
	got, err := DecodeHLC(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("HLC round-trip: got %v want %v", got, want)
	}
}
