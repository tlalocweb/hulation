/**
 * Node test for hula-visitor-crypto.js. Run with:  node hula-visitor-crypto.test.js
 *
 * Requires Node 20+ (WebCrypto X25519). Validates cross-language interop with
 * hulation/pkg/visitorcrypto — the pinned vector here is the SAME one
 * TestDeterministicVector asserts in Go, so a wire-format change breaks both.
 *
 * There's no JS test runner wired into the repo build yet; this is a
 * standalone assert-or-throw script. Exit 0 = pass, non-zero = fail.
 */
"use strict";
const c = require("../scripts/hula-visitor-crypto.js");

function hex(bytes) {
  return Array.prototype.map
    .call(bytes, (b) => ("0" + b.toString(16)).slice(-2))
    .join("");
}
function fromHex(s) {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.substr(i * 2, 2), 16);
  return out;
}

const WANT_VECTOR =
  "0faa684ed28867b97f4a6a2dee5df8ce974e76b7018e3f22a1c4cf2678570f20" +
  "0df6b2c5367ec9bc233992f772a3d2277c1d59d08ffad05333edb99a3734c989" +
  "3e05b79b384b7703";

let failures = 0;
function check(name, cond) {
  if (cond) {
    console.log("  ok   " + name);
  } else {
    console.error("  FAIL " + name);
    failures++;
  }
}

async function run() {
  console.log("hula-visitor-crypto.js tests");

  // 1. Capability check.
  check("isSupported()", c.isSupported() === true);

  // 2. Pinned cross-language vector: deterministic seal matches Go.
  const recipientPriv = new Uint8Array(32).fill(0x11);
  const ephPriv = new Uint8Array(32).fill(0x22);
  const recipientPub = await c.publicFromPrivate(recipientPriv);
  const env = await c.sealWithEphemeral(
    ephPriv,
    recipientPub,
    new TextEncoder().encode("hula-visitor-chat vector")
  );
  check("deterministic vector matches Go", hex(env) === WANT_VECTOR);

  // 3. Open a Go-sealed envelope (the pinned bytes).
  const pt = await c.open(recipientPriv, fromHex(WANT_VECTOR));
  check(
    "open Go-sealed envelope",
    new TextDecoder().decode(pt) === "hula-visitor-chat vector"
  );

  // 4. Random seal → open round-trip.
  const msg = new TextEncoder().encode('{"email":"v@x.io","first_message":"hi"}');
  const env2 = await c.seal(recipientPub, msg);
  const pt2 = await c.open(recipientPriv, env2);
  check("random round-trip", hex(pt2) === hex(msg));

  // 5. Fresh ephemeral per seal → distinct envelopes for identical plaintext.
  const a = await c.seal(recipientPub, msg);
  const b = await c.seal(recipientPub, msg);
  check("fresh ephemeral per seal", hex(a) !== hex(b));

  // 6. Tampered ciphertext → open rejects.
  const tampered = env2.slice();
  tampered[40] ^= 0x01;
  let rejected = false;
  try {
    await c.open(recipientPriv, tampered);
  } catch (_) {
    rejected = true;
  }
  check("tampered ciphertext rejected", rejected);

  // 7. Wrong recipient → open rejects.
  const wrongPriv = new Uint8Array(32).fill(0x99);
  let rejected2 = false;
  try {
    await c.open(wrongPriv, env2);
  } catch (_) {
    rejected2 = true;
  }
  check("wrong recipient rejected", rejected2);

  // 8. __selfTest convenience entry point.
  check("__selfTest()", (await c.__selfTest()) === true);

  console.log(failures === 0 ? "ALL PASS" : failures + " FAILURE(S)");
  process.exit(failures === 0 ? 0 : 1);
}

run().catch((e) => {
  console.error("test harness error:", e);
  process.exit(1);
});
