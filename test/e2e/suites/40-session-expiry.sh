#!/bin/bash
# Suite 40: mobile session expiration + proactive token refresh.
#
# The iOS app discovers its JWT expiry from WhoAmI (token_expires,
# RFC3339), refreshes shortly before expiration via /api/v1/auth/refresh,
# and expects a clean 401 — never an ambiguous error — once a token is
# invalid. Endpoints exercised:
#
#   GET  /api/v1/auth/whoami    — token_expires populated from verified JWT
#   POST /api/v1/auth/refresh   — rotates to a new token, later expiry
#
# Depends on suite 01 having authenticated hulactl (admin JWT on the
# runner).

admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)

if [ -z "$admin_token" ]; then
    fail "suite 40: no admin token from hulactl.yaml — did suite 01 run?"
    return 0 2>/dev/null || exit 0
fi

# --- 1. WhoAmI reports token_expires from the verified JWT -----------

whoami_json=$(curl_test -s \
    -H "Authorization: Bearer ${admin_token}" \
    "https://${HULA_HOST}/api/v1/auth/whoami" || true)

assert_contains "$whoami_json" '"username"' "whoami returns identity"
assert_contains "$whoami_json" '"token_expires"' "whoami includes token_expires"

expires1=$(echo "$whoami_json" | grep -oE '"token_expires":"[^"]+"' | cut -d'"' -f4)
if [ -n "$expires1" ]; then
    pass "token_expires is populated ($expires1)"
else
    fail "token_expires is populated" "whoami: $whoami_json"
fi

# RFC3339 + in the future.
exp_epoch=$(date -d "$expires1" +%s 2>/dev/null || echo 0)
now_epoch=$(date +%s)
if [ "$exp_epoch" -gt "$now_epoch" ]; then
    pass "token_expires parses as RFC3339 and is in the future"
else
    fail "token_expires parses as RFC3339 and is in the future" "got: $expires1"
fi

# --- 2. Refresh rotates to a new usable token ------------------------

refresh_json=$(curl_test -s -X POST \
    -H "Authorization: Bearer ${admin_token}" \
    -H "Content-Type: application/json" -d '{}' \
    "https://${HULA_HOST}/api/v1/auth/refresh" || true)

assert_contains "$refresh_json" '"ok"' "refresh returns ok status"
new_token=$(echo "$refresh_json" | grep -oE '"token":"[^"]+"' | cut -d'"' -f4)

if [ -n "$new_token" ] && [ "$new_token" != "$admin_token" ]; then
    pass "refresh issues a new (different) token"
else
    fail "refresh issues a new (different) token" "refresh: $refresh_json"
fi

# --- 3. The refreshed token works and expires later ------------------

sleep 1  # ensure the new exp lands at a strictly later second

whoami2_json=$(curl_test -s \
    -H "Authorization: Bearer ${new_token}" \
    "https://${HULA_HOST}/api/v1/auth/whoami" || true)

expires2=$(echo "$whoami2_json" | grep -oE '"token_expires":"[^"]+"' | cut -d'"' -f4)
exp2_epoch=$(date -d "$expires2" +%s 2>/dev/null || echo 0)

if [ "$exp2_epoch" -gt "$exp_epoch" ]; then
    pass "refreshed token expires later than the original"
else
    fail "refreshed token expires later than the original" "old=$expires1 new=$expires2"
fi

# Identity survives the refresh.
assert_contains "$whoami2_json" '"admin"' "refreshed token keeps the admin identity"

# --- 4. Invalid tokens get a clean 401, not an ambiguous error -------

# Appending to the signature segment always invalidates it (replacing the
# last char would be a no-op when it already equals the replacement).
corrupt="${new_token}xx"
status_corrupt=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${corrupt}" \
    "https://${HULA_HOST}/api/v1/auth/whoami" || true)
assert_eq "$status_corrupt" "401" "corrupted token gets 401 from whoami"

status_refresh_corrupt=$(curl_test -s -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer ${corrupt}" \
    -H "Content-Type: application/json" -d '{}' \
    "https://${HULA_HOST}/api/v1/auth/refresh" || true)
assert_eq "$status_refresh_corrupt" "401" "corrupted token cannot refresh (401)"

status_garbage=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer utter-garbage" \
    "https://${HULA_HOST}/api/v1/auth/whoami" || true)
assert_eq "$status_garbage" "401" "malformed token gets 401 from whoami"

# A stale/invalid bearer must NOT break the public login surface.
status_login=$(curl_test -s -o /dev/null -w '%{http_code}' -X POST \
    -H "Authorization: Bearer ${corrupt}" \
    -H "Content-Type: application/json" -d '{}' \
    "https://${HULA_HOST}/api/v1/auth/opaque/login/init" || true)
if [ "$status_login" != "401" ]; then
    pass "stale bearer does not lock out the login endpoint (got $status_login)"
else
    fail "stale bearer does not lock out the login endpoint" "login init returned 401"
fi
