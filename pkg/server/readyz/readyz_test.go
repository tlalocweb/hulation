package readyz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeState struct {
	alive      bool
	leaderID   string
	leaderAddr string
	applied    uint64
	last       uint64
}

func (f *fakeState) RaftAlive() bool                  { return f.alive }
func (f *fakeState) LeaderInfo() (string, string)     { return f.leaderID, f.leaderAddr }
func (f *fakeState) AppliedIndex() uint64             { return f.applied }
func (f *fakeState) LastIndex() uint64                { return f.last }

func probe(h http.Handler) (int, response) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/readyz", nil)
	h.ServeHTTP(rec, req)
	var body response
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestHandler_NilStateReturns200(t *testing.T) {
	code, body := probe(Handler(nil))
	if code != http.StatusOK {
		t.Errorf("got %d, want 200", code)
	}
	if !body.OK {
		t.Errorf("body.OK = false")
	}
}

func TestHandler_RaftDown_503(t *testing.T) {
	code, body := probe(Handler(&fakeState{alive: false}))
	if code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", code)
	}
	if body.Reason != "raft_down" {
		t.Errorf("reason got %q, want raft_down", body.Reason)
	}
}

func TestHandler_LaggingFollower_503(t *testing.T) {
	st := &fakeState{
		alive: true, applied: 100, last: 250,
		leaderID: "leader", leaderAddr: "leader.example:443",
	}
	code, body := probe(Handler(st))
	if code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", code)
	}
	if body.Reason != "raft_lagging" {
		t.Errorf("reason got %q, want raft_lagging", body.Reason)
	}
}

func TestHandler_CaughtUpAtTolerance_OK(t *testing.T) {
	st := &fakeState{
		alive: true, applied: 100, last: 100 + CatchupTolerance,
		leaderID: "leader", leaderAddr: "leader.example:443",
	}
	code, body := probe(Handler(st))
	if code != http.StatusOK {
		t.Errorf("got %d, want 200 (delta=%d ≤ tolerance)", code, CatchupTolerance)
	}
	if !body.OK {
		t.Errorf("body.OK = false")
	}
}

func TestHandler_NoLeader_503(t *testing.T) {
	st := &fakeState{alive: true, applied: 100, last: 100, leaderID: "", leaderAddr: ""}
	code, body := probe(Handler(st))
	if code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", code)
	}
	if body.Reason != "no_leader" {
		t.Errorf("reason got %q, want no_leader", body.Reason)
	}
}

func TestHandler_HappyLeader_OK(t *testing.T) {
	st := &fakeState{
		alive: true, applied: 50, last: 50,
		leaderID: "self", leaderAddr: "self.example:443",
	}
	code, body := probe(Handler(st))
	if code != http.StatusOK {
		t.Errorf("got %d, want 200", code)
	}
	if !body.OK {
		t.Errorf("body.OK = false")
	}
}

func TestHandler_AppliedAheadOfLast_NotLagging(t *testing.T) {
	// AppliedIndex > LastIndex shouldn't happen in practice but
	// the handler must NOT report lagging in that case.
	st := &fakeState{
		alive: true, applied: 200, last: 100,
		leaderID: "self", leaderAddr: "self.example:443",
	}
	code, _ := probe(Handler(st))
	if code != http.StatusOK {
		t.Errorf("got %d, want 200 (applied > last is not lagging)", code)
	}
}
