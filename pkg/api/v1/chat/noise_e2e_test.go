package chat

// End-to-end Noise-over-gRPC test for the chat stream. Spins up the real
// StreamServer on an in-process bufconn, runs the full IK handshake with a
// flynn/noise initiator, then exercises a transport-mode round trip by sending
// an inner SubscribeAgent through the encrypted stream and asserting the
// server's response (an encrypted error frame, since runAgentStream rejects
// the unauthenticated context) decrypts cleanly.
//
// This is the level above noise_stream_test.go — that file exercises the
// handshake state machine and wrapper in isolation; this one wires the whole
// thing through a real grpc.Server / grpc.ClientConn / generated service stub.

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
	chatpkg "github.com/tlalocweb/hulation/pkg/chat"
	"github.com/tlalocweb/hulation/pkg/server/authware"
)

func TestNoiseAgentStreamEndToEnd(t *testing.T) {
	// 1. Server static key.
	serverSecret := make([]byte, 32)
	if _, err := rand.Read(serverSecret); err != nil {
		t.Fatalf("rand: %s", err)
	}
	serverKeypair, err := noiseStaticKeyFromSecret(serverSecret)
	if err != nil {
		t.Fatalf("derive server key: %s", err)
	}

	// 2. Stand up a real gRPC server with the StreamServer. We intentionally
	//    skip the auth interceptor and the hub/store/router — the Noise
	//    handshake completes before any of those are touched, and runAgentStream
	//    will emit an "unauthorized" frame when it sees no claims in context.
	//    That error round-tripping is exactly what we want to verify here.
	svr := &StreamServer{noiseStaticSecret: serverSecret}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	chatspec.RegisterChatStreamServiceServer(grpcSrv, svr)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	t.Cleanup(grpcSrv.Stop)

	// 3. Dial the in-process server.
	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := chatspec.NewChatStreamServiceClient(conn)
	stream, err := client.AgentStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %s", err)
	}

	// 4. Build the initiator side and send msg 1.
	clientKeypair, err := noiseCipherSuite.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("client keypair: %s", err)
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: clientKeypair,
		PeerStatic:    serverKeypair.Public,
	})
	if err != nil {
		t.Fatalf("init handshake: %s", err)
	}
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("write msg1: %s", err)
	}
	if err := stream.Send(&chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Noise{
			Noise: &chatspec.NoiseEnvelope{Payload: msg1, IsHandshake: true},
		},
	}); err != nil {
		t.Fatalf("send msg1: %s", err)
	}

	// 5. Receive msg 2 and complete the handshake on the initiator side.
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv msg2: %s", err)
	}
	ne := resp.GetNoise()
	if ne == nil {
		t.Fatalf("expected NoiseEnvelope msg2, got %+v", resp)
	}
	if !ne.GetIsHandshake() {
		t.Fatalf("msg2 must have is_handshake=true")
	}
	_, initSendCS, initRecvCS, err := hs.ReadMessage(nil, ne.GetPayload())
	if err != nil {
		t.Fatalf("init read msg2: %s", err)
	}
	if initSendCS == nil || initRecvCS == nil {
		t.Fatalf("init: handshake did not complete (CipherStates nil)")
	}
	_ = initSendCS // not used in the auth-failure path — the server closes after error.

	// 6. Without auth claims in the stream context, runAgentStream emits an
	//    "unauthorized" error frame and returns immediately after the handshake
	//    completes; the wrapper encrypts that frame and forwards it. Receive +
	//    decrypt that frame to validate the full transport-mode round trip
	//    (encrypt on server, decrypt on client).
	//
	//    We intentionally do NOT send an inner SubscribeAgent first — the server
	//    has already closed by the time we'd try, and the resulting EOF would
	//    mask the unauthorized signal we're checking for.
	resp2, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv transport frame: %s", err)
	}
	ne2 := resp2.GetNoise()
	if ne2 == nil {
		t.Fatalf("expected transport NoiseEnvelope, got %+v (downgrade to plaintext detected)", resp2)
	}
	if ne2.GetIsHandshake() {
		t.Fatalf("transport frame must have is_handshake=false")
	}
	decrypted, err := initRecvCS.Decrypt(nil, nil, ne2.GetPayload())
	if err != nil {
		t.Fatalf("init decrypt: %s", err)
	}
	innerSrv := &chatspec.AgentServerFrame{}
	if err := proto.Unmarshal(decrypted, innerSrv); err != nil {
		t.Fatalf("unmarshal inner server frame: %s", err)
	}
	errOut := innerSrv.GetError()
	if errOut == nil {
		t.Fatalf("expected inner StreamError, got %+v", innerSrv)
	}
	if errOut.GetCode() != "unauthorized" {
		t.Fatalf("expected code=unauthorized, got %q (msg=%q)",
			errOut.GetCode(), errOut.GetMessage())
	}
}

// TestNoiseAgentStreamHappyPath drives the full Noise-wrapped session loop end
// to end: handshake → encrypted SubscribeAgent → encrypted SubscribedAck →
// encrypted AgentMessage → encrypted local-echo StreamChatMessage. Validates
// that the wrapper round-trips every direction's frames intact and that
// runAgentStream's hub/store/auth machinery is reachable through Noise.
func TestNoiseAgentStreamHappyPath(t *testing.T) {
	const (
		username  = "test-agent"
		serverID  = "srv-1"
		msgText   = "hello from the test"
		msgClient = "cli-001"
	)
	sessionID := uuid.New()

	// Server key + session pre-seeded in the fake store.
	serverSecret := make([]byte, 32)
	if _, err := rand.Read(serverSecret); err != nil {
		t.Fatalf("rand: %s", err)
	}
	serverKeypair, err := noiseStaticKeyFromSecret(serverSecret)
	if err != nil {
		t.Fatalf("derive server key: %s", err)
	}
	store := newFakeSessionStore()
	store.put(chatpkg.Session{
		ID:        sessionID,
		ServerID:  serverID,
		VisitorID: "visitor-x",
		Status:    chatpkg.StatusAssigned, // any non-closed status works.
	})

	hub := chatpkg.NewHub()
	svr := &StreamServer{
		store:             store,
		hubFn:             func() *chatpkg.Hub { return hub },
		routerFn:          func() *chatpkg.Router { return nil }, // optional dep; nil is fine.
		acl:               func(context.Context) ACLResolution { return ACLResolution{Superadmin: true} },
		noiseStaticSecret: serverSecret,
	}

	// gRPC server with a stream interceptor that injects claims — the real
	// AdminBearerStreamInterceptor lives in server/, not in this package, so we
	// stub the bit that lands a *authware.Claims on the stream context.
	streamInterceptor := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := context.WithValue(ss.Context(), authware.ClaimsKey, &authware.Claims{Username: username})
		return handler(srv, &claimsServerStream{ServerStream: ss, ctx: ctx})
	}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer(grpc.StreamInterceptor(streamInterceptor))
	chatspec.RegisterChatStreamServiceServer(grpcSrv, svr)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := chatspec.NewChatStreamServiceClient(conn).AgentStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %s", err)
	}

	// Initiator handshake.
	clientKeypair, err := noiseCipherSuite.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("client keypair: %s", err)
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: clientKeypair,
		PeerStatic:    serverKeypair.Public,
	})
	if err != nil {
		t.Fatalf("init handshake: %s", err)
	}
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("write msg1: %s", err)
	}
	if err := stream.Send(&chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Noise{
			Noise: &chatspec.NoiseEnvelope{Payload: msg1, IsHandshake: true},
		},
	}); err != nil {
		t.Fatalf("send msg1: %s", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv msg2: %s", err)
	}
	ne := resp.GetNoise()
	if ne == nil || !ne.GetIsHandshake() {
		t.Fatalf("expected handshake msg2, got %+v", resp)
	}
	_, sendCS, recvCS, err := hs.ReadMessage(nil, ne.GetPayload())
	if err != nil {
		t.Fatalf("init read msg2: %s", err)
	}

	// Tiny send/recv helpers — every wire frame on this stream is a Noise
	// envelope, so it pays to push the encryption inside one place.
	sendInner := func(inner *chatspec.AgentClientFrame) error {
		plaintext, err := proto.Marshal(inner)
		if err != nil {
			return err
		}
		ct, err := sendCS.Encrypt(nil, nil, plaintext)
		if err != nil {
			return err
		}
		return stream.Send(&chatspec.AgentClientFrame{
			Body: &chatspec.AgentClientFrame_Noise{
				Noise: &chatspec.NoiseEnvelope{Payload: ct, IsHandshake: false},
			},
		})
	}
	recvInner := func() (*chatspec.AgentServerFrame, error) {
		f, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		ne := f.GetNoise()
		if ne == nil {
			return nil, errors.New("expected NoiseEnvelope, got plaintext")
		}
		pt, err := recvCS.Decrypt(nil, nil, ne.GetPayload())
		if err != nil {
			return nil, err
		}
		inner := &chatspec.AgentServerFrame{}
		if err := proto.Unmarshal(pt, inner); err != nil {
			return nil, err
		}
		return inner, nil
	}

	// 1) SubscribeAgent → expect SubscribedAck.
	if err := sendInner(&chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Subscribe{
			Subscribe: &chatspec.SubscribeAgent{
				ServerId:  serverID,
				SessionId: sessionID.String(),
			},
		},
	}); err != nil {
		t.Fatalf("send subscribe: %s", err)
	}
	got, err := recvInner()
	if err != nil {
		t.Fatalf("recv subscribed: %s", err)
	}
	ack := got.GetSubscribed()
	if ack == nil {
		t.Fatalf("expected SubscribedAck, got %+v", got)
	}
	if ack.GetSessionId() != sessionID.String() {
		t.Fatalf("ack session_id: got %q, want %q", ack.GetSessionId(), sessionID.String())
	}

	// runAgentStream also publishes an agent_joined presence frame on the hub
	// right after subscribing. The writer goroutine drains the subscriber's
	// outbox and translates each frame, so we may see the presence frame next.
	// Skip any system-typed frames until the chat-message echo arrives.
	consumeUntilMessage := func() *chatspec.StreamChatMessage {
		for {
			f, err := recvInner()
			if err != nil {
				t.Fatalf("recv: %s", err)
			}
			if m := f.GetMsg(); m != nil {
				return m
			}
			// presence / system events arrive as SystemEvent; ignore.
			if f.GetSystem() == nil {
				t.Fatalf("unexpected non-msg frame: %+v", f)
			}
		}
	}

	// 2) Send AgentMessage → expect the local-echo StreamChatMessage carrying
	//    our client_id.
	if err := sendInner(&chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Msg{
			Msg: &chatspec.AgentMessage{Content: msgText, ClientId: msgClient},
		},
	}); err != nil {
		t.Fatalf("send msg: %s", err)
	}
	echo := consumeUntilMessage()
	if echo.GetContent() != msgText {
		t.Fatalf("echo content: got %q, want %q", echo.GetContent(), msgText)
	}
	if echo.GetClientId() != msgClient {
		t.Fatalf("echo client_id: got %q, want %q", echo.GetClientId(), msgClient)
	}
	if echo.GetDirection() != chatspec.ChatMessageDirection_CHAT_MESSAGE_DIRECTION_AGENT {
		t.Fatalf("echo direction: got %v, want AGENT", echo.GetDirection())
	}

	// Persistence: the fake store should have recorded the message.
	if got := store.messagesFor(sessionID); len(got) != 1 || got[0].Content != msgText {
		t.Fatalf("store messages: got %+v, want one %q message", got, msgText)
	}
}

// claimsServerStream wraps a grpc.ServerStream to override Context().
type claimsServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *claimsServerStream) Context() context.Context { return s.ctx }

// fakeSessionStore is a tiny in-memory implementation of the sessionStore
// interface — enough surface for runAgentStream to walk through a happy-path
// SubscribeAgent → AppendMessage cycle without a ClickHouse instance.
type fakeSessionStore struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]chatpkg.Session
	messages map[uuid.UUID][]chatpkg.Message
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		sessions: make(map[uuid.UUID]chatpkg.Session),
		messages: make(map[uuid.UUID][]chatpkg.Message),
	}
}

func (f *fakeSessionStore) put(sess chatpkg.Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[sess.ID] = sess
}

func (f *fakeSessionStore) messagesFor(id uuid.UUID) []chatpkg.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]chatpkg.Message, len(f.messages[id]))
	copy(out, f.messages[id])
	return out
}

func (f *fakeSessionStore) GetSession(_ context.Context, _ string, id uuid.UUID) (chatpkg.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return chatpkg.Session{}, chatpkg.ErrNotFound
	}
	return sess, nil
}

func (f *fakeSessionStore) MarkSessionOpen(_ context.Context, _ string, id uuid.UUID) (chatpkg.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return chatpkg.Session{}, chatpkg.ErrNotFound
	}
	sess.Status = chatpkg.StatusOpen
	f.sessions[id] = sess
	return sess, nil
}

func (f *fakeSessionStore) MarkSessionClosed(_ context.Context, _ string, id uuid.UUID, _ string) (chatpkg.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return chatpkg.Session{}, chatpkg.ErrNotFound
	}
	sess.Status = chatpkg.StatusClosed
	f.sessions[id] = sess
	return sess, nil
}

func (f *fakeSessionStore) AppendMessage(_ context.Context, m chatpkg.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[m.SessionID] = append(f.messages[m.SessionID], m)
	return nil
}

// TestNoiseAgentStreamRejectsNoiseWhenServerHasNoKey covers the negative path:
// a client tries to open a Noise session against a server that has no static
// key configured. The server should send back a plaintext (no envelope) error
// frame indicating Noise is unavailable, not silently fall through to
// plaintext mode.
func TestNoiseAgentStreamRejectsNoiseWhenServerHasNoKey(t *testing.T) {
	svr := &StreamServer{} // noiseStaticSecret left nil
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	chatspec.RegisterChatStreamServiceServer(grpcSrv, svr)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("dial: %s", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := chatspec.NewChatStreamServiceClient(conn).AgentStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %s", err)
	}
	if err := stream.Send(&chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Noise{
			Noise: &chatspec.NoiseEnvelope{Payload: []byte{0x01}, IsHandshake: true},
		},
	}); err != nil {
		t.Fatalf("send noise frame: %s", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %s", err)
	}
	errOut := resp.GetError()
	if errOut == nil {
		t.Fatalf("expected plaintext StreamError, got %+v", resp)
	}
	if errOut.GetCode() != "noise_unavailable" {
		t.Fatalf("expected code=noise_unavailable, got %q", errOut.GetCode())
	}
}
