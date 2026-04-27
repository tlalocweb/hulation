#!/bin/bash
# Suite 16: basic RBAC smoke.
#
# Phase-0 RBAC state: the server-access ACL helpers
# (pkg/server/authware/access) are in place but the three RPCs
# (GrantServerAccess / RevokeServerAccess / ListServerAccess) are
# still Unimplemented pending the Bolt user store. This suite
# exercises what works today:
#
#   1. ListAuthProviders is publicly reachable.
#   2. WhoAmI requires a valid token.
#   3. GetMyPermissions echoes the caller's JWT permissions.

# Unauthenticated ListAuthProviders (via REST gateway).
plist=$(curl_test -s "https://${HULA_HOST}/api/v1/auth/providers" || true)
assert_contains "$plist" "providers" "ListAuthProviders returns 'providers' field"

# WhoAmI without a token → 401 (via REST gateway).
status_code=$(curl_test -s -o /dev/null -w '%{http_code}' \
    "https://${HULA_HOST}/api/v1/auth/whoami" || true)
assert_eq "$status_code" "401" "WhoAmI without token returns 401"

# WhoAmI with the admin token → 200 plus correct identity.
# Reuse the token stored from suite 01.
admin_token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)
if [ -n "$admin_token" ]; then
    whoami=$(curl_test -s \
        -H "Authorization: Bearer ${admin_token}" \
        "https://${HULA_HOST}/api/v1/auth/whoami" || true)
    assert_contains "$whoami" '"ok"' "WhoAmI with admin token returns ok"
    assert_contains "$whoami" '"is_admin"' "WhoAmI includes is_admin flag"
else
    fail "RBAC suite: no admin token from hulactl.yaml — did suite 01 run first?"
fi

# GetMyPermissions with the admin token → the admin role list.
if [ -n "$admin_token" ]; then
    perms=$(curl_test -s \
        -H "Authorization: Bearer ${admin_token}" \
        "https://${HULA_HOST}/api/v1/auth/me/permissions" || true)
    assert_contains "$perms" "allow_permissions" "GetMyPermissions returns allow_permissions field"
fi
