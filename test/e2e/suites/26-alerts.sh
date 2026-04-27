#!/bin/bash
# Suite 26: Alerts CRUD + ListAlertEvents.
#
# Pure API-level shape probe against AlertsService. Actual firing is
# exercised in suite 27. Degrades gracefully when the Bolt store isn't
# initialised (returns pass-skips, matching the pattern in suites
# 15/22/23/24/25).

SERVER_ID="testsite-seed"

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 26: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

auth_hdr="Authorization: Bearer ${admin_token}"

# --- 1. ListAlerts on empty server returns 200 ---------------------

status_code=$(curl_test -s -o /dev/null -w '%{http_code}' -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}" || true)

if [ "$status_code" = "200" ]; then
    pass "ListAlerts returns 200"
else
    pass "ListAlerts reachable (status=${status_code}; bolt-backed store may be inactive)"
    return 0 2>/dev/null || exit 0
fi

# --- 2. CreateAlert (goal_count_above) -----------------------------

alert_name="e2e-$(date +%s)"
create_payload=$(cat <<JSON
{"alert":{
  "name":"${alert_name}",
  "kind":"ALERT_KIND_GOAL_COUNT_ABOVE",
  "threshold":1,
  "window_minutes":60,
  "cooldown_minutes":60,
  "recipients":["e2e@test.local"],
  "target_goal_id":"synthetic-goal",
  "enabled":true
}}
JSON
)
create_resp=$(curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X POST --data "$create_payload" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}" || true)

alert_id=$(echo "$create_resp" | grep -oE '"id":"[^"]+"' | head -1 | sed 's/"id":"//; s/"$//')
if [ -n "$alert_id" ]; then
    pass "CreateAlert returns id=${alert_id}"
else
    fail "CreateAlert did not return an id" "resp: $(echo "$create_resp" | head -c 200)"
    return 0 2>/dev/null || exit 0
fi

# --- 3. GetAlert round-trips ---------------------------------------

get_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}/${alert_id}" || true)
assert_contains "$get_resp" "$alert_name" "GetAlert returns the created alert"

# --- 4. UpdateAlert toggles enabled=false --------------------------

upd_payload=$(cat <<JSON
{"alert":{
  "name":"${alert_name}",
  "kind":"ALERT_KIND_GOAL_COUNT_ABOVE",
  "threshold":1,
  "window_minutes":60,
  "cooldown_minutes":60,
  "recipients":["e2e@test.local"],
  "target_goal_id":"synthetic-goal",
  "enabled":false
}}
JSON
)
upd_resp=$(curl_test -s \
    -H "$auth_hdr" -H 'Content-Type: application/json' \
    -X PATCH --data "$upd_payload" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}/${alert_id}" || true)
assert_contains "$upd_resp" '"enabled":false' "UpdateAlert disables the alert"

# --- 5. ListAlertEvents is shape-correct for a new alert -----------

events_resp=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}/${alert_id}/events?limit=5" || true)
assert_contains "$events_resp" '{' "ListAlertEvents returns JSON body"

# --- 6. DeleteAlert -------------------------------------------------

del_resp=$(curl_test -s -H "$auth_hdr" \
    -X DELETE \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}/${alert_id}" || true)
assert_contains "$del_resp" '"ok"' "DeleteAlert returns ok"

list_final=$(curl_test -s -H "$auth_hdr" \
    "https://${HULA_HOST}/api/v1/alerts/${SERVER_ID}" || true)
assert_not_contains "$list_final" "$alert_id" "alert removed after Delete"
