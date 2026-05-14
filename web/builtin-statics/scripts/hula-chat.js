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
  };

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
          email:           email,
          first_message:   first,
          turnstile_token: token,
        };
        if (coords) {
          body.latitude  = coords.latitude;
          body.longitude = coords.longitude;
        }
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
            appendMsg(frame.direction === "agent" ? "them" : "me", frame.content || "");
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
