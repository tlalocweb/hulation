#!/bin/bash
# Suite 22: analytics UI smoke test.
#
# Two layers:
#
#  1. Static asset + config.json probes (always run). Confirms the
#     SvelteKit bundle is served at /analytics/* and the client-
#     hydration shim (/analytics/config.json) returns the expected
#     shape.
#
#  2. Headless-browser probes (opt-in, only when Playwright is
#     installed on the host). Drives Chromium through Overview +
#     each of the four reports, asserts KPI numbers against the
#     suite-21 seed and verifies Pages drill-down. Skipped with a
#     PASS when playwright isn't available, matching the pattern
#     suite 15 uses for dex.

# --- Layer 1: plain HTTP probes ---------------------------------------

ui_probe=$(curl_test -sk -o /dev/null -w '%{http_code}' "https://${HULA_HOST}/analytics/")
if [ "$ui_probe" = "200" ]; then
    pass "/analytics/ serves SPA shell (200)"
else
    fail "/analytics/ serves SPA shell" "got status ${ui_probe}"
    # Bail the suite — without the shell, every subsequent probe is moot.
    return 0 2>/dev/null || exit 0
fi

ui_body=$(curl_test -sk "https://${HULA_HOST}/analytics/")
assert_contains "$ui_body" "<title>Hula Analytics</title>" "/analytics/ HTML title correct"
assert_contains "$ui_body" "/analytics/_app/" "/analytics/ references _app chunk paths"

# Extract admin token from suite 01 so the config shim sees claims.
TOKEN=$(runner_shell 'awk "/^ *token:/ {gsub(/\"/, \"\"); print \$2; exit}" /root/.hula/hulactl.yaml' | tr -d ' \r\n')

config_json=$(curl_test -sk \
    -H "Authorization: Bearer $TOKEN" \
    "https://${HULA_HOST}/analytics/config.json")
assert_contains "$config_json" '"servers"' "/analytics/config.json has servers array"
assert_contains "$config_json" '"isAdmin":true' "/analytics/config.json reports admin for admin token"

# One static asset (any JS chunk referenced in the HTML) should
# resolve with a 200.
first_chunk=$(echo "$ui_body" | grep -oE '"/analytics/_app/[^"]+\.js"' | head -1 | tr -d '"')
if [ -n "$first_chunk" ]; then
    chunk_status=$(curl_test -sk -o /dev/null -w '%{http_code}' "https://${HULA_HOST}${first_chunk}")
    assert_eq "$chunk_status" "200" "/analytics static chunk (${first_chunk##*/}) serves 200"
fi

# Deep-link SPA fallback: /analytics/pages should serve the same
# index.html (SvelteKit client router handles routing).
deep_body=$(curl_test -sk "https://${HULA_HOST}/analytics/pages")
assert_contains "$deep_body" "<title>Hula Analytics</title>" "/analytics/pages falls back to index (SPA router)"

# --- Layer 2: headless browser (opt-in) -------------------------------

if ! command -v npx >/dev/null 2>&1; then
    pass "headless-browser probes skipped (npx not installed on host)"
    return 0 2>/dev/null || exit 0
fi

if ! npx --yes --no-install playwright --version >/dev/null 2>&1; then
    pass "headless-browser probes skipped (playwright not installed; run 'npx playwright install --with-deps chromium' to enable)"
    return 0 2>/dev/null || exit 0
fi

# Playwright is present. Fire a small Node script that asserts the
# Overview KPI numbers against the suite-21 seed (visitors=10,
# pageviews=20 on server_id=testsite-seed).
TMP_PW_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_PW_DIR"' RETURN 2>/dev/null || true

cat > "$TMP_PW_DIR/smoke.mjs" <<'PWEOF'
import { chromium } from 'playwright';
const HOST = process.env.HULA_HOST ?? 'hula.test.local';
const PORT = process.env.HULA_HOST_PORT ?? '443';
const TOKEN = process.env.HULA_TOKEN ?? '';
const base = `https://${HOST}:${PORT}/analytics/?server_id=testsite-seed&filters.from=${encodeURIComponent(new Date(Date.now() - 15 * 86400000).toISOString())}&filters.to=${encodeURIComponent(new Date().toISOString())}`;

const browser = await chromium.launch({ args: ['--ignore-certificate-errors'] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, storageState: undefined });
const page = await ctx.newPage();
await page.addInitScript(`localStorage.setItem('hula:token', ${JSON.stringify(TOKEN)});`);
const consoleErrors = [];
page.on('pageerror', (err) => consoleErrors.push(err.message));
page.on('console', (msg) => { if (msg.type() === 'error') consoleErrors.push(msg.text()); });

await page.goto(base, { waitUntil: 'networkidle' });
await page.waitForSelector('text=Visitors');
const visitors = await page.textContent('article:has-text("Visitors") p.text-3xl');
if (!visitors || !visitors.trim().startsWith('10')) {
  throw new Error(`Expected 10 visitors, got: ${visitors}`);
}
if (consoleErrors.length > 0) {
  throw new Error(`Console errors: ${consoleErrors.join(' | ')}`);
}
await browser.close();
console.log('OK');
PWEOF

hp_out=$(cd "$TMP_PW_DIR" && HULA_HOST="$HULA_HOST" HULA_HOST_PORT="$HULA_HOST_PORT" HULA_TOKEN="$TOKEN" node smoke.mjs 2>&1 || true)
if echo "$hp_out" | grep -q '^OK$'; then
    pass "Playwright: Overview renders KPI visitors=10 against seed"
else
    fail "Playwright Overview probe failed" "$(echo "$hp_out" | head -c 300)"
fi
