/**
 * hula-visitor-crypto.js — browser side of visitor-chat app-layer encryption.
 *
 * Mirrors hulation/pkg/visitorcrypto (Go) byte-for-byte. The widget seals
 * visitor message content to the installation's visitor-chat X25519 public key
 * before the bytes hit HTTPS, so a TLS-inspecting middlebox sees only
 * ciphertext.
 *
 * Wire format (v1), identical to the Go package:
 *
 *   envelope = epk(32) || AES-256-GCM(ciphertext || tag)
 *   shared   = X25519(ephemeral_priv, recipient_pub)
 *   key32    = HKDF-SHA256(IKM=shared, salt=epk||recipient_pub,
 *                          info="hula-visitor-chat-v1")
 *   nonce    = 12 zero bytes  (safe: fresh ephemeral per envelope)
 *
 * AES-GCM (not ChaCha) so this uses native WebCrypto with zero dependencies.
 *
 * Browser support: X25519 in WebCrypto landed in Chrome 133 / Firefox 132 /
 * Safari 17.4. On older engines isSupported() returns false and the widget
 * falls back to plaintext (chat still works; just not middlebox-opaque).
 *
 * THIS IS DEFENSE-IN-DEPTH, NOT A TLS REPLACEMENT. It does not defend against
 * an active MITM that tampers with this very script. See
 * docs/visitor-chat-encryption.md for the SRI / signed-manifest mitigations.
 */
(function (root) {
  "use strict";

  var INFO = "hula-visitor-chat-v1";
  var KEY_LEN = 32;
  var NONCE = new Uint8Array(12); // all zeros

  // PKCS#8 DER prefix for a raw X25519 private key. WebCrypto won't import a
  // raw 32-byte X25519 private directly — it must be wrapped. Prefix decodes
  // as: SEQ{ INT 0, SEQ{ OID 1.3.101.110 }, OCTETSTRING{ OCTETSTRING(32) } }.
  var PKCS8_X25519_PREFIX = new Uint8Array([
    0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06,
    0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20,
  ]);

  function subtle() {
    var c = (root.crypto || (typeof globalThis !== "undefined" && globalThis.crypto));
    return c && c.subtle ? c.subtle : null;
  }

  /**
   * Best-effort sync capability check. The real proof is whether an X25519
   * importKey succeeds, which is async; callers that need certainty should
   * try a seal and handle rejection. This gates the widget's "should I even
   * try" decision cheaply.
   */
  function isSupported() {
    return !!subtle() && typeof Uint8Array !== "undefined";
  }

  function concatBytes(a, b) {
    var out = new Uint8Array(a.length + b.length);
    out.set(a, 0);
    out.set(b, a.length);
    return out;
  }

  function importRecipientPublic(pubBytes) {
    return subtle().importKey("raw", pubBytes, { name: "X25519" }, false, []);
  }

  function importRawPrivate(privBytes) {
    if (privBytes.length !== KEY_LEN) {
      return Promise.reject(new Error("private key must be 32 bytes"));
    }
    var pkcs8 = concatBytes(PKCS8_X25519_PREFIX, privBytes);
    return subtle().importKey("pkcs8", pkcs8, { name: "X25519" }, false, ["deriveBits"]);
  }

  function deriveSharedSecret(privKey, pubKey) {
    return subtle()
      .deriveBits({ name: "X25519", public: pubKey }, privKey, 256)
      .then(function (bits) { return new Uint8Array(bits); });
  }

  // HKDF-SHA256(IKM=shared, salt=epk||recipientPub, info=INFO) → 32 bytes.
  function deriveKey(sharedBytes, epkBytes, recipientPubBytes) {
    var salt = concatBytes(epkBytes, recipientPubBytes);
    var enc = new TextEncoder();
    return subtle()
      .importKey("raw", sharedBytes, "HKDF", false, ["deriveBits"])
      .then(function (hk) {
        return subtle().deriveBits(
          { name: "HKDF", hash: "SHA-256", salt: salt, info: enc.encode(INFO) },
          hk,
          256
        );
      })
      .then(function (bits) { return new Uint8Array(bits); });
  }

  function aesKey(keyBytes) {
    return subtle().importKey("raw", keyBytes, "AES-GCM", false, ["encrypt", "decrypt"]);
  }

  // Internal: seal with a fully-specified ephemeral CryptoKey pair.
  function sealWithEphemeralKeys(ephPrivKey, ephPubBytes, recipientPubBytes, plaintextBytes) {
    return importRecipientPublic(recipientPubBytes)
      .then(function (recipientPub) {
        return deriveSharedSecret(ephPrivKey, recipientPub);
      })
      .then(function (shared) {
        return deriveKey(shared, ephPubBytes, recipientPubBytes);
      })
      .then(aesKey)
      .then(function (k) {
        return subtle().encrypt(
          { name: "AES-GCM", iv: NONCE, tagLength: 128 },
          k,
          plaintextBytes
        );
      })
      .then(function (ctBuf) {
        return concatBytes(ephPubBytes, new Uint8Array(ctBuf));
      });
  }

  /**
   * seal(recipientPubBytes, plaintextBytes) → Promise<Uint8Array envelope>.
   * Generates a fresh ephemeral X25519 keypair per call. This is the
   * production path for one-shot visitor→server messages (chat-start fields).
   */
  function seal(recipientPubBytes, plaintextBytes) {
    if (recipientPubBytes.length !== KEY_LEN) {
      return Promise.reject(new Error("recipient public must be 32 bytes"));
    }
    return subtle()
      .generateKey({ name: "X25519" }, true, ["deriveBits"])
      .then(function (kp) {
        return subtle().exportKey("raw", kp.publicKey).then(function (rawPub) {
          return sealWithEphemeralKeys(
            kp.privateKey,
            new Uint8Array(rawPub),
            recipientPubBytes,
            plaintextBytes
          );
        });
      });
  }

  /**
   * sealWithEphemeral(ephPrivBytes, recipientPubBytes, plaintextBytes) →
   * Promise<Uint8Array envelope>. Deterministic — supply a fixed 32-byte
   * ephemeral private. Used by the cross-language vector test and by the WS
   * session path (where we keep the ephemeral to also open agent replies).
   */
  function sealWithEphemeral(ephPrivBytes, recipientPubBytes, plaintextBytes) {
    if (recipientPubBytes.length !== KEY_LEN) {
      return Promise.reject(new Error("recipient public must be 32 bytes"));
    }
    return importRawPrivate(ephPrivBytes).then(function (ephPrivKey) {
      return publicFromPrivate(ephPrivBytes).then(function (ephPubBytes) {
        return sealWithEphemeralKeys(ephPrivKey, ephPubBytes, recipientPubBytes, plaintextBytes);
      });
    });
  }

  /**
   * open(recipientPrivBytes, envelopeBytes) → Promise<Uint8Array plaintext>.
   * The visitor uses this to decrypt agent replies sealed to its per-session
   * public key. Mirrors Go visitorcrypto.Open.
   */
  function open(recipientPrivBytes, envelopeBytes) {
    if (envelopeBytes.length < KEY_LEN + 16) {
      return Promise.reject(new Error("envelope shorter than minimum"));
    }
    var epk = envelopeBytes.slice(0, KEY_LEN);
    var ct = envelopeBytes.slice(KEY_LEN);
    var recipientPriv, recipientPubBytes;
    return importRawPrivate(recipientPrivBytes)
      .then(function (priv) {
        recipientPriv = priv;
        return publicFromPrivate(recipientPrivBytes);
      })
      .then(function (pub) {
        recipientPubBytes = pub;
        return importRecipientPublic(epk);
      })
      .then(function (ephPub) {
        return deriveSharedSecret(recipientPriv, ephPub);
      })
      .then(function (shared) {
        return deriveKey(shared, epk, recipientPubBytes);
      })
      .then(aesKey)
      .then(function (k) {
        return subtle().decrypt({ name: "AES-GCM", iv: NONCE, tagLength: 128 }, k, ct);
      })
      .then(function (ptBuf) { return new Uint8Array(ptBuf); });
  }

  /**
   * publicFromPrivate(privBytes) → Promise<Uint8Array(32) pub>. Derives the
   * X25519 public from a raw private by importing it then re-deriving via the
   * basepoint. WebCrypto has no direct "private→public" so we use the JWK
   * round-trip: import pkcs8, export jwk, read the `x` (public) coordinate.
   */
  function publicFromPrivate(privBytes) {
    var pkcs8 = concatBytes(PKCS8_X25519_PREFIX, privBytes);
    return subtle()
      .importKey("pkcs8", pkcs8, { name: "X25519" }, true, ["deriveBits"])
      .then(function (priv) {
        return subtle().exportKey("jwk", priv);
      })
      .then(function (jwk) {
        return b64urlDecode(jwk.x);
      });
  }

  // --- base64url helpers (no padding), browser + node safe ---

  function b64urlEncode(bytes) {
    var bin = "";
    for (var i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
    var b64 = (root.btoa ? root.btoa(bin) : Buffer.from(bytes).toString("base64"));
    return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function b64urlDecode(str) {
    var b64 = str.replace(/-/g, "+").replace(/_/g, "/");
    while (b64.length % 4) b64 += "=";
    if (root.atob) {
      var bin = root.atob(b64);
      var out = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
      return out;
    }
    return new Uint8Array(Buffer.from(b64, "base64"));
  }

  /**
   * Self-test against the pinned cross-language vector from
   * pkg/visitorcrypto/visitorcrypto_test.go::TestDeterministicVector.
   * Returns Promise<true> or rejects with a descriptive error. Used by the
   * node test harness; harmless to call in production for a boot-time
   * sanity check.
   */
  function __selfTest() {
    var recipientPriv = new Uint8Array(32).fill(0x11);
    var ephPriv = new Uint8Array(32).fill(0x22);
    var plaintext = new TextEncoder().encode("hula-visitor-chat vector");
    var wantHex =
      "0faa684ed28867b97f4a6a2dee5df8ce974e76b7018e3f22a1c4cf2678570f20" +
      "0df6b2c5367ec9bc233992f772a3d2277c1d59d08ffad05333edb99a3734c989" +
      "3e05b79b384b7703";

    return publicFromPrivate(recipientPriv)
      .then(function (recipientPub) {
        return sealWithEphemeral(ephPriv, recipientPub, plaintext);
      })
      .then(function (env) {
        var gotHex = Array.prototype.map
          .call(env, function (b) { return ("0" + b.toString(16)).slice(-2); })
          .join("");
        if (gotHex !== wantHex) {
          throw new Error("vector mismatch:\n got  " + gotHex + "\n want " + wantHex);
        }
        // Also confirm we can open the Go-sealed envelope.
        var wantEnv = new Uint8Array(wantHex.length / 2);
        for (var i = 0; i < wantEnv.length; i++) {
          wantEnv[i] = parseInt(wantHex.substr(i * 2, 2), 16);
        }
        return open(recipientPriv, wantEnv);
      })
      .then(function (pt) {
        var s = new TextDecoder().decode(pt);
        if (s !== "hula-visitor-chat vector") {
          throw new Error("open mismatch: " + s);
        }
        return true;
      });
  }

  var api = {
    isSupported: isSupported,
    seal: seal,
    sealWithEphemeral: sealWithEphemeral,
    open: open,
    publicFromPrivate: publicFromPrivate,
    b64urlEncode: b64urlEncode,
    b64urlDecode: b64urlDecode,
    INFO: INFO,
    __selfTest: __selfTest,
  };

  // Browser global + CommonJS (node test harness).
  root.HulaVisitorCrypto = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(typeof globalThis !== "undefined" ? globalThis : this);
