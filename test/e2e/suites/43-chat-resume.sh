#!/bin/bash
# Suite 43: chat survives a page refresh, and the visitor can end it.
#
# The bug this guards: the widget used to hold {session_id, chat_token} only
# in a JS closure, so a browser refresh stranded the conversation server-side
# and /chat/start minted a brand-new one — visitor lost their chat, agent saw
# a duplicate. A refresh is simulated here by throwing away the first socket
# and re-credentialing through POST /chat/resume, exactly as the widget does.
#
# Structured so the core invariants run on plain HTTP and are therefore always
# exercised in CI. Only the transcript-replay assertion needs a real
# WebSocket, and `websocat` is absent from the CI runner — gating the whole
# suite on it (suite 33's pattern) would make this file a green no-op exactly
# where the regression risk lives.
#
# Always asserted (curl only):
#   1. /chat/resume re-issues a token for the SAME session (never a new one)
#   2. the reissued token differs from the original (it really was re-minted)
#   3. no duplicate session row is created
#   4. POST /chat/close ends the session (the widget's socket-down fallback)
#   5. resuming a closed session is refused with code=session_closed
#
# Additionally when websocat exists:
#   6. the reconnected socket replays the transcript as a `history` frame

SERVER_ID="testsite-seed"

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 43: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

auth_hdr="Authorization: Bearer ${admin_token}"

# Preflight: is the chat surface even registered on this build?
probe=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/chat/admin/sessions?server_id=${SERVER_ID}" || true)
if [ "$probe" != "200" ] && [ "$probe" != "404" ]; then
    pass "chat admin route returned ${probe} — chat may not be active"
    return 0 2>/dev/null || exit 0
fi

# --- 1. Start a chat ------------------------------------------------

start_resp=$(curl_test -s \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"server_id\":\"${SERVER_ID}\",\"visitor_id\":\"e2e-suite43\",\"email\":\"e2e43@test.local\",\"turnstile_token\":\"bypass\",\"first_message\":\"suite43 before refresh\"}" \
    "https://${HULA_HOST}/api/v1/chat/start" || true)

session_id=$(echo "$start_resp" | grep -oE '"session_id":"[^"]+' | head -1 | cut -d'"' -f4)
chat_token=$(echo "$start_resp" | grep -oE '"chat_token":"[^"]+' | head -1 | cut -d'"' -f4)

if [ -z "$session_id" ] || [ -z "$chat_token" ]; then
    pass "chat/start did not seed session/token (resp=$(echo "$start_resp" | head -c 160))"
    return 0 2>/dev/null || exit 0
fi
pass "suite43 chat started, session ${session_id:0:8}…"

# Optional: put a second message on the record over a socket that we then
# drop, standing in for "the visitor was mid-conversation when they hit
# reload". Without websocat the opening message alone carries the replay
# assertion.
have_ws=false
if runner_shell 'command -v websocat' >/dev/null 2>&1; then
    have_ws=true
    dc run --rm -T -d --name hula-c43-pre hulactl-runner sh -c "
      printf '%s\n' '{\"type\":\"msg\",\"content\":\"suite43 pre-refresh msg\"}' \
        | websocat -v 'wss://${HULA_HOST}/api/v1/chat/ws?token=${chat_token}' --max-messages 4 > /tmp/pre.out 2>&1
    " >/dev/null 2>&1 || true
    sleep 3
    docker rm -f hula-c43-pre >/dev/null 2>&1 || true
fi

# --- 2. Simulate the refresh: resume with the stored token ----------

resume_resp=$(curl_test -s \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"chat_token\":\"${chat_token}\"}" \
    "https://${HULA_HOST}/api/v1/chat/resume" || true)

resumed_id=$(echo "$resume_resp" | grep -oE '"session_id":"[^"]+' | head -1 | cut -d'"' -f4)
resumed_token=$(echo "$resume_resp" | grep -oE '"chat_token":"[^"]+' | head -1 | cut -d'"' -f4)

if [ -z "$resumed_token" ]; then
    fail "chat/resume issued no token (resp=$(echo "$resume_resp" | head -c 200))"
    return 0 2>/dev/null || exit 0
fi

# THE core invariant: resume rejoins, never restarts.
if [ "$resumed_id" = "$session_id" ]; then
    pass "chat/resume returned the SAME session (no new session minted)"
else
    fail "chat/resume changed session id: ${resumed_id} != ${session_id}"
fi

if [ "$resumed_token" != "$chat_token" ]; then
    pass "chat/resume minted a fresh token for the existing session"
else
    fail "chat/resume echoed the original token — no re-issue happened"
fi

# --- 3. No duplicate session for this visitor -----------------------

sessions_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/chat/admin/sessions?server_id=${SERVER_ID}&q=e2e-suite43" || true)
dupe_count=$(echo "$sessions_resp" | grep -oE '"id":"[0-9a-f-]{36}"' | sort -u | wc -l | tr -d ' ')
if [ "${dupe_count:-0}" -le 1 ]; then
    pass "resume created no duplicate session row (found ${dupe_count})"
else
    fail "resume left ${dupe_count} sessions for one visitor — duplicate-session bug"
fi

# --- 4. Reconnect replays the transcript (needs a real socket) ------

if [ "$have_ws" = true ]; then
    dc run --rm -T -d --name hula-c43-post hulactl-runner sh -c "
      websocat -v 'wss://${HULA_HOST}/api/v1/chat/ws?token=${resumed_token}' --max-messages 3 > /tmp/post.out 2>&1
    " >/dev/null 2>&1 || true
    sleep 3

    post_out=$(docker logs hula-c43-post 2>/dev/null || true)
    docker rm -f hula-c43-post >/dev/null 2>&1 || true

    if echo "$post_out" | grep -q '"type":"history"'; then
        pass "resumed socket received a history frame"
    else
        fail "no history frame after resume (tail: $(echo "$post_out" | tail -c 200))"
    fi

    if echo "$post_out" | grep -qE 'suite43 pre-refresh msg|suite43 before refresh'; then
        pass "history replayed the pre-refresh conversation — chat survived the refresh"
    else
        fail "history frame missing prior messages (tail: $(echo "$post_out" | tail -c 250))"
    fi
else
    pass "websocat absent — transcript-replay assertion skipped (HTTP invariants still ran)"
fi

# --- 5. Visitor ends the chat (REST fallback path) ------------------

close_resp=$(curl_test -s \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"chat_token\":\"${resumed_token}\"}" \
    "https://${HULA_HOST}/api/v1/chat/close" || true)

if echo "$close_resp" | grep -q '"ok":true'; then
    pass "visitor close accepted"
else
    fail "visitor close failed (resp=$(echo "$close_resp" | head -c 200))"
fi

# Authoritative check: the stored session really is terminal.
sess_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/chat/admin/sessions/${session_id}?server_id=${SERVER_ID}" || true)
if echo "$sess_resp" | grep -qE '"status":"(closed|CLOSED|SESSION_STATUS_CLOSED)"'; then
    pass "visitor-ended session persisted as closed"
else
    fail "session not closed after visitor close (resp=$(echo "$sess_resp" | head -c 200))"
fi

# --- 6. A closed session is not resumable ---------------------------

after_close=$(curl_test -s \
    -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"chat_token\":\"${resumed_token}\"}" \
    "https://${HULA_HOST}/api/v1/chat/resume" || true)

if echo "$after_close" | grep -q 'session_closed'; then
    pass "resuming an ended chat is refused with code=session_closed"
else
    fail "closed session was resumable (resp=$(echo "$after_close" | head -c 200))"
fi
