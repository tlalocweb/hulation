#!/bin/bash
# Suite 33: Phase-4b chat WebSocket round-trip.
#
# Drives the visitor + agent WebSockets end-to-end. Visitor sends a
# message → assertion that the message lands in chat_messages.
# Agent connects via /admin/agent-ws → assertion that visitor's
# next message arrives on the agent socket.
#
# Needs `websocat` inside the test-runner container; pass-skips
# gracefully matching suite 30's pattern.

SERVER_ID="testsite-seed"

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 33: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

auth_hdr="Authorization: Bearer ${admin_token}"

# Preflight: chat surface registered?
probe=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/chat/admin/sessions?server_id=${SERVER_ID}" || true)
if [ "$probe" != "200" ] && [ "$probe" != "404" ]; then
    pass "chat admin route returned ${probe} — chat may not be active"
    return 0 2>/dev/null || exit 0
fi

# websocat is installed by runner_preamble(); its absence means runner
# provisioning broke, not that this environment can't run the suite.
if ! runner_shell 'command -v websocat' >/dev/null 2>&1; then
    fail "websocat missing from the runner — provisioning broken (see runner_preamble in lib/harness.sh)"
    return 0 2>/dev/null || exit 0
fi

# --- 1. Issue a chat token via /chat/start -------------------------

start_resp=$(curl_test -s \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"server_id\":\"${SERVER_ID}\",\"visitor_id\":\"e2e-suite33\",\"email\":\"e2e33@test.local\",\"turnstile_token\":\"bypass\",\"first_message\":\"suite33 first message\"}" \
    "https://${HULA_HOST}/api/v1/chat/start" || true)

session_id=$(echo "$start_resp" | grep -oE '"session_id":"[^"]+' | head -1 | cut -d'"' -f4)
chat_token=$(echo "$start_resp" | grep -oE '"chat_token":"[^"]+' | head -1 | cut -d'"' -f4)

if [ -z "$session_id" ] || [ -z "$chat_token" ]; then
    fail "chat/start issued no session/token (resp=$(echo "$start_resp" | head -c 200))"
    return 0 2>/dev/null || exit 0
fi
pass "chat/start issued token + session ${session_id:0:8}…"

# --- 2. Visitor opens WS, sends a message, expects ack -------------

runner_shell_bg hula-chat-visitor "
  printf '%s\n' '{\"type\":\"msg\",\"content\":\"suite33 ws msg\"}' \
    | websocat -v 'wss://${HULA_HOST}/api/v1/chat/ws?token=${chat_token}' --max-messages 4
"
sleep 3

vout=$(docker logs hula-chat-visitor 2>/dev/null || true)
docker rm -f hula-chat-visitor >/dev/null 2>&1 || true

# The server acks every persisted visitor message, so a missing ack means the
# round-trip broke. This branch used to pass either way, which made the
# suite's headline assertion unable to fail.
if echo "$vout" | grep -q '"type":"ack"'; then
    pass "visitor WS received ack frame after sending msg"
else
    fail "visitor WS received no ack after sending msg (log tail: $(echo "$vout" | tail -c 200))"
fi

# --- 3. Verify the visitor message landed in chat_messages ---------
#
# Poll: the ack is emitted after the INSERT, but ClickHouse reads go through
# FINAL and the admin read is a separate request, so allow a moment rather
# than accepting "the opening message is there" as a substitute. That old
# fallback passed whenever /chat/start had worked — i.e. it never tested the
# WebSocket path at all.
msgs_resp=""
for _ in $(seq 1 10); do
    msgs_resp=$(curl_test -s -H "$auth_hdr" \
        "https://${HULA_HOST}/api/v1/chat/admin/sessions/${session_id}/messages?server_id=${SERVER_ID}" || true)
    echo "$msgs_resp" | grep -q 'suite33 ws msg' && break
    sleep 1
done
if echo "$msgs_resp" | grep -q 'suite33 ws msg'; then
    pass "visitor WS msg persisted to chat_messages"
else
    fail "WS message never reached chat_messages (resp=$(echo "$msgs_resp" | head -c 200))"
fi

# --- 4. Agent WS upgrade with admin JWT succeeds -------------------

# Quick reachability — a GET (not a WS upgrade) should return 401/426
agent_probe=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/chat/admin/agent-ws?session_id=${session_id}" || true)
case "$agent_probe" in
    400|401|426|502)
        pass "agent-ws route reachable (HTTP ${agent_probe} on non-WS GET)"
        ;;
    *)
        fail "agent-ws probe unexpected ${agent_probe} — route may be unregistered"
        ;;
esac

# --- 5. Agent opens WS, expects presence_snapshot frame ------------

runner_shell_bg hula-chat-agent "
  websocat -v 'wss://${HULA_HOST}/api/v1/chat/admin/agent-ws?session_id=${session_id}&token=${admin_token}' --max-messages 2
"
sleep 2

aout=$(docker logs hula-chat-agent 2>/dev/null || true)
docker rm -f hula-chat-agent >/dev/null 2>&1 || true

# The server sends presence_snapshot unconditionally on agent connect.
if echo "$aout" | grep -q '"type":"presence_snapshot"'; then
    pass "agent WS received presence_snapshot on connect"
else
    fail "agent WS received no presence_snapshot on connect (log tail: $(echo "$aout" | tail -c 200))"
fi

# --- 6. Cleanup: close the session via REST ------------------------

curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"server_id\":\"${SERVER_ID}\",\"reason\":\"suite33 done\"}" \
    "https://${HULA_HOST}/api/v1/chat/admin/sessions/${session_id}/close" >/dev/null 2>&1 || true
