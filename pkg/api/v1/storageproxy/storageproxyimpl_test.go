package storageproxy

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
)

type fakeApplier struct {
	leader     bool
	leaderAddr string
	applied    uint64
	applyErr   error
	gotPayload []byte
}

func (f *fakeApplier) IsLeader() bool                 { return f.leader }
func (f *fakeApplier) LeaderInfo() (string, string)   { return "leader-id", f.leaderAddr }
func (f *fakeApplier) ApplyEncodedAsLeader(_ context.Context, b []byte) (uint64, error) {
	f.gotPayload = b
	return f.applied, f.applyErr
}

func TestApply_RejectsEmpty(t *testing.T) {
	svc := New(&fakeApplier{leader: true})
	_, err := svc.Apply(context.Background(), &internalspec.ApplyRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("got %v, want InvalidArgument", err)
	}
}

func TestApply_NotLeader_ReturnsHint(t *testing.T) {
	svc := New(&fakeApplier{leader: false, leaderAddr: "east.example.com:443"})
	_, err := svc.Apply(context.Background(), &internalspec.ApplyRequest{Command: []byte{1, 2}})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
	if !contains(err.Error(), "east.example.com:443") {
		t.Errorf("error %q does not name leader", err)
	}
}

func TestApply_NoLeader_Unavailable(t *testing.T) {
	svc := New(&fakeApplier{leader: false, leaderAddr: ""})
	_, err := svc.Apply(context.Background(), &internalspec.ApplyRequest{Command: []byte{1}})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("got %v, want Unavailable", err)
	}
}

func TestApply_HappyPath(t *testing.T) {
	a := &fakeApplier{leader: true, applied: 42}
	svc := New(a)
	resp, err := svc.Apply(context.Background(), &internalspec.ApplyRequest{Command: []byte("payload")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if resp.GetAppliedIndex() != 42 {
		t.Errorf("AppliedIndex got=%d want=42", resp.GetAppliedIndex())
	}
	if string(a.gotPayload) != "payload" {
		t.Errorf("payload not forwarded: %q", a.gotPayload)
	}
}

func TestApply_LeaderApplyError(t *testing.T) {
	a := &fakeApplier{leader: true, applyErr: errors.New("apply failed")}
	svc := New(a)
	_, err := svc.Apply(context.Background(), &internalspec.ApplyRequest{Command: []byte("p")})
	if status.Code(err) != codes.Internal {
		t.Errorf("got %v, want Internal", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
