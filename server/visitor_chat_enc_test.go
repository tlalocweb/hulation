package server

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/pkg/visitorcrypto"
	"github.com/tlalocweb/hulation/utils"
)

// testVisitorKey generates a fresh visitor-chat keypair and installs the
// private into a test config. Returns the raw 32-byte public for sealing.
func testVisitorKey(t *testing.T) []byte {
	t.Helper()
	privB64, err := utils.GenerateNoiseStaticKey()
	if err != nil {
		t.Fatalf("gen key: %s", err)
	}
	withConfig(t, &config.Config{VisitorChatKey: privB64})
	priv, _ := utils.DecodeNoiseStaticKey(privB64)
	pub, err := visitorcrypto.PublicKeyFromPrivate(priv)
	if err != nil {
		t.Fatalf("derive pub: %s", err)
	}
	return pub
}

func TestVisitorChatPrivateKey_ConfiguredAndNot(t *testing.T) {
	withConfig(t, &config.Config{})
	if _, ok := visitorChatPrivateKey(); ok {
		t.Fatalf("expected no key when unconfigured")
	}
	_ = testVisitorKey(t)
	if _, ok := visitorChatPrivateKey(); !ok {
		t.Fatalf("expected key when configured")
	}
}

func TestOpenVisitorEnvelope_RoundTrip(t *testing.T) {
	pub := testVisitorKey(t)
	plaintext := []byte(`{"email":"v@x.io","first_message":"ship to MX?"}`)
	env, err := visitorcrypto.Seal(nil, pub, plaintext)
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	// Browser sends base64url-no-pad.
	envB64 := base64.RawURLEncoding.EncodeToString(env)
	got, err := openVisitorEnvelope(envB64)
	if err != nil {
		t.Fatalf("open: %s", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("mismatch: %s", got)
	}
}

func TestOpenVisitorEnvelope_PaddedB64AlsoAccepted(t *testing.T) {
	pub := testVisitorKey(t)
	env, _ := visitorcrypto.Seal(nil, pub, []byte("hi"))
	padded := base64.URLEncoding.EncodeToString(env) // with '=' padding
	got, err := openVisitorEnvelope(padded)
	if err != nil || string(got) != "hi" {
		t.Fatalf("padded b64 not accepted: %v / %s", err, got)
	}
}

func TestOpenVisitorEnvelope_RejectsGarbage(t *testing.T) {
	_ = testVisitorKey(t)
	if _, err := openVisitorEnvelope("!!!not base64!!!"); err == nil {
		t.Fatalf("expected error on garbage")
	}
	// Valid b64 but too short to be an envelope.
	if _, err := openVisitorEnvelope(base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})); err == nil {
		t.Fatalf("expected error on short envelope")
	}
}

func TestOpenVisitorEnvelope_DisabledWhenNoKey(t *testing.T) {
	withConfig(t, &config.Config{})
	if _, err := openVisitorEnvelope("AAAA"); err == nil {
		t.Fatalf("expected error when no key configured")
	}
}

func TestDecodeVisitorSessionPub(t *testing.T) {
	if _, ok := decodeVisitorSessionPub(""); ok {
		t.Fatalf("empty must be not-ok")
	}
	if _, ok := decodeVisitorSessionPub("short"); ok {
		t.Fatalf("short must be not-ok")
	}
	good := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if k, ok := decodeVisitorSessionPub(good); !ok || len(k) != 32 {
		t.Fatalf("valid 32-byte key rejected")
	}
}

func TestMaybeSealOutbound(t *testing.T) {
	// Generate a "visitor session" keypair: server seals to its public, we
	// open with its private to prove the round trip.
	vpubB64, err := utils.GenerateNoiseStaticKey()
	if err != nil {
		t.Fatalf("gen: %s", err)
	}
	vpriv, _ := utils.DecodeNoiseStaticKey(vpubB64)
	vpub, _ := visitorcrypto.PublicKeyFromPrivate(vpriv)

	// 1. A msg frame with content gets sealed: content removed, enc added,
	//    and the enc opens back to the original content.
	in := []byte(`{"type":"msg","direction":"agent","id":"abc","content":"hello there"}`)
	out := maybeSealOutbound(in, vpub)
	var frame map[string]any
	if err := json.Unmarshal(out, &frame); err != nil {
		t.Fatalf("unmarshal out: %s", err)
	}
	if _, hasContent := frame["content"]; hasContent {
		t.Fatalf("content not removed: %s", out)
	}
	encStr, ok := frame["enc"].(string)
	if !ok || encStr == "" {
		t.Fatalf("enc missing: %s", out)
	}
	if frame["direction"] != "agent" || frame["id"] != "abc" {
		t.Fatalf("other fields not preserved: %s", out)
	}
	env, _ := base64.RawURLEncoding.DecodeString(encStr)
	dec, err := visitorcrypto.Open(vpriv, env)
	if err != nil {
		t.Fatalf("open sealed outbound: %s", err)
	}
	if string(dec) != "hello there" {
		t.Fatalf("decrypted content mismatch: %s", dec)
	}

	// 2. Non-msg frames pass through unchanged.
	sys := []byte(`{"type":"system","content":"Session closed."}`)
	if got := maybeSealOutbound(sys, vpub); string(got) != string(sys) {
		t.Fatalf("system frame altered: %s", got)
	}

	// 3. Content-less msg frame (e.g. an ack rendered as msg) passes through.
	noContent := []byte(`{"type":"msg","id":"x"}`)
	if got := maybeSealOutbound(noContent, vpub); string(got) != string(noContent) {
		t.Fatalf("contentless msg altered: %s", got)
	}

	// 4. Non-JSON passes through untouched.
	junk := []byte("not json")
	if got := maybeSealOutbound(junk, vpub); string(got) != string(junk) {
		t.Fatalf("junk altered")
	}
}

func TestSealToVisitor_OpensWithVisitorPriv(t *testing.T) {
	vB64, _ := utils.GenerateNoiseStaticKey()
	vpriv, _ := utils.DecodeNoiseStaticKey(vB64)
	vpub, _ := visitorcrypto.PublicKeyFromPrivate(vpriv)

	envB64, err := sealToVisitor(vpub, []byte("agent reply"))
	if err != nil {
		t.Fatalf("seal: %s", err)
	}
	env, _ := base64.RawURLEncoding.DecodeString(envB64)
	got, err := visitorcrypto.Open(vpriv, env)
	if err != nil || string(got) != "agent reply" {
		t.Fatalf("round trip: %v / %s", err, got)
	}
}
