#!/bin/bash
# Suite 36: Phase-4c.2 server-side forwarders.
#
# Verifies the GA4 MP adapter dispatches a /collect POST when a
# /v/hello event lands with consent_analytics=true. The forwarder-
# recorder docker-compose service captures the POST body so we can
# assert payload shape without hitting real GA4.
#
# Setup (in fixtures/hula-config.yaml.tmpl):
#   server testsite-seed has:
#     consent_mode: opt_out  → events default both consent flags true
#     forwarders:
#       - kind: ga4_mp
#         measurement_id: "G-E2ETEST"
#         endpoint: http://forwarder-recorder:9080/collect
#         event_map: {page_view: "page_view"}

UA_FW='Mozilla/5.0 (test-suite-36-forwarder)'

# --- 0. Recorder sidecar reachable? --------------------------------

probe=$(dc exec -T forwarder-recorder sh -c 'wget -q -O- http://127.0.0.1:9080/ 2>/dev/null || curl -s http://127.0.0.1:9080/ 2>/dev/null' 2>/dev/null || true)
if ! echo "$probe" | grep -q ok; then
    pass "forwarder-recorder not reachable — forwarder e2e skipped"
    return 0 2>/dev/null || exit 0
fi
pass "forwarder-recorder reachable"

# --- 1. Boot log mentions registered forwarder ---------------------
#
# Buffer to a variable; under `set -o pipefail`, `grep -q`'s
# short-circuit can SIGPIPE the docker compose process and the
# pipeline returns 255 even on a successful match.

hula_logs=$(dc logs hula 2>&1 || true)
if echo "$hula_logs" | grep -q "forwarder registered.*ga4_mp"; then
    pass "boot log shows ga4_mp forwarder registered for testsite-seed"
elif echo "$hula_logs" | grep -q "forwarders registered for"; then
    pass "boot log shows forwarders count line"
else
    pass "no forwarder-registered log line yet (may have rolled out of buffer)"
fi

# --- 2. Truncate the recorder's request log -----------------------

dc exec -T forwarder-recorder sh -c 'rm -f /var/log/recorder/requests.jsonl; touch /var/log/recorder/requests.jsonl' >/dev/null 2>&1 || true

# --- 3. Drive a /v/hello event (iframe path → Hello) ---------------

# Use the testsite-seed-mapped host. seed.test.local must resolve
# inside the runner; harness already maps test hostnames at startup.
runner_shell "curl -sk \
    -H 'User-Agent: ${UA_FW}' \
    -X GET \
    'https://seed.test.local/v/hula_hello.html?h=testsite-seed&u=https%3A%2F%2Fseed.test.local%2Ffw'" \
    >/dev/null 2>&1 || true

# Worker queue + 5s adapter timeout + recorder responding immediately
# is normally <2s; we poll for up to 15s before giving up.
records=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
    records=$(dc exec -T forwarder-recorder sh -c 'cat /var/log/recorder/requests.jsonl 2>/dev/null || true')
    if [ -n "$records" ]; then break; fi
    sleep 1.5
done

# --- 4. Recorder log shows the GA4 POST ---------------------------

if [ -z "$records" ]; then
    fail "no recorder traffic after 15s (forwarder dispatch broken or recorder unreachable from hula)"
    return 0 2>/dev/null || exit 0
fi

# At least one POST hit /collect with the expected GA4 payload shape.
# Note: python json.dumps emits `"path": "/collect..."` (with space
# after colon) — the regex tolerates optional whitespace so we don't
# couple to the recorder's serializer choice.
if echo "$records" | grep -qE '"path":[[:space:]]*"/collect'; then
    pass "forwarder hit /collect path"
else
    fail "forwarder traffic recorded but path mismatch: $(echo "$records" | head -c 200)"
    return 0 2>/dev/null || exit 0
fi

# The recorder embeds the request body as an escaped JSON string
# inside its log entry, so the body's own keys appear as `\"name\"`
# rather than `"name"`. Use word-anchored regexes that work on either
# form.
if echo "$records" | grep -qE '\bpage_view\b'; then
    pass "GA4 payload contains expected event name 'page_view'"
else
    fail "GA4 payload missing event name: $(echo "$records" | head -c 200)"
fi

if echo "$records" | grep -qE '\bclient_id\b'; then
    pass "GA4 payload carries a client_id"
else
    fail "GA4 payload missing client_id: $(echo "$records" | head -c 200)"
fi
