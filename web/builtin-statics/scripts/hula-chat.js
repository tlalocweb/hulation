// Hula built-in visitor chat widget.
//
// Mustache-templated by handler/builtinstatic.go. Per-host vars:
//
//   {{server_id}}             — host's configured server ID
//   {{chat_start_url}}        — full path to POST /api/v1/chat/start
//   {{chat_ws_url}}           — default WS path (response may override)
//   {{css_url}}               — URL for the bundled stylesheet
//   {{turnstile_token_default}} — fallback captcha token ("dev" in dev,
//                                  "" in prod; widget passes through)
//
// Embed: <script src="/{prefix}scripts/hula-chat.js" defer></script>.
// Trigger: any element with [data-hula-chat-open] is bound; if none
// exists, a floating bottom-right button is auto-injected.
//
// Customer override: drop a file at <static-root>/{prefix}scripts/hula-chat.js
// in the per-host root directory. The static-overlay check in the
// handler will serve that file instead.

(function () {
  "use strict";

  var CFG = {
    serverId:         "{{server_id}}",
    chatStartUrl:     "{{chat_start_url}}",
    chatWsUrl:        "{{chat_ws_url}}",
    // Resume re-issues a chat token for a session persisted across a page
    // refresh (never mints a new one); close ends the chat explicitly.
    chatResumeUrl:    "{{chat_resume_url}}",
    chatCloseUrl:     "{{chat_close_url}}",
    cssUrl:           "{{css_url}}",
    // captchaProvider is "turnstile" | "recaptcha" | "" (none).
    // sitekey is the per-provider public key; default is what we
    // send as turnstile_token when the widget has no challenge to
    // render (test_bypass mode in the server's chat config).
    captchaProvider:  "{{captcha_provider}}",
    captchaSitekey:   "{{captcha_sitekey}}",
    captchaDefault:   "{{captcha_token_default}}",
    // Visitor-chat encryption. visitorChatPub is the installation's
    // visitor-chat X25519 public (base64url, no padding); empty when the
    // install hasn't configured visitor_chat_key. cryptoUrl loads the crypto
    // module on demand. When either is missing — or the browser lacks
    // WebCrypto X25519 — the widget runs in plaintext (chat still works).
    visitorChatPub:   "{{visitor_chat_public_key_b64}}",
    cryptoUrl:        "{{visitor_crypto_url}}",
    // Integrity hardening (see the hula-mobile repo's
    // docs/visitor-chat-encryption.md §Hardening; not in this repo).
    // cryptoSri pins the dynamically-loaded crypto module via Subresource
    // Integrity. manifestUrl + manifestKey let the widget fetch + verify the
    // signed widget manifest (ed25519) and cross-check the server pubkey it's
    // about to seal to. All empty when encryption / manifest signing is off.
    cryptoSri:        "{{visitor_crypto_sri}}",
    manifestUrl:      "{{widget_manifest_url}}",
    manifestKey:      "{{widget_manifest_public_key_b64}}",
  };

  // --- Session persistence ------------------------------------------
  //
  // The chat session outlives the page. Without this the {session_id,
  // chat_token} pair lived only in a closure variable, so any refresh
  // stranded the conversation server-side and started a brand-new one —
  // the visitor lost their chat and the agent saw a duplicate.
  //
  // localStorage (not sessionStorage) so the chat also survives closing the
  // tab and coming back, which is what an explicit "End chat" implies: the
  // conversation persists until deliberately ended.
  //
  // The stored token is a bearer credential, so: it is scoped per server_id
  // and re-checked on load (a token minted for one site is never replayed at
  // another), and it is cleared the moment the session goes terminal. How
  // long it stays useful is the server's call, not ours — chat.resume_window.
  //
  // Every access is guarded: localStorage throws in Safari private mode and
  // when the visitor has site data disabled. On failure the widget silently
  // degrades to the old in-memory-only behaviour rather than breaking chat.

  var STORAGE_VERSION = 1;
  var STORAGE_KEY = "hula-chat:" + CFG.serverId;

  function storageSave(sess, panelOpen) {
    if (!sess || !sess.chat_token || !sess.session_id) return;
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify({
        v:          STORAGE_VERSION,
        server_id:  CFG.serverId,
        session_id: sess.session_id,
        chat_token: sess.chat_token,
        chat_url:   sess.chat_url || "",
        open:       !!panelOpen,
      }));
    } catch (_) { /* private mode / storage disabled — in-memory only */ }
  }

  function storageLoad() {
    try {
      var raw = window.localStorage.getItem(STORAGE_KEY);
      if (!raw) return null;
      var s = JSON.parse(raw);
      if (!s || s.v !== STORAGE_VERSION) return null;
      if (!s.chat_token || !s.session_id) return null;
      // Defence in depth: the key is already server-scoped, but never hand a
      // token to a host it wasn't issued for.
      if (s.server_id !== CFG.serverId) return null;
      return s;
    } catch (_) { return null; }
  }

  function storageClear() {
    try { window.localStorage.removeItem(STORAGE_KEY); } catch (_) {}
  }

  // --- Visitor-chat encryption -------------------------------------
  //
  // enc holds the negotiated crypto state for this page session:
  //   .ready       — true once the module loaded + a session keypair exists
  //   .serverPub   — Uint8Array(32) server recipient key
  //   .sessionPriv — Uint8Array(32) our per-session private (opens agent replies)
  //   .sessionPub  — Uint8Array(32) our per-session public (sent as ?vpub=)
  // All null/false when encryption is unavailable → widget falls back to
  // plaintext transparently.
  var enc = { ready: false, serverPub: null, sessionPriv: null, sessionPub: null };

  function loadCryptoModule() {
    return new Promise(function (resolve) {
      if (window.HulaVisitorCrypto) return resolve(window.HulaVisitorCrypto);
      if (!CFG.cryptoUrl) return resolve(null);
      var s = document.createElement("script");
      // Pin the crypto module via SRI when the server published a hash. Set
      // integrity + crossOrigin BEFORE src: some browsers may start the fetch
      // the instant src is assigned, so the integrity attribute must already be
      // in place for the request to be consistently integrity-protected. A
      // tampered module then fails the check, the browser refuses to run it,
      // onerror fires, and we fall back to plaintext — attacker code can never
      // execute in place of the real crypto. crossOrigin is required for the
      // browser to enforce integrity on the fetched script.
      if (CFG.cryptoSri) {
        s.integrity = CFG.cryptoSri;
        s.crossOrigin = "anonymous";
      }
      s.async = true;
      s.src = CFG.cryptoUrl;
      s.onload = function () { resolve(window.HulaVisitorCrypto || null); };
      s.onerror = function () { resolve(null); };
      document.head.appendChild(s);
    });
  }

  // --- Signed widget manifest verification -------------------------
  //
  // Best-effort integrity cross-check. Returns a promise resolving to:
  //   "ok"       — manifest signature valid AND its pubkey/SRI match what this
  //                page received (proceed with encryption).
  //   "skip"     — no manifest configured, ed25519 verify unavailable, or the
  //                manifest couldn't be fetched/parsed (transient). Proceed
  //                with SRI-only protection.
  //   "tampered" — a cryptographic contradiction: the manifest fails to verify,
  //                or verifies but pins a DIFFERENT pubkey/SRI than we hold.
  //                Treat the encrypted path as untrustworthy.
  //
  // This is detection + an out-of-band-verifiable artifact, not enforcement:
  // on "tampered" the widget refuses to seal to the suspect key and runs
  // plaintext (the documented graceful-fallback policy). Hard fail-closed is
  // the separate "require encryption" config knob.
  function ed25519Supported() {
    // crypto.subtle for importKey/verify AND TextEncoder for manifestCanonical.
    // Missing either is a capability gap → we want a clean "skip", not a false
    // "tampered" from manifestCanonical throwing after the manifest is fetched.
    return !!(window.crypto && window.crypto.subtle && typeof TextEncoder !== "undefined");
  }

  function manifestCanonical(m) {
    // MUST match handler/widget_manifest.go::canonicalManifestMessage byte-for-byte.
    var lines = ["hula-widget-manifest-v1"];
    lines.push("server_id=" + (m.server_id || ""));
    lines.push("visitor_chat_public_key_b64=" + (m.visitor_chat_public_key_b64 || ""));
    var scripts = m.scripts || {};
    Object.keys(scripts).sort().forEach(function (name) {
      lines.push("script=" + name + "=" + scripts[name]);
    });
    lines.push("issued_at=" + (m.issued_at || ""));
    // Trailing newline after every line, including the last (Go writes '\n' per line).
    return new TextEncoder().encode(lines.join("\n") + "\n");
  }

  function verifyManifest(mod) {
    if (!CFG.manifestUrl || !CFG.manifestKey) return Promise.resolve("skip");
    if (!ed25519Supported()) return Promise.resolve("skip");

    // `fetched` flips once we hold a parsed manifest. It's the line between
    // "couldn't check" and "checked and it's wrong":
    //   - errors BEFORE it (bad pinned key, ed25519 unsupported, fetch/parse
    //     failure) are capability/transient → "skip" (SRI-only protection).
    //   - errors AFTER it (sig won't base64-decode, importKey/verify throws a
    //     DataError/OperationError on malformed material, sig invalid, or a
    //     pubkey/SRI mismatch) are properties of the manifest we received →
    //     "tampered". WebCrypto can THROW rather than return false on malformed
    //     signatures, so a bare catch must not silently downgrade those.
    var fetched = false;

    // Decode + import the *pinned* key first. A failure here is our own config
    // or a capability gap, not manifest tampering.
    var pinnedKey;
    try {
      pinnedKey = mod.b64urlDecode(CFG.manifestKey);
    } catch (_) {
      warn("widget manifest: pinned key not decodable (config); skipping check");
      return Promise.resolve("skip");
    }

    return window.crypto.subtle
      .importKey("raw", pinnedKey, { name: "Ed25519" }, false, ["verify"])
      .then(function (key) {
        return fetch(CFG.manifestUrl, { credentials: "omit", cache: "no-store" })
          .then(function (resp) {
            if (!resp.ok) throw new Error("manifest fetch " + resp.status);
            return resp.json();
          })
          .then(function (m) {
            fetched = true; // from here, any failure is the manifest's fault
            var sigBytes = mod.b64urlDecode(m.sig || "");
            var msgBytes = manifestCanonical(m);
            return window.crypto.subtle
              .verify({ name: "Ed25519" }, key, sigBytes, msgBytes)
              .then(function (valid) {
                if (!valid) {
                  warn("widget manifest signature did not verify — refusing to trust encryption keys");
                  return "tampered";
                }
                // Authentic signature. Ensure the page got the SAME pubkey + SRI
                // the install signed; a mismatch means something swapped the
                // key/module after signing.
                if (m.visitor_chat_public_key_b64 !== CFG.visitorChatPub) {
                  warn("widget manifest pubkey mismatch — refusing to seal to a swapped key");
                  return "tampered";
                }
                var sm = (m.scripts || {})["hula-visitor-crypto.js"];
                if (CFG.cryptoSri && sm && sm !== CFG.cryptoSri) {
                  warn("widget manifest crypto-module SRI mismatch");
                  return "tampered";
                }
                return "ok";
              });
          });
      })
      .catch(function (e) {
        warn("widget manifest check: " + (e && e.message ? e.message : e));
        // A manifest we fetched but couldn't decode/verify is a tamper signal;
        // an importKey/capability or fetch failure is not.
        return fetched ? "tampered" : "skip";
      });
  }

  function warn(msg) {
    try { (window.console || {}).warn && console.warn("[hula] " + msg); } catch (_) {}
  }

  // Best-effort: returns a promise that resolves true when encryption is set
  // up for this session, false when we should run plaintext. Never rejects.
  function initEncryption() {
    if (!CFG.visitorChatPub || !CFG.cryptoUrl) return Promise.resolve(false);
    return loadCryptoModule().then(function (mod) {
      if (!mod || !mod.isSupported()) return false;
      // Cross-check the signed widget manifest before trusting any key. On a
      // positive tamper signal, refuse encryption (run plaintext) rather than
      // seal to a key an attacker may have swapped.
      return verifyManifest(mod).then(function (status) {
        if (status === "tampered") return false;
        return finishInitEncryption(mod);
      });
    });
  }

  function finishInitEncryption(mod) {
    return Promise.resolve().then(function () {
      var serverPub;
      try {
        serverPub = mod.b64urlDecode(CFG.visitorChatPub);
      } catch (_) { return false; }
      if (serverPub.length !== 32) return false;
      // Generate a per-session keypair. We need the raw private to open agent
      // replies, so generate raw bytes and derive the public via the module.
      // A missing CSPRNG must NOT proceed: an all-zero (or non-random) private
      // is a predictable key that defeats the encryption entirely — treat it as
      // "encryption unsupported" and fall back to plaintext.
      if (!window.crypto || typeof window.crypto.getRandomValues !== "function") {
        return false;
      }
      var priv = new Uint8Array(32);
      window.crypto.getRandomValues(priv);
      return mod.publicFromPrivate(priv).then(function (pub) {
        enc.serverPub = serverPub;
        enc.sessionPriv = priv;
        enc.sessionPub = pub;
        enc.ready = true;
        return true;
      }).catch(function () { return false; });
    });
  }

  // --- CSS injection ------------------------------------------------

  function injectStylesheet() {
    if (!CFG.cssUrl) return;
    if (document.querySelector('link[data-hula-chat-css]')) return;
    var link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = CFG.cssUrl;
    link.setAttribute("data-hula-chat-css", "1");
    document.head.appendChild(link);
  }

  // --- DOM construction ---------------------------------------------

  function makeEl(tag, attrs, html) {
    var el = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (k === "class") el.className = attrs[k];
        else if (k === "text") el.textContent = attrs[k];
        else el.setAttribute(k, attrs[k]);
      }
    }
    if (html != null) el.innerHTML = html;
    return el;
  }

  function buildPanel() {
    var root = makeEl("section", { id: "hula-chat", "aria-hidden": "true" });
    root.innerHTML = ''
      + '<header class="hc-bar">'
      +   '<strong>Chat with us</strong>'
      +   '<span class="hc-bar-actions">'
      // "End chat" is deliberately distinct from the &times; button: &times;
      // only hides the panel and leaves the conversation live, whereas this
      // ends it for good (after confirmation). Hidden until a session exists.
      +     '<button type="button" class="hc-end" hidden>End chat</button>'
      +     '<button class="hc-close" aria-label="Close">&times;</button>'
      +   '</span>'
      + '</header>'
      + '<div class="hc-start">'
      +   '<label>Your email</label>'
      +   '<input type="email" name="email" placeholder="you@example.com" autocomplete="email" />'
      +   '<label>How can we help?</label>'
      +   '<textarea name="first" placeholder="Hi! I have a question…"></textarea>'
      +   '<div class="hc-captcha" id="hc-captcha" hidden></div>'
      +   '<div class="hc-error" id="hc-error" hidden></div>'
      +   '<button class="hc-primary hc-start-btn" type="button">Start chat</button>'
      + '</div>'
      + '<div class="hc-log" hidden></div>'
      + '<div class="hc-composer" hidden>'
      +   '<input type="text" name="msg" placeholder="Type a message…" />'
      +   '<button type="button" class="hc-send" disabled>Send</button>'
      + '</div>'
      + '<div class="hc-ended" hidden>'
      +   '<p class="hc-ended-note">This chat has ended.</p>'
      +   '<button type="button" class="hc-primary hc-restart">Start new chat</button>'
      + '</div>'
      // Shown when the server reports this session was taken over by another
      // tab. The socket is NOT auto-reconnected in that state (that would
      // livelock two tabs kicking each other); the visitor opts back in here.
      + '<div class="hc-takeover" hidden>'
      +   '<p class="hc-takeover-note">This chat is open in another tab.</p>'
      +   '<button type="button" class="hc-primary hc-resume-here">Continue here</button>'
      + '</div>'
      // In-widget confirmation for "End chat". Built here rather than using
      // window.confirm so it is styleable, themed, and doesn't block the JS
      // thread (which would stall the socket).
      + '<div class="hc-confirm" hidden role="dialog" aria-modal="true" aria-labelledby="hc-confirm-title">'
      +   '<div class="hc-confirm-box">'
      +     '<p class="hc-confirm-title" id="hc-confirm-title">End this chat?</p>'
      +     '<p class="hc-confirm-note">You won\'t be able to send any more messages in this conversation.</p>'
      +     '<div class="hc-confirm-actions">'
      +       '<button type="button" class="hc-btn-secondary hc-confirm-cancel">Cancel</button>'
      +       '<button type="button" class="hc-btn-danger hc-confirm-ok">End chat</button>'
      +     '</div>'
      +   '</div>'
      + '</div>';
    document.body.appendChild(root);
    return root;
  }

  function buildFAB(onClick) {
    var btn = makeEl("button", {
      id: "hula-chat-fab",
      type: "button",
      "aria-label": "Open chat",
    });
    btn.innerHTML = '<span class="hc-fab-dot"></span>Chat';
    btn.addEventListener("click", onClick);
    document.body.appendChild(btn);
    return btn;
  }

  // --- Geolocation (silent — only when already granted) -------------

  // Returns a Promise that resolves to {latitude, longitude} or null.
  // Never triggers the browser's permission prompt: if the visitor
  // hasn't already granted geolocation to this origin, we return
  // null and the server falls back to IP-based country lookup.
  function trySilentGeolocation() {
    if (!navigator.geolocation || !navigator.permissions) {
      return Promise.resolve(null);
    }
    return navigator.permissions
      .query({ name: "geolocation" })
      .then(function (status) {
        if (status.state !== "granted") return null;
        return new Promise(function (resolve) {
          var settled = false;
          var timer = setTimeout(function () {
            if (!settled) { settled = true; resolve(null); }
          }, 3000);
          navigator.geolocation.getCurrentPosition(
            function (pos) {
              if (settled) return;
              settled = true;
              clearTimeout(timer);
              resolve({
                latitude: pos.coords.latitude,
                longitude: pos.coords.longitude,
              });
            },
            function () {
              if (settled) return;
              settled = true;
              clearTimeout(timer);
              resolve(null);
            },
            { timeout: 3000, maximumAge: 5 * 60 * 1000 }
          );
        });
      })
      .catch(function () { return null; });
  }

  // --- Captcha (optional, provider-dispatched) ---------------------

  // Captcha adapters share a small surface: load() returns a Promise
  // that resolves when the provider's JS API is callable on window;
  // render(container) draws the challenge and remembers its widget
  // ID; getToken() returns the user-supplied response string (empty
  // until the user solves the challenge); reset() clears the
  // challenge so the next chat-start attempt uses a fresh token.
  //
  // The script tag is injected at most once per page (data-attr
  // sentinel). When a customer's site already loaded the script
  // (e.g. for an unrelated form), we detect window.turnstile /
  // window.grecaptcha and skip injection entirely.

  function makeCaptchaAdapter() {
    if (!CFG.captchaProvider || !CFG.captchaSitekey) return null;
    if (CFG.captchaProvider === "turnstile") return turnstileAdapter();
    if (CFG.captchaProvider === "recaptcha") return recaptchaAdapter();
    return null;
  }

  // Generic helper: inject a third-party script tag exactly once,
  // poll for the global object's readiness, resolve when ready.
  function injectAndAwait(opts) {
    return new Promise(function (resolve) {
      if (opts.isReady()) { resolve(); return; }
      if (!document.querySelector('script[data-hula-captcha="' + opts.sentinel + '"]')) {
        var s = document.createElement("script");
        s.src = opts.src;
        s.async = true;
        s.defer = true;
        s.setAttribute("data-hula-captcha", opts.sentinel);
        document.head.appendChild(s);
      }
      // 50ms × 200 = 10s budget; submit path surfaces the empty-token
      // error if the provider script never finishes loading.
      var tries = 0;
      var iv = setInterval(function () {
        if (opts.isReady()) {
          clearInterval(iv);
          resolve();
        } else if (++tries > 200) {
          clearInterval(iv);
          resolve();
        }
      }, 50);
    });
  }

  function turnstileAdapter() {
    var ready = null;
    var widgetId = null;
    return {
      providerName: "turnstile",
      load: function () {
        if (ready) return ready;
        ready = injectAndAwait({
          sentinel: "turnstile",
          src: "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit",
          isReady: function () {
            return window.turnstile && typeof window.turnstile.render === "function";
          },
        });
        return ready;
      },
      render: function (container) {
        if (!window.turnstile) return;
        container.hidden = false;
        if (widgetId !== null) {
          window.turnstile.reset(widgetId);
          return;
        }
        widgetId = window.turnstile.render(container, { sitekey: CFG.captchaSitekey });
      },
      getToken: function () {
        if (!window.turnstile || widgetId === null) return "";
        return window.turnstile.getResponse(widgetId) || "";
      },
      reset: function () {
        if (window.turnstile && widgetId !== null) window.turnstile.reset(widgetId);
      },
    };
  }

  function recaptchaAdapter() {
    var ready = null;
    var widgetId = null;
    return {
      providerName: "recaptcha",
      load: function () {
        if (ready) return ready;
        // ?render=explicit prevents grecaptcha from auto-rendering
        // anything based on the .g-recaptcha class; we render the
        // widget ourselves inside the chat panel.
        ready = injectAndAwait({
          sentinel: "recaptcha",
          src: "https://www.google.com/recaptcha/api.js?render=explicit",
          isReady: function () {
            return window.grecaptcha && typeof window.grecaptcha.render === "function";
          },
        });
        return ready;
      },
      render: function (container) {
        if (!window.grecaptcha) return;
        container.hidden = false;
        if (widgetId !== null) {
          window.grecaptcha.reset(widgetId);
          return;
        }
        widgetId = window.grecaptcha.render(container, { sitekey: CFG.captchaSitekey });
      },
      getToken: function () {
        if (!window.grecaptcha || widgetId === null) return "";
        return window.grecaptcha.getResponse(widgetId) || "";
      },
      reset: function () {
        if (window.grecaptcha && widgetId !== null) window.grecaptcha.reset(widgetId);
      },
    };
  }

  // Single per-page adapter instance; null when no captcha configured.
  var CAPTCHA = makeCaptchaAdapter();

  // --- Main controller ----------------------------------------------

  function mount() {
    injectStylesheet();
    // Kick off encryption setup as early as possible so enc.ready is set by
    // the time the visitor finishes typing the form. Non-blocking; the submit
    // handler tolerates it not yet being ready (falls back to plaintext for
    // that one chat-start, then the WS still uses whatever state exists).
    // The promise is retained because the resume-on-load path below MUST
    // await it: dialing before the keypair exists would present no ?vpub= and
    // silently drop the connection to plaintext.
    var encSettled = initEncryption().catch(function () { return false; });
    var panel = buildPanel();

    var startEl    = panel.querySelector(".hc-start");
    var logEl      = panel.querySelector(".hc-log");
    var composerEl = panel.querySelector(".hc-composer");
    var endedEl    = panel.querySelector(".hc-ended");
    var takeoverEl = panel.querySelector(".hc-takeover");
    var confirmEl  = panel.querySelector(".hc-confirm");
    var emailInput = panel.querySelector('input[name="email"]');
    var firstInput = panel.querySelector('textarea[name="first"]');
    var msgInput   = panel.querySelector('input[name="msg"]');
    var startBtn   = panel.querySelector(".hc-start-btn");
    var sendBtn    = panel.querySelector(".hc-send");
    var restartBtn = panel.querySelector(".hc-restart");
    var closeBtn   = panel.querySelector(".hc-close");
    var endBtn     = panel.querySelector(".hc-end");
    var resumeHereBtn  = panel.querySelector(".hc-resume-here");
    var confirmCancel  = panel.querySelector(".hc-confirm-cancel");
    var confirmOk      = panel.querySelector(".hc-confirm-ok");
    var captchaContainer = panel.querySelector("#hc-captcha");
    var errorEl    = panel.querySelector("#hc-error");

    var ws = null;
    var session = null;
    // chatEnded latches once the server declares the session terminal
    // (session_closed frame). It gates the composer AND suppresses the
    // generic "Disconnected." notice — an explicit close is final, not
    // a "you can keep typing" reconnect prompt.
    var chatEnded = false;
    // takenOver latches when the server reports another tab claimed this
    // session. Suppresses auto-reconnect: two tabs that both redialled would
    // kick each other forever.
    var takenOver = false;
    // seenIds de-dupes messages that arrive both in the replayed history and
    // as a live frame (possible for anything published between subscribe and
    // the history read).
    var seenIds = {};
    // historyPending / deferredMsgs hold live frames while an async history
    // render is in flight, so the authoritative swap can't discard them.
    var historyPending = false;
    var deferredMsgs = [];
    // wsGeneration increments per dial. Reconnects are routine now, so an
    // in-flight async render from a replaced socket must not overwrite the
    // current one's transcript when it finally resolves.
    var wsGeneration = 0;
    // statusNote is a local-only sys line (e.g. "waiting for an agent") that
    // must survive a history re-render — the server has no copy of it, so the
    // authoritative-transcript swap would otherwise drop it for good.
    var statusNote = "";
    // Reconnect backoff, reset on every successful open.
    var RECONNECT_BACKOFF_MS = [1000, 2000, 4000, 8000, 16000, 30000];
    var backoffIdx = 0;
    var reconnectTimer = null;
    var endFallbackTimer = null;

    function showError(msg) {
      if (!errorEl) return;
      errorEl.textContent = msg || "";
      errorEl.hidden = !msg;
    }

    function appendMsg(kind, text) {
      var m = makeEl("div", { class: "hc-msg hc-msg-" + kind, text: text });
      logEl.appendChild(m);
      logEl.scrollTop = logEl.scrollHeight;
    }

    function open() {
      panel.classList.add("hc-open");
      panel.setAttribute("aria-hidden", "false");
      (session ? msgInput : emailInput).focus();
      // Remember the panel was open so a refresh restores it open rather than
      // collapsed — otherwise "the chat survived" isn't visible to the user.
      if (session) storageSave(session, true);
      // Lazily load + render the captcha each open. Already-loaded
      // script short-circuits; already-rendered widget gets reset.
      if (CAPTCHA && !session) {
        CAPTCHA.load().then(function () { CAPTCHA.render(captchaContainer); });
      }
    }
    function close() {
      panel.classList.remove("hc-open");
      panel.setAttribute("aria-hidden", "true");
      // Note: this only hides the panel. The socket stays open and the chat
      // stays live — ending it is the separate "End chat" action.
      if (session) storageSave(session, false);
    }

    closeBtn.addEventListener("click", close);

    startBtn.addEventListener("click", function () {
      var email = (emailInput.value || "").trim();
      var first = (firstInput.value || "").trim();
      if (!email) { emailInput.focus(); return; }

      // Captcha token wins over the dev-mode default when an adapter
      // is configured. Empty token + configured captcha is a user
      // error ("please complete the challenge"); empty token + no
      // captcha is fine — server runs in test_bypass / no-captcha.
      var token = CFG.captchaDefault;
      if (CAPTCHA) {
        token = CAPTCHA.getToken();
        if (!token) {
          showError("Please complete the security check.");
          return;
        }
      }
      showError("");

      startBtn.disabled = true;
      var prevLabel = startBtn.textContent;
      startBtn.textContent = "Starting…";

      trySilentGeolocation().then(function (coords) {
        var body = {
          server_id:       CFG.serverId,
          turnstile_token: token,
        };
        if (coords) {
          body.latitude  = coords.latitude;
          body.longitude = coords.longitude;
        }
        // Encrypted path: seal {email, first_message} to the server's
        // visitor-chat key so a TLS-inspecting middlebox sees only ciphertext.
        // Falls back to plaintext fields when encryption isn't available.
        if (enc.ready) {
          var payload = new TextEncoder().encode(
            JSON.stringify({ email: email, first_message: first })
          );
          return window.HulaVisitorCrypto.seal(enc.serverPub, payload).then(function (envBytes) {
            body.enc = window.HulaVisitorCrypto.b64urlEncode(envBytes);
            return fetch(CFG.chatStartUrl, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              credentials: "omit",
              body: JSON.stringify(body),
            });
          });
        }
        body.email = email;
        body.first_message = first;
        return fetch(CFG.chatStartUrl, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "omit",
          body: JSON.stringify(body),
        });
      }).then(function (res) {
        return res.json().then(function (body) {
          if (!res.ok) {
            var err = new Error(body.message || ("chat-start " + res.status));
            err.code = body.code || "";
            throw err;
          }
          return body;
        });
      }).then(function (sess) {
        session = sess;
        // Persist immediately: from here a refresh must resume this session
        // rather than strand it and start another.
        storageSave(sess, true);
        enterChatUI();
        statusNote = "Session " + sess.session_id.slice(0, 8) + " — waiting for an agent…";
        appendMsg("sys", statusNote);
        if (first) appendMsg("me", first);
        openWebSocket();
      }).catch(function (e) {
        startBtn.disabled = false;
        startBtn.textContent = prevLabel;
        // Token is single-use — any server-side rejection means the
        // next attempt needs a fresh one.
        if (CAPTCHA) CAPTCHA.reset();
        showError("Couldn't start chat: " + (e && e.message ? e.message : e));
      });
    });

    // enterChatUI swaps the panel from the pre-chat form to the live
    // conversation view. Shared by the fresh-start and resume paths.
    function enterChatUI() {
      startEl.hidden = true;
      logEl.hidden = false;
      composerEl.hidden = false;
      takeoverEl.hidden = true;
      endedEl.hidden = true;
      endBtn.hidden = false;
    }

    // --- Resume -------------------------------------------------------
    //
    // Re-credential a persisted session. Never mints a new one: on any
    // failure the widget drops the stored session and shows the start form,
    // so a stale token can't wedge the widget.
    //
    // Called both on page load (the refresh case) and before each reconnect —
    // the chat token is short-lived, so a socket that drops after it expires
    // needs a fresh one before redialling.
    function requestResume(chatToken) {
      return fetch(CFG.chatResumeUrl, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "omit",
        body: JSON.stringify({ chat_token: chatToken }),
      }).then(function (res) {
        return res.json().then(function (body) {
          if (!res.ok) {
            var err = new Error(body.message || ("chat-resume " + res.status));
            err.code = body.code || "";
            throw err;
          }
          return body;
        });
      });
    }

    // resumeOnLoad runs at mount when a stored session exists. Success
    // rebuilds the conversation view and dials the socket, which replays the
    // transcript. Failure is silent: the visitor just sees the normal start
    // form, exactly as before this feature existed.
    function resumeOnLoad(stored) {
      requestResume(stored.chat_token).then(function (sess) {
        session = sess;
        storageSave(sess, stored.open);
        enterChatUI();
        openWebSocket();
        if (stored.open) open();
      }).catch(function () {
        // Ended, aged out, or no longer valid — nothing to restore.
        storageClear();
      });
    }

    function clearReconnect() {
      if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
    }

    // scheduleReconnect backs off 1/2/4/8/16/30s. Suppressed for terminal and
    // taken-over sessions — both are states the socket must NOT come back
    // from on its own.
    function scheduleReconnect() {
      if (chatEnded || takenOver || !session) return;
      var delay = RECONNECT_BACKOFF_MS[Math.min(backoffIdx, RECONNECT_BACKOFF_MS.length - 1)];
      backoffIdx++;
      clearReconnect();
      reconnectTimer = setTimeout(reconnectNow, delay);
    }

    // reconnectNow refreshes the token first, then redials. Going through
    // resume (rather than reusing the in-memory token) is what lets a socket
    // that dropped for longer than the token's 30-minute life come back.
    function reconnectNow() {
      if (chatEnded || takenOver || !session) return;
      requestResume(session.chat_token).then(function (sess) {
        session = sess;
        storageSave(sess, panel.classList.contains("hc-open"));
        openWebSocket();
      }).catch(function (e) {
        // The session is gone for good (closed, swept, or past the resume
        // window). Surface it as a normal ending rather than retrying
        // forever against something that will never come back.
        if (e && (e.code === "session_closed" || e.code === "resume_expired" || e.code === "cannot_resume")) {
          endChat();
          return;
        }
        // Transient (offline, 5xx) — keep trying.
        scheduleReconnect();
      });
    }
    resumeHereBtn.addEventListener("click", function () {
      takenOver = false;
      takeoverEl.hidden = true;
      composerEl.hidden = false;
      msgInput.disabled = false;
      backoffIdx = 0;
      appendMsg("sys", "Reconnecting…");
      reconnectNow();
    });

    // renderHistory replaces the transcript with the server's authoritative
    // copy. It REPLACES rather than appends so the optimistic local echo of a
    // just-sent first message doesn't end up duplicated next to its persisted
    // twin.
    function renderHistory(items) {
      if (!items || !items.length) return;
      // Decryption below is genuinely async (WebCrypto), so live frames can
      // interleave. Buffer them until the swap is done — see the "msg" case.
      historyPending = true;
      deferredMsgs = [];
      var gen = wsGeneration;
      var pending = items.map(function (m) {
        if (m.enc && enc.ready) {
          var env;
          try {
            env = window.HulaVisitorCrypto.b64urlDecode(m.enc);
          } catch (_) {
            return Promise.resolve({ m: m, text: "[could not decrypt message]" });
          }
          return window.HulaVisitorCrypto.open(enc.sessionPriv, env)
            .then(function (pt) { return { m: m, text: new TextDecoder().decode(pt) }; })
            .catch(function () { return { m: m, text: "[could not decrypt message]" }; });
        }
        return Promise.resolve({ m: m, text: m.content || "" });
      });
      // Promise.all, not a per-message .then: decryption is async, and
      // resolving out of order would scramble the transcript.
      Promise.all(pending).then(function (rows) {
        // A newer dial already owns the log (and the pending-frame buffer);
        // this render is stale, so drop it rather than overwrite.
        if (gen !== wsGeneration) return;
        // Swap in one synchronous block. Clearing before the awaits would
        // leave the log empty across them, and a live message landing in that
        // window would end up rendered ABOVE the history that precedes it.
        logEl.innerHTML = "";
        seenIds = {};
        rows.forEach(function (r) {
          if (r.m.id) seenIds[r.m.id] = true;
          appendMsg(kindForDirection(r.m.direction), r.text);
        });
        // The transcript is authoritative, so the wipe above also removed any
        // local status line. Restore it — it's UI state the server has no
        // copy of, and on a fresh start it's the "waiting for an agent" note.
        if (statusNote) appendMsg("sys", statusNote);
        // Replay anything that landed mid-render, in arrival order.
        historyPending = false;
        var held = deferredMsgs;
        deferredMsgs = [];
        held.forEach(handleMsgFrame);
      }).catch(function () {
        // Never strand the socket in "buffering" mode — a stuck flag would
        // silently swallow every subsequent message.
        if (gen !== wsGeneration) return;
        historyPending = false;
        var held = deferredMsgs;
        deferredMsgs = [];
        held.forEach(handleMsgFrame);
      });
    }

    function kindForDirection(dir) {
      if (dir === "agent") return "them";
      if (dir === "visitor") return "me";
      return "sys";
    }

    // handleMsgFrame renders one live "msg" frame. Split out of the socket's
    // message switch so buffered frames (held across a history render) can be
    // replayed through exactly the same path.
    function handleMsgFrame(frame) {
      // Skip anything already rendered from the replayed history.
      if (frame.id && seenIds[frame.id]) return;
      if (frame.id) seenIds[frame.id] = true;
      // Encrypted inbound: open the sealed envelope with our session
      // private. On failure show a placeholder rather than dropping —
      // the operator's message still arrived, we just can't render it.
      if (frame.enc && enc.ready) {
        var who = frame.direction === "agent" ? "them" : "me";
        var env;
        try {
          env = window.HulaVisitorCrypto.b64urlDecode(frame.enc);
        } catch (_) {
          // Decode failure: still surface a placeholder so the operator's
          // message isn't silently lost.
          appendMsg(who, "[could not decrypt message]");
          return;
        }
        window.HulaVisitorCrypto.open(enc.sessionPriv, env).then(function (pt) {
          appendMsg(who, new TextDecoder().decode(pt));
        }).catch(function () {
          appendMsg(who, "[could not decrypt message]");
        });
        return;
      }
      appendMsg(frame.direction === "agent" ? "them" : "me", frame.content || "");
    }

    function openWebSocket() {
      clearReconnect();
      // Invalidate any in-flight async work owned by the previous socket.
      wsGeneration++;
      historyPending = false;
      deferredMsgs = [];
      // Drop any previous socket before dialling a new one so a reconnect
      // can't leave two live sockets racing onto the same UI.
      if (ws) {
        try { ws.onclose = null; ws.close(); } catch (_) {}
        ws = null;
      }
      var wsBase = session.chat_url
        || (location.origin.replace(/^http/, "ws") + CFG.chatWsUrl);
      var url = wsBase + "?token=" + encodeURIComponent(session.chat_token);
      // Present our per-session public so the server seals agent replies to it.
      if (enc.ready) {
        url += "&vpub=" + encodeURIComponent(window.HulaVisitorCrypto.b64urlEncode(enc.sessionPub));
      }
      ws = new WebSocket(url);
      ws.onopen = function () {
        sendBtn.disabled = false;
        backoffIdx = 0;
        msgInput.focus();
      };
      ws.onmessage = function (ev) {
        var frame;
        try { frame = JSON.parse(ev.data); } catch (_) { return; }
        switch (frame.type) {
          case "history":
            // Authoritative transcript sent right after connect — this is
            // what makes a refreshed page look like nothing happened.
            renderHistory(frame.messages);
            break;
          case "session_takeover":
            // Another tab claimed this session. Go passive: do NOT
            // reconnect, or the two tabs would kick each other in a loop.
            takenOver = true;
            sendBtn.disabled = true;
            msgInput.disabled = true;
            composerEl.hidden = true;
            takeoverEl.hidden = false;
            clearReconnect();
            break;
          case "msg":
            // While a history render is in flight the log is about to be
            // replaced wholesale, so anything appended now would be wiped by
            // that swap and is NOT in the replayed set (it was published after
            // the server read it). Hold it and replay it afterwards.
            if (historyPending) { deferredMsgs.push(frame); break; }
            handleMsgFrame(frame);
            break;
          case "session_closed":
            // Authoritative terminal signal from the server. The chat
            // can never accept another message — end it locally.
            endChat();
            break;
          case "presence":
            // Presence (incl. agent_left) is not surfaced today, and
            // must never be after an explicit close: session_closed is
            // final, so we deliberately don't render "agent left, keep
            // typing" here.
            break;
          case "ack":
          default:
            break;
        }
      };
      ws.onclose = function () {
        sendBtn.disabled = true;
        // A terminal close already showed "This chat has ended." — don't
        // follow it with a "Disconnected." that implies reconnect/retry.
        if (chatEnded) return;
        // Taken over by another tab: this socket is meant to stay down.
        if (takenOver) return;
        // Otherwise the session is still live — the connection just dropped.
        // Reconnect rather than stranding the visitor, which is the whole
        // point of persisting the session.
        if (backoffIdx === 0) appendMsg("sys", "Reconnecting…");
        scheduleReconnect();
      };
    }

    // endChat marks the session terminal in the UI: disable the
    // composer, swap in the "This chat has ended." banner (with a Start
    // new chat action), and drop the socket. Idempotent.
    function endChat() {
      if (chatEnded) return;
      chatEnded = true;
      clearReconnect();
      if (endFallbackTimer) { clearTimeout(endFallbackTimer); endFallbackTimer = null; }
      // The session is terminal: drop the persisted credential so a later
      // refresh starts fresh instead of resuming a dead chat.
      storageClear();
      msgInput.disabled = true;
      sendBtn.disabled = true;
      composerEl.hidden = true;
      takeoverEl.hidden = true;
      endBtn.hidden = true;
      endedEl.hidden = false;
      appendMsg("sys", "This chat has ended.");
      try { if (ws) ws.close(); } catch (_) {}
    }

    // --- End chat (visitor-initiated) ---------------------------------

    var lastFocusedBeforeConfirm = null;

    function openConfirm() {
      lastFocusedBeforeConfirm = document.activeElement;
      confirmEl.hidden = false;
      // Focus the safe option, not the destructive one.
      confirmCancel.focus();
    }

    function closeConfirm() {
      confirmEl.hidden = true;
      if (lastFocusedBeforeConfirm && lastFocusedBeforeConfirm.focus) {
        try { lastFocusedBeforeConfirm.focus(); } catch (_) {}
      }
    }

    endBtn.addEventListener("click", function () {
      if (chatEnded) return;
      openConfirm();
    });
    confirmCancel.addEventListener("click", closeConfirm);

    // Keep Tab inside the dialog while it's up, and let Esc dismiss it.
    confirmEl.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.preventDefault(); closeConfirm(); return; }
      if (e.key !== "Tab") return;
      var focusables = [confirmCancel, confirmOk];
      var idx = focusables.indexOf(document.activeElement);
      e.preventDefault();
      var next = e.shiftKey ? idx - 1 : idx + 1;
      if (next < 0) next = focusables.length - 1;
      if (next >= focusables.length) next = 0;
      focusables[next].focus();
    });

    confirmOk.addEventListener("click", function () {
      closeConfirm();
      if (chatEnded) return;
      // Lock the composer immediately — the visitor asked to be done, so no
      // further typing regardless of how long the server takes to confirm.
      msgInput.disabled = true;
      sendBtn.disabled = true;
      clearReconnect();

      // Prefer the socket. Fall back to REST when it's down: a broken
      // connection is precisely when someone wants to leave, and without the
      // fallback that's the one case "End chat" wouldn't work.
      var viaSocket = false;
      try {
        if (ws && ws.readyState === 1) {
          ws.send(JSON.stringify({ type: "close" }));
          viaSocket = true;
        }
      } catch (_) {}
      if (!viaSocket && session && session.chat_token) {
        fetch(CFG.chatCloseUrl, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "omit",
          body: JSON.stringify({ chat_token: session.chat_token }),
        }).catch(function () { /* fallback timer below still ends the UI */ });
      }

      // The server's session_closed frame is authoritative and normally
      // lands within a round-trip; endChat() is idempotent, so this timer
      // only matters when the socket died silently or REST never answered.
      endFallbackTimer = setTimeout(endChat, 2500);
    });

    // startNewChat tears down the ended session and returns to the start
    // form, so the next "Start chat" mints a brand-new session id.
    function startNewChat() {
      clearReconnect();
      try { if (ws) { ws.onclose = null; ws.close(); } } catch (_) {}
      ws = null;
      session = null;
      chatEnded = false;
      takenOver = false;
      backoffIdx = 0;
      seenIds = {};
      historyPending = false;
      deferredMsgs = [];
      statusNote = "";
      storageClear();
      takeoverEl.hidden = true;
      endBtn.hidden = true;
      logEl.innerHTML = "";
      msgInput.disabled = false;
      msgInput.value = "";
      sendBtn.disabled = true;
      endedEl.hidden = true;
      composerEl.hidden = true;
      logEl.hidden = true;
      startEl.hidden = false;
      startBtn.disabled = false;
      startBtn.textContent = "Start chat";
      showError("");
      if (CAPTCHA) {
        CAPTCHA.load().then(function () { CAPTCHA.render(captchaContainer); });
      }
      emailInput.focus();
    }
    restartBtn.addEventListener("click", startNewChat);

    function sendMessage() {
      if (chatEnded) return;
      var text = (msgInput.value || "").trim();
      if (!text || !ws || ws.readyState !== 1) return;
      if (enc.ready) {
        // Seal content to the server's visitor-chat key. Only append to the UI
        // and clear the input once the send actually happens — otherwise a seal
        // failure or a socket that closed before the promise resolves would
        // leave the message looking delivered when it never left.
        var payload = new TextEncoder().encode(text);
        window.HulaVisitorCrypto.seal(enc.serverPub, payload).then(function (envBytes) {
          if (!ws || ws.readyState !== 1) {
            appendMsg("sys", "Couldn't send — connection lost. Please retype and try again.");
            return;
          }
          ws.send(JSON.stringify({ type: "msg", enc: window.HulaVisitorCrypto.b64urlEncode(envBytes) }));
          appendMsg("me", text);
          msgInput.value = "";
        }).catch(function () {
          appendMsg("sys", "Couldn't encrypt your message. Please retype and try again.");
        });
        return;
      }
      ws.send(JSON.stringify({ type: "msg", content: text }));
      appendMsg("me", text);
      msgInput.value = "";
    }
    sendBtn.addEventListener("click", sendMessage);
    msgInput.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); sendMessage(); }
    });

    // Trigger binding: customer markup wins; FAB only when absent.
    var triggers = document.querySelectorAll("[data-hula-chat-open]");
    if (triggers.length > 0) {
      Array.prototype.forEach.call(triggers, function (t) {
        t.addEventListener("click", open);
      });
    } else {
      buildFAB(open);
    }

    // Restore a chat left running on a previous page view. Deferred until
    // encryption has settled so the socket presents its ?vpub= and the
    // replayed transcript comes back sealed rather than in the clear.
    //
    // Visitors with no stored session (the common case) do no extra work
    // here — storageLoad() is a single synchronous read that returns null.
    var stored = storageLoad();
    if (stored) {
      encSettled.then(function () { resumeOnLoad(stored); });
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount);
  } else {
    mount();
  }
})();
