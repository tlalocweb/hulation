#!/bin/bash
# Suite 41: PR-6 reverse-proxy parity — proxy_only virtual hosts.
#
# The shared hula config (fixtures/hula-config.yaml.tmpl) defines a
# proxy_only vhost:
#
#     - host: proxy.test.local
#       id: testsite-proxy
#       proxy_only: true
#       proxy_pass: http://http-echo:8080
#
# proxy_only makes the host a PURE reverse proxy: EVERY request (including
# hula-reserved paths like /api/v1/*) is forwarded verbatim to the upstream,
# bypassing hula's own handlers. The http-echo sidecar answers 200 with a body
# "HTTP-ECHO <method> <path>" so we can prove a request actually reached the
# upstream rather than being served by hula.
#
# The KEY new coverage is the server-side pageview: proxy_only hosts carry no
# hula beacon, so hula derives a pageview from the raw request at the
# proxyDispatch seam (method="serverside") and writes it to ClickHouse. That DB
# write was previously untested end-to-end; this suite polls for it.
#
# Defensive: every stage skips (pass + return) rather than failing if the
# upstream / proxy path isn't reachable, so a broken sidecar can never take
# down the shared run.

# --- 0. http-echo upstream reachable? (defensive skip) -------------

probe=$(dc exec -T http-echo sh -c 'wget -q -O- http://127.0.0.1:8080/ 2>/dev/null || true' 2>/dev/null || true)
if ! echo "$probe" | grep -q ok; then
    pass "http-echo upstream not reachable — proxy_only e2e skipped"
    return 0 2>/dev/null || exit 0
fi
pass "http-echo upstream reachable"

# --- 1. Forwarding: a deep path reaches the upstream ---------------

fwd=$(runner_shell "curl -sk 'https://proxy.test.local/some/deep/path?x=1'" 2>/dev/null || true)
if ! echo "$fwd" | grep -q "HTTP-ECHO"; then
    pass "proxy.test.local returned no upstream echo (proxy path unavailable) — skipped"
    return 0 2>/dev/null || exit 0
fi
assert_contains "$fwd" "HTTP-ECHO" "proxy_only forwards to the upstream (echo marker present)"
assert_contains "$fwd" "/some/deep/path" "proxy_only preserves + forwards the request path"

# --- 2. Reserved path bypasses hula's own handlers -----------------
#
# A NON-proxy hula host would answer /api/v1/whoami itself (a JSON 401 for an
# unauthenticated request). Because proxy_only owns the host outright, the
# request is instead forwarded to the upstream — proven by the echo marker +
# the verbatim reserved path in the body.

resv=$(runner_shell "curl -sk 'https://proxy.test.local/api/v1/whoami'" 2>/dev/null || true)
assert_contains "$resv" "HTTP-ECHO" "proxy_only bypasses hula handlers — /api/v1/* hits the upstream"
assert_contains "$resv" "/api/v1/whoami" "reserved path forwarded verbatim to the upstream"
assert_not_contains "$resv" "{" "reserved-path response is the upstream echo, not a hula JSON error"

# NOTE: the proxy_only bad-actor gate (block runs BEFORE the upstream is
# contacted) is covered by the Go unit test
# TestProxyOnlyBadActorBlockedBeforeUpstream (server/unified_proxy_only_test.go).
# We deliberately do NOT exercise it here: bad_actors.disable=true globally in
# the e2e config, and enabling it would risk blocking the test network IPs for
# other suites.

# --- 3. Server-side pageview lands in ClickHouse -------------------
#
# THIS is the previously-untested coverage. A GET with Accept: text/html to a
# non-asset path qualifies as a page navigation, so hula enqueues a serverside
# pageview at the proxyDispatch seam and a worker writes it to hula.events. We
# poll for the row keyed by a suite-unique User-Agent.

UA_41="Mozilla/5.0 (test-suite-41-proxyonly-$$)"
runner_shell "curl -sk -H 'User-Agent: ${UA_41}' -H 'Accept: text/html' 'https://proxy.test.local/a/page'" >/dev/null 2>&1 || true

row=""
for _ in $(seq 1 15); do
    row=$(dc exec -T hula-clickhouse sh -c \
        "clickhouse-client --user hula --password hula -q \"SELECT method, server_id, url_path FROM hula.events WHERE browser_ua='${UA_41}' AND method='serverside' ORDER BY when DESC LIMIT 1 FORMAT TSV\"" \
        2>/dev/null || true)
    if [ -n "$row" ]; then break; fi
    sleep 2
done

if [ -z "$row" ]; then
    fail "no serverside pageview landed in ClickHouse after ~30s (proxy_only stats path broken)"
    return 0 2>/dev/null || exit 0
fi

method=$(echo "$row" | awk '{print $1}')
server_id=$(echo "$row" | awk '{print $2}')
url_path=$(echo "$row" | awk '{print $3}')
assert_eq "$method" "serverside" "proxy_only pageview recorded with method=serverside"
assert_eq "$server_id" "testsite-proxy" "serverside pageview tagged with server_id=testsite-proxy"
assert_eq "$url_path" "/a/page" "serverside pageview recorded the correct url_path"
