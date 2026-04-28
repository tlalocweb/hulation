package server

// Visitor WebSocket handler for /api/v1/chat/ws.
//
// The token query param is the chat-session JWT issued by
// /chat/start. We:
//
//  1. Parse + validate the token (signature, expiry, subject).
//  2. Confirm the session row in ClickHouse exists and isn't
//     closed/expired.
//  3. Upgrade the connection.
//  4. Kick any prior visitor WS bound to the same session (the
//     visitor reconnected; only one visitor at a time).
//  5. Subscribe on the chat hub.
//  6. Drain inbound frames: persist visitor messages, fan out to
//     other subscribers (agent[s]), echo a small `ack` frame to
//     the sender so the SPA can flip its "sending" indicator.
//  7. Drain outbound (sub.Out): write to the WS with a 10s
//     deadline; on write error, close.
//  8. Heartbeat: server pings every 25s; idle > 90s without any
//     client activity (incl. pong) → close.
//
// Frame shapes (see PLAN_4B.md §4.1):
//
//   visitor → server: { "type":"msg",   "content":"..." }
//                     { "type":"typing","active":true }
//                     { "type":"ping" }
//   server  → visitor: { "type":"msg",  "id":"...","direction":"agent",
//                       "content":"...","ts":"..." }
//                     { "type":"system","content":"..." }
//                     { "type":"ack","id":"..." }
//                     { "type":"presence","event":"agent_joined",
//                       "agent":"..." }
//                     { "type":"typing","from":"agent",
//                       "agent":"...","active":true }
//                     { "type":"error","code":"...","message":"..." }

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/tlalocweb/hulation/log"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
)

// visitorWSConfig holds the small set of timing / rate knobs for
// the visitor WS. Pulled out so tests can shorten them.
type visitorWSConfig struct {
	PingInterval time.Duration
	PongTimeout  time.Duration
	WriteTimeout time.Duration
	// Per-session message rate cap.
	RateLimitWindow time.Duration
	RateLimitMax    int
}

func defaultVisitorWSConfig() visitorWSConfig {
	return visitorWSConfig{
		PingInterval:    25 * time.Second,
		PongTimeout:     90 * time.Second,
		WriteTimeout:    10 * time.Second,
		RateLimitWindow: 30 * time.Second,
		RateLimitMax:    10,
	}
}

// chatWSUpgrader checks the request origin against allowed values.
// We start permissive (any origin) — the chat token is the
// security boundary, and the embedding website is whatever the
// operator deploys. CORS-style origin pinning can be added later
// per-server.
var chatWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// visitorFrameIn is the shape we accept from the visitor.
type visitorFrameIn struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Active  *bool  `json:"active,omitempty"`
}

func chatVisitorWSHandler(svc *chatpkg.VisitorWS) http.HandlerFunc {
	cfg := defaultVisitorWSConfig()
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		claims, err := chatpkg.ParseToken(svc.JWTKey, token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		sessionID, err := uuid.Parse(claims.SessionID)
		if err != nil {
			http.Error(w, "invalid sid", http.StatusUnauthorized)
			return
		}

		// Confirm session exists + isn't closed.
		sess, err := svc.Store.GetSession(r.Context(), claims.ServerID, sessionID)
		if err != nil {
			if errors.Is(err, chatpkg.ErrNotFound) {
				http.Error(w, "session not found", http.StatusGone)
				return
			}
			log.Warnf("chat ws: get session: %s", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if sess.Status == chatpkg.StatusClosed || sess.Status == chatpkg.StatusExpired {
			http.Error(w, "session closed", http.StatusGone)
			return
		}

		// Kick any prior visitor socket so a reconnect doesn't
		// stack a ghost reader against the same session.
		if old := svc.Hub.VisitorSubscriber(sessionID); old != nil {
			// Send a system close-ish notice; we don't have a
			// handle on the old conn directly, but draining its
			// Out + closing it via the hub achieves the effect:
			// the writer goroutine on the old WS will see the
			// channel close and exit.
			_ = trySend(old.Out, mustMarshal(map[string]any{
				"type":    "system",
				"content": "Reconnected from another tab; closing this connection.",
			}))
			svc.Hub.Unsubscribe(sessionID, old)
			close(old.Out)
		}

		conn, err := chatWSUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Warnf("chat ws upgrade: %s", err)
			return
		}
		defer conn.Close()

		sub := &chatpkg.Subscriber{
			Out:  make(chan []byte, chatpkg.SubscriberOutBufferSize),
			Role: chatpkg.RoleVisitor,
		}
		snap := svc.Hub.Subscribe(sessionID, sub)
		defer svc.Hub.Unsubscribe(sessionID, sub)
		// Send the snapshot first so the widget knows whether an
		// agent is already in the room.
		_ = trySend(sub.Out, presenceSnapshotFrame(snap))
		// Tell other subscribers (agents) the visitor connected.
		svc.Hub.Publish(sessionID, mustMarshal(map[string]any{
			"type":  "presence",
			"event": "visitor_connected",
		}), sub)

		ratelimit := chatpkg.NewRateLimiter(cfg.RateLimitWindow, cfg.RateLimitMax)
		// Reset deadlines based on cfg.
		conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
			return nil
		})

		stop := make(chan struct{})

		// Reader goroutine.
		go func() {
			defer close(stop)
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				conn.SetReadDeadline(time.Now().Add(cfg.PongTimeout))
				var fr visitorFrameIn
				if err := json.Unmarshal(raw, &fr); err != nil {
					_ = trySend(sub.Out, errFrame("bad_frame", "invalid JSON"))
					continue
				}
				switch fr.Type {
				case "ping":
					// Visitor-initiated ping; we just acknowledge
					// by resetting the deadline (already done
					// above).
				case "typing":
					active := fr.Active != nil && *fr.Active
					svc.Hub.Publish(sessionID, mustMarshal(map[string]any{
						"type":   "typing",
						"from":   "visitor",
						"active": active,
					}), sub)
				case "msg":
					content := strings.TrimSpace(fr.Content)
					if content == "" {
						_ = trySend(sub.Out, errFrame("empty_content", "message cannot be empty"))
						continue
					}
					if !ratelimit.Allow(claims.ServerID, claims.SessionID) {
						_ = trySend(sub.Out, errFrame("rate_limited", "too many messages"))
						continue
					}
					msg := chatpkg.Message{
						ID:        uuid.New(),
						SessionID: sessionID,
						ServerID:  claims.ServerID,
						VisitorID: claims.VisitorID,
						Direction: chatpkg.DirVisitor,
						Content:   content,
						When:      time.Now().UTC(),
					}
					if err := svc.Store.AppendMessage(r.Context(), msg); err != nil {
						log.Warnf("chat ws append: %s", err)
						_ = trySend(sub.Out, errFrame("internal", "could not persist"))
						continue
					}
					// Ack to the sender; broadcast the full msg to
					// agents (and any peer subscribers).
					_ = trySend(sub.Out, mustMarshal(map[string]any{
						"type": "ack",
						"id":   msg.ID.String(),
					}))
					svc.Hub.Publish(sessionID, mustMarshal(map[string]any{
						"type":      "msg",
						"id":        msg.ID.String(),
						"direction": "visitor",
						"content":   content,
						"ts":        msg.When,
					}), sub)
				default:
					_ = trySend(sub.Out, errFrame("unknown_type", fr.Type))
				}
			}
		}()

		// Writer loop.
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
				// Reader exited (read error / close / ctx done).
				// Tell everyone else the visitor disconnected.
				svc.Hub.Publish(sessionID, mustMarshal(map[string]any{
					"type":  "presence",
					"event": "visitor_disconnected",
				}), sub)
				return
			}
		}
	}
}

// trySend is a non-blocking send. Returns false when the buffer's
// full. Used for handshake frames where we'd rather drop than
// block the upgrader.
func trySend(out chan<- []byte, frame []byte) bool {
	select {
	case out <- frame:
		return true
	default:
		return false
	}
}

func mustMarshal(v any) []byte {
	buf, err := json.Marshal(v)
	if err != nil {
		// Should not happen for our small map[string]any values;
		// if it does, send a sentinel error frame so the receiver
		// at least sees something.
		return []byte(`{"type":"error","code":"internal","message":"frame marshal"}`)
	}
	return buf
}

func errFrame(code, message string) []byte {
	return mustMarshal(map[string]any{
		"type":    "error",
		"code":    code,
		"message": message,
	})
}

func presenceSnapshotFrame(snap chatpkg.PresenceSnapshot) []byte {
	return mustMarshal(map[string]any{
		"type":           "presence_snapshot",
		"visitor_online": snap.VisitorOnline,
		"agents":         snap.Agents,
	})
}

// quiet vet on context import (kept for future ctx propagation).
var _ = context.Background
