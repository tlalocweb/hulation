package handler

// Overlay-aware HTTP handler for Hula's built-in static assets
// (chat widget JS + CSS, plus future bundled files).
//
// Per request, the handler:
//
//   1. Resolves the per-host config from the Host header.
//   2. Looks for a customer overlay file at <host.Root>/<URL path>.
//      If present, that file wins and we serve it as-is.
//   3. Otherwise renders the embedded mustache template (loaded from
//      web/builtin-statics) with per-host variables.
//
// The "overlay" mechanism keeps the customer's deployment pattern
// simple: drop a file at <static-root>/<prefix>scripts/hula-chat.js
// and Hula will serve that instead of the built-in widget. Same goes
// for the CSS. The path prefix is configurable via
// tune.GetBuiltinStaticPrefix().

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cbroglie/mustache"

	"github.com/tlalocweb/hulation/app"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/tune"
	"github.com/tlalocweb/hulation/pkg/visitorcrypto"
	"github.com/tlalocweb/hulation/utils"
	builtinstatics "github.com/tlalocweb/hulation/web/builtin-statics"
)

// BuiltinStaticAsset describes a single bundled file the unified
// server should route. EmbedPath is the relative path inside the
// builtinstatics embed.FS; URLPath is the request path the unified
// server will match; ContentType is the response MIME.
type BuiltinStaticAsset struct {
	EmbedPath   string
	URLPath     string
	ContentType string
}

// BuiltinChatJSAsset returns the descriptor for the chat-widget JS,
// at /<prefix>scripts/hula-chat.js.
func BuiltinChatJSAsset() BuiltinStaticAsset {
	return BuiltinStaticAsset{
		EmbedPath:   "scripts/hula-chat.js",
		URLPath:     "/" + tune.GetBuiltinStaticPrefix() + "scripts/hula-chat.js",
		ContentType: "text/javascript; charset=utf-8",
	}
}

// BuiltinChatCryptoJSAsset returns the descriptor for the visitor-chat
// encryption module, at /<prefix>scripts/hula-visitor-crypto.js. Static (no
// per-host template vars) — the widget loads it on demand when the install has
// a visitor_chat_key.
func BuiltinChatCryptoJSAsset() BuiltinStaticAsset {
	return BuiltinStaticAsset{
		EmbedPath:   "scripts/hula-visitor-crypto.js",
		URLPath:     "/" + tune.GetBuiltinStaticPrefix() + "scripts/hula-visitor-crypto.js",
		ContentType: "text/javascript; charset=utf-8",
	}
}

// BuiltinChatCSSAsset returns the descriptor for the chat-widget CSS,
// at /<prefix>styles/hula-chat.css.
func BuiltinChatCSSAsset() BuiltinStaticAsset {
	return BuiltinStaticAsset{
		EmbedPath:   "styles/hula-chat.css",
		URLPath:     "/" + tune.GetBuiltinStaticPrefix() + "styles/hula-chat.css",
		ContentType: "text/css; charset=utf-8",
	}
}

// BuiltinStaticHandler returns a net/http handler for one built-in
// asset. The host-lookup table is captured at registration time from
// app.GetConfig(); the template is parsed on first hit and cached
// for subsequent requests.
func BuiltinStaticHandler(asset BuiltinStaticAsset) http.HandlerFunc {
	cfg := app.GetConfig()
	hosts := buildHostLookup(cfg)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		host := normaliseHost(r.Host)
		srv := hosts[host]
		if srv == nil {
			http.Error(w, "unknown host", http.StatusBadRequest)
			return
		}

		// 1. Customer overlay — same URL path under the host's static
		//    root wins, no templating applied.
		if overlay, ok := resolveOverlay(srv.Root, r.URL.Path); ok {
			w.Header().Set("Content-Type", asset.ContentType)
			http.ServeFile(w, r, overlay)
			return
		}

		// 2. Embedded fallback — parse (cached) + render per request.
		rendered, err := renderBuiltinAsset(asset.EmbedPath, buildBuiltinVars(srv))
		if err != nil {
			log.Errorf("builtinstatic: render %s: %s", asset.EmbedPath, err.Error())
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", asset.ContentType)
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		_, _ = w.Write(rendered)
	}
}

// builtinTmplCache memoises parsed mustache templates by embed path so each
// asset is parsed once across all requests (and across the static handler +
// the widget-manifest handler, which both render the same templates).
var builtinTmplCache sync.Map // map[string]*mustache.Template

// renderBuiltinAsset parses (cached) and renders an embedded asset with vars.
// Shared by BuiltinStaticHandler and the widget-manifest handler so the bytes a
// browser fetches and the bytes we hash for SRI are produced by identical code.
func renderBuiltinAsset(embedPath string, vars map[string]string) ([]byte, error) {
	var tmpl *mustache.Template
	if v, ok := builtinTmplCache.Load(embedPath); ok {
		tmpl = v.(*mustache.Template)
	} else {
		data, err := builtinstatics.Get(embedPath)
		if err != nil {
			return nil, err
		}
		t, err := mustache.ParseString(string(data))
		if err != nil {
			return nil, err
		}
		builtinTmplCache.Store(embedPath, t)
		tmpl = t
	}
	var buf bytes.Buffer
	if err := tmpl.FRender(&buf, vars); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildHostLookup builds host-header → *config.Server for the registered
// hosts (host + aliases). Misconfigured / empty hosts are skipped.
func buildHostLookup(cfg *config.Config) map[string]*config.Server {
	hosts := map[string]*config.Server{}
	if cfg == nil {
		return hosts
	}
	for _, s := range cfg.Servers {
		if s == nil || s.Host == "" {
			continue
		}
		hosts[strings.ToLower(s.Host)] = s
		for _, a := range s.Aliases {
			if a != "" {
				hosts[strings.ToLower(a)] = s
			}
		}
	}
	return hosts
}

// normaliseHost lowercases and strips the port from r.Host, matching
// the same canonicalisation unified_static.go does for static routes.
func normaliseHost(rawHost string) string {
	host := strings.ToLower(rawHost)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}

// resolveOverlay translates a request URL path to an absolute file
// path under root and reports whether a regular file lives there.
// Refuses any path that would resolve outside the root (path
// traversal defence) — those silently fall through to the embedded
// template.
//
// Both sides are resolved to absolute paths and compared via
// filepath.Rel so a "../" segment, a doubled-up "..//.." chain, or a
// canonicalised path that lands outside root all fail the check. The
// returned path is absolute, suitable for passing directly to
// http.ServeFile.
func resolveOverlay(root, urlPath string) (string, bool) {
	if root == "" {
		return "", false
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", false
	}
	rel := strings.TrimPrefix(urlPath, "/")
	candidate := filepath.Join(rootAbs, filepath.FromSlash(rel))
	candidateAbs, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", false
	}
	relToRoot, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", false
	}
	fi, err := os.Stat(candidateAbs)
	if err != nil || fi.IsDir() {
		return "", false
	}
	return candidateAbs, true
}

// buildBuiltinVars constructs the mustache variable map for the
// host. Adding a new {{var}} in a bundled asset means adding it
// here. Stays a flat string map so the same dict feeds JS, CSS, and
// future HTML templates uniformly.
func buildBuiltinVars(srv *config.Server) map[string]string {
	return buildBuiltinVarsFromConfig(srv, app.GetConfig())
}

// buildBuiltinVarsFromConfig is buildBuiltinVars against an explicit config
// snapshot. The widget-manifest handler uses this so the signing key and the
// templated vars (visitor pubkey, SRIs) it signs over come from the SAME config
// pointer — a concurrent ReloadConfig() between two app.GetConfig() reads could
// otherwise sign with one visitor_chat_key while emitting another's public key,
// yielding an intermittently invalid signature.
func buildBuiltinVarsFromConfig(srv *config.Server, cfg *config.Config) map[string]string {
	prefix := tune.GetBuiltinStaticPrefix()

	captchaProvider := ""
	captchaSitekey := ""
	captchaDefault := ""
	if cfg != nil && cfg.Chat != nil && cfg.Chat.Captcha != nil {
		if cfg.Chat.Captcha.TestBypass {
			// Test-bypass mode accepts any non-empty token. Letting
			// the widget pass "dev" through keeps a stock dev install
			// fully working without the customer wiring a real
			// captcha in.
			captchaDefault = "dev"
		}
		// Provider drives which JS API the widget loads (Cloudflare
		// Turnstile vs Google reCAPTCHA v2). Sitekey is the per-
		// deployment public key for whichever provider is selected.
		// Empty sitekey → widget skips the challenge UI entirely and
		// falls back to captcha_default; server still verifies tokens
		// per its own provider config, so dev installs can run with
		// no captcha at all.
		switch cfg.Chat.Captcha.Provider {
		case "", "turnstile":
			captchaProvider = "turnstile"
		case "recaptcha":
			captchaProvider = "recaptcha"
		default:
			// Unknown provider — leave widget without a captcha
			// adapter. Server-side warning is already logged in
			// chat_boot.captchaFromConfig.
		}
		captchaSitekey = cfg.Chat.Captcha.SiteKey
	}

	// Visitor-chat encryption. When the install has a visitor_chat_key, derive
	// its public and template it in so the widget can seal content to it. Empty
	// → widget runs plaintext. The crypto module lives beside the widget under
	// the same static prefix.
	visitorChatPub := ""
	if cfg != nil && cfg.VisitorChatKey != "" {
		if priv, err := utils.DecodeNoiseStaticKey(cfg.VisitorChatKey); err == nil {
			if pub, perr := visitorcrypto.PublicKeyFromPrivate(priv); perr == nil {
				visitorChatPub = base64.RawURLEncoding.EncodeToString(pub)
			}
		}
	}

	// Widget integrity hardening (see widget_manifest.go). visitor_crypto_sri is
	// the SRI of the static crypto module so the widget can pin it when it loads
	// it dynamically. widget_manifest_* let the widget fetch + verify the signed
	// manifest. ALL gated on encryption being configured (visitorChatPub != "")
	// so they're genuinely empty when it's off — the widget runs plaintext and
	// never loads the crypto module, so there's nothing to pin, and we skip the
	// per-render overlay filesystem lookup for non-encryption installs.
	cryptoSRI := ""
	manifestPub := ""
	manifestURL := ""
	if visitorChatPub != "" {
		cryptoAsset := BuiltinChatCryptoJSAsset()
		if sri, had, err := overlaySRI(srv, cryptoAsset.URLPath); had && err == nil {
			// Customer overlay is what the browser actually fetches — pin its hash.
			cryptoSRI = sri
		} else {
			if had && err != nil {
				log.Errorf("builtinstatic: read crypto-module overlay for SRI: %s", err)
			}
			if sri, err := staticAssetSRI(cryptoAsset.EmbedPath); err == nil {
				cryptoSRI = sri
			}
		}
		if pub, ok := ManifestSigningPublicB64(cfg); ok {
			manifestPub = pub
			manifestURL = BuiltinWidgetManifestURL()
		}
	}

	return map[string]string{
		"server_id":             srv.ID,
		"chat_start_url":        "/api/v1/chat/start",
		"chat_ws_url":           "/api/v1/chat/ws",
		"css_url":               "/" + prefix + "styles/hula-chat.css",
		"captcha_provider":      captchaProvider,
		"captcha_sitekey":       captchaSitekey,
		"captcha_token_default": captchaDefault,
		// Empty string when encryption isn't configured; widget treats that as
		// "plaintext mode".
		"visitor_chat_public_key_b64": visitorChatPub,
		"visitor_crypto_url":          "/" + prefix + "scripts/hula-visitor-crypto.js",
		"visitor_crypto_sri":          cryptoSRI,
		"widget_manifest_url":         manifestURL,
		"widget_manifest_public_key_b64": manifestPub,
	}
}
