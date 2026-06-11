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
      s.src = CFG.cryptoUrl;
      s.async = true;
      // Pin the crypto module via SRI when the server published a hash. A
      // tampered module fails the integrity check, the browser refuses to run
      // it, onerror fires, and we fall back to plaintext — i.e. attacker code
      // can never execute in place of the real crypto. crossOrigin is required
      // for the browser to enforce integrity on the fetched script.
      if (CFG.cryptoSri) {
        s.integrity = CFG.cryptoSri;
        s.crossOrigin = "anonymous";
      }
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
      +   '<button class="hc-close" aria-label="Close">&times;</button>'
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
    initEncryption().catch(function () { /* plaintext fallback */ });
    var panel = buildPanel();

    var startEl    = panel.querySelector(".hc-start");
    var logEl      = panel.querySelector(".hc-log");
    var composerEl = panel.querySelector(".hc-composer");
    var emailInput = panel.querySelector('input[name="email"]');
    var firstInput = panel.querySelector('textarea[name="first"]');
    var msgInput   = panel.querySelector('input[name="msg"]');
    var startBtn   = panel.querySelector(".hc-start-btn");
    var sendBtn    = panel.querySelector(".hc-send");
    var closeBtn   = panel.querySelector(".hc-close");
    var captchaContainer = panel.querySelector("#hc-captcha");
    var errorEl    = panel.querySelector("#hc-error");

    var ws = null;
    var session = null;

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
      // Lazily load + render the captcha each open. Already-loaded
      // script short-circuits; already-rendered widget gets reset.
      if (CAPTCHA && !session) {
        CAPTCHA.load().then(function () { CAPTCHA.render(captchaContainer); });
      }
    }
    function close() {
      panel.classList.remove("hc-open");
      panel.setAttribute("aria-hidden", "true");
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
        startEl.hidden = true;
        logEl.hidden = false;
        composerEl.hidden = false;
        appendMsg("sys", "Session " + sess.session_id.slice(0, 8) + " — waiting for an agent…");
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

    function openWebSocket() {
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
        msgInput.focus();
      };
      ws.onmessage = function (ev) {
        var frame;
        try { frame = JSON.parse(ev.data); } catch (_) { return; }
        switch (frame.type) {
          case "msg":
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
                break;
              }
              window.HulaVisitorCrypto.open(enc.sessionPriv, env).then(function (pt) {
                appendMsg(who, new TextDecoder().decode(pt));
              }).catch(function () {
                appendMsg(who, "[could not decrypt message]");
              });
            } else {
              appendMsg(frame.direction === "agent" ? "them" : "me", frame.content || "");
            }
            break;
          case "presence":
            // not surfaced today
            break;
          case "ack":
          default:
            break;
        }
      };
      ws.onclose = function () {
        sendBtn.disabled = true;
        appendMsg("sys", "Disconnected.");
      };
    }

    function sendMessage() {
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
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount);
  } else {
    mount();
  }
})();
