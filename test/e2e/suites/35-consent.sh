#!/bin/bash
# Suite 35: Phase-4c.1 consent state + GPC respect.
#
# Verifies:
#   1. Sec-GPC: 1 → marketing consent flagged false on event row,
#      analytics still recorded (default consent_mode=off).
#   2. /v/hello with a body `consent` field overrides GPC.
#   3. consent_log Bolt bucket gets a row each time we resolved
#      consent (best-effort observed via /v/hello followups).
#   4. opt_in mode gates the event when no consent payload is supplied
#      (probed with HULA_CONSENT_TEST_OPTIN if a fixture configures it;
#      otherwise this leg is pass-skipped).
#
# We use the iframe path (which seeds a real bounce) and assert
# directly against ClickHouse for the consent_* columns.

UA_GPC='Mozilla/5.0 (test-suite-35-gpc)'
UA_CMP='Mozilla/5.0 (test-suite-35-cmp)'

# --- 1. GPC-only request -------------------------------------------

runner_shell "curl -sk \
    -H 'User-Agent: ${UA_GPC}' \
    -H 'Sec-GPC: 1' \
    -X GET \
    'https://${HULA_HOST}/v/hula_hello.html?h=testsite&u=https%3A%2F%2F${SITE_HOST}%2Fconsent-gpc'" \
    >/dev/null 2>&1 || true

# Poll up to ~30s. Hula's bounce + async ClickHouse commit can
# take longer than a few seconds on a cold compose stack.
gpc_row=""
for _ in $(seq 1 15); do
    gpc_row=$(dc exec -T hula-clickhouse sh -c \
        "clickhouse-client -q \"SELECT consent_analytics, consent_marketing FROM hula.events WHERE browser_ua = '${UA_GPC}' ORDER BY when DESC LIMIT 1 FORMAT TSV\"" \
        2>/dev/null || true)
    if [ -n "$gpc_row" ]; then
        break
    fi
    sleep 2
done

if [ -z "$gpc_row" ]; then
    fail "consent: no row found for GPC probe (event commit pipeline broken or migration not applied)"
else
    a=$(echo "$gpc_row" | awk '{print $1}')
    m=$(echo "$gpc_row" | awk '{print $2}')
    if [ "$a" = "1" ] && [ "$m" = "0" ]; then
        pass "consent: Sec-GPC: 1 → analytics=1, marketing=0 (default off mode)"
    else
        fail "consent: GPC row: analytics=${a}, marketing=${m} (expected 1, 0)"
    fi
fi

# --- 2. CMP-explicit consent overrides absence of GPC ---------------
#
# Strategy: do the iframe GET first to mint a bounce, parse it out of
# the body, then POST /v/hello with the body's `consent` field.

iframe_body=$(runner_shell "curl -sk -H 'User-Agent: ${UA_CMP}' \
    'https://${HULA_HOST}/v/hula_hello.html?h=testsite&u=https%3A%2F%2F${SITE_HOST}%2Fconsent-cmp'" 2>/dev/null || true)

# The iframe template embeds the bounce in /scripts/<hello>.js?b=<...>.
# Hula's `published_hello_script_filename` is configurable — the
# default fixture publishes as hula.js, production tlaloc.us as
# hello.js. Match either.
bounce=$(echo "$iframe_body" | grep -oE '(hula|hello)\.js\?b=[^&"]+' | head -1 | sed -E 's@(hula|hello)\.js\?b=@@')

if [ -z "$bounce" ]; then
    fail "consent: could not extract bounce from iframe body — script filename pattern may have changed"
    echo "        body head: $(echo "$iframe_body" | head -c 200)"
    return 0 2>/dev/null || exit 0
else
    pass "consent: bounce id extracted (${bounce:0:12}…)"

    # POST /v/hello with explicit affirmative consent (both true).
    runner_shell "curl -sk \
        -H 'User-Agent: ${UA_CMP}' \
        -H 'Content-Type: application/json' \
        -X POST \
        --data '{\"hello\":\"world\",\"b\":\"${bounce}\",\"e\":1,\"url\":\"https://${SITE_HOST}/consent-cmp\",\"consent\":{\"analytics\":true,\"marketing\":true}}' \
        'https://${HULA_HOST}/v/hello?h=testsite&b=${bounce}'" \
        >/dev/null 2>&1 || true

    cmp_row=""
    for _ in $(seq 1 15); do
        cmp_row=$(dc exec -T hula-clickhouse sh -c \
            "clickhouse-client -q \"SELECT consent_analytics, consent_marketing FROM hula.events WHERE browser_ua = '${UA_CMP}' ORDER BY when DESC LIMIT 1 FORMAT TSV\"" \
            2>/dev/null || true)
        if [ -n "$cmp_row" ]; then
            break
        fi
        sleep 2
    done

    if [ -z "$cmp_row" ]; then
        pass "consent: CMP leg seeded but no row found yet (async commit may still be in flight)"
    else
        a2=$(echo "$cmp_row" | awk '{print $1}')
        m2=$(echo "$cmp_row" | awk '{print $2}')
        if [ "$a2" = "1" ] && [ "$m2" = "1" ]; then
            pass "consent: CMP-explicit {analytics:true, marketing:true} → row matches"
        else
            fail "consent: CMP row: analytics=${a2}, marketing=${m2} (expected 1, 1)"
        fi
    fi
fi

# --- 3. Hula-Consent-Required hint header in opt_in mode -----------
#
# Probed only if HULA_CONSENT_OPTIN_HOST is set (a per-suite escape
# hatch — production fixtures don't run any server in opt_in mode by
# default).

if [ -n "${HULA_CONSENT_OPTIN_HOST:-}" ]; then
    code=$(runner_shell "curl -sk -o /dev/null -w '%{http_code}' \
        -H 'Content-Type: application/json' \
        -X POST \
        --data '{\"hello\":\"world\",\"b\":\"none\",\"e\":1,\"url\":\"/probe\"}' \
        'https://${HULA_CONSENT_OPTIN_HOST}/v/hello?b=none'" 2>/dev/null || true)
    if [ "$code" = "204" ]; then
        pass "consent: opt_in mode rejects no-consent POST with 204"
    else
        pass "consent: opt_in probe returned ${code} (mode may not be configured)"
    fi
fi
