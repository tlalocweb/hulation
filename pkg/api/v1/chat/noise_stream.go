package chat

// Noise_IK session-wrap for the AgentStream. When a client opts into Noise, every
// AgentClientFrame / AgentServerFrame on the wire carries a NoiseEnvelope; the
// decrypted payload inside is a serialised "inner" AgentClientFrame /
// AgentServerFrame (whose Body MUST NOT itself be a NoiseEnvelope variant).
//
// Handshake (Noise_IK_25519_ChaChaPoly_SHA256):
//
//	msg 1 (client → server)   e, es, s, ss     — first AgentClientFrame.noise (is_handshake=true)
//	msg 2 (server → client)   e, ee, se        — first AgentServerFrame.noise (is_handshake=true)
//
// The client-side counterpart lives in hula-mobile/common/src/noise.rs +
// chatstream.rs::open_agent_stream_noise. Both sides exchange empty handshake
// payloads; the first transport frame from the client carries SubscribeAgent.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/flynn/noise"
	"google.golang.org/protobuf/proto"

	chatspec "github.com/tlalocweb/hulation/pkg/apispec/v1/chat"
)

// noiseCipherSuite is the locked pattern + cipher for v1. Changing it is a
// wire-breaking change; bump a protocol version when revisiting.
var noiseCipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// agentStream narrows ChatStreamService_AgentStreamServer to just the methods
// the per-session loop touches, so the plaintext stream and the Noise-wrapped
// stream can share an implementation.
type agentStream interface {
	Recv() (*chatspec.AgentClientFrame, error)
	Send(*chatspec.AgentServerFrame) error
	Context() context.Context
}

// handleNoiseAgentStream takes over an AgentStream once the first frame is
// detected as a Noise handshake initiator message. It performs the responder
// half of the handshake and then runs the same per-session loop the plaintext
// path uses, with all inbound/outbound frames flowing through transport-mode
// Noise.
func (s *StreamServer) handleNoiseAgentStream(
	stream chatspec.ChatStreamService_AgentStreamServer,
	firstHandshake *chatspec.NoiseEnvelope,
) error {
	// Resolve the static key per handshake so config reload / rotation is live.
	var secret []byte
	if s.noiseStaticFn != nil {
		secret = s.noiseStaticFn()
	}
	if len(secret) != 32 {
		return sendStreamError(stream, "noise_unavailable",
			"server does not have a Noise static key configured")
	}
	if !firstHandshake.GetIsHandshake() {
		return sendStreamError(stream, "invalid_handshake",
			"first NoiseEnvelope must have is_handshake=true")
	}

	staticKey, err := noiseStaticKeyFromSecret(secret)
	if err != nil {
		return sendStreamError(stream, "noise_init", err.Error())
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		StaticKeypair: staticKey,
	})
	if err != nil {
		return sendStreamError(stream, "noise_init", err.Error())
	}

	// msg 1 (client → server): e, es, s, ss.
	if _, _, _, err := hs.ReadMessage(nil, firstHandshake.GetPayload()); err != nil {
		return sendStreamError(stream, "noise_handshake", err.Error())
	}
	// msg 2 (server → client): e, ee, se. Completes the handshake — Split
	// returns the transport-mode CipherStates. flynn/noise returns the cipher
	// states in absolute (not local-relative) order: cs1 = initiator→responder,
	// cs2 = responder→initiator. From the responder's POV that means cs1 is the
	// *recv* side and cs2 is the *send* side.
	msg2, recvCS, sendCS, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return sendStreamError(stream, "noise_handshake", err.Error())
	}
	if sendCS == nil || recvCS == nil {
		return sendStreamError(stream, "noise_handshake",
			"split CipherStates not returned after IK msg 2")
	}
	if err := stream.Send(&chatspec.AgentServerFrame{
		Body: &chatspec.AgentServerFrame_Noise{
			Noise: &chatspec.NoiseEnvelope{Payload: msg2, IsHandshake: true},
		},
	}); err != nil {
		return err
	}

	return s.runAgentStream(&noiseStreamWrapper{
		inner:  stream,
		sendCS: sendCS,
		recvCS: recvCS,
	})
}

// noiseStreamWrapper adapts ChatStreamService_AgentStreamServer to agentStream
// with on-the-wire Noise framing. Recv pulls a NoiseEnvelope off the inner
// stream, decrypts it, and parses an inner AgentClientFrame. Send serialises
// the AgentServerFrame, encrypts it, and forwards as a NoiseEnvelope.
type noiseStreamWrapper struct {
	inner  chatspec.ChatStreamService_AgentStreamServer
	sendCS *noise.CipherState
	recvCS *noise.CipherState
}

func (w *noiseStreamWrapper) Context() context.Context { return w.inner.Context() }

func (w *noiseStreamWrapper) Recv() (*chatspec.AgentClientFrame, error) {
	env, err := w.inner.Recv()
	if err != nil {
		return nil, err
	}
	ne := env.GetNoise()
	if ne == nil {
		return nil, errors.New("noise: received plaintext frame after handshake")
	}
	if ne.GetIsHandshake() {
		return nil, errors.New("noise: unexpected handshake frame after transport mode")
	}
	plaintext, err := w.recvCS.Decrypt(nil, nil, ne.GetPayload())
	if err != nil {
		return nil, fmt.Errorf("noise decrypt: %w", err)
	}
	inner := &chatspec.AgentClientFrame{}
	if err := proto.Unmarshal(plaintext, inner); err != nil {
		return nil, fmt.Errorf("noise inner unmarshal: %w", err)
	}
	// An inner frame that is itself a Noise envelope would let a peer loop the
	// protocol; reject it.
	if inner.GetNoise() != nil {
		return nil, errors.New("noise: inner AgentClientFrame must not carry Noise")
	}
	return inner, nil
}

func (w *noiseStreamWrapper) Send(frame *chatspec.AgentServerFrame) error {
	if frame.GetNoise() != nil {
		return errors.New("noise: inner AgentServerFrame must not carry Noise")
	}
	plaintext, err := proto.Marshal(frame)
	if err != nil {
		return fmt.Errorf("noise inner marshal: %w", err)
	}
	ciphertext, err := w.sendCS.Encrypt(nil, nil, plaintext)
	if err != nil {
		return fmt.Errorf("noise encrypt: %w", err)
	}
	return w.inner.Send(&chatspec.AgentServerFrame{
		Body: &chatspec.AgentServerFrame_Noise{
			Noise: &chatspec.NoiseEnvelope{Payload: ciphertext, IsHandshake: false},
		},
	})
}

// noiseStaticKeyFromSecret expands a 32-byte X25519 secret into the DHKey shape
// flynn/noise expects (private + matching public). We feed the secret to
// GenerateKeypair via a deterministic reader so the resulting DHKey uses
// exactly our configured private bytes and the matching public derived by the
// same cipher suite that will run the handshake.
func noiseStaticKeyFromSecret(secret []byte) (noise.DHKey, error) {
	if len(secret) != 32 {
		return noise.DHKey{}, fmt.Errorf("noise static secret must be 32 bytes, got %d", len(secret))
	}
	priv := make([]byte, 32)
	copy(priv, secret)
	kp, err := noiseCipherSuite.GenerateKeypair(&staticReader{buf: priv})
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("derive noise static keypair: %w", err)
	}
	return kp, nil
}

// staticReader serves a fixed byte slice, tracking an offset so callers that
// read in multiple smaller chunks still get every byte before EOF. Feeding
// GenerateKeypair a deterministic "random" stream makes it return a keypair
// built from our configured secret. (A one-shot reader that marked itself
// consumed after a single partial Read could hand back a short buffer and make
// key derivation flaky if the cipher suite ever reads in pieces.)
type staticReader struct {
	buf []byte
	off int
}

func (r *staticReader) Read(p []byte) (int, error) {
	if r.off >= len(r.buf) {
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.off:])
	r.off += n
	return n, nil
}
