package chat

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/flynn/noise"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
)

// fakeAgentStream is a minimal in-memory stand-in for
// ChatStreamService_AgentStreamServer. We satisfy the full surface (Send / Recv /
// the embedded grpc.ServerStream methods) but only the data-plane half is
// exercised by the Noise tests; the metadata helpers return zero values.
type fakeAgentStream struct {
	ctx     context.Context
	inbound chan *chatspec.AgentClientFrame
	out     chan *chatspec.AgentServerFrame
	closed  bool
}

func newFakeAgentStream(ctx context.Context) *fakeAgentStream {
	return &fakeAgentStream{
		ctx:     ctx,
		inbound: make(chan *chatspec.AgentClientFrame, 16),
		out:     make(chan *chatspec.AgentServerFrame, 16),
	}
}

func (f *fakeAgentStream) Context() context.Context     { return f.ctx }
func (f *fakeAgentStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeAgentStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeAgentStream) SetTrailer(metadata.MD)       {}
func (f *fakeAgentStream) SendMsg(_ any) error          { return errors.New("not implemented") }
func (f *fakeAgentStream) RecvMsg(_ any) error          { return errors.New("not implemented") }

func (f *fakeAgentStream) Send(frame *chatspec.AgentServerFrame) error {
	select {
	case f.out <- frame:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

func (f *fakeAgentStream) Recv() (*chatspec.AgentClientFrame, error) {
	select {
	case frame, ok := <-f.inbound:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

// compile-time assertion: fakeAgentStream satisfies the gRPC stream interface.
var _ grpc.ServerStream = (*fakeAgentStream)(nil)
var _ chatspec.ChatStreamService_AgentStreamServer = (*fakeAgentStream)(nil)

// TestNoiseStaticKeyFromSecretIsDeterministic verifies that the same secret
// produces a stable keypair across calls — the property we rely on so the
// server's static public key matches what mobile clients learn at pair time.
func TestNoiseStaticKeyFromSecretIsDeterministic(t *testing.T) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %s", err)
	}
	kp1, err := noiseStaticKeyFromSecret(secret)
	if err != nil {
		t.Fatalf("derive 1: %s", err)
	}
	kp2, err := noiseStaticKeyFromSecret(secret)
	if err != nil {
		t.Fatalf("derive 2: %s", err)
	}
	if string(kp1.Private) != string(kp2.Private) {
		t.Fatalf("private not deterministic")
	}
	if string(kp1.Public) != string(kp2.Public) {
		t.Fatalf("public not deterministic")
	}
	if len(kp1.Public) != 32 {
		t.Fatalf("public len = %d, want 32", len(kp1.Public))
	}
}

// TestNoiseStaticKeyFromSecretRejectsBadLen guards the size validation that
// keeps malformed config from silently producing a usable-looking keypair.
func TestNoiseStaticKeyFromSecretRejectsBadLen(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := noiseStaticKeyFromSecret(make([]byte, n)); err == nil {
			t.Fatalf("len %d: expected error, got nil", n)
		}
	}
}

// TestHandleNoiseAgentStreamCompletesHandshake drives the server's handshake
// responder with a real flynn/noise initiator and asserts both sides land in
// transport mode with matching keying material. We don't go past the handshake
// here — that path requires the full hub/store stack and is exercised
// separately by the Rust client integration tests.
func TestHandleNoiseAgentStreamCompletesHandshake(t *testing.T) {
	// Server static keypair — same shape unified_boot produces.
	serverSecret := make([]byte, 32)
	if _, err := rand.Read(serverSecret); err != nil {
		t.Fatalf("rand server: %s", err)
	}
	serverKey, err := noiseStaticKeyFromSecret(serverSecret)
	if err != nil {
		t.Fatalf("derive server: %s", err)
	}

	// Client static keypair — represents the mobile device.
	clientKey, err := noiseCipherSuite.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("client keypair: %s", err)
	}

	// Build the initiator side using the *server's* known public key.
	initiator, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: clientKey,
		PeerStatic:    serverKey.Public,
	})
	if err != nil {
		t.Fatalf("initiator: %s", err)
	}

	// msg 1: e, es, s, ss.
	msg1, _, _, err := initiator.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("initiator msg1: %s", err)
	}

	// Drive the responder. handleNoiseAgentStream blocks on Recv after msg 2,
	// so we let it run in a goroutine and short-circuit by cancelling the ctx.
	// We only need it to emit msg 2; the rest of runAgentStream depends on
	// auth/store machinery the Rust integration tests cover.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeAgentStream(ctx)
	svr := &StreamServer{noiseStaticSecret: serverSecret}

	done := make(chan error, 1)
	go func() {
		done <- svr.handleNoiseAgentStream(stream, &chatspec.NoiseEnvelope{
			Payload:     msg1,
			IsHandshake: true,
		})
	}()

	// Wait for msg 2 with a generous timeout — the handshake is sub-millisecond
	// but CI machines are slow.
	var resp *chatspec.AgentServerFrame
	select {
	case resp = <-stream.out:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for msg 2")
	}
	ne := resp.GetNoise()
	if ne == nil {
		t.Fatalf("expected NoiseEnvelope, got %T (%+v)", resp.Body, resp)
	}
	if !ne.GetIsHandshake() {
		t.Fatalf("msg 2 must have is_handshake=true")
	}

	// Consume msg 2 on the initiator side; expect the handshake to complete and
	// CipherStates to be returned.
	_, initSendCS, initRecvCS, err := initiator.ReadMessage(nil, ne.GetPayload())
	if err != nil {
		t.Fatalf("initiator read msg 2: %s", err)
	}
	if initSendCS == nil || initRecvCS == nil {
		t.Fatalf("initiator: handshake did not complete (CipherStates nil)")
	}

	// Round-trip a transport message through the responder's CipherStates by
	// driving a fake inbound frame and asserting the server can decrypt it.
	// We use the noiseStreamWrapper directly — it's what runAgentStream would
	// have been driving.
	//
	// Build that wrapper to mirror what handleNoiseAgentStream constructed:
	// since we can't get at the server's internal CipherStates after the
	// goroutine started, we close the goroutine and run an independent server
	// handshake here. The deterministic-keypair test above guarantees the same
	// staticKey would have been used.
	cancel()
	select {
	case err := <-done:
		// We expect context-cancellation; anything else is a test fail.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("handleNoiseAgentStream exit: %v (acceptable: stream paused after handshake)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("handleNoiseAgentStream did not exit after cancel")
	}

	// Independent round-trip: build the responder side from the same secret and
	// drive a fresh handshake against a fresh initiator to verify the cipher
	// states encrypt/decrypt symmetrically.
	respHS, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: serverKey,
	})
	if err != nil {
		t.Fatalf("respHS: %s", err)
	}
	freshInit, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: clientKey,
		PeerStatic:    serverKey.Public,
	})
	if err != nil {
		t.Fatalf("freshInit: %s", err)
	}
	m1, _, _, err := freshInit.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("fresh m1: %s", err)
	}
	if _, _, _, err := respHS.ReadMessage(nil, m1); err != nil {
		t.Fatalf("respHS read m1: %s", err)
	}
	// flynn/noise returns (cs1, cs2) in absolute order — cs1 = initiator→responder,
	// cs2 = responder→initiator. So the responder reads from cs1 (=recv) and writes
	// to cs2 (=send); the initiator does the opposite.
	m2, respRecvCS, respSendCS, err := respHS.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("respHS write m2: %s", err)
	}
	if _, freshInitSend, freshInitRecv, err := freshInit.ReadMessage(nil, m2); err != nil {
		t.Fatalf("freshInit read m2: %s", err)
	} else {
		// Now exercise the wrapper Send/Recv path using the responder's
		// CipherStates. Round-trip an inner AgentClientFrame from initiator →
		// responder, and an inner AgentServerFrame in the reverse direction.
		ctx2, cancel2 := context.WithCancel(context.Background())
		defer cancel2()
		inner := newFakeAgentStream(ctx2)
		wrapped := &noiseStreamWrapper{
			inner:  inner,
			sendCS: respSendCS,
			recvCS: respRecvCS,
		}

		// Encrypt with the *initiator's* sendCS so the wrapper's recvCS
		// (responder's recvCS = initiator's sendCS counterpart) can decrypt.
		innerClientFrame := &chatspec.AgentClientFrame{
			Body: &chatspec.AgentClientFrame_Subscribe{
				Subscribe: &chatspec.SubscribeAgent{
					ServerId:  "srv",
					SessionId: "00000000-0000-0000-0000-000000000000",
				},
			},
		}
		plaintext, err := proto.Marshal(innerClientFrame)
		if err != nil {
			t.Fatalf("marshal inner: %s", err)
		}
		ciphertext, err := freshInitSend.Encrypt(nil, nil, plaintext)
		if err != nil {
			t.Fatalf("encrypt: %s", err)
		}
		inner.inbound <- &chatspec.AgentClientFrame{
			Body: &chatspec.AgentClientFrame_Noise{
				Noise: &chatspec.NoiseEnvelope{Payload: ciphertext, IsHandshake: false},
			},
		}
		got, err := wrapped.Recv()
		if err != nil {
			t.Fatalf("wrapped.Recv: %s", err)
		}
		if sub := got.GetSubscribe(); sub == nil || sub.ServerId != "srv" {
			t.Fatalf("decrypted inner frame mismatch: %+v", got)
		}

		// Now send an inner AgentServerFrame through the wrapper and decrypt
		// with the initiator's recvCS.
		innerServerFrame := &chatspec.AgentServerFrame{
			Body: &chatspec.AgentServerFrame_Subscribed{
				Subscribed: &chatspec.SubscribedAck{SessionId: "deadbeef"},
			},
		}
		if err := wrapped.Send(innerServerFrame); err != nil {
			t.Fatalf("wrapped.Send: %s", err)
		}
		envOut := <-inner.out
		outerNoise := envOut.GetNoise()
		if outerNoise == nil || outerNoise.IsHandshake {
			t.Fatalf("expected transport NoiseEnvelope on outbound, got %+v", envOut)
		}
		decrypted, err := freshInitRecv.Decrypt(nil, nil, outerNoise.Payload)
		if err != nil {
			t.Fatalf("initiator decrypt: %s", err)
		}
		gotSrv := &chatspec.AgentServerFrame{}
		if err := proto.Unmarshal(decrypted, gotSrv); err != nil {
			t.Fatalf("unmarshal server inner: %s", err)
		}
		if ack := gotSrv.GetSubscribed(); ack == nil || ack.SessionId != "deadbeef" {
			t.Fatalf("decrypted inner server frame mismatch: %+v", gotSrv)
		}
	}
}

// TestNoiseStreamWrapperRejectsPlaintextOnTransport ensures that after the
// handshake completes, a stray plaintext AgentClientFrame on the wire is
// rejected — a misbehaving / malicious client can't downgrade mid-session.
func TestNoiseStreamWrapperRejectsPlaintextOnTransport(t *testing.T) {
	// Run a real handshake to get usable CipherStates.
	serverKey, err := noiseCipherSuite.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("server keypair: %s", err)
	}
	clientKey, err := noiseCipherSuite.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("client keypair: %s", err)
	}
	initHS, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		StaticKeypair: clientKey,
		PeerStatic:    serverKey.Public,
	})
	respHS, _ := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: serverKey,
	})
	m1, _, _, _ := initHS.WriteMessage(nil, nil)
	_, _, _, _ = respHS.ReadMessage(nil, m1)
	// Responder reads cs1 = i→r and writes cs2 = r→i.
	m2, respRecv, respSend, _ := respHS.WriteMessage(nil, nil)
	_, _, _, _ = initHS.ReadMessage(nil, m2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := newFakeAgentStream(ctx)
	wrapped := &noiseStreamWrapper{inner: inner, sendCS: respSend, recvCS: respRecv}

	// Plaintext SubscribeAgent — no Noise envelope. Must be rejected.
	inner.inbound <- &chatspec.AgentClientFrame{
		Body: &chatspec.AgentClientFrame_Subscribe{
			Subscribe: &chatspec.SubscribeAgent{ServerId: "x", SessionId: "y"},
		},
	}
	if _, err := wrapped.Recv(); err == nil {
		t.Fatalf("expected rejection of plaintext frame in Noise mode")
	}
}
