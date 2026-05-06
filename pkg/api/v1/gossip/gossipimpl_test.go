package gossip

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	gossippkg "github.com/tlalocweb/hulation/pkg/gossip"
)

func openTestStore(t *testing.T, nodeID string) *gossippkg.Store {
	t.Helper()
	s, err := gossippkg.OpenStore(filepath.Join(t.TempDir(), "crdt.db"), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPush_AppliesDeltas(t *testing.T) {
	store := openTestStore(t, "local")
	svc := New(store)

	rec := gossippkg.LWWRecord{
		HLC:     gossippkg.HLC{WallMs: 1000, Counter: 0, NodeID: "remote"},
		Payload: []byte("hello"),
	}
	enc, err := gossippkg.EncodeLWW(rec)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Push(context.Background(), &internalspec.PushRequest{
		SourceNodeId: "remote",
		Deltas: []*internalspec.Delta{{
			Bucket:  gossippkg.BucketVisitors,
			Key:     "srv/v1",
			Payload: enc,
			Hlc:     &internalspec.HLC{WallMs: 1000, Counter: 0, NodeId: "remote"},
		}},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if resp.GetAccepted() != 1 {
		t.Errorf("Accepted got %d, want 1", resp.GetAccepted())
	}
	got, err := store.GetLWW(context.Background(), gossippkg.BucketVisitors, "srv/v1")
	if err != nil {
		t.Fatalf("GetLWW: %v", err)
	}
	if string(got.Payload) != "hello" {
		t.Errorf("payload mismatch: %q", got.Payload)
	}
}

func TestPush_NilRequestRejected(t *testing.T) {
	svc := New(openTestStore(t, "x"))
	if _, err := svc.Push(context.Background(), nil); status.Code(err) != codes.InvalidArgument {
		t.Errorf("got %v, want InvalidArgument", err)
	}
}

func TestPush_NoBackendRejected(t *testing.T) {
	svc := New(nil)
	_, err := svc.Push(context.Background(), &internalspec.PushRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
}

func TestSyncDigest_ReturnsMissingDeltas(t *testing.T) {
	store := openTestStore(t, "local")
	ctx := context.Background()
	if _, err := store.PutLWW(ctx, gossippkg.BucketVisitors, "k1", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutLWW(ctx, gossippkg.BucketVisitors, "k2", []byte("v2")); err != nil {
		t.Fatal(err)
	}

	svc := New(store)
	resp, err := svc.SyncDigest(ctx, &internalspec.SyncDigestRequest{
		SourceNodeId: "remote",
		Digests: []*internalspec.BucketDigest{{
			Bucket:    gossippkg.BucketVisitors,
			HighWater: &internalspec.HLC{}, // remote knows nothing
		}},
	})
	if err != nil {
		t.Fatalf("SyncDigest: %v", err)
	}
	if len(resp.GetDeltas()) != 2 {
		t.Errorf("returned %d deltas, want 2", len(resp.GetDeltas()))
	}
	if resp.GetRemoteHlc().GetNodeId() != "local" {
		t.Errorf("remote_hlc.node_id got %q, want local", resp.GetRemoteHlc().GetNodeId())
	}
}

// failingBackend lets us assert error mapping.
type failingBackend struct {
	gossippkg.Store
}

func (f *failingBackend) ApplyDelta(_ context.Context, _ gossippkg.Delta) (bool, gossippkg.HLC, error) {
	return false, gossippkg.HLC{}, errors.New("simulated apply failure")
}
func (f *failingBackend) SyncFromDigest(_ context.Context, _ map[string]gossippkg.HLC, _ int) ([]gossippkg.Delta, error) {
	return nil, errors.New("simulated sync failure")
}
func (f *failingBackend) Clock() *gossippkg.Clock { return gossippkg.NewClock("local") }

func TestSyncDigest_BackendError_Internal(t *testing.T) {
	svc := New(&failingBackend{})
	_, err := svc.SyncDigest(context.Background(), &internalspec.SyncDigestRequest{})
	if status.Code(err) != codes.Internal {
		t.Errorf("got %v, want Internal", err)
	}
}

func TestPush_PartialApplyContinues(t *testing.T) {
	store := openTestStore(t, "local")
	svc := New(store)

	rec := gossippkg.LWWRecord{HLC: gossippkg.HLC{WallMs: 1000, Counter: 0, NodeID: "remote"}, Payload: []byte("ok")}
	enc, _ := gossippkg.EncodeLWW(rec)
	resp, err := svc.Push(context.Background(), &internalspec.PushRequest{
		Deltas: []*internalspec.Delta{
			// First delta: bad bucket → skipped silently
			{Bucket: "garbage", Key: "x", Payload: enc, Hlc: &internalspec.HLC{WallMs: 1, NodeId: "remote"}},
			// Second delta: valid → applied
			{Bucket: gossippkg.BucketVisitors, Key: "k", Payload: enc, Hlc: &internalspec.HLC{WallMs: 1000, NodeId: "remote"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAccepted() != 1 {
		t.Errorf("Accepted=%d, want 1", resp.GetAccepted())
	}
}
