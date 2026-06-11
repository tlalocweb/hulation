package handler

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlalocweb/hulation/config"
)

// fixedVisitorSecret is a stable 32-byte "visitor_chat_key" secret used across
// the manifest tests so the derived signing key + signatures are reproducible
// (and cross-checkable from the JS test).
func fixedVisitorSecret() []byte {
	return bytes.Repeat([]byte{0x11}, 32)
}

// mustDerive derives a signing key or fails the test (the error path can't
// happen for a 32-byte HKDF read, but we assert it rather than ignore it).
func mustDerive(t *testing.T, secret []byte) ed25519.PrivateKey {
	t.Helper()
	k, err := deriveManifestSigningKey(secret)
	if err != nil {
		t.Fatalf("deriveManifestSigningKey: %v", err)
	}
	return k
}

func TestSRISHA384Format(t *testing.T) {
	got := sriSHA384([]byte("hello"))
	sum := sha512.Sum384([]byte("hello"))
	want := "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("sriSHA384 = %q, want %q", got, want)
	}
	if got[:7] != "sha384-" {
		t.Fatalf("missing sha384- prefix: %q", got)
	}
}

func TestDeriveManifestSigningKeyDeterministicAndSeparated(t *testing.T) {
	secret := fixedVisitorSecret()
	k1 := mustDerive(t, secret)
	k2 := mustDerive(t, secret)
	if !bytes.Equal(k1, k2) {
		t.Fatal("derivation not deterministic for the same secret")
	}
	// Different secret → different key.
	other := mustDerive(t, bytes.Repeat([]byte{0x22}, 32))
	if bytes.Equal(k1, other) {
		t.Fatal("different secrets produced the same key")
	}
	// Domain separation: the signing seed must not be the raw secret.
	if bytes.Equal(k1.Seed(), secret) {
		t.Fatal("signing seed equals the raw visitor_chat secret (no domain separation)")
	}
	// Pin the derived public so the JS cross-check can verify against the same key.
	pub := k1.Public().(ed25519.PublicKey)
	t.Logf("derived manifest pub (b64url): %s", base64.RawURLEncoding.EncodeToString(pub))
}

// canonicalGoldenInputs is the fixed manifest the golden + cross-language tests
// sign. Keep in sync with hula-widget-manifest.test.js.
func canonicalGoldenManifest() *WidgetManifest {
	return &WidgetManifest{
		V:          1,
		ServerID:   "mysite",
		VisitorPub: "dmlzaXRvci1wdWJsaWMta2V5LTMyLWJ5dGVzLW9rITEx",
		Scripts: map[string]string{
			// Deliberately out of sorted order in the map; canonical must sort.
			"hula-visitor-crypto.js": "sha384-CRYPTO",
			"hula-chat.js":           "sha384-ENTRY",
		},
		IssuedAt: "2026-06-10T00:00:00Z",
	}
}

func TestCanonicalManifestMessageGolden(t *testing.T) {
	got := canonicalManifestMessage(canonicalGoldenManifest())
	want := "hula-widget-manifest-v1\n" +
		"server_id=mysite\n" +
		"visitor_chat_public_key_b64=dmlzaXRvci1wdWJsaWMta2V5LTMyLWJ5dGVzLW9rITEx\n" +
		"script=hula-chat.js=sha384-ENTRY\n" +
		"script=hula-visitor-crypto.js=sha384-CRYPTO\n" +
		"issued_at=2026-06-10T00:00:00Z\n"
	if string(got) != want {
		t.Fatalf("canonical mismatch:\n got=%q\nwant=%q", got, want)
	}
	// Emit the hex so the JS test can pin the exact same bytes.
	t.Logf("canonical hex: %s", hex.EncodeToString(got))
}

func TestBuildSignedManifestRoundTrip(t *testing.T) {
	priv := mustDerive(t, fixedVisitorSecret())
	m := canonicalGoldenManifest()
	signed := buildSignedManifest(
		priv, m.ServerID, m.VisitorPub,
		m.Scripts["hula-chat.js"], m.Scripts["hula-visitor-crypto.js"],
		m.IssuedAt,
	)
	if signed.Sig == "" {
		t.Fatal("manifest not signed")
	}
	sig, err := base64.RawURLEncoding.DecodeString(signed.Sig)
	if err != nil {
		t.Fatalf("sig not base64url: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	// The signature must verify over the canonical message rebuilt from the
	// returned manifest (proves the JS verifier, which does the same, will pass).
	if !ed25519.Verify(pub, canonicalManifestMessage(&signed), sig) {
		t.Fatal("signature did not verify over the canonical message")
	}
	// A single-byte tamper of the pubkey must break verification.
	bad := signed
	bad.VisitorPub = bad.VisitorPub + "x"
	if ed25519.Verify(pub, canonicalManifestMessage(&bad), sig) {
		t.Fatal("signature verified over a tampered manifest")
	}
	// Pin the signature for the JS cross-language test.
	t.Logf("golden sig (b64url): %s", signed.Sig)
}

func TestOverlaySRIUsesOverlayBytes(t *testing.T) {
	root := t.TempDir()
	cryptoAsset := BuiltinChatCryptoJSAsset()
	// Place an overlay at exactly the path resolveOverlay looks for (root +
	// the URL path), so BuiltinStaticHandler would serve these bytes.
	rel := strings.TrimPrefix(cryptoAsset.URLPath, "/")
	dest := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	overlay := []byte("// customer overlay crypto module\n")
	if err := os.WriteFile(dest, overlay, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &config.Server{Root: root}
	sri, had, err := overlaySRI(srv, cryptoAsset.URLPath)
	if err != nil || !had {
		t.Fatalf("expected overlay found, had=%v err=%v", had, err)
	}
	if want := sriSHA384(overlay); sri != want {
		t.Fatalf("overlay SRI = %q, want %q (hash of the overlay bytes)", sri, want)
	}
	// Must differ from the embedded module's SRI — otherwise the overlay wasn't
	// actually used and an overlay install would get a mismatched integrity.
	emb, err := staticAssetSRI(cryptoAsset.EmbedPath)
	if err != nil {
		t.Fatalf("embedded SRI: %v", err)
	}
	if sri == emb {
		t.Fatal("overlay SRI equals embedded SRI — overlay bytes not hashed")
	}
}

func TestOverlaySRINoneWithoutFile(t *testing.T) {
	srv := &config.Server{Root: t.TempDir()}
	if _, had, err := overlaySRI(srv, BuiltinChatCryptoJSAsset().URLPath); had || err != nil {
		t.Fatalf("expected no overlay, had=%v err=%v", had, err)
	}
	if _, had, _ := overlaySRI(nil, BuiltinChatCryptoJSAsset().URLPath); had {
		t.Fatal("nil srv must report no overlay")
	}
}

func TestManifestSigningKeyTriState(t *testing.T) {
	// Not configured → (nil, nil): manifest legitimately off (handler 404s).
	if k, err := manifestSigningKey(&config.Config{}); k != nil || err != nil {
		t.Fatalf("no key: got (key=%v, err=%v), want (nil, nil)", k != nil, err)
	}
	// Configured + valid → (key, nil).
	good := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	if k, err := manifestSigningKey(&config.Config{VisitorChatKey: good}); k == nil || err != nil {
		t.Fatalf("valid key: got (key=%v, err=%v), want (key, nil)", k != nil, err)
	}
	// Set but malformed → (nil, err): operator error (handler should 500).
	if k, err := manifestSigningKey(&config.Config{VisitorChatKey: "tooshort"}); k != nil || err == nil {
		t.Fatalf("bad key: got (key=%v, err=%v), want (nil, err)", k != nil, err)
	}
}

func TestBuiltinVarsGateIntegrityOnEncryption(t *testing.T) {
	srv := &config.Server{ID: "mysite"}

	// Encryption OFF (no visitor_chat_key): integrity vars must be empty so the
	// widget's "all empty when off" contract holds and we skip overlay lookups.
	off := buildBuiltinVarsFromConfig(srv, &config.Config{})
	for _, k := range []string{"visitor_chat_public_key_b64", "visitor_crypto_sri", "widget_manifest_url", "widget_manifest_public_key_b64"} {
		if off[k] != "" {
			t.Fatalf("encryption off: %s = %q, want empty", k, off[k])
		}
	}

	// Encryption ON: the same vars become non-empty.
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	on := buildBuiltinVarsFromConfig(srv, &config.Config{VisitorChatKey: key})
	for _, k := range []string{"visitor_chat_public_key_b64", "visitor_crypto_sri", "widget_manifest_url", "widget_manifest_public_key_b64"} {
		if on[k] == "" {
			t.Fatalf("encryption on: %s is empty, want set", k)
		}
	}
}

func TestStaticAssetSRIMatchesEmbed(t *testing.T) {
	// The crypto module must hash to a real sha384 SRI (it's the value templated
	// into the widget + listed in the manifest).
	sri, err := staticAssetSRI(BuiltinChatCryptoJSAsset().EmbedPath)
	if err != nil {
		t.Fatalf("staticAssetSRI: %v", err)
	}
	if len(sri) < 8 || sri[:7] != "sha384-" {
		t.Fatalf("bad SRI: %q", sri)
	}
	// Cached second call returns the same value.
	sri2, err := staticAssetSRI(BuiltinChatCryptoJSAsset().EmbedPath)
	if err != nil {
		t.Fatalf("staticAssetSRI (cached): %v", err)
	}
	if sri != sri2 {
		t.Fatalf("cached SRI differs: %q vs %q", sri, sri2)
	}
}
