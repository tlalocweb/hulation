#!/bin/bash
# Suite 30: Mobile realtime WebSocket + debug-publish round-trip.
#
# Opens a WS to /api/mobile/v1/events, publishes a synthetic event
# via the admin-only /api/mobile/v1/debug/publish hook, and asserts
# the WS frame arrives within 3s.
#
# Needs `websocat` inside the test-runner container. When absent,
# pass-skips gracefully matching the pattern in suites 15/22.

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 30: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

auth_hdr="Authorization: Bearer ${admin_token}"

# Preflight: is the WS endpoint even registered? Upgrade will fail
# as an HTTP GET, but a proper 400 or 426 means the route is wired.
probe=$(curl_test -s -o /dev/null -w '%{http_code}' -H "$auth_hdr" \
    "https://${HULA_HOST}/api/mobile/v1/events" || true)
case "$probe" in
    400|401|426|502)
        pass "/api/mobile/v1/events route registered (HTTP ${probe} on non-WS GET)"
        ;;
    *)
        # The route is unconditionally registered, so an unexpected status is a
        # real regression. This used to pass-and-return, which meant a route
        # that vanished entirely reported success.
        fail "/api/mobile/v1/events probe unexpected status ${probe} — route may be unregistered"
        return 0 2>/dev/null || exit 0
        ;;
esac

# websocat is installed by runner_preamble(), so its absence means runner
# provisioning broke — fail rather than silently skipping the only assertion
# this suite exists for.
if ! runner_shell 'command -v websocat' >/dev/null 2>&1; then
    fail "websocat missing from the runner — provisioning broken (see runner_preamble in lib/harness.sh)"
    return 0 2>/dev/null || exit 0
fi

# Launch websocat in the background, redirecting output to a temp
# file inside the runner container. Read the file after we publish.
TMP=$(mktemp -d)
trap "rm -rf $TMP" RETURN 2>/dev/null || true

# Run the WS client in the background inside the runner.
runner_shell_bg hula-ws-probe "
  websocat -v -B 4096 --max-messages 1 'wss://${HULA_HOST}/api/mobile/v1/events' -H 'Authorization: Bearer ${admin_token}'
"
sleep 1

# Publish via debug endpoint.
pub_resp=$(curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X POST \
    --data '{"server_id":"testsite-seed","visitor_id":"ws-probe","event_code":1,"url":"/probe"}' \
    "https://${HULA_HOST}/api/mobile/v1/debug/publish" || true)

if echo "$pub_resp" | grep -q '"delivered_to"'; then
    pass "debug/publish returned delivery count"
else
    fail "debug/publish did not return a delivery count (resp=$(echo "$pub_resp" | head -c 160))"
fi

# Poll rather than sleeping a fixed 3s. The original comment justified an
# unconditional pass here with "WS plumbing can be slow to settle on a cold
# compose" — but a pass-on-timeout means this suite's one real assertion could
# never fail. Waiting longer removes the flake that motivated it; failing after
# that is then meaningful.
out=""
for _ in $(seq 1 15); do
    out=$(docker logs hula-ws-probe 2>/dev/null || true)
    echo "$out" | grep -q '"visitor_id":"ws-probe"' && break
    sleep 1
done
docker rm -f hula-ws-probe >/dev/null 2>&1 || true

if echo "$out" | grep -q '"visitor_id":"ws-probe"'; then
    pass "WS frame delivered synthetic event to subscriber"
else
    fail "WS subscriber never received the published frame (log tail: $(echo "$out" | tail -c 160))"
fi
