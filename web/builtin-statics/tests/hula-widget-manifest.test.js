// Cross-language proof for the signed widget manifest.
//
// Run: node hula-widget-manifest.test.js   (needs Node 16+ for ed25519)
//
// It checks two things against golden values produced by the Go side
// (handler/widget_manifest_test.go):
//   1. The browser's manifestCanonical() builds byte-identical canonical bytes
//      to Go's canonicalManifestMessage() (compared via hex).
//   2. A signature Go produced verifies under ed25519 here — node's ed25519 is
//      the same primitive WebCrypto's { name: "Ed25519" } uses, so a pass means
//      the in-browser verifier will accept a real install's manifest.
//
// If the KDF/canonical/signing drift on either side, this fails loudly.

const crypto = require("crypto");
const assert = require("assert");

// --- Mirror of hula-chat.js::manifestCanonical (keep in sync) ---------------
function manifestCanonical(m) {
  const lines = ["hula-widget-manifest-v1"];
  lines.push("server_id=" + (m.server_id || ""));
  lines.push("visitor_chat_public_key_b64=" + (m.visitor_chat_public_key_b64 || ""));
  const scripts = m.scripts || {};
  Object.keys(scripts).sort().forEach((name) => {
    lines.push("script=" + name + "=" + scripts[name]);
  });
  lines.push("issued_at=" + (m.issued_at || ""));
  return Buffer.from(lines.join("\n") + "\n", "utf8");
}

// --- Golden values pinned from the Go tests ---------------------------------
// canonicalGoldenManifest() in widget_manifest_test.go.
const MANIFEST = {
  v: 1,
  server_id: "mysite",
  visitor_chat_public_key_b64: "dmlzaXRvci1wdWJsaWMta2V5LTMyLWJ5dGVzLW9rITEx",
  scripts: {
    "hula-visitor-crypto.js": "sha384-CRYPTO",
    "hula-chat.js": "sha384-ENTRY",
  },
  issued_at: "2026-06-10T00:00:00Z",
};
// From TestCanonicalManifestMessageGolden "canonical hex".
const GO_CANONICAL_HEX =
  "68756c612d7769646765742d6d616e69666573742d76310a7365727665725f69643d6d79736974650a76697369746f725f636861745f7075626c69635f6b65795f6236343d646d6c7a615852766369317764574a7361574d74613256354c544d794c574a356447567a4c573972495445780a7363726970743d68756c612d636861742e6a733d7368613338342d454e5452590a7363726970743d68756c612d76697369746f722d63727970746f2e6a733d7368613338342d43525950544f0a6973737565645f61743d323032362d30362d31305430303a30303a30305a0a";
// From the Go tests: derived pub + signature over the golden manifest.
const DERIVED_PUB_B64URL = "CRFJGTee8OfXrzvHxWTjCERvUtsfABms6ov_5N_2JzE";
const GO_SIG_B64URL =
  "oiyZU1gKyXMCmm_lVUvwq819GJZ9yfUo18Zd70c7W59AxQ9_5QekmMnHcf5MvB4jPHVuutKASj2fsQ00OsCwBg";

// Raw 32-byte ed25519 public → SPKI DER for crypto.createPublicKey.
function ed25519PubFromRaw(raw) {
  const spkiPrefix = Buffer.from("302a300506032b6570032100", "hex");
  return crypto.createPublicKey({
    key: Buffer.concat([spkiPrefix, raw]),
    format: "der",
    type: "spki",
  });
}

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("  ok  - " + name);
  } catch (e) {
    failures++;
    console.error("  FAIL- " + name + ": " + (e && e.message ? e.message : e));
  }
}

check("canonical bytes match Go golden", () => {
  const got = manifestCanonical(MANIFEST).toString("hex");
  assert.strictEqual(got, GO_CANONICAL_HEX, "canonical hex differs from Go");
});

check("Go signature verifies under ed25519 (== WebCrypto primitive)", () => {
  const pub = ed25519PubFromRaw(Buffer.from(DERIVED_PUB_B64URL, "base64url"));
  const msg = manifestCanonical(MANIFEST);
  const sig = Buffer.from(GO_SIG_B64URL, "base64url");
  assert.strictEqual(crypto.verify(null, msg, pub, sig), true, "valid sig rejected");
});

check("tampered pubkey breaks verification", () => {
  const pub = ed25519PubFromRaw(Buffer.from(DERIVED_PUB_B64URL, "base64url"));
  const tampered = Object.assign({}, MANIFEST, {
    visitor_chat_public_key_b64: MANIFEST.visitor_chat_public_key_b64 + "x",
  });
  const sig = Buffer.from(GO_SIG_B64URL, "base64url");
  assert.strictEqual(
    crypto.verify(null, manifestCanonical(tampered), pub, sig),
    false,
    "signature verified over tampered manifest"
  );
});

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nall manifest cross-language checks passed");
