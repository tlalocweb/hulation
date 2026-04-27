package server

// WebSocket handler for /api/mobile/v1/events. Registers on the
// unified server's ServeMux fallback (same mechanism as visitor
// tracking) — gRPC streaming wasn't worth the toolchain load for a
// single endpoint.
//
// Protocol:
//   1. Client opens WS with Authorization: Bearer <jwt> header and
//      optional ?server_id=<id> query param.
//   2. Server upgrades, validates the bearer, registers a Subscriber
//      on the realtime.Hub.
//   3. Server sends a ping every 25s; client is expected to respond
//      with a pong. Idle > 60s → connection closes.
//   4. Each new visitor event matching the subscriber's server_id
//      scope is JSON-serialised + written as a text frame.
//
// Slow clients are dropped: the hub drops messages when their
// buffer fills (non-blocking send), but this handler also closes
// the connection if a single write blocks > 10s.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/realtime"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

var wsLog = log.GetTaggedLogger("mobile-ws", "Mobile realtime WebSocket")

// upgrader is shared across requests. CheckOrigin returns true —
// JWT auth gates access, and same-origin is too restrictive for a
// mobile-app client.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// HandleMobileWS upgrades the request to a WebSocket and streams
// realtime events. Registered by RegisterFallbackRoutes at
// /api/mobile/v1/events.
func HandleMobileWS(w http.ResponseWriter, r *http.Request) {
	// Claims are installed by AdminBearerHTTPMiddleware on every HTTP
	// request. WebSocket upgrade requests carry the same Authorization
	// header so the middleware has already populated the context.
	claims, ok := r.Context().Value(authware.ClaimsKey).(*authware.Claims)
	if !ok || claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	hub := realtime.Global()
	if hub == nil {
		http.Error(w, "realtime hub unavailable", http.StatusServiceUnavailable)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade() already wrote an error response on its own.
		return
	}
	defer conn.Close()

	serverID := r.URL.Query().Get("server_id")
	sub := hub.Subscribe(serverID)
	defer hub.Unsubscribe(sub)

	wsLog.Debugf("ws opened: user=%s server_id=%s subs=%d", claims.Username, serverID, hub.ActiveSubscribers())

	// Keep-alive: ping every 25s, fail closed if pongs stop.
	pingTicker := time.NewTicker(25 * time.Second)
	defer pingTicker.Stop()
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	// Reader goroutine: consume client frames (we don't expect any
	// besides control frames) so gorilla's internal read loop fires
	// SetPongHandler when the client pings back.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case evt, open := <-sub.Ch:
			if !open {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			buf, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, buf); err != nil {
				return
			}
		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
