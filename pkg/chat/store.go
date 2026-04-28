// Package chat is the Phase-4b visitor-chat backend. The package is
// split into a few small files:
//
//   store.go    — ClickHouse readers/writers for chat_sessions +
//                 chat_messages (this file).
//   service.go  — Higher-level operations the WS handlers and the
//                 admin RPCs share (start session, append message,
//                 close session, search). Stage 4b.4 fills it in.
//   token.go    — Chat-session JWT issuance + verification (4b.3).
//   captcha/    — Turnstile / reCAPTCHA (4b.3).
//   emailverify/— AfterShip email-verifier wrapper (4b.3).
//   hub.go      — Per-session pub/sub; visitor + N agents (4b.4/4b.5).
//   router.go   — Next-available routing + queue (4b.6).
//
// 4b.2 only needs the store: every other piece reads/writes through
// it.
package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by GetSession / GetMessages helpers when
// the requested row doesn't exist. Callers translate to a gRPC
// NotFound at the RPC boundary.
var ErrNotFound = errors.New("chat: not found")

// SessionStatus mirrors the proto enum + the LowCardinality(String)
// column on chat_sessions. Kept as bare strings so the SQL writers
// don't have to cross the proto boundary.
const (
	StatusQueued   = "queued"
	StatusAssigned = "assigned"
	StatusOpen     = "open"
	StatusClosed   = "closed"
	StatusExpired  = "expired"
)

// MessageDirection mirrors chat_messages.direction.
const (
	DirVisitor = "visitor"
	DirAgent   = "agent"
	DirSystem  = "system"
	DirBot     = "bot"
)

// Session is the in-Go shape; the store hands these to higher
// layers. Times are UTC.
type Session struct {
	ID              uuid.UUID
	ServerID        string
	VisitorID       string
	VisitorEmail    string
	VisitorCountry  string
	VisitorDevice   string
	VisitorIP       string
	UserAgent       string
	StartedAt       time.Time
	ClosedAt        *time.Time
	LastMessageAt   time.Time
	MessageCount    uint32
	Status          string
	AssignedAgentID string
	AssignedAt      *time.Time
	Meta            string
}

// Message is the in-Go shape for one chat_messages row.
type Message struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	ServerID  string
	VisitorID string
	Direction string
	SenderID  string
	Content   string
	When      time.Time
}

// Store is the ClickHouse-backed persistence layer for chat. Holds
// a *sql.DB; safe to share across goroutines (database/sql handles
// the pool internally). Construct with NewStore(db).
type Store struct {
	db *sql.DB
}

// NewStore returns a Store wrapping db. Passing nil is valid — the
// store methods all return an "analytics DB unavailable" error in
// that case so callers degrade gracefully (matching the analytics
// package's pattern).
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// DB returns the underlying *sql.DB so that other Phase-4b helpers
// (the search RPC, the ForgetVisitor extension) can issue ad-hoc
// queries without the store wrapping every shape they need.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) checkDB() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("chat: analytics DB unavailable")
	}
	return nil
}

// --- Sessions ---------------------------------------------------

// CreateSession persists a brand-new session row with status=queued
// (or whatever caller-supplied status). Caller assigns the UUID up
// front so it can be returned to the visitor in the chat token
// before the INSERT acks.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	if err := s.checkDB(); err != nil {
		return err
	}
	if sess.ID == uuid.Nil || sess.ServerID == "" {
		return fmt.Errorf("chat: id and server_id required")
	}
	if sess.Status == "" {
		sess.Status = StatusQueued
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now().UTC()
	}
	if sess.LastMessageAt.IsZero() {
		sess.LastMessageAt = sess.StartedAt
	}
	const q = `
INSERT INTO chat_sessions
  (id, server_id, visitor_id, visitor_email,
   visitor_country, visitor_device, visitor_ip, user_agent,
   started_at, closed_at, last_message_at, message_count,
   status, assigned_agent_id, assigned_at, meta)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	visitorIP := sess.VisitorIP
	if visitorIP == "" {
		visitorIP = "::"
	}
	_, err := s.db.ExecContext(ctx, q,
		sess.ID, sess.ServerID, sess.VisitorID, sess.VisitorEmail,
		sess.VisitorCountry, sess.VisitorDevice, visitorIP, sess.UserAgent,
		sess.StartedAt, nullableTime(sess.ClosedAt), sess.LastMessageAt, sess.MessageCount,
		sess.Status, sess.AssignedAgentID, nullableTime(sess.AssignedAt), sess.Meta,
	)
	if err != nil {
		return fmt.Errorf("chat: insert session: %w", err)
	}
	return nil
}

// GetSession returns the latest version of (server_id, session_id)
// after the ReplacingMergeTree FINAL collapse.
func (s *Store) GetSession(ctx context.Context, serverID string, id uuid.UUID) (Session, error) {
	if err := s.checkDB(); err != nil {
		return Session{}, err
	}
	const q = `
SELECT id, server_id, visitor_id, visitor_email,
       visitor_country, visitor_device, IPv6NumToString(visitor_ip), user_agent,
       started_at, closed_at, last_message_at, message_count,
       status, assigned_agent_id, assigned_at, meta
FROM chat_sessions FINAL
WHERE server_id = ? AND id = ?
LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, serverID, id)
	out, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return out, err
}

// ListSessionsFilter narrows ListSessions. Empty fields = no filter.
type ListSessionsFilter struct {
	ServerID string
	Statuses []string  // OR'd
	From     time.Time // zero = no lower bound
	To       time.Time // zero = no upper bound
	Q        string    // case-insensitive substring on visitor_email/visitor_id/assigned_agent_id
	Limit    uint32
	Offset   uint32
}

// ListSessions returns matching sessions newest-first, plus the
// pre-paging total.
func (s *Store) ListSessions(ctx context.Context, f ListSessionsFilter) ([]Session, uint64, error) {
	if err := s.checkDB(); err != nil {
		return nil, 0, err
	}
	if f.ServerID == "" {
		return nil, 0, fmt.Errorf("chat: server_id required")
	}

	var (
		args  []any
		conds = []string{"server_id = ?"}
	)
	args = append(args, f.ServerID)
	if len(f.Statuses) > 0 {
		ph := strings.Repeat("?,", len(f.Statuses))
		conds = append(conds, "status IN ("+ph[:len(ph)-1]+")")
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	if !f.From.IsZero() {
		conds = append(conds, "started_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		conds = append(conds, "started_at <= ?")
		args = append(args, f.To)
	}
	if f.Q != "" {
		conds = append(conds,
			"(positionCaseInsensitive(visitor_email, ?) > 0"+
				" OR positionCaseInsensitive(visitor_id, ?) > 0"+
				" OR positionCaseInsensitive(assigned_agent_id, ?) > 0)")
		args = append(args, f.Q, f.Q, f.Q)
	}
	where := strings.Join(conds, " AND ")

	limit := uint32(50)
	if f.Limit > 0 {
		limit = f.Limit
	}
	if limit > 1000 {
		limit = 1000
	}

	listQ := `
SELECT id, server_id, visitor_id, visitor_email,
       visitor_country, visitor_device, IPv6NumToString(visitor_ip), user_agent,
       started_at, closed_at, last_message_at, message_count,
       status, assigned_agent_id, assigned_at, meta
FROM chat_sessions FINAL
WHERE ` + where + `
ORDER BY started_at DESC, id DESC
LIMIT ? OFFSET ?`
	listArgs := append(append([]any(nil), args...), limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, listQ, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("chat: list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	countQ := `SELECT count() FROM chat_sessions FINAL WHERE ` + where
	var total uint64
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("chat: count sessions: %w", err)
	}
	return out, total, nil
}

// UpdateSessionMutator returns a Session reflecting the desired
// post-update state; the store INSERTs a new row (ReplacingMergeTree
// merges on read). The mutator is given the current Session and
// must return the next one. Returns ErrNotFound if the session
// doesn't exist.
func (s *Store) UpdateSession(
	ctx context.Context,
	serverID string,
	id uuid.UUID,
	mutate func(Session) Session,
) (Session, error) {
	cur, err := s.GetSession(ctx, serverID, id)
	if err != nil {
		return Session{}, err
	}
	next := mutate(cur)
	// Always bump last_message_at so ReplacingMergeTree's version
	// column favours the newer row even when the caller didn't.
	if !next.LastMessageAt.After(cur.LastMessageAt) {
		next.LastMessageAt = time.Now().UTC()
	}
	if err := s.CreateSession(ctx, next); err != nil {
		return Session{}, err
	}
	return next, nil
}

// MarkSessionClosed flips status to "closed" and stamps closed_at.
// Idempotent — re-closing a closed session is a no-op.
func (s *Store) MarkSessionClosed(ctx context.Context, serverID string, id uuid.UUID, _ string) (Session, error) {
	return s.UpdateSession(ctx, serverID, id, func(cur Session) Session {
		if cur.Status == StatusClosed {
			return cur
		}
		now := time.Now().UTC()
		cur.Status = StatusClosed
		cur.ClosedAt = &now
		cur.LastMessageAt = now
		return cur
	})
}

// AssignSession sets assigned_agent_id + assigned_at + status=assigned
// in one INSERT. The router calls it whenever next-available picks
// an agent or an admin TakeSession()s a queued/released session.
func (s *Store) AssignSession(ctx context.Context, serverID string, id uuid.UUID, agent string) (Session, error) {
	return s.UpdateSession(ctx, serverID, id, func(cur Session) Session {
		now := time.Now().UTC()
		cur.AssignedAgentID = agent
		cur.AssignedAt = &now
		if cur.Status == StatusQueued {
			cur.Status = StatusAssigned
		}
		cur.LastMessageAt = now
		return cur
	})
}

// ReleaseSession clears the assignment and re-queues. Called when an
// admin clicks "Release" or the router auto-releases on agent
// disconnect after the grace window.
func (s *Store) ReleaseSession(ctx context.Context, serverID string, id uuid.UUID) (Session, error) {
	return s.UpdateSession(ctx, serverID, id, func(cur Session) Session {
		now := time.Now().UTC()
		cur.AssignedAgentID = ""
		cur.AssignedAt = nil
		if cur.Status == StatusAssigned || cur.Status == StatusOpen {
			cur.Status = StatusQueued
		}
		cur.LastMessageAt = now
		return cur
	})
}

// MarkSessionOpen flips a session into OPEN status (used when an
// agent's per-session WS connects and starts driving the chat).
func (s *Store) MarkSessionOpen(ctx context.Context, serverID string, id uuid.UUID) (Session, error) {
	return s.UpdateSession(ctx, serverID, id, func(cur Session) Session {
		now := time.Now().UTC()
		if cur.Status == StatusClosed {
			return cur
		}
		cur.Status = StatusOpen
		cur.LastMessageAt = now
		return cur
	})
}

// --- Messages ---------------------------------------------------

// AppendMessage persists one chat_messages row. Caller assigns the
// UUID and the When timestamp; the store also bumps the parent
// chat_sessions.message_count + last_message_at via UpdateSession.
func (s *Store) AppendMessage(ctx context.Context, m Message) error {
	if err := s.checkDB(); err != nil {
		return err
	}
	if m.ID == uuid.Nil || m.SessionID == uuid.Nil || m.ServerID == "" {
		return fmt.Errorf("chat: id/session_id/server_id required")
	}
	if m.Direction == "" {
		return fmt.Errorf("chat: direction required")
	}
	if m.When.IsZero() {
		m.When = time.Now().UTC()
	}
	const q = `
INSERT INTO chat_messages
  (id, session_id, server_id, visitor_id, direction, sender_id, content, ` + "`when`" + `)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, q,
		m.ID, m.SessionID, m.ServerID, m.VisitorID, m.Direction, m.SenderID, m.Content, m.When,
	); err != nil {
		return fmt.Errorf("chat: insert message: %w", err)
	}
	// Bump the session's denormalised counters. Best-effort: a stale
	// counter is cosmetic only (ReplacingMergeTree FINAL still gives
	// us monotonic last_message_at).
	if _, err := s.UpdateSession(ctx, m.ServerID, m.SessionID, func(cur Session) Session {
		cur.MessageCount++
		cur.LastMessageAt = m.When
		return cur
	}); err != nil {
		return fmt.Errorf("chat: bump session counters: %w", err)
	}
	return nil
}

// ListMessages returns the message timeline for a session. Older
// first by default (matches the SPA thread render order). Returns
// the pre-paging total count.
func (s *Store) ListMessages(ctx context.Context, serverID string, sessionID uuid.UUID, limit, offset uint32) ([]Message, uint64, error) {
	if err := s.checkDB(); err != nil {
		return nil, 0, err
	}
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = `
SELECT id, session_id, server_id, visitor_id, direction, sender_id, content, ` + "`when`" + `
FROM chat_messages
WHERE server_id = ? AND session_id = ?
ORDER BY ` + "`when`" + ` ASC, id ASC
LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, serverID, sessionID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("chat: list messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	const countQ = `SELECT count() FROM chat_messages WHERE server_id = ? AND session_id = ?`
	var total uint64
	if err := s.db.QueryRowContext(ctx, countQ, serverID, sessionID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("chat: count messages: %w", err)
	}
	return out, total, nil
}

// SearchFilter narrows SearchMessages.
type SearchFilter struct {
	ServerID string
	Q        string
	From     time.Time
	To       time.Time
	Limit    uint32
	Offset   uint32
}

// SearchMessages runs the FTS query (multiSearchAny over the
// content column, backed by tokenbf_v1 + ngrambf_v1 skip indexes
// declared in schema/chat_v1.sql). Tokens are extracted by
// whitespace-splitting q + lowercasing. Returns matched messages
// newest-first.
func (s *Store) SearchMessages(ctx context.Context, f SearchFilter) ([]Message, uint64, error) {
	if err := s.checkDB(); err != nil {
		return nil, 0, err
	}
	if f.ServerID == "" {
		return nil, 0, fmt.Errorf("chat: server_id required")
	}
	tokens := tokenize(f.Q)
	if len(tokens) == 0 {
		return nil, 0, nil
	}
	limit := uint32(50)
	if f.Limit > 0 {
		limit = f.Limit
	}
	if limit > 500 {
		limit = 500
	}
	conds := []string{"server_id = ?"}
	args := []any{f.ServerID}
	if !f.From.IsZero() {
		conds = append(conds, "`when` >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		conds = append(conds, "`when` <= ?")
		args = append(args, f.To)
	}
	conds = append(conds, "multiSearchAnyCaseInsensitive(content, ?) > 0")
	args = append(args, tokens)
	where := strings.Join(conds, " AND ")

	listQ := `
SELECT id, session_id, server_id, visitor_id, direction, sender_id, content, ` + "`when`" + `
FROM chat_messages
WHERE ` + where + `
ORDER BY ` + "`when`" + ` DESC, id DESC
LIMIT ? OFFSET ?`
	listArgs := append(append([]any(nil), args...), limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, listQ, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("chat: search messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	countQ := `SELECT count() FROM chat_messages WHERE ` + where
	var total uint64
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("chat: count search: %w", err)
	}
	return out, total, nil
}

// ForgetVisitor deletes every chat row for (server_id, visitor_id)
// — both messages and the session metadata. Reused by the existing
// /analytics/forget RPC's GDPR-erasure extension. ALTER … DELETE
// is async on ClickHouse; the call returns once the mutation is
// queued.
func (s *Store) ForgetVisitor(ctx context.Context, serverID, visitorID string) error {
	if err := s.checkDB(); err != nil {
		return err
	}
	if serverID == "" || visitorID == "" {
		return fmt.Errorf("chat: server_id and visitor_id required")
	}
	for _, table := range []string{"chat_messages", "chat_sessions"} {
		if _, err := s.db.ExecContext(ctx,
			"ALTER TABLE "+table+" DELETE WHERE server_id = ? AND visitor_id = ?",
			serverID, visitorID,
		); err != nil {
			return fmt.Errorf("chat: forget visitor %s: %w", table, err)
		}
	}
	return nil
}

// --- internals --------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(r rowScanner) (Session, error) {
	var (
		s         Session
		closedAt  sql.NullTime
		assignAt  sql.NullTime
		visitorIP string
	)
	if err := r.Scan(
		&s.ID, &s.ServerID, &s.VisitorID, &s.VisitorEmail,
		&s.VisitorCountry, &s.VisitorDevice, &visitorIP, &s.UserAgent,
		&s.StartedAt, &closedAt, &s.LastMessageAt, &s.MessageCount,
		&s.Status, &s.AssignedAgentID, &assignAt, &s.Meta,
	); err != nil {
		return Session{}, err
	}
	s.VisitorIP = visitorIP
	if closedAt.Valid {
		t := closedAt.Time
		s.ClosedAt = &t
	}
	if assignAt.Valid {
		t := assignAt.Time
		s.AssignedAt = &t
	}
	return s, nil
}

func scanMessage(r rowScanner) (Message, error) {
	var m Message
	if err := r.Scan(
		&m.ID, &m.SessionID, &m.ServerID, &m.VisitorID,
		&m.Direction, &m.SenderID, &m.Content, &m.When,
	); err != nil {
		return Message{}, err
	}
	return m, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

// tokenize splits q on whitespace and lowercases each token. Used
// by SearchMessages — multiSearchAnyCaseInsensitive expects an
// Array(String) of literal tokens. Empty tokens (consecutive
// whitespace, leading/trailing) are dropped.
func tokenize(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	parts := strings.Fields(q)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
