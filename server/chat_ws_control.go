package server

// Agent control WebSocket — `WS /api/v1/chat/admin/agent-control-ws`.
//
// One per logged-in admin per server_id. While open, the agent is
// in the per-server "ready" pool the Router uses for next-available
// routing. Frames flowing:
//
//   server → agent: { "type":"queue_snapshot", "queued":[...], "assigned":[...] }
//                   { "type":"session_assigned", "session_id":"..." }
//                   { "type":"session_released", "session_id":"..." }  // (4b.6+)
//                   { "type":"ping" }
//   agent → server: { "type":"ack", "session_id":"..." }
//                   { "type":"decline", "session_id":"..." }
//                   { "type":"ping" }
//
// Authn: same admin JWT + chat ACL as the per-session agent WS.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/model"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	chatimpl "github.com/tlalocweb/hulation/pkg/api/v1/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

type agentControlWS struct {
	Router *chatpkg.Router
	ACL    chatimpl.ACLLookup
	// Store is used by `take` from REST + by the queue snapshot
	// to resolve session metadata when needed.
	Store *chatpkg.Store
}

func chatAgentControlWSHandler(deps *agentControlWS) http.HandlerFunc {
	cfg := defaultVisitorWSConfig()
	return func(w http.ResponseWriter, r *http.Request) {
		bearer := extractAdminBearer(r)
		if bearer == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		ok, perms, err := model.VerifyJWTClaims(model.GetDB(), bearer)
		if err != nil || !ok || perms == nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		username := perms.UserID
		if username == "" {
			http.Error(w, "no claims", http.StatusUnauthorized)
			return
		}
		serverID := r.URL.Query().Get("server_id")
		if serverID == "" {
			http.Error(w, "server_id required", http.StatusBadRequest)
			return
		}

		// Build a Claims for the ACL hook.
		appcfg := hulaapp.GetConfig()
		adminUser := ""
		if appcfg != nil && appcfg.Admin != nil {
			adminUser = appcfg.Admin.Username
		}
		roles := []string{}
		if username != "" && adminUser != "" && username == adminUser {
			roles = append(roles, "admin")
		}
		claims := &authware.Claims{
			Username:    username,
			Roles:       roles,
			Permissions: perms.ListCaps(),
		}
		claims.Subject = username
		ctx := context.WithValue(r.Context(), authware.ClaimsKey, claims)

		acl := deps.ACL(ctx)
		permitted := false
		for _, sid := range acl.Allowed {
			if sid == serverID {
				permitted = true
				break
			}
		}
		if !permitted {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		conn, err := chatWSUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warnf("chat control ws upgrade: %s", err)
			return
		}
		defer conn.Close()

		slot := &chatpkg.AgentSlot{
			Username: username,
			Out:      make(chan []byte, 32),
			JoinedAt: time.Now().UTC(),
		}
		snap := deps.Router.AgentReady(serverID, slot)
		defer deps.Router.AgentGone(serverID, username)

		// Send the initial queue_snapshot.
		_ = trySend(slot.Out, mustMarshal(map[string]any{
			"type":         "queue_snapshot",
			"queued":       snapshotEntriesQueued(snap.Queued),
			"assigned":     snapshotEntriesAssigned(snap.Assigned),
			"ready_agents": snap.ReadyAgents,
		}))

		conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
			return nil
		})

		stop := make(chan struct{})
		go func() {
			defer close(stop)
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
				var fr struct {
					Type      string `json:"type"`
					SessionID string `json:"session_id,omitempty"`
				}
				if err := json.Unmarshal(raw, &fr); err != nil {
					_ = trySend(slot.Out, errFrame("bad_frame", "invalid JSON"))
					continue
				}
				switch fr.Type {
				case "ping":
					// no-op
				case "ack":
					sid, err := uuid.Parse(fr.SessionID)
					if err != nil {
						_ = trySend(slot.Out, errFrame("bad_frame", "invalid session_id"))
						continue
					}
					deps.Router.Ack(serverID, username, sid)
				case "decline":
					sid, err := uuid.Parse(fr.SessionID)
					if err != nil {
						_ = trySend(slot.Out, errFrame("bad_frame", "invalid session_id"))
						continue
					}
					deps.Router.Decline(serverID, username, sid)
				default:
					_ = trySend(slot.Out, errFrame("unknown_type", fr.Type))
				}
			}
		}()

		ticker := time.NewTicker(cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-slot.Out:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(cfg.WriteTimeout))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(cfg.WriteTimeout))
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(cfg.WriteTimeout)); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}
}

func snapshotEntriesQueued(qs []chatpkg.QueueEntry) []map[string]any {
	out := make([]map[string]any, 0, len(qs))
	for _, q := range qs {
		out = append(out, map[string]any{
			"session_id":         q.SessionID.String(),
			"queued_for_seconds": int(q.QueuedFor.Seconds()),
		})
	}
	return out
}

func snapshotEntriesAssigned(as []chatpkg.AssignmentEntry) []map[string]any {
	out := make([]map[string]any, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]any{
			"session_id": a.SessionID.String(),
			"agent":      a.Agent,
		})
	}
	return out
}
