package chat

// Terminal-session enforcement. A closed (or expired) session is
// terminal: the visitor can never send another message, and the
// server is the authority — not the widget's local UI state.
//
// This file centralises the three concerns the WS + gRPC + REST close
// paths all share:
//
//   1. IsTerminalStatus / ErrSessionClosed — the status vocabulary.
//   2. ReloadOpenSession — reload from the source of truth (ClickHouse)
//      and reject a visitor message when the session is terminal.
//   3. CloseSession — mark closed (idempotent) + broadcast the
//      authoritative session_closed frame to every subscriber, and
//      flip the in-process "closed" flag the hub keeps so a concurrent
//      visitor send loses the race deterministically.
//
// Keeping it here means chat_ws_visitor.go, chat_ws_agent.go,
// chatstreamimpl.go and chatimpl.go all speak the same closure
// semantics instead of each re-deriving them.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// ErrSessionClosed is returned by ReloadOpenSession when the session
// exists but is terminal (closed or expired). Callers translate it to
// the transport's terminal signal: HTTP 409 (REST) or a session_closed
// frame (WS / gRPC stream). Distinct from ErrNotFound (no such row).
var ErrSessionClosed = errors.New("chat: session closed")

// IsTerminalStatus reports whether status means the session can never
// accept another message. Both closed (agent hung up) and expired
// (TTL / sweeper) are terminal.
func IsTerminalStatus(status string) bool {
	return status == StatusClosed || status == StatusExpired
}

// SessionClosedFrame is the authoritative terminal frame the server
// broadcasts to the visitor (and echoes on any post-close send
// attempt). Once the widget sees it, the composer must be disabled —
// the session is terminal and will never accept another message.
//
// Wire shape: {"type":"session_closed"[, "reason":"..."]}. reason is
// omitted when empty so the common case stays compact.
func SessionClosedFrame(reason string) []byte {
	m := map[string]any{"type": "session_closed"}
	if reason != "" {
		m["reason"] = reason
	}
	buf, err := json.Marshal(m)
	if err != nil {
		// The map is trivially marshalable; a failure here is
		// impossible in practice, but never return an empty frame the
		// hub would silently drop.
		return []byte(`{"type":"session_closed"}`)
	}
	return buf
}

// sessionGetter is the narrow read surface ReloadOpenSession needs.
// *Store satisfies it, as does the stream server's sessionStore
// interface, so the same gate works everywhere without dragging in the
// full store.
type sessionGetter interface {
	GetSession(ctx context.Context, serverID string, id uuid.UUID) (Session, error)
}

// ReloadOpenSession fetches (serverID, id) from the source of truth and
// returns it only when it can still accept a visitor message. It maps a
// terminal status to ErrSessionClosed and a missing row to ErrNotFound
// so callers branch with errors.Is. Never returns an optimistic /
// cached value — the whole point is to defeat a stale "still open" view
// held by a long-lived WS reader after an agent closed the chat.
func ReloadOpenSession(ctx context.Context, store sessionGetter, serverID string, id uuid.UUID) (Session, error) {
	sess, err := store.GetSession(ctx, serverID, id)
	if err != nil {
		return Session{}, err
	}
	if IsTerminalStatus(sess.Status) {
		return sess, ErrSessionClosed
	}
	return sess, nil
}

// closableStore is the store surface CloseSession needs: read the
// current state (to detect an already-terminal session) and persist
// the closed transition. *Store and the stream server's sessionStore
// both satisfy it.
type closableStore interface {
	GetSession(ctx context.Context, serverID string, id uuid.UUID) (Session, error)
	MarkSessionClosed(ctx context.Context, serverID string, id uuid.UUID, reason string) (Session, error)
}

// CloseSession is the one close path every agent-close surface funnels
// through (REST CloseSession RPC, agent WS "close", agent gRPC stream
// Close). It:
//
//   - reads the current session; if it is already terminal the call is
//     a no-op success (transitioned=false) — no duplicate persist, no
//     duplicate broadcast. This is the idempotency guarantee.
//   - otherwise persists status=closed and, before returning, marks the
//     session closed in the in-process hub and broadcasts the
//     authoritative session_closed frame to every subscriber (visitor +
//     any agents). The hub flag is set under the hub's lock so a
//     visitor send racing the close observes "closed" the instant this
//     returns — closure always wins.
//
// An explicit close is deliberately distinct from an agent merely
// disconnecting: a transient WS drop never calls this, so it never
// marks the session closed or broadcasts session_closed. Only a real
// close does.
//
// hub may be nil (unit tests / a boot window before the hub exists);
// the persist still happens, the broadcast is simply skipped.
func CloseSession(
	ctx context.Context,
	store closableStore,
	hub *Hub,
	serverID string,
	id uuid.UUID,
	reason string,
) (sess Session, transitioned bool, err error) {
	prev, err := store.GetSession(ctx, serverID, id)
	if err != nil {
		return Session{}, false, err
	}
	if IsTerminalStatus(prev.Status) {
		// Already terminal: idempotent no-op. Refresh the in-process flag
		// (a no-op unless a subscriber is currently live on the session)
		// but do not re-broadcast.
		hub.MarkClosed(id)
		return prev, false, nil
	}
	next, err := store.MarkSessionClosed(ctx, serverID, id, reason)
	if err != nil {
		return Session{}, false, err
	}
	// Flip the in-process flag first so any visitor send that reaches
	// the reader after this point is rejected even before the broadcast
	// lands, then fan the terminal frame out to the room.
	hub.MarkClosed(id)
	hub.Publish(id, SessionClosedFrame(reason), nil)
	return next, true, nil
}
