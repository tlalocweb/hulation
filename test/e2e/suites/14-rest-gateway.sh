#!/bin/bash
# Suite 14: REST gateway (grpc-gateway) smoke test.
#
# Confirms gRPC services are reachable via JSON-over-HTTPS at /api/v1/*.
# Uses curl + the normal TLS trust chain — same path a browser takes.

# StatusService.Status (public — no auth).
status_output=$(curl_test -s "https://${HULA_HOST}/api/v1/status" || true)

assert_contains "$status_output" '"ok"' "REST /api/v1/status returns ok field"
assert_contains "$status_output" 'true' "REST /api/v1/status reports alive"

# AuthService.ListAuthProviders (public) — empty list is OK, just want 200.
providers_output=$(curl_test -s "https://${HULA_HOST}/api/v1/auth/providers" || true)

# The field exists either as "providers": [...] or omitted entirely when
# no providers are configured. Both cases return valid JSON.
assert_contains "$providers_output" '{' "REST /api/v1/auth/providers returns a JSON object"
assert_not_contains "$providers_output" "404" "REST gateway is not a 404"
