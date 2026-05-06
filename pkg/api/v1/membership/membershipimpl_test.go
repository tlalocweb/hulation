package membership

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	internalspec "github.com/tlalocweb/hulation/pkg/apispec/v1/internalapi"
	storagepkg "github.com/tlalocweb/hulation/pkg/store/storage"
	localstorage "github.com/tlalocweb/hulation/pkg/store/storage/local"
)

type fakeCluster struct {
	mu          sync.Mutex
	isLeader    bool
	leaderID    string
	leaderAddr  string
	members     []ClusterMember
	addCalls    []addCall
	removeCalls []string
	addErr      error
	removeErr   error
	lastIndex   uint64
	appliedIdx  uint64
}

type addCall struct {
	nodeID, addr string
}

func (f *fakeCluster) IsLeader() bool                  { return f.isLeader }
func (f *fakeCluster) LeaderInfo() (string, string)    { return f.leaderID, f.leaderAddr }
func (f *fakeCluster) Members() []ClusterMember        { return f.members }
func (f *fakeCluster) LastIndex() uint64               { return f.lastIndex }
func (f *fakeCluster) AppliedIndex() uint64            { return f.appliedIdx }
func (f *fakeCluster) AddVoter(id, addr string, _ uint64, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.addCalls = append(f.addCalls, addCall{id, addr})
	return nil
}
func (f *fakeCluster) RemoveServer(id string, _ uint64, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removeCalls = append(f.removeCalls, id)
	return nil
}

func newTestService(t *testing.T, isLeader bool) (*Service, *fakeCluster, storagepkg.Storage) {
	t.Helper()
	store, err := localstorage.Open(localstorage.Options{Path: t.TempDir() + "/data.db"})
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := &fakeCluster{
		isLeader:   isLeader,
		leaderID:   "leader-A",
		leaderAddr: "leader.example.com:443",
	}
	if !isLeader {
		// Non-leader case: there's still a leader somewhere.
		c.leaderID = "leader-A"
		c.leaderAddr = "leader.example.com:443"
	}
	svc := New(c, store, "team-1")
	return svc, c, store
}

func TestJoin_HappyPath(t *testing.T) {
	svc, cluster, store := newTestService(t, true)
	if err := store.Put(context.Background(), BootstrapTokenKey, []byte("secret-token")); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId:         "team-1",
		NodeId:         "node-east",
		RaftAddr:       "east.example.com:443",
		BootstrapToken: "secret-token",
		ChConnected:    true,
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if resp.GetLeaderId() != "leader-A" {
		t.Errorf("LeaderId got=%q want=leader-A", resp.GetLeaderId())
	}
	if len(cluster.addCalls) != 1 || cluster.addCalls[0].nodeID != "node-east" {
		t.Errorf("expected one AddVoter for node-east, got %+v", cluster.addCalls)
	}
	got, _ := store.Get(context.Background(), CHConnectedPrefix+"node-east")
	if string(got) != "1" {
		t.Errorf("ch_connected hint not written; got %q", got)
	}
}

func TestJoin_TeamIDMismatch(t *testing.T) {
	svc, _, store := newTestService(t, true)
	_ = store.Put(context.Background(), BootstrapTokenKey, []byte("ok"))

	_, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId: "wrong-team", NodeId: "n", RaftAddr: "a", BootstrapToken: "ok",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
}

func TestJoin_BadToken(t *testing.T) {
	svc, _, store := newTestService(t, true)
	_ = store.Put(context.Background(), BootstrapTokenKey, []byte("the-real-thing"))

	_, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId: "team-1", NodeId: "n", RaftAddr: "a", BootstrapToken: "guessed",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", err)
	}
}

func TestJoin_NotLeader_ReturnsLeaderHint(t *testing.T) {
	svc, _, store := newTestService(t, false)
	_ = store.Put(context.Background(), BootstrapTokenKey, []byte("ok"))

	_, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId: "team-1", NodeId: "n", RaftAddr: "a", BootstrapToken: "ok",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
}

func TestJoin_NoBootstrapTokenConfigured(t *testing.T) {
	svc, _, _ := newTestService(t, true)
	_, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId: "team-1", NodeId: "n", RaftAddr: "a", BootstrapToken: "anything",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, want FailedPrecondition", err)
	}
}

func TestLeave_HappyPath(t *testing.T) {
	svc, cluster, store := newTestService(t, true)
	_ = store.Put(context.Background(), CHConnectedPrefix+"node-x", []byte("1"))

	_, err := svc.Leave(context.Background(), &internalspec.LeaveRequest{
		TeamId: "team-1", NodeId: "node-x",
	})
	if err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if len(cluster.removeCalls) != 1 || cluster.removeCalls[0] != "node-x" {
		t.Errorf("RemoveServer not called as expected: %+v", cluster.removeCalls)
	}
	got, _ := store.Get(context.Background(), CHConnectedPrefix+"node-x")
	if len(got) != 0 {
		t.Errorf("ch_connected hint not cleared: %q", got)
	}
}

func TestStatus_PopulatesMembersWithCHFlag(t *testing.T) {
	svc, cluster, store := newTestService(t, true)
	cluster.members = []ClusterMember{
		{NodeID: "node-a", RaftAddr: "a:443", Suffrage: "voter", IsLeader: true},
		{NodeID: "node-b", RaftAddr: "b:443", Suffrage: "voter"},
	}
	cluster.lastIndex = 100
	cluster.appliedIdx = 99
	_ = store.Put(context.Background(), CHConnectedPrefix+"node-a", []byte("1"))

	resp, err := svc.Status(context.Background(), &internalspec.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetLastIndex() != 100 || resp.GetAppliedIndex() != 99 {
		t.Errorf("indices wrong: %d/%d", resp.GetLastIndex(), resp.GetAppliedIndex())
	}
	if len(resp.GetMembers()) != 2 {
		t.Fatalf("expected 2 members, got %d", len(resp.GetMembers()))
	}
	if !resp.GetMembers()[0].GetIsLeader() || !resp.GetMembers()[0].GetChConnected() {
		t.Error("node-a should be leader + ch_connected")
	}
	if resp.GetMembers()[1].GetChConnected() {
		t.Error("node-b should NOT be ch_connected")
	}
}

func TestSetBootstrapToken_NotLeader(t *testing.T) {
	svc, _, _ := newTestService(t, false)
	if err := svc.SetBootstrapToken(context.Background(), []byte("x")); err == nil {
		t.Error("expected error from non-leader")
	}
}

func TestSetBootstrapToken_RoundTrip(t *testing.T) {
	svc, _, store := newTestService(t, true)
	if err := svc.SetBootstrapToken(context.Background(), []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(context.Background(), BootstrapTokenKey)
	if string(got) != "fresh" {
		t.Errorf("token round-trip got=%q want=fresh", got)
	}
}

func TestJoin_AddVoterError(t *testing.T) {
	svc, cluster, store := newTestService(t, true)
	_ = store.Put(context.Background(), BootstrapTokenKey, []byte("ok"))
	cluster.addErr = errors.New("dup id")

	_, err := svc.Join(context.Background(), &internalspec.JoinRequest{
		TeamId: "team-1", NodeId: "n", RaftAddr: "a", BootstrapToken: "ok",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("got %v, want Internal", err)
	}
}
