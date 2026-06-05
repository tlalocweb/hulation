package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
)

// resolutionAllows is the central per-server gate shared by AgentStream + ControlStream;
// pin its behaviour so future ACL changes are explicit.
func TestResolutionAllows(t *testing.T) {
	cases := []struct {
		name     string
		res      ACLResolution
		serverID string
		want     bool
	}{
		{"empty denies", ACLResolution{}, "acme", false},
		{"super grants everything", ACLResolution{Superadmin: true}, "anywhere", true},
		{"matching id grants", ACLResolution{Allowed: []string{"acme"}}, "acme", true},
		{"non-matching denies", ACLResolution{Allowed: []string{"acme"}}, "beta", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolutionAllows(c.res, c.serverID)
			if got != c.want {
				t.Fatalf("resolutionAllows(%+v, %q) = %v, want %v", c.res, c.serverID, got, c.want)
			}
		})
	}
}

func TestMapDirection(t *testing.T) {
	cases := map[string]chatspec.ChatMessageDirection{
		chatpkg.DirVisitor: chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_VISITOR,
		chatpkg.DirAgent:   chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_AGENT,
		chatpkg.DirSystem:  chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_SYSTEM,
		chatpkg.DirBot:     chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_BOT,
		"":                 chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_UNSPECIFIED,
		"unknown":          chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_UNSPECIFIED,
	}
	for in, want := range cases {
		if got := mapDirection(in); got != want {
			t.Fatalf("mapDirection(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMapSessionStatus(t *testing.T) {
	cases := map[string]chatspec.ChatSessionStatus{
		chatpkg.StatusQueued:   chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_QUEUED,
		chatpkg.StatusAssigned: chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_ASSIGNED,
		chatpkg.StatusOpen:     chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_OPEN,
		chatpkg.StatusClosed:   chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_CLOSED,
		chatpkg.StatusExpired:  chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_EXPIRED,
		"":                     chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_UNSPECIFIED,
	}
	for in, want := range cases {
		if got := mapSessionStatus(in); got != want {
			t.Fatalf("mapSessionStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

// translateAgentJSONToProto must cover the seven JSON frame shapes the hub publishes
// (msg / system / presence / typing / error / _pong) plus unrecognized → nil.
func TestTranslateAgentJSONToProto(t *testing.T) {
	sessionID := uuid.New()

	t.Run("msg with client_id", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":      "msg",
			"id":        "11111111-1111-1111-1111-111111111111",
			"direction": chatpkg.DirAgent,
			"agent":     "alice",
			"content":   "hello",
			"ts":        time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
			"client_id": "c-42",
		})
		out, err := translateAgentJSONToProto(raw, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		got := out.GetMsg()
		if got == nil {
			t.Fatalf("expected msg frame, got %v", out)
		}
		if got.SenderId != "alice" || got.Content != "hello" || got.ClientId != "c-42" {
			t.Fatalf("translated msg lost fields: %+v", got)
		}
		if got.Direction != chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_AGENT {
			t.Fatalf("direction = %v", got.Direction)
		}
		if got.SessionId != sessionID.String() {
			t.Fatalf("session_id not injected: %q", got.SessionId)
		}
	})

	t.Run("visitor msg uses visitor field as sender", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":      "msg",
			"id":        "22222222-2222-2222-2222-222222222222",
			"direction": chatpkg.DirVisitor,
			"visitor":   "v-ghost",
			"content":   "hi",
			"ts":        time.Now().UTC().Format(time.RFC3339),
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		if got := out.GetMsg(); got == nil || got.SenderId != "v-ghost" {
			t.Fatalf("visitor sender lost: %+v", got)
		}
	})

	t.Run("system frame", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":    "system",
			"content": "Session closed by agent.",
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		sys := out.GetSystem()
		if sys == nil || sys.Message != "Session closed by agent." || sys.Kind != "info" {
			t.Fatalf("system frame: %+v", sys)
		}
	})

	t.Run("presence frame becomes SystemEvent with namespaced kind", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":  "presence",
			"event": "agent_joined",
			"agent": "alice",
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		sys := out.GetSystem()
		if sys == nil || sys.Kind != "presence:agent_joined" || sys.Message != "alice" {
			t.Fatalf("presence frame: %+v", sys)
		}
	})

	t.Run("typing frame from visitor active", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":   "typing",
			"from":   "visitor",
			"active": true,
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		sys := out.GetSystem()
		if sys == nil || sys.Kind != "typing:visitor" || sys.Message != "on" {
			t.Fatalf("typing frame: %+v", sys)
		}
	})

	t.Run("error frame", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":    "error",
			"code":    "rate_limited",
			"message": "slow down",
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		e := out.GetError()
		if e == nil || e.Code != "rate_limited" || e.Message != "slow down" {
			t.Fatalf("error frame: %+v", e)
		}
	})

	t.Run("pong synthetic frame", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type": "_pong",
			"ts":   time.Now().UTC().Format(time.RFC3339),
		})
		out, _ := translateAgentJSONToProto(raw, sessionID)
		if out.GetPong() == nil {
			t.Fatalf("pong frame: %+v", out)
		}
	})

	t.Run("unknown type yields nil frame", func(t *testing.T) {
		raw := mustMarshal(map[string]any{"type": "wat"})
		out, err := translateAgentJSONToProto(raw, sessionID)
		if err != nil || out != nil {
			t.Fatalf("unknown should be (nil, nil), got (%+v, %v)", out, err)
		}
	})

	t.Run("malformed JSON errors", func(t *testing.T) {
		_, err := translateAgentJSONToProto([]byte("not json"), sessionID)
		if err == nil {
			t.Fatal("expected JSON error")
		}
	})
}

func TestErrFrameRoundTrip(t *testing.T) {
	raw := errFrame("bad_things", "specifically these things")
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "error" || got["code"] != "bad_things" || got["message"] != "specifically these things" {
		t.Fatalf("errFrame: %+v", got)
	}
}

func TestTrySendDropsWhenFull(t *testing.T) {
	out := make(chan []byte, 1)
	if !trySend(out, []byte("first")) {
		t.Fatal("first should succeed")
	}
	if trySend(out, []byte("second")) {
		t.Fatal("second should drop")
	}
}

func TestSnapshotToProto(t *testing.T) {
	now := time.Now().UTC()
	sid1 := uuid.New()
	sid2 := uuid.New()
	snap := chatpkg.Snapshot{
		Queued: []chatpkg.QueueEntry{
			{SessionID: sid1, QueuedAt: now.Add(-30 * time.Second), QueuedFor: 30 * time.Second},
		},
		Assigned: []chatpkg.AssignmentEntry{
			{SessionID: sid2, Agent: "alice"},
		},
		ReadyAgents: []string{"alice", "bob"},
	}
	out := snapshotToProto(snap)
	if len(out.Entries) != 1 || out.Entries[0].SessionId != sid1.String() {
		t.Fatalf("entries lost: %+v", out.Entries)
	}
	if out.Entries[0].QueuedForSeconds != 30 {
		t.Fatalf("queued_for_seconds = %d", out.Entries[0].QueuedForSeconds)
	}
	if len(out.Assigned) != 1 || out.Assigned[0].SessionId != sid2.String() || out.Assigned[0].Agent != "alice" {
		t.Fatalf("assigned lost: %+v", out.Assigned)
	}
	if len(out.ReadyAgents) != 2 || out.ReadyAgents[0] != "alice" {
		t.Fatalf("ready_agents lost: %+v", out.ReadyAgents)
	}
}

func TestTranslateControlJSONToProto(t *testing.T) {
	t.Run("queue_snapshot full", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type": "queue_snapshot",
			"queued": []map[string]any{
				{"session_id": "sid-1", "queued_for_seconds": 12},
				{"session_id": "sid-2", "queued_for_seconds": 0},
			},
			"assigned": []map[string]any{
				{"session_id": "sid-3", "agent": "alice"},
			},
			"ready_agents": []string{"alice", "bob"},
		})
		out, err := translateControlJSONToProto(raw)
		if err != nil {
			t.Fatal(err)
		}
		q := out.GetQueue()
		if q == nil {
			t.Fatalf("expected queue, got %+v", out)
		}
		if len(q.Entries) != 2 || q.Entries[0].SessionId != "sid-1" || q.Entries[0].QueuedForSeconds != 12 {
			t.Fatalf("entries lost: %+v", q.Entries)
		}
		if len(q.Assigned) != 1 || q.Assigned[0].Agent != "alice" {
			t.Fatalf("assigned lost: %+v", q.Assigned)
		}
		if len(q.ReadyAgents) != 2 {
			t.Fatalf("ready_agents lost: %+v", q.ReadyAgents)
		}
	})

	t.Run("session_assigned", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":       "session_assigned",
			"session_id": "abc-123",
		})
		out, _ := translateControlJSONToProto(raw)
		got := out.GetAssigned()
		if got == nil || got.SessionId != "abc-123" {
			t.Fatalf("assigned frame: %+v", got)
		}
	})

	t.Run("session_released", func(t *testing.T) {
		raw := mustMarshal(map[string]any{
			"type":       "session_released",
			"session_id": "abc-123",
			"reason":     "transferred",
		})
		out, _ := translateControlJSONToProto(raw)
		got := out.GetReleased()
		if got == nil || got.Reason != "transferred" {
			t.Fatalf("released frame: %+v", got)
		}
	})

	t.Run("unknown type yields nil", func(t *testing.T) {
		raw := mustMarshal(map[string]any{"type": "novel"})
		out, err := translateControlJSONToProto(raw)
		if err != nil || out != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", out, err)
		}
	})

	t.Run("malformed JSON errors", func(t *testing.T) {
		_, err := translateControlJSONToProto([]byte("nope"))
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestJsonNumberAsUint32(t *testing.T) {
	cases := map[any]uint32{
		float64(5):  5,
		float64(-1): 0,
		int(42):     42,
		int64(99):   99,
		"text":      0,
		nil:         0,
	}
	for in, want := range cases {
		if got := jsonNumberAsUint32(in); got != want {
			t.Fatalf("jsonNumberAsUint32(%v) = %d, want %d", in, got, want)
		}
	}
}

// Quiet the unused-import warnings in environments where helpers aren't all exercised.
var _ = context.Background
