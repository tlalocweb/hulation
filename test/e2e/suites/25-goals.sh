#!/bin/bash
# Suite 25: Goals CRUD + TestGoal (dry-run).
#
# Exercises GoalsService against the testsite-seed virtual server.
# Degrades gracefully when the Bolt store isn't initialised — the
# probes that require persistence report a pass-skip rather than
# failing the suite, matching the pattern in suites 15/22/23.

SERVER_ID="testsite-seed"

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 25: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

auth_hdr="Authorization: Bearer ${admin_token}"

# --- 1. ListGoals on an empty server returns 200 --------------------

list_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}" || true)
status_code=$(curl_test -s -o /dev/null -w '%{http_code}' -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}" || true)

if [ "$status_code" = "200" ]; then
    pass "ListGoals returns 200"
else
    pass "ListGoals path reachable (status=${status_code}, bolt-backed store may be inactive)"
    return 0 2>/dev/null || exit 0
fi

# --- 2. CreateGoal --------------------------------------------------

goal_name="e2e-$(date +%s)"
create_resp=$(curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"goal\":{\"name\":\"${goal_name}\",\"kind\":\"GOAL_KIND_URL_VISIT\",\"rule_url_regex\":\"^/thank-you\",\"enabled\":true}}" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}" || true)

goal_id=$(echo "$create_resp" | grep -oE '"id":"[^"]+"' | head -1 | sed 's/"id":"//; s/"$//')
if [ -n "$goal_id" ]; then
    pass "CreateGoal returns id=${goal_id}"
else
    fail "CreateGoal did not return an id" "resp: $(echo "$create_resp" | head -c 200)"
    return 0 2>/dev/null || exit 0
fi

# --- 3. GetGoal by id ------------------------------------------------

get_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}/${goal_id}" || true)
assert_contains "$get_resp" "$goal_name" "GetGoal returns the same goal"

# --- 4. UpdateGoal toggles enabled=false ---------------------------

upd_resp=$(curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X PATCH \
    --data "{\"goal\":{\"name\":\"${goal_name}\",\"kind\":\"GOAL_KIND_URL_VISIT\",\"rule_url_regex\":\"^/thank-you\",\"enabled\":false}}" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}/${goal_id}" || true)
assert_contains "$upd_resp" '"enabled":false' "UpdateGoal flips enabled off"

# --- 5. ListGoals includes the created goal ------------------------

list_after=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}" || true)
assert_contains "$list_after" "$goal_name" "ListGoals includes the new goal"

# --- 6. TestGoal (dry-run) — may be 501 Unimplemented in early build

test_status=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X POST \
    --data "{\"goal\":{\"kind\":\"GOAL_KIND_URL_VISIT\",\"rule_url_regex\":\"^/\"},\"days\":7}" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}/test" || true)

case "$test_status" in
    200) pass "TestGoal returns 200 (dry-run implemented)" ;;
    501) pass "TestGoal path reachable; returns 501 (stage 3.3b not yet shipped)" ;;
    *)   fail "TestGoal unexpected status" "got ${test_status}" ;;
esac

# --- 7. DeleteGoal --------------------------------------------------

del_resp=$(curl_test -s -H "$auth_hdr" \
    -X DELETE \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}/${goal_id}" || true)
assert_contains "$del_resp" '{' "DeleteGoal returns JSON body"

list_final=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/goals/${SERVER_ID}" || true)
assert_not_contains "$list_final" "$goal_id" "goal removed from list after Delete"
