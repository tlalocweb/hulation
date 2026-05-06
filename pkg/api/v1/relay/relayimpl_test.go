package relay

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	"github.com/tlalocweb/hulation/model"
	relaypkg "github.com/tlalocweb/hulation/pkg/relay"
)

type fakeWriter struct {
	mu      sync.Mutex
	got     [][]byte
	err     error
	accept  uint32
	reject  uint32
}

func (f *fakeWriter) Insert(_ context.Context, encoded [][]byte) (uint32, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, 0, f.err
	}
	f.got = append(f.got, encoded...)
	a := uint32(len(encoded))
	if f.accept != 0 {
		a = f.accept
	}
	return a, f.reject, nil
}

func encodeForTest(t *testing.T, ev *model.Event) []byte {
	t.Helper()
	b, err := relaypkg.EncodeEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecordEventBatch_HappyPath(t *testing.T) {
	w := &fakeWriter{}
	svc := New(w)

	ev1 := &model.Event{HModel: model.HModel{ID: "e1"}, BelongsTo: "v1"}
	ev2 := &model.Event{HModel: model.HModel{ID: "e2"}, BelongsTo: "v2"}
	resp, err := svc.RecordEventBatch(context.Background(), &internalspec.RecordEventBatchRequest{
		Events:       [][]byte{encodeForTest(t, ev1), encodeForTest(t, ev2)},
		SourceNodeId: "node-west",
	})
	if err != nil {
		t.Fatalf("RecordEventBatch: %v", err)
	}
	if resp.GetAccepted() != 2 || resp.GetRejected() != 0 {
		t.Errorf("counts: accepted=%d rejected=%d, want 2/0", resp.GetAccepted(), resp.GetRejected())
	}
	if len(w.got) != 2 {
		t.Errorf("writer got %d encoded events, want 2", len(w.got))
	}
}

func TestRecordEventBatch_EmptyOK(t *testing.T) {
	svc := New(&fakeWriter{})
	resp, err := svc.RecordEventBatch(context.Background(), &internalspec.RecordEventBatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAccepted() != 0 || resp.GetRejected() != 0 {
		t.Errorf("empty batch should be 0/0, got %d/%d", resp.GetAccepted(), resp.GetRejected())
	}
}

func TestRecordEventBatch_NoWriter_FailedPrecondition(t *testing.T) {
	svc := New(nil)
	_, err := svc.RecordEventBatch(context.Background(), &internalspec.RecordEventBatchRequest{
		Events: [][]byte{[]byte("x")},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
}

func TestRecordEventBatch_WriterError_Internal(t *testing.T) {
	svc := New(&fakeWriter{err: errors.New("ch unreachable")})
	ev := &model.Event{HModel: model.HModel{ID: "e1"}}
	_, err := svc.RecordEventBatch(context.Background(), &internalspec.RecordEventBatchRequest{
		Events: [][]byte{encodeForTest(t, ev)},
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("got %v, want Internal", err)
	}
}
