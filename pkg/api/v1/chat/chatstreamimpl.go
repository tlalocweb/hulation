package chat

// ChatStreamService — bidirectional / server-streaming chat RPCs that replace the two
// WebSocket endpoints in `server/chat_ws_agent.go` + `server/chat_ws_control.go`.
//
// The Go implementation here is a thin bridge: chat fan-out still happens through the
// per-process `chatpkg.Hub` (JSON []byte frames keyed by session_id). The WS handlers
// continue to publish/subscribe to that hub, and so does this gRPC server — every frame
// flows through the same place, so WS + gRPC clients can coexist on the same session
// during the transition.
//
// Frame translation:
//   - The reader goroutine receives proto `AgentClientFrame` from the gRPC stream and
//     converts to JSON map frames before publishing to the hub (matching the existing WS
//     wire format).
//   - The writer goroutine receives JSON frames from its `Subscriber.Out` channel,
//     translates them to proto `AgentServerFrame`, and emits on the gRPC stream.
//
// Auth: an `AdminBearerStreamInterceptor` (paired with the existing unary one) populates
// `authware.Claims` on the stream's context; we read it here. The chat ACL closure is
// shared with `ChatService.Server.acl` so per-server access stays consistent.
//
// Noise: the eventual Noise_IK session-wrap layer wraps these frames — `SubscribeAgent`
// and `ControlSubscribe` become Noise handshake messages, subsequent frames are Noise
// transport messages. That layer doesn't change the public interface here.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tlalocweb/hulation/log"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
)

// StreamServer is the running instance bound to the gRPC server.
type StreamServer struct {
	chatspec.UnimplementedChatStreamServiceServer
	store             *chatpkg.Store
	hubFn             func() *chatpkg.Hub
	routerFn          func() *chatpkg.Router
	acl               ACLLookup
	noiseStaticSecret []byte // 32-byte X25519 private key; nil disables Noise mode.
}

// NewStreamServer wires the dependencies. `hubFn` / `routerFn` are lazy accessors — the
// hub + router singletons aren't constructed until `registerChatPublic()` runs, which is
// AFTER service registration. Passing closures avoids the temporal coupling.
//
// `noiseStaticSecret` (optional) is the server's 32-byte X25519 private key used as the
// responder static key in Noise_IK handshakes. When nil, clients attempting Noise mode
// receive an error frame; plaintext streams still work.
func NewStreamServer(
	store *chatpkg.Store,
	hubFn func() *chatpkg.Hub,
	routerFn func() *chatpkg.Router,
	acl ACLLookup,
	noiseStaticSecret []byte,
) *StreamServer {
	return &StreamServer{
		store:             store,
		hubFn:             hubFn,
		routerFn:          routerFn,
		acl:               acl,
		noiseStaticSecret: noiseStaticSecret,
	}
}

func resolutionAllows(r ACLResolution, serverID string) bool {
	if r.Superadmin {
		return true
	}
	for _, s := range r.Allowed {
		if s == serverID {
			return true
		}
	}
	return false
}

// AgentStream — bidirectional per-session stream. Mirrors the flow in
// server/chat_ws_agent.go: handshake → subscribe to hub → router.Ack → reader/writer
// goroutines → cleanup on exit.
//
// The first frame off the wire decides the mode:
//   - SubscribeAgent  → plaintext stream, runAgentStream on the raw stream.
//   - NoiseEnvelope   → Noise_IK responder handshake; runAgentStream is then driven via
//     a wrapper that decrypts inbound + encrypts outbound frames.
func (s *StreamServer) AgentStream(stream chatspec.ChatStreamService_AgentStreamServer) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if ne := first.GetNoise(); ne != nil {
		return s.handleNoiseAgentStream(stream, ne)
	}
	return s.runAgentStream(&firstFrameStream{
		inner: stream,
		first: first,
	})
}

// firstFrameStream replays a frame already pulled off the wire as the next Recv result,
// then delegates to the underlying stream. We use it so AgentStream can peek at the first
// frame to detect Noise mode without changing runAgentStream's contract.
type firstFrameStream struct {
	inner chatspec.ChatStreamService_AgentStreamServer
	first *chatspec.AgentClientFrame
}

func (s *firstFrameStream) Context() context.Context { return s.inner.Context() }

func (s *firstFrameStream) Recv() (*chatspec.AgentClientFrame, error) {
	if s.first != nil {
		f := s.first
		s.first = nil
		return f, nil
	}
	return s.inner.Recv()
}

func (s *firstFrameStream) Send(f *chatspec.AgentServerFrame) error { return s.inner.Send(f) }

// runAgentStream handles the per-session loop on top of an agentStream-shaped transport.
// Used by both the plaintext path (firstFrameStream) and the Noise path (noiseStreamWrapper).
func (s *StreamServer) runAgentStream(stream agentStream) error {
	ctx := stream.Context()

	claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if claims == nil || claims.Username == "" {
		return sendInnerStreamError(stream, "unauthorized", "missing or invalid bearer token")
	}
	username := claims.Username

	// Receive the SubscribeAgent handshake.
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	sub := first.GetSubscribe()
	if sub == nil {
		return sendInnerStreamError(stream, "invalid_handshake",
			"first AgentClientFrame must be SubscribeAgent")
	}
	if sub.ServerId == "" || sub.SessionId == "" {
		return sendInnerStreamError(stream, "invalid_handshake",
			"SubscribeAgent.server_id and session_id are required")
	}

	sessionID, err := uuid.Parse(sub.SessionId)
	if err != nil {
		return sendInnerStreamError(stream, "invalid_handshake", "session_id is not a UUID")
	}

	res := ACLResolution{}
	if s.acl != nil {
		res = s.acl(ctx)
	}
	if !resolutionAllows(res, sub.ServerId) {
		return sendInnerStreamError(stream, "forbidden",
			"no access to server "+sub.ServerId)
	}

	// Session lookup: scoped read first, then fall back to per-server discovery within
	// the ACL set (mirrors chat_ws_agent.go's behaviour where session_id alone may not
	// uniquely identify the partition).
	sess, err := s.store.GetSession(ctx, sub.ServerId, sessionID)
	if errors.Is(err, chatpkg.ErrNotFound) {
		for _, sid := range res.Allowed {
			if cand, candErr := s.store.GetSession(ctx, sid, sessionID); candErr == nil {
				sess = cand
				err = nil
				break
			}
		}
	}
	if err != nil || sess.ID == uuid.Nil {
		return sendInnerStreamError(stream, "session_not_found",
			"no session "+sub.SessionId)
	}
	if !resolutionAllows(res, sess.ServerID) {
		return sendInnerStreamError(stream, "forbidden",
			"no access to server "+sess.ServerID)
	}
	if sess.Status == chatpkg.StatusClosed || sess.Status == chatpkg.StatusExpired {
		return sendInnerStreamError(stream, "session_closed",
			"session is closed or expired")
	}

	hub := s.hubFn()
	if hub == nil {
		return sendInnerStreamError(stream, "hub_unavailable", "chat hub not ready")
	}

	subscriber := &chatpkg.Subscriber{
		Out:     make(chan []byte, chatpkg.SubscriberOutBufferSize),
		Role:    chatpkg.RoleAgent,
		AgentID: username,
	}
	hub.Subscribe(sessionID, subscriber)
	defer hub.Unsubscribe(sessionID, subscriber)

	// Send the SubscribedAck.
	if err := stream.Send(&chatspec.AgentServerFrame{
		Body: &chatspec.AgentServerFrame_Subscribed{
			Subscribed: &chatspec.SubscribedAck{
				SessionId: sess.ID.String(),
				Status:    mapSessionStatus(sess.Status),
			},
		},
	}); err != nil {
		return err
	}

	// Broadcast agent_joined; on exit broadcast agent_left.
	hub.Publish(sessionID, mustMarshal(map[string]any{
		"type":  "presence",
		"event": "agent_joined",
		"agent": username,
	}), subscriber)
	defer func() {
		hub.Publish(sessionID, mustMarshal(map[string]any{
			"type":  "presence",
			"event": "agent_left",
			"agent": username,
		}), subscriber)
	}()

	if sess.Status == chatpkg.StatusQueued || sess.Status == chatpkg.StatusAssigned {
		if _, err := s.store.MarkSessionOpen(ctx, sess.ServerID, sessionID); err != nil {
			log.Warnf("chat stream: mark open: %s", err)
		}
	}
	if r := s.routerFn(); r != nil {
		r.Ack(sess.ServerID, username, sessionID)
	}

	// Writer goroutine: drain subscriber.Out, translate to proto, send.
	stopWriter := make(chan struct{})
	writerErr := make(chan error, 1)
	go func() {
		defer func() { writerErr <- nil }()
		for {
			select {
			case <-stopWriter:
				return
			case raw, ok := <-subscriber.Out:
				if !ok {
					return
				}
				frame, err := translateAgentJSONToProto(raw, sess.ID)
				if err != nil || frame == nil {
					continue
				}
				if err := stream.Send(frame); err != nil {
					writerErr <- err
					return
				}
			}
		}
	}()
	defer close(stopWriter)

	// Reader loop.
	for {
		in, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch body := in.Body.(type) {
		case *chatspec.AgentClientFrame_Ping:
			_ = body
			trySend(subscriber.Out, mustMarshal(map[string]any{
				"type": "_pong",
				"ts":   time.Now().UTC().Format(time.RFC3339),
			}))
		case *chatspec.AgentClientFrame_Typing:
			active := body.Typing != nil && body.Typing.Active
			hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":   "typing",
				"from":   "agent",
				"agent":  username,
				"active": active,
			}), subscriber)
		case *chatspec.AgentClientFrame_Msg:
			content := strings.TrimSpace(body.Msg.GetContent())
			if content == "" {
				trySend(subscriber.Out, errFrame("empty_content", "message cannot be empty"))
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
			if err := s.store.AppendMessage(ctx, msg); err != nil {
				log.Warnf("chat stream append: %s", err)
				trySend(subscriber.Out, errFrame("internal", "could not persist"))
				continue
			}
			// Local echo carries the client_id back so the gRPC client can pair its
			// AgentMessage with the resulting StreamChatMessage without waiting for the
			// hub round-trip.
			trySend(subscriber.Out, mustMarshal(map[string]any{
				"type":      "msg",
				"id":        msg.ID.String(),
				"direction": "agent",
				"agent":     username,
				"content":   content,
				"ts":        msg.When.Format(time.RFC3339),
				"client_id": body.Msg.GetClientId(),
			}))
			// Hub fan-out to other subscribers (visitor WS, other agent streams).
			hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":      "msg",
				"id":        msg.ID.String(),
				"direction": "agent",
				"agent":     username,
				"content":   content,
				"ts":        msg.When.Format(time.RFC3339),
			}), subscriber)
		case *chatspec.AgentClientFrame_Close:
			_ = body
			if _, err := s.store.MarkSessionClosed(ctx, sess.ServerID, sessionID, ""); err != nil {
				log.Warnf("chat stream close: %s", err)
			}
			hub.Publish(sessionID, mustMarshal(map[string]any{
				"type":    "system",
				"content": "Session closed by agent.",
			}), nil)
			return nil
		case *chatspec.AgentClientFrame_Subscribe:
			_ = body
			trySend(subscriber.Out, errFrame("duplicate_handshake",
				"subscribe is only valid as the first frame"))
		default:
			trySend(subscriber.Out, errFrame("unknown_type", "unrecognized frame"))
		}
	}
}

// ControlStream — server-streaming per-server agent control events. Mirrors the flow in
// server/chat_ws_control.go: AgentReady → emit initial snapshot → drain slot.Out (router
// pushes session_assigned + queue_snapshot frames) → AgentGone on exit.
func (s *StreamServer) ControlStream(req *chatspec.ControlSubscribe, stream chatspec.ChatStreamService_ControlStreamServer) error {
	ctx := stream.Context()

	claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if claims == nil || claims.Username == "" {
		return sendControlError(stream, "unauthorized", "missing or invalid bearer token")
	}
	username := claims.Username

	if req.GetServerId() == "" {
		return sendControlError(stream, "invalid_request", "server_id is required")
	}
	res := ACLResolution{}
	if s.acl != nil {
		res = s.acl(ctx)
	}
	if !resolutionAllows(res, req.ServerId) {
		return sendControlError(stream, "forbidden",
			"no access to server "+req.ServerId)
	}

	router := s.routerFn()
	if router == nil {
		return sendControlError(stream, "router_unavailable", "chat router not ready")
	}

	slot := &chatpkg.AgentSlot{
		Username: username,
		Out:      make(chan []byte, chatpkg.SubscriberOutBufferSize),
		JoinedAt: time.Now().UTC(),
	}
	snap := router.AgentReady(req.ServerId, slot)
	defer router.AgentGone(req.ServerId, username)

	// Initial snapshot for the new agent.
	if err := stream.Send(&chatspec.ControlServerFrame{
		Body: &chatspec.ControlServerFrame_Queue{
			Queue: snapshotToProto(snap),
		},
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case raw, ok := <-slot.Out:
			if !ok {
				return nil
			}
			frame, err := translateControlJSONToProto(raw)
			if err != nil || frame == nil {
				continue
			}
			if err := stream.Send(frame); err != nil {
				return err
			}
		}
	}
}

// AckSession clears the 30s re-route timer the router set when it picked this agent.
func (s *StreamServer) AckSession(ctx context.Context, req *chatspec.AckSessionRequest) (*chatspec.AckSessionResponse, error) {
	claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if claims == nil || claims.Username == "" {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	if req.GetServerId() == "" || req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id and session_id are required")
	}
	sessionID, err := uuid.Parse(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "session_id is not a UUID")
	}

	res := ACLResolution{}
	if s.acl != nil {
		res = s.acl(ctx)
	}
	if !resolutionAllows(res, req.ServerId) {
		return nil, status.Errorf(codes.PermissionDenied, "no access to server %s", req.ServerId)
	}
	router := s.routerFn()
	if router == nil {
		return nil, status.Error(codes.Unavailable, "chat router not ready")
	}
	router.Ack(req.ServerId, claims.Username, sessionID)
	return &chatspec.AckSessionResponse{}, nil
}

// DeclineSession re-queues the assignment and returns the next chosen agent's username
// (empty when no other ready agent is available).
func (s *StreamServer) DeclineSession(ctx context.Context, req *chatspec.DeclineSessionRequest) (*chatspec.DeclineSessionResponse, error) {
	claims, _ := ctx.Value(authware.ClaimsKey).(*authware.Claims)
	if claims == nil || claims.Username == "" {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	if req.GetServerId() == "" || req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "server_id and session_id are required")
	}
	sessionID, err := uuid.Parse(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "session_id is not a UUID")
	}

	res := ACLResolution{}
	if s.acl != nil {
		res = s.acl(ctx)
	}
	if !resolutionAllows(res, req.ServerId) {
		return nil, status.Errorf(codes.PermissionDenied, "no access to server %s", req.ServerId)
	}
	router := s.routerFn()
	if router == nil {
		return nil, status.Error(codes.Unavailable, "chat router not ready")
	}
	newAssignee, _ := router.Decline(req.ServerId, claims.Username, sessionID)
	return &chatspec.DeclineSessionResponse{NewAssignee: newAssignee}, nil
}

func snapshotToProto(snap chatpkg.Snapshot) *chatspec.QueueSnapshot {
	out := &chatspec.QueueSnapshot{
		GeneratedAt: timestamppb.New(time.Now().UTC()),
	}
	out.Entries = make([]*chatspec.QueueEntry, 0, len(snap.Queued))
	for _, q := range snap.Queued {
		out.Entries = append(out.Entries, &chatspec.QueueEntry{
			SessionId:        q.SessionID.String(),
			QueuedAt:         timestamppb.New(q.QueuedAt),
			QueuedForSeconds: uint32(q.QueuedFor / time.Second),
		})
	}
	out.Assigned = make([]*chatspec.AssignmentEntry, 0, len(snap.Assigned))
	for _, a := range snap.Assigned {
		out.Assigned = append(out.Assigned, &chatspec.AssignmentEntry{
			SessionId: a.SessionID.String(),
			Agent:     a.Agent,
		})
	}
	out.ReadyAgents = append([]string{}, snap.ReadyAgents...)
	return out
}

// translateControlJSONToProto converts the JSON frames the router pushes to slot.Out into
// the proto control frames. Mirrors the WS handler's frame catalog (queue_snapshot,
// session_assigned). Unrecognised shapes return (nil, nil) and the caller drops them.
func translateControlJSONToProto(raw []byte) (*chatspec.ControlServerFrame, error) {
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	t, _ := frame["type"].(string)
	switch t {
	case "queue_snapshot":
		q := &chatspec.QueueSnapshot{
			GeneratedAt: timestamppb.New(time.Now().UTC()),
		}
		if qs, ok := frame["queued"].([]any); ok {
			for _, e := range qs {
				m, _ := e.(map[string]any)
				if m == nil {
					continue
				}
				sid, _ := m["session_id"].(string)
				secs := jsonNumberAsUint32(m["queued_for_seconds"])
				q.Entries = append(q.Entries, &chatspec.QueueEntry{
					SessionId:        sid,
					QueuedForSeconds: secs,
				})
			}
		}
		if as, ok := frame["assigned"].([]any); ok {
			for _, e := range as {
				m, _ := e.(map[string]any)
				if m == nil {
					continue
				}
				sid, _ := m["session_id"].(string)
				ag, _ := m["agent"].(string)
				q.Assigned = append(q.Assigned, &chatspec.AssignmentEntry{
					SessionId: sid, Agent: ag,
				})
			}
		}
		if ra, ok := frame["ready_agents"].([]any); ok {
			for _, a := range ra {
				if s, ok := a.(string); ok {
					q.ReadyAgents = append(q.ReadyAgents, s)
				}
			}
		}
		return &chatspec.ControlServerFrame{
			Body: &chatspec.ControlServerFrame_Queue{Queue: q},
		}, nil
	case "session_assigned":
		sid, _ := frame["session_id"].(string)
		return &chatspec.ControlServerFrame{
			Body: &chatspec.ControlServerFrame_Assigned{
				Assigned: &chatspec.SessionAssigned{
					SessionId:   sid,
					AssignedAt:  timestamppb.New(time.Now().UTC()),
				},
			},
		}, nil
	case "session_released":
		sid, _ := frame["session_id"].(string)
		reason, _ := frame["reason"].(string)
		return &chatspec.ControlServerFrame{
			Body: &chatspec.ControlServerFrame_Released{
				Released: &chatspec.SessionReleased{
					SessionId:  sid,
					Reason:     reason,
					ReleasedAt: timestamppb.New(time.Now().UTC()),
				},
			},
		}, nil
	}
	return nil, nil
}

func jsonNumberAsUint32(v any) uint32 {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0
		}
		return uint32(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint32(n)
	case int64:
		if n < 0 {
			return 0
		}
		return uint32(n)
	}
	return 0
}

// --- JSON ↔ proto bridge ---

func translateAgentJSONToProto(raw []byte, sessionID uuid.UUID) (*chatspec.AgentServerFrame, error) {
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	t, _ := frame["type"].(string)
	switch t {
	case "msg":
		id, _ := frame["id"].(string)
		direction, _ := frame["direction"].(string)
		content, _ := frame["content"].(string)
		var sender string
		if v, ok := frame["agent"].(string); ok && v != "" {
			sender = v
		} else if v, ok := frame["visitor"].(string); ok {
			sender = v
		}
		clientID, _ := frame["client_id"].(string)
		var ts time.Time
		if tsStr, ok := frame["ts"].(string); ok {
			ts, _ = time.Parse(time.RFC3339, tsStr)
		}
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_Msg{
				Msg: &chatspec.StreamChatMessage{
					Id:        id,
					SessionId: sessionID.String(),
					Direction: mapDirection(direction),
					SenderId:  sender,
					Content:   content,
					When:      timestamppb.New(ts),
					ClientId:  clientID,
				},
			},
		}, nil
	case "system":
		content, _ := frame["content"].(string)
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_System{
				System: &chatspec.SystemEvent{Kind: "info", Message: content},
			},
		}, nil
	case "presence":
		event, _ := frame["event"].(string)
		agent, _ := frame["agent"].(string)
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_System{
				System: &chatspec.SystemEvent{Kind: "presence:" + event, Message: agent},
			},
		}, nil
	case "typing":
		from, _ := frame["from"].(string)
		active, _ := frame["active"].(bool)
		message := "off"
		if active {
			message = "on"
		}
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_System{
				System: &chatspec.SystemEvent{Kind: "typing:" + from, Message: message},
			},
		}, nil
	case "error":
		code, _ := frame["code"].(string)
		message, _ := frame["message"].(string)
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_Error{
				Error: &chatspec.StreamError{Code: code, Message: message},
			},
		}, nil
	case "_pong":
		var ts time.Time
		if tsStr, ok := frame["ts"].(string); ok {
			ts, _ = time.Parse(time.RFC3339, tsStr)
		}
		return &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_Pong{
				Pong: &chatspec.AgentPong{ServerTime: timestamppb.New(ts)},
			},
		}, nil
	}
	return nil, nil
}

func mapDirection(s string) chatspec.ChatMessageDirection {
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

func mapSessionStatus(s string) chatspec.ChatSessionStatus {
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

func errFrame(code, message string) []byte {
	return mustMarshal(map[string]any{
		"type":    "error",
		"code":    code,
		"message": message,
	})
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func trySend(out chan<- []byte, frame []byte) bool {
	select {
	case out <- frame:
		return true
	default:
		return false
	}
}

func sendStreamError(stream chatspec.ChatStreamService_AgentStreamServer, code, message string) error {
	return stream.Send(&chatspec.AgentServerFrame{
		Body: &chatspec.AgentServerFrame_Error{
			Error: &chatspec.StreamError{Code: code, Message: message},
		},
	})
}

// sendInnerStreamError mirrors sendStreamError for the agentStream interface so the
// per-session loop can emit errors regardless of whether the underlying transport is the
// raw gRPC stream or the Noise-wrapped one.
func sendInnerStreamError(stream agentStream, code, message string) error {
	return stream.Send(&chatspec.AgentServerFrame{
		Body: &chatspec.AgentServerFrame_Error{
			Error: &chatspec.StreamError{Code: code, Message: message},
		},
	})
}

func sendControlError(stream chatspec.ChatStreamService_ControlStreamServer, code, message string) error {
	return stream.Send(&chatspec.ControlServerFrame{
		Body: &chatspec.ControlServerFrame_Error{
			Error: &chatspec.StreamError{Code: code, Message: message},
		},
	})
}

