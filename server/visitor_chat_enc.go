package server

// Visitor-chat app-layer encryption helpers shared by the chat-start handler
// (server/chat_start.go) and the visitor WebSocket handler
// (server/chat_ws_visitor.go).
//
// Threat model: a TLS-inspecting middlebox between the visitor's browser and
// the Hula server (corporate MITM proxy, captive portal). The widget seals
// chat content to the installation's visitor-chat X25519 public key before the
// bytes hit HTTPS; the server decrypts at the edge and then processes
// plaintext normally (store, moderate, fan out to agents, search). The
// encryption boundary is browser↔server-edge — NOT end-to-end to the agent.
// The server is trusted with plaintext; the middlebox is not.
//
// Sealed-box per message (pkg/visitorcrypto): no session-key state, one X25519
// scalar mult per message — irrelevant at human chat rates, and it reuses the
// exact primitive the cross-language vector pins.
//
//   visitor → server : sealed to the server static pub; server Opens with the
//                      visitor_chat_key private.
//   server → visitor : sealed to the visitor's per-session pub (presented as
//                      the WS ?vpub= query param); the browser opens with its
//                      in-memory session private.
//
// When visitor_chat_key is unset, or the browser is too old for WebCrypto
// X25519, the widget sends plaintext and these helpers degrade to no-ops — the
// chat still works, just not middlebox-opaque.

import (
	"encoding/base64"
	"encoding/json"

	hulaapp "github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/pkg/visitorcrypto"
	"github.com/tlalocweb/hulation/utils"
)

// visitorChatPrivateKey returns the installation's decoded 32-byte
// visitor-chat X25519 private and true when configured + well-formed. Read
// from config per call (cheap); mirrors how installation_identity.go reads it.
func visitorChatPrivateKey() ([]byte, bool) {
	cfg := hulaapp.GetConfig()
	if cfg == nil || cfg.VisitorChatKey == "" {
		return nil, false
	}
	priv, err := utils.DecodeNoiseStaticKey(cfg.VisitorChatKey) // generic 32-byte X25519 b64url decoder
	if err != nil {
		return nil, false
	}
	return priv, true
}

// openVisitorEnvelope decodes a base64url envelope and Opens it with the
// installation's visitor-chat private key. Returns the plaintext bytes.
// Callers only reach here when both an envelope and a configured key exist.
func openVisitorEnvelope(envB64 string) ([]byte, error) {
	priv, ok := visitorChatPrivateKey()
	if !ok {
		return nil, errVisitorChatDisabled
	}
	env, err := decodeB64URL(envB64)
	if err != nil {
		return nil, err
	}
	return visitorcrypto.Open(priv, env)
}

// sealToVisitor seals plaintext to the visitor's per-session public key,
// returning a base64url envelope. Used by the WS writer to re-seal outbound
// agent message content. vpub is the raw 32-byte key the browser presented as
// ?vpub=.
func sealToVisitor(vpub, plaintext []byte) (string, error) {
	env, err := visitorcrypto.Seal(nil, vpub, plaintext)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(env), nil
}

// maybeSealOutbound re-seals the content of an outbound "msg" frame to the
// visitor's session public key, replacing the plaintext "content" field with
// an "enc" field. Non-msg frames (system, ack, presence, typing, error) and
// content-less msg frames pass through unchanged — only chat message content
// is encrypted; operational frames stay plaintext (they carry no visitor PII
// or message text). On any marshal/seal failure the original frame is returned
// unchanged so a crypto hiccup degrades to plaintext rather than dropping the
// message.
func maybeSealOutbound(raw, vpub []byte) []byte {
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return raw
	}
	if frame["type"] != "msg" {
		return raw
	}
	content, ok := frame["content"].(string)
	if !ok || content == "" {
		return raw
	}
	envB64, err := sealToVisitor(vpub, []byte(content))
	if err != nil {
		return raw
	}
	delete(frame, "content")
	frame["enc"] = envB64
	out, err := json.Marshal(frame)
	if err != nil {
		return raw
	}
	return out
}

// decodeVisitorSessionPub decodes the ?vpub= query param (the visitor's
// per-session X25519 public). Returns (key, true) only for a well-formed
// 32-byte value. Empty or malformed → (nil, false), i.e. fall back to
// plaintext outbound.
func decodeVisitorSessionPub(vpubB64 string) ([]byte, bool) {
	if vpubB64 == "" {
		return nil, false
	}
	b, err := decodeB64URL(vpubB64)
	if err != nil || len(b) != 32 {
		return nil, false
	}
	return b, true
}

// decodeB64URL accepts both padded and unpadded base64url (browsers emit
// unpadded; tolerate either).
func decodeB64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// errVisitorChatDisabled is returned when an envelope arrives but the
// installation has no visitor-chat key configured — a client/config mismatch.
var errVisitorChatDisabled = visitorChatError("visitor chat encryption not configured on this installation")

type visitorChatError string

func (e visitorChatError) Error() string { return string(e) }
