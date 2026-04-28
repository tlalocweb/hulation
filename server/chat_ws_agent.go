package server

// Agent (admin) WebSocket handler for /api/v1/chat/admin/agent-ws.
//
// Live, bidirectional. The visitor side runs at /chat/ws and uses a
// chat-session JWT; this side requires an admin JWT plus
// server_access ACL for the session's server_id. Multiple agents
// can share a session (handy for senior-observing-junior or live
// handoff); the hub broadcasts every frame to every subscriber
// except the originator.
//
// Auth flow on upgrade:
//  1. Read JWT from `Authorization: Bearer …` header OR `?token=`
//     query (browsers can't always set headers on WS upgrades).
//  2. Validate via the same model.VerifyJWTClaims path the
//     AdminBearerInterceptor uses; populate authware.Claims.
//  3. Confirm session exists + isn't closed.
//  4. Run the chat ACL check for (claims.username, session.ServerID)
//     using the ACL hook bound at boot (chat_acl.go). Without this
//     check, any admin JWT could chat into any tenant's sessions.
//  5. Subscribe on the hub as RoleAgent + broadcast `agent_joined`.
//  6. Mark the session OPEN if it was QUEUED/ASSIGNED — opening
//     the agent WS *is* the implicit "agent picked up" signal.
//
// Frame shapes (PLAN_4B.md §4.2):
//
//   agent → server: { "type":"msg",    "content":"..." }
//                   { "type":"typing", "active":true }
//                   { "type":"ping" }
//                   { "type":"close",  "reason":"..." }
//   server → agent: { "type":"msg", ... }    (visitor messages)
//                   { "type":"presence", "event":"...", "agent":"..." }
//                   { "type":"typing", "from":"visitor", "active":true }
//                   { "type":"ack", "id":"..." }
//                   { "type":"error", "code":"...","message":"..." }

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

// agentWS is the dependency bundle. Built in chat_boot.go.
type agentWS struct {
	Store  *chatpkg.Store
	Hub    *chatpkg.Hub
	ACL    chatimpl.ACLLookup
	Router *chatpkg.Router
}

// chatAgentWSHandler builds a handler closing over deps. Auth is
// enforced inline (no annotations — gorilla.Upgrade bypasses the
// gRPC chain anyway).
func chatAgentWSHandler(deps *agentWS) http.HandlerFunc {
	cfg := defaultVisitorWSConfig() // reuse the same timing constants
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth: prefer Authorization header; fall back to ?token=
		// query for browsers.
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
		// Build the same skeletal Claims the AdminBearerInterceptor
		// would have populated, then run the chat ACL check.
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

		sidStr := r.URL.Query().Get("session_id")
		sessionID, err := uuid.Parse(sidStr)
		if err != nil {
			http.Error(w, "invalid session_id", http.StatusBadRequest)
			return
		}

		sess, err := deps.Store.GetSession(ctx, "", sessionID)
		// Note: empty server_id matches any. We don't know which
		// server until we read the row, so do an unscoped read
		// then ACL on the server_id we get back.
		if errors.Is(err, chatpkg.ErrNotFound) {
			// Try to discover the session by server-by-server.
			// Cheap because chat_sessions is partitioned by
			// (server_id, started_at, id) — a pure id lookup
			// without server_id may not match. Iterate
			// configured servers from the ACL until one succeeds.
			res := deps.ACL(ctx)
			for _, sid := range res.Allowed {
				if cand, candErr := deps.Store.GetSession(ctx, sid, sessionID); candErr == nil {
					sess = cand
					err = nil
					break
				}
			}
		}
		if err != nil || sess.ID == uuid.Nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if sess.Status == chatpkg.StatusClosed || sess.Status == chatpkg.StatusExpired {
			http.Error(w, "session closed", http.StatusGone)
			return
		}
		// ACL check on the discovered server_id.
		acl := deps.ACL(ctx)
		permitted := false
		for _, sid := range acl.Allowed {
			if sid == sess.ServerID {
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
			log.Warnf("chat agent ws upgrade: %s", err)
			return
		}
		defer conn.Close()

		sub := &chatpkg.Subscriber{
			Out:     make(chan []byte, chatpkg.SubscriberOutBufferSize),
			Role:    chatpkg.RoleAgent,
			AgentID: username,
		}
		snap := deps.Hub.Subscribe(sessionID, sub)
		defer deps.Hub.Unsubscribe(sessionID, sub)

		// Snapshot first; presence broadcast second.
		_ = trySend(sub.Out, presenceSnapshotFrame(snap))
		deps.Hub.Publish(sessionID, mustMarshal(map[string]any{
			"type":  "presence",
			"event": "agent_joined",
			"agent": username,
		}), sub)

		// Flip status to OPEN if it was queued/assigned. Best-
		// effort; a failure here doesn't kill the WS.
		if sess.Status == chatpkg.StatusQueued || sess.Status == chatpkg.StatusAssigned {
			if _, err := deps.Store.MarkSessionOpen(ctx, sess.ServerID, sessionID); err != nil {
				log.Warnf("chat agent ws: mark open: %s", err)
			}
		}
		// Tell the router we picked it up — cancels the 30s
		// re-route timer the Enqueue() call started.
		if deps.Router != nil {
			deps.Router.Ack(sess.ServerID, username, sessionID)
		}

		conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
			return nil
		})

		stop := make(chan struct{})
		go agentReader(ctx, conn, deps, sess, sessionID, sub, username, stop)

		// Writer.
		ticker := time.NewTicker(cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-sub.Out:
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
				deps.Hub.Publish(sessionID, mustMarshal(map[string]any{
					"type":  "presence",
					"event": "agent_left",
					"agent": username,
				}), sub)
				return
			}
		}
	}
}

type agentFrameIn struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Active  *bool  `json:"active,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func agentReader(
	ctx context.Context,
	conn *websocket.Conn,
	deps *agentWS,
	sess chatpkg.Session,
	sessionID uuid.UUID,
	sub *chatpkg.Subscriber,
	username string,
	stop chan<- struct{},
) {
	defer close(stop)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(defaultVisitorWSConfig().PongTimeout))
		var fr agentFrameIn
		if err := json.Unmarshal(raw, &fr); err != nil {
			_ = trySend(sub.Out, errFrame("bad_frame", "invalid JSON"))
			continue
		}
		switch fr.Type {
		case "ping":
			// no-op; SetReadDeadline above already extends
		case "typing":
			active := fr.Active != nil && *fr.Active
			deps.Hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":   "typing",
				"from":   "agent",
				"agent":  username,
				"active": active,
			}), sub)
		case "msg":
			content := strings.TrimSpace(fr.Content)
			if content == "" {
				_ = trySend(sub.Out, errFrame("empty_content", "message cannot be empty"))
				continue
			}
			msg := chatpkg.Message{
				ID:        uuid.New(),
				SessionID: sessionID,
				ServerID:  sess.ServerID,
				VisitorID: sess.VisitorID,
				Direction: chatpkg.DirAgent,
				SenderID:  username,
				Content:   content,
				When:      time.Now().UTC(),
			}
			if err := deps.Store.AppendMessage(ctx, msg); err != nil {
				log.Warnf("chat agent ws append: %s", err)
				_ = trySend(sub.Out, errFrame("internal", "could not persist"))
				continue
			}
			_ = trySend(sub.Out, mustMarshal(map[string]any{
				"type": "ack",
				"id":   msg.ID.String(),
			}))
			deps.Hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":      "msg",
				"id":        msg.ID.String(),
				"direction": "agent",
				"agent":     username,
				"content":   content,
				"ts":        msg.When,
			}), sub)
		case "close":
			_, err := deps.Store.MarkSessionClosed(ctx, sess.ServerID, sessionID, fr.Reason)
			if err != nil {
				log.Warnf("chat agent ws close: %s", err)
			}
			deps.Hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":    "system",
				"content": "Session closed by agent.",
			}), nil)
			return
		default:
			_ = trySend(sub.Out, errFrame("unknown_type", fr.Type))
		}
	}
}

// extractAdminBearer pulls the bearer token off either the
// Authorization header or the ?token= query string. The chat
// admin SPA prefers the header (which is the gateway-standard);
// browsers attempting the WS upgrade can only set the query.
func extractAdminBearer(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimPrefix(authz, "Bearer ")
	}
	return r.URL.Query().Get("token")
}
