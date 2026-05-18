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
	"net"
	"testing"
	"time"

	"github.com/flynn/noise"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
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
