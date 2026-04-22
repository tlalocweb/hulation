#!/bin/bash
# Suite 15: SSO OIDC login flow (Google mock via dex).
#
# Exercises the full Google OIDC login path by standing up a dex
# container configured as a fake identity provider, then driving
# hula's LoginOIDC + LoginWithCode RPCs through the full
# authorization-code exchange.
#
# Current state: PLACEHOLDER. This suite requires a dex mock that
# isn't baked into the default e2e stack. When the dex fixture is
# added to test/e2e/fixtures/docker-compose.yaml (or a sidecar
# compose file), this suite's body is what runs.
#
# The suite short-circuits with a pass when dex isn't available so
# the rest of the harness keeps running. Operators running with dex
# configured in their local fork can delete the gate.

# Gate: require a dex container in the compose project.
dex_running=$(dc ps --services 2>/dev/null | grep -c '^dex$' || true)

if [ "$dex_running" = "0" ]; then
    pass "SSO OIDC suite skipped (no dex container in compose stack)"
    echo "  To enable: add a dex service to test/e2e/fixtures/docker-compose.yaml"
    echo "  configured with a mock Google OAuth client, and provide matching"
    echo "  auth.providers entries in hula-config.yaml.tmpl."
    return 0
fi

# --- Full flow, runs when dex is present ---

# 1. ListAuthProviders should surface 'google' (the dex-fronted provider).
plist=$(curl_in_network -s "https://${HULA_HOST}/api/v1/auth/providers" || true)
assert_contains "$plist" "google" "ListAuthProviders surfaces google entry"

# 2. Kick off LoginOIDC. hula returns a redirect URL pointing at dex.
oidc_start=$(curl_in_network -s -X POST \
    -H 'Content-Type: application/json' \
    -d '{"username":"test@example.com","onetimetoken":"suite15-ott"}' \
    "https://${HULA_HOST}/api/v1/auth/oidc" || true)
assert_contains "$oidc_start" "url" "LoginOIDC returns a redirect URL"

# 3. Extract the dex authorization URL.
dex_url=$(echo "$oidc_start" | grep -oE '"url"\s*:\s*"[^"]+"' | head -1 \
    | sed -E 's/.*"url"\s*:\s*"([^"]+)"/\1/')
if [ -z "$dex_url" ]; then
    fail "could not extract dex authorize URL from LoginOIDC response"
    return 1
fi

# 4. Drive dex's authorize endpoint. The dex mock auto-approves the
#    test user and redirects with ?code=....
code_redirect=$(runner_shell "curl -sL -o /dev/null -w '%{url_effective}' '$dex_url'" || true)
code=$(echo "$code_redirect" | grep -oE 'code=[^&]+' | head -1 | cut -d= -f2)
if [ -z "$code" ]; then
    fail "could not extract authorization code from dex redirect"
    return 1
fi

# 5. Finish the OIDC handshake. LoginWithCode should return a JWT.
login_done=$(curl_in_network -s -X POST \
    -H 'Content-Type: application/json' \
    -d "{\"provider\":\"google\",\"code\":\"$code\",\"onetimetoken\":\"suite15-ott\"}" \
    "https://${HULA_HOST}/api/v1/auth/code" || true)
assert_contains "$login_done" '"token"' "LoginWithCode returns a JWT"
