// Package chat implements the ChatService gRPC API — admin-side
// REST endpoints for browsing chat history, taking sessions,
// posting agent replies, and searching the message corpus.
//
// The visitor-facing surface (POST /chat/start, WS /chat/ws) lives
// in server/chat_*.go because gRPC doesn't speak WebSocket and the
// start handler has too many auxiliary concerns to fit cleanly in
// a service definition. Both surfaces share pkg/chat (store, hub,
// router, service helpers).
//
// Permission annotations on chat.proto gate every endpoint via the
// existing authware interceptor:
//
//   reads  → server.{server_id}.analytics.read
//   writes → server.{server_id}.admin
//
// So this package doesn't repeat the ACL check; it trusts the
// interceptor and reads claims when it needs the agent username
// (PostAdminMessage's sender_id, TakeSession's assigned_agent_id).

package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

// timeNow is a package-level alias so tests can stub it (and so
// the When-stamping convention lives in one place — the store
// also defaults to time.Now().UTC() if a caller forgets).
var timeNow = func() time.Time { return time.Now().UTC() }

// LiveSessionsView is the small read-only interface the chat
// service uses to surface hub + router state on /chat/admin/queue
// and /chat/admin/live-sessions. Implemented by pkg/chat/hub +
// pkg/chat/router (stages 4b.4 / 4b.6); 4b.2 ships a no-op default
// so the RPC compiles even before those stages land.
type LiveSessionsView interface {
	// AgentsFor returns the set of agent usernames currently
	// subscribed to the per-session WS for sessionID.
	AgentsFor(sessionID uuid.UUID) []string
	// VisitorOnline returns true when at least one visitor WS is
	// open against sessionID.
	VisitorOnline(sessionID uuid.UUID) bool
}

// noopLiveView returns "no agents, visitor offline" for every
// session — the safe default until the hub is wired up.
type noopLiveView struct{}

func (noopLiveView) AgentsFor(uuid.UUID) []string { return nil }
func (noopLiveView) VisitorOnline(uuid.UUID) bool { return false }

// ACLResolution is the small claim-resolved access bundle the chat
// service consults per-RPC. Mirrors pkg/api/v1/analytics's
// ACLResolution exactly so the wire-up in server/chat_acl.go can
// reuse the same Bolt-backed lookup logic.
type ACLResolution struct {
	// Allowed is the explicit list of server_ids the caller can
	// touch. Empty means no access.
	Allowed []string
	// Superadmin: skip the intersection step. Reserved for the
	// admin / root roles populated by AdminBearerInterceptor.
	Superadmin bool
}

// ACLLookup is the per-RPC hook that produces an ACLResolution
// from an incoming gRPC context. The unified server passes a
// closure bound to the live config so we don't pull config into
// this package.
type ACLLookup func(ctx context.Context) ACLResolution

// Server implements chatspec.ChatServiceServer.
type Server struct {
	chatspec.UnimplementedChatServiceServer
	store *chatpkg.Store
	live  LiveSessionsView
	acl   ACLLookup
}

// New constructs the service.
//
//	store — ClickHouse-backed persistence (must be non-nil at runtime;
//	        nil-tolerant for unit tests, where every RPC will return
//	        a graceful "DB unavailable").
//	live  — hub-backed presence view; nil → noop (safe default until
//	        stages 4b.4/4b.6 wire the real hub).
//	acl   — claim → allowed-server-ids resolver. nil → "deny all"
//	        (a fail-closed default that prevents the kind of
//	        no-auth-bypass the 4b.2 smoke test caught).
func New(store *chatpkg.Store, live LiveSessionsView, acl ACLLookup) *Server {
	if live == nil {
		live = noopLiveView{}
	}
	if acl == nil {
		acl = func(context.Context) ACLResolution { return ACLResolution{} }
	}
	return &Server{store: store, live: live, acl: acl}
}

// authorize is called at the top of every RPC. Returns Unauthenticated
// when there are no claims on the context, PermissionDenied when the
// caller has claims but no access to serverID. Empty serverID
// short-circuits to InvalidArgument so callers don't have to repeat
// the check.
//
// Superadmins (admin/root roles) get an Allowed list populated with
// every configured server_id by the unified-server-side hook
// (server/chat_acl.go). We deliberately *don't* short-circuit on
// the Superadmin flag — restricting even superadmins to the set of
// configured servers stops typos against `?server_id=xyz` from
// silently returning empty data, which is what analytics' builder
// does too.
func (s *Server) authorize(ctx context.Context, serverID string) error {
	if serverID == "" {
		return status.Error(codes.InvalidArgument, "server_id required")
	}
	if _, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); !ok {
		return status.Error(codes.Unauthenticated, "no claims in context")
	}
	res := s.acl(ctx)
	for _, id := range res.Allowed {
		if id == serverID {
			return nil
		}
	}
	return status.Errorf(codes.PermissionDenied, "no access to server %q", serverID)
}

// callerUsername extracts the admin username from the request's
// authware claims. Returns "" when no claims are attached, which
// PostAdminMessage / TakeSession treat as an internal error (the
// interceptor should always populate them on annotated routes).
func callerUsername(ctx context.Context) string {
	if c, ok := ctx.Value(authware.ClaimsKey).(*authware.Claims); ok && c != nil {
		return c.Username
	}
	return ""
}

func mustParseSession(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}
	return id, nil
}

func sessionToProto(s chatpkg.Session) *chatspec.ChatSession {
	out := &chatspec.ChatSession{
		Id:              s.ID.String(),
		ServerId:        s.ServerID,
		VisitorId:       s.VisitorID,
		VisitorEmail:    s.VisitorEmail,
		VisitorCountry:  s.VisitorCountry,
		VisitorDevice:   s.VisitorDevice,
		VisitorIp:       s.VisitorIP,
		UserAgent:       s.UserAgent,
		MessageCount:    s.MessageCount,
		Status:          statusStringToEnum(s.Status),
		AssignedAgentId: s.AssignedAgentID,
		Meta:            s.Meta,
	}
	if !s.StartedAt.IsZero() {
		out.StartedAt = timestamppb.New(s.StartedAt)
	}
	if s.ClosedAt != nil && !s.ClosedAt.IsZero() {
		out.ClosedAt = timestamppb.New(*s.ClosedAt)
	}
	if !s.LastMessageAt.IsZero() {
		out.LastMessageAt = timestamppb.New(s.LastMessageAt)
	}
	if s.AssignedAt != nil && !s.AssignedAt.IsZero() {
		out.AssignedAt = timestamppb.New(*s.AssignedAt)
	}
	return out
}

func messageToProto(m chatpkg.Message) *chatspec.ChatMessage {
	out := &chatspec.ChatMessage{
		Id:        m.ID.String(),
		SessionId: m.SessionID.String(),
		ServerId:  m.ServerID,
		VisitorId: m.VisitorID,
		Direction: directionStringToEnum(m.Direction),
		SenderId:  m.SenderID,
		Content:   m.Content,
	}
	if !m.When.IsZero() {
		out.When = timestamppb.New(m.When)
	}
	return out
}

func statusStringToEnum(s string) chatspec.ChatSessionStatus {
	switch s {
	case chatpkg.StatusQueued:
		return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_QUEUED
	case chatpkg.StatusAssigned:
		return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_ASSIGNED
	case chatpkg.StatusOpen:
		return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_OPEN
	case chatpkg.StatusClosed:
		return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_CLOSED
	case chatpkg.StatusExpired:
		return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_EXPIRED
	}
	return chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_UNSPECIFIED
}

func statusEnumToString(e chatspec.ChatSessionStatus) string {
	switch e {
	case chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_QUEUED:
		return chatpkg.StatusQueued
	case chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_ASSIGNED:
		return chatpkg.StatusAssigned
	case chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_OPEN:
		return chatpkg.StatusOpen
	case chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_CLOSED:
		return chatpkg.StatusClosed
	case chatspec.ChatSessionStatus_CHAT_SESSION_STATUS_EXPIRED:
		return chatpkg.StatusExpired
	}
	return ""
}

func directionStringToEnum(s string) chatspec.ChatMessageDirection {
	switch s {
	case chatpkg.DirVisitor:
		return chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_VISITOR
	case chatpkg.DirAgent:
		return chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_AGENT
	case chatpkg.DirSystem:
		return chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_SYSTEM
	case chatpkg.DirBot:
		return chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_BOT
	}
	return chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_UNSPECIFIED
}

// --- RPCs --------------------------------------------------------

func (s *Server) ListSessions(ctx context.Context, req *chatspec.ListSessionsRequest) (*chatspec.ListSessionsResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	filter := chatpkg.ListSessionsFilter{
		ServerID: req.GetServerId(),
		Q:        req.GetQ(),
		Limit:    req.GetLimit(),
		Offset:   req.GetOffset(),
	}
	for _, st := range req.GetStatus() {
		v := statusEnumToString(st)
		if v != "" {
			filter.Statuses = append(filter.Statuses, v)
		}
	}
	if req.GetFrom() != nil {
		filter.From = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		filter.To = req.GetTo().AsTime()
	}
	rows, total, err := s.store.ListSessions(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list sessions: %v", err)
	}
	out := &chatspec.ListSessionsResponse{TotalCount: total}
	for _, r := range rows {
		out.Sessions = append(out.Sessions, sessionToProto(r))
	}
	return out, nil
}

func (s *Server) GetSession(ctx context.Context, req *chatspec.GetSessionRequest) (*chatspec.ChatSession, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	sess, err := s.store.GetSession(ctx, req.GetServerId(), id)
	if errors.Is(err, chatpkg.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	return sessionToProto(sess), nil
}

func (s *Server) GetMessages(ctx context.Context, req *chatspec.GetMessagesRequest) (*chatspec.GetMessagesResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	rows, total, err := s.store.ListMessages(ctx, req.GetServerId(), id, req.GetLimit(), req.GetOffset())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list messages: %v", err)
	}
	out := &chatspec.GetMessagesResponse{TotalCount: total}
	for _, r := range rows {
		out.Messages = append(out.Messages, messageToProto(r))
	}
	return out, nil
}

func (s *Server) PostAdminMessage(ctx context.Context, req *chatspec.PostAdminMessageRequest) (*chatspec.ChatMessage, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetContent() == "" {
		return nil, status.Error(codes.InvalidArgument, "content required")
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	user := callerUsername(ctx)
	if user == "" {
		return nil, status.Error(codes.Internal, "missing claims (interceptor not run?)")
	}
	sess, err := s.store.GetSession(ctx, req.GetServerId(), id)
	if errors.Is(err, chatpkg.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess.Status == chatpkg.StatusClosed {
		return nil, status.Error(codes.FailedPrecondition, "session closed")
	}
	msg := chatpkg.Message{
		ID:        uuid.New(),
		SessionID: id,
		ServerID:  req.GetServerId(),
		VisitorID: sess.VisitorID,
		Direction: chatpkg.DirAgent,
		SenderID:  user,
		Content:   req.GetContent(),
		When:      timeNow(),
	}
	if err := s.store.AppendMessage(ctx, msg); err != nil {
		return nil, status.Errorf(codes.Internal, "append message: %v", err)
	}
	return messageToProto(msg), nil
}

func (s *Server) CloseSession(ctx context.Context, req *chatspec.CloseSessionRequest) (*chatspec.CloseSessionResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	sess, err := s.store.MarkSessionClosed(ctx, req.GetServerId(), id, req.GetReason())
	if errors.Is(err, chatpkg.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "close session: %v", err)
	}
	return &chatspec.CloseSessionResponse{Session: sessionToProto(sess)}, nil
}

func (s *Server) TakeSession(ctx context.Context, req *chatspec.TakeSessionRequest) (*chatspec.ChatSession, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	user := callerUsername(ctx)
	if user == "" {
		return nil, status.Error(codes.Internal, "missing claims")
	}
	cur, err := s.store.GetSession(ctx, req.GetServerId(), id)
	if errors.Is(err, chatpkg.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if cur.Status == chatpkg.StatusClosed {
		return nil, status.Error(codes.FailedPrecondition, "session closed")
	}
	if cur.AssignedAgentID != "" && cur.AssignedAgentID != user && !req.GetForce() {
		return nil, status.Errorf(codes.AlreadyExists,
			"session already assigned to %q (use force=1 to override)", cur.AssignedAgentID)
	}
	sess, err := s.store.AssignSession(ctx, req.GetServerId(), id, user)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "assign session: %v", err)
	}
	return sessionToProto(sess), nil
}

func (s *Server) ReleaseSession(ctx context.Context, req *chatspec.ReleaseSessionRequest) (*chatspec.ChatSession, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	id, err := mustParseSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	sess, err := s.store.ReleaseSession(ctx, req.GetServerId(), id)
	if errors.Is(err, chatpkg.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "release session: %v", err)
	}
	return sessionToProto(sess), nil
}

func (s *Server) GetQueue(ctx context.Context, req *chatspec.GetQueueRequest) (*chatspec.GetQueueResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	queued, _, err := s.store.ListSessions(ctx, chatpkg.ListSessionsFilter{
		ServerID: req.GetServerId(),
		Statuses: []string{chatpkg.StatusQueued},
		Limit:    100,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "queue: %v", err)
	}
	assigned, _, err := s.store.ListSessions(ctx, chatpkg.ListSessionsFilter{
		ServerID: req.GetServerId(),
		Statuses: []string{chatpkg.StatusAssigned},
		Limit:    100,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "queue assigned: %v", err)
	}
	out := &chatspec.GetQueueResponse{}
	for _, r := range queued {
		out.Queued = append(out.Queued, sessionToProto(r))
	}
	for _, r := range assigned {
		out.Assigned = append(out.Assigned, sessionToProto(r))
	}
	return out, nil
}

func (s *Server) GetLiveSessions(ctx context.Context, req *chatspec.GetLiveSessionsRequest) (*chatspec.GetLiveSessionsResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	rows, _, err := s.store.ListSessions(ctx, chatpkg.ListSessionsFilter{
		ServerID: req.GetServerId(),
		Statuses: []string{chatpkg.StatusAssigned, chatpkg.StatusOpen},
		Limit:    200,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "live sessions: %v", err)
	}
	out := &chatspec.GetLiveSessionsResponse{}
	for _, r := range rows {
		live := &chatspec.LiveSession{
			SessionId:     r.ID.String(),
			VisitorId:     r.VisitorID,
			VisitorEmail:  r.VisitorEmail,
			VisitorOnline: s.live.VisitorOnline(r.ID),
			Agents:        s.live.AgentsFor(r.ID),
		}
		if !r.LastMessageAt.IsZero() {
			live.LastMessageAt = timestamppb.New(r.LastMessageAt)
		}
		out.Sessions = append(out.Sessions, live)
	}
	return out, nil
}

func (s *Server) SearchMessages(ctx context.Context, req *chatspec.SearchMessagesRequest) (*chatspec.SearchMessagesResponse, error) {
	if err := s.authorize(ctx, req.GetServerId()); err != nil {
		return nil, err
	}
	if req.GetQ() == "" {
		return &chatspec.SearchMessagesResponse{}, nil
	}
	filter := chatpkg.SearchFilter{
		ServerID: req.GetServerId(),
		Q:        req.GetQ(),
		Limit:    req.GetLimit(),
		Offset:   req.GetOffset(),
	}
	if req.GetFrom() != nil {
		filter.From = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		filter.To = req.GetTo().AsTime()
	}
	rows, total, err := s.store.SearchMessages(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search: %v", err)
	}
	out := &chatspec.SearchMessagesResponse{TotalCount: total}
	for _, r := range rows {
		out.Messages = append(out.Messages, messageToProto(r))
	}
	return out, nil
}

// Compile-time interface assertion.
var _ chatspec.ChatServiceServer = (*Server)(nil)

// quiet unused import on builds when this package is the only
// referenced symbol from `fmt`.
var _ = fmt.Sprintf
