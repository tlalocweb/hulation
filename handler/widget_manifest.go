package handler

// Signed widget integrity manifest — the "trust root off the TLS channel" half
// of the visitor-chat hardening (see hula-mobile/docs/visitor-chat-encryption.md
// §Hardening). Served at GET /<prefix>hula-widget-manifest.json.
//
// The manifest lets a customer (and the running widget) verify that the bytes
// they received for the chat widget + crypto module, and the visitor-chat
// public key they're about to seal to, match what the *install* published —
// even across a TLS-inspecting middlebox or a tampering CDN. It carries:
//
//	{
//	  "v": 1,
//	  "server_id": "<host server id>",
//	  "visitor_chat_public_key_b64": "<X25519 pub, base64url nopad>",
//	  "scripts": {
//	    "hula-chat.js":           "sha384-<base64>",   // rendered for this host
//	    "hula-visitor-crypto.js": "sha384-<base64>"    // static
//	  },
//	  "issued_at": "<rfc3339>",
//	  "sig": "<ed25519 over the canonical message, base64url nopad>"
//	}
//
// The signing key is HKDF-derived from `visitor_chat_key` (domain-separated
// info label) so no new operator-managed secret is introduced: an install that
// has configured visitor-chat encryption automatically has a manifest key. The
// matching public is published at GET /api/v1/installation/identity as
// `widget_manifest_public_key_b64` for out-of-band pinning. When no
// visitor_chat_key is set there is nothing to protect, and the endpoint 404s.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/tune"
	"github.com/tlalocweb/hulation/utils"
	builtinstatics "github.com/tlalocweb/hulation/web/builtin-statics"
)

// manifestSignInfo is the HKDF info label that domain-separates the manifest
// signing key from every other use of visitor_chat_key (the sealed-box recipient
// key). Changing it rotates every install's manifest signing identity.
const manifestSignInfo = "hula-widget-manifest-sign-v1"

// manifestCanonicalPrefix is the first line of the signed message — a version /
// domain tag so a signature can never be confused with another protocol's.
const manifestCanonicalPrefix = "hula-widget-manifest-v1"

// entryScriptName / cryptoScriptName are the manifest's stable script keys
// (basename only — the URL prefix is host config, the integrity is what matters).
const (
	entryScriptName  = "hula-chat.js"
	cryptoScriptName = "hula-visitor-crypto.js"
)

// staticSRICache memoises the SRI of static (non-templated) embedded assets,
// keyed by embed path. The crypto module never changes at runtime, so its hash
// is computed once.
var staticSRICache sync.Map // map[string]string

// WidgetManifest is the JSON wire shape. Field order in JSON is irrelevant —
// the signature is over canonicalManifestMessage, not the JSON encoding.
type WidgetManifest struct {
	V          int               `json:"v"`
	ServerID   string            `json:"server_id"`
	VisitorPub string            `json:"visitor_chat_public_key_b64"`
	Scripts    map[string]string `json:"scripts"`
	IssuedAt   string            `json:"issued_at"`
	Sig        string            `json:"sig,omitempty"`
}

// BuiltinWidgetManifestURL is the request path the manifest is served at. The
// builtin-static prefix already carries the "hula-" branding (default
// "hula-scripts/…"), so the file part is just "widget-manifest.json" — giving
// "/hula-widget-manifest.json" by default rather than a doubled "hula-hula-".
func BuiltinWidgetManifestURL() string {
	return "/" + tune.GetBuiltinStaticPrefix() + "widget-manifest.json"
}

// sriSHA384 returns the Subresource Integrity token for data: the standard
// "sha384-" prefix followed by the *standard* base64 (with padding) of the
// SHA-384 digest, exactly as the browser's `integrity` attribute expects.
func sriSHA384(data []byte) string {
	sum := sha512.Sum384(data)
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// sriErrLogged dedupes the "couldn't hash an embedded asset" error log so a
// misbuild is visible once rather than on every widget request.
var sriErrLogged sync.Map // map[string]struct{}

// staticAssetSRI returns the SRI of an embedded, non-templated asset, computing
// it once and caching. A read failure means a missing/unreadable embedded asset
// (a build error) — it disables SRI pinning for that asset, so log it once so
// the misbuild is detectable rather than silently dropping `integrity`.
func staticAssetSRI(embedPath string) (string, error) {
	if v, ok := staticSRICache.Load(embedPath); ok {
		return v.(string), nil
	}
	data, err := builtinstatics.Get(embedPath)
	if err != nil {
		if _, dup := sriErrLogged.LoadOrStore(embedPath, struct{}{}); !dup {
			log.Errorf("widget-manifest: cannot hash embedded asset %q for SRI: %s (integrity pinning disabled for it)", embedPath, err)
		}
		return "", err
	}
	sri := sriSHA384(data)
	staticSRICache.Store(embedPath, sri)
	return sri, nil
}

// overlaySRI returns the SRI of a customer overlay file for urlPath under the
// host's static root, when one exists. BuiltinStaticHandler serves overlay
// files as-is (no templating), so when an overlay is present the SRI we pin /
// publish MUST be the overlay's hash, not the embedded asset's — otherwise the
// browser's integrity check fails the load on overlay installs. hadOverlay
// reports whether an overlay was found so callers fall back to embedded/rendered
// bytes when it isn't. Overlay files are not cached (they can change on disk).
func overlaySRI(srv *config.Server, urlPath string) (sri string, hadOverlay bool, err error) {
	if srv == nil {
		return "", false, nil
	}
	overlay, ok := resolveOverlay(srv.Root, urlPath)
	if !ok {
		return "", false, nil
	}
	data, rerr := os.ReadFile(overlay)
	if rerr != nil {
		return "", true, rerr
	}
	return sriSHA384(data), true, nil
}

// deriveManifestSigningKey turns the 32-byte visitor_chat secret into a
// deterministic ed25519 signing key via HKDF-SHA256 with a domain-separating
// info label. Same secret → same key across restarts and hosts.
func deriveManifestSigningKey(visitorChatSecret []byte) ed25519.PrivateKey {
	r := hkdf.New(sha256.New, visitorChatSecret, nil, []byte(manifestSignInfo))
	seed := make([]byte, ed25519.SeedSize)
	// HKDF-Expand can serve up to 255*32 bytes; ReadFull of 32 cannot short-read.
	_, _ = io.ReadFull(r, seed)
	return ed25519.NewKeyFromSeed(seed)
}

// manifestSigningKey resolves the install's manifest signing key from config,
// reporting false when visitor-chat encryption (hence the manifest) is disabled.
func manifestSigningKey(cfg *config.Config) (ed25519.PrivateKey, bool) {
	if cfg == nil || cfg.VisitorChatKey == "" {
		return nil, false
	}
	secret, err := utils.DecodeNoiseStaticKey(cfg.VisitorChatKey)
	if err != nil {
		log.Warnf("widget-manifest: decode visitor_chat_key: %s", err)
		return nil, false
	}
	return deriveManifestSigningKey(secret), true
}

// ManifestSigningPublicB64 returns the base64url (no pad) ed25519 public key
// that signs this install's widget manifest, for publishing at
// /api/v1/installation/identity. Reports false when the manifest is disabled.
func ManifestSigningPublicB64(cfg *config.Config) (string, bool) {
	priv, ok := manifestSigningKey(cfg)
	if !ok {
		return "", false
	}
	pub := priv.Public().(ed25519.PublicKey)
	return base64.RawURLEncoding.EncodeToString(pub), true
}

// canonicalManifestMessage builds the exact bytes that get signed/verified.
// A line-oriented, fixed-order encoding (NOT the JSON) so Go and the browser
// can reproduce it byte-for-byte without depending on JSON key ordering. Script
// entries are emitted in sorted key order.
func canonicalManifestMessage(m *WidgetManifest) []byte {
	var b strings.Builder
	b.WriteString(manifestCanonicalPrefix)
	b.WriteByte('\n')
	b.WriteString("server_id=")
	b.WriteString(m.ServerID)
	b.WriteByte('\n')
	b.WriteString("visitor_chat_public_key_b64=")
	b.WriteString(m.VisitorPub)
	b.WriteByte('\n')
	names := make([]string, 0, len(m.Scripts))
	for name := range m.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("script=")
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(m.Scripts[name])
		b.WriteByte('\n')
	}
	b.WriteString("issued_at=")
	b.WriteString(m.IssuedAt)
	b.WriteByte('\n')
	return []byte(b.String())
}

// buildSignedManifest assembles and signs the manifest for one host. entrySRI is
// the SHA-384 of the *rendered* hula-chat.js for this host (computed by the
// caller, since rendering is host-specific); cryptoSRI is the static module's.
func buildSignedManifest(priv ed25519.PrivateKey, serverID, visitorPub, entrySRI, cryptoSRI, issuedAt string) WidgetManifest {
	m := WidgetManifest{
		V:          1,
		ServerID:   serverID,
		VisitorPub: visitorPub,
		Scripts: map[string]string{
			entryScriptName:  entrySRI,
			cryptoScriptName: cryptoSRI,
		},
		IssuedAt: issuedAt,
	}
	sig := ed25519.Sign(priv, canonicalManifestMessage(&m))
	m.Sig = base64.RawURLEncoding.EncodeToString(sig)
	return m
}

// WidgetManifestHandler serves the signed per-host widget manifest. Mirrors
// BuiltinStaticHandler's host resolution. 404 when the install has no
// visitor_chat_key (nothing to protect); 500 only on internal render/embed
// failure.
func WidgetManifestHandler() http.HandlerFunc {
	cfg := app.GetConfig()
	hosts := buildHostLookup(cfg)

	return func(w http.ResponseWriter, r *http.Request) {
		// GET only — the route is registered as "GET <path>"; Go 1.22 method
		// patterns mean HEAD wouldn't reach here anyway.
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		host := normaliseHost(r.Host)
		srv := hosts[host]
		if srv == nil {
			http.Error(w, "unknown host", http.StatusBadRequest)
			return
		}

		// Re-read config each request so a key rotation / reload is reflected
		// without a restart (consistent with installationIdentityHandler).
		liveCfg := app.GetConfig()
		priv, ok := manifestSigningKey(liveCfg)
		if !ok {
			http.Error(w, "widget manifest not configured", http.StatusNotFound)
			return
		}

		// Same config snapshot for both the signing key (above) and the template
		// vars (below) so the signature can't be over a pubkey from a different
		// reload generation.
		vars := buildBuiltinVarsFromConfig(srv, liveCfg)
		visitorPub := vars["visitor_chat_public_key_b64"]
		cryptoSRI := vars["visitor_crypto_sri"]
		if cryptoSRI == "" {
			// Static asset hash should always be available; treat as internal.
			log.Errorf("widget-manifest: missing crypto module SRI")
			http.Error(w, "manifest unavailable", http.StatusInternalServerError)
			return
		}

		// SRI of the entry script as the browser will actually receive it: the
		// customer overlay file when present (served as-is), otherwise the
		// rendered embedded template. Rendering reuses the same template + vars
		// the static handler serves, so the hash matches the served bytes.
		entryAsset := BuiltinChatJSAsset()
		var entrySRI string
		if sri, had, oerr := overlaySRI(srv, entryAsset.URLPath); had {
			if oerr != nil {
				log.Errorf("widget-manifest: read entry overlay for SRI: %s", oerr)
				http.Error(w, "manifest unavailable", http.StatusInternalServerError)
				return
			}
			entrySRI = sri
		} else {
			entryBytes, rerr := renderBuiltinAsset(entryAsset.EmbedPath, vars)
			if rerr != nil {
				log.Errorf("widget-manifest: render entry: %s", rerr)
				http.Error(w, "manifest unavailable", http.StatusInternalServerError)
				return
			}
			entrySRI = sriSHA384(entryBytes)
		}

		manifest := buildSignedManifest(
			priv, srv.ID, visitorPub, entrySRI, cryptoSRI,
			time.Now().UTC().Format(time.RFC3339),
		)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		_ = json.NewEncoder(w).Encode(&manifest)
	}
}
