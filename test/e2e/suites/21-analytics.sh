#!/bin/bash
# Suite 21: analytics API end-to-end.
#
# Seeds ClickHouse with a deterministic set of events via
# fixtures/analytics-seed.sh, then fires every /api/v1/analytics/*
# endpoint and asserts golden numbers.
#
# Seed (see analytics-seed.sh):
#   10 distinct visitors, 20 pageviews over 14 days
#   2 countries (US=8 visitors, DE=3 visitors) — note v-008 is DE-only
#   3 device_category values (desktop=5, mobile=4, tablet=1 unique)
#   5 url_paths (/, /about, /pricing, /docs, /docs/api)
#   3 channels (Direct, Search, Referral)
#   server_id = 'testsite-seed' (distinct from testsite / testsite-staging
#                                to avoid collisions with other suites)

# Seed the deterministic fixture.
bash "$HULA_E2E_ROOT/fixtures/analytics-seed.sh" >/dev/null
pass "seeded analytics fixture"

# Extract admin token from suite-01's saved hulactl config.
TOKEN=$(runner_shell 'awk "/^ *token:/ {gsub(/\"/, \"\"); print \$2; exit}" /root/.hula/hulactl.yaml' | tr -d ' \r\n')
if [ -z "$TOKEN" ]; then
    fail "suite 21: no admin token (did suite 01 run?)"
    return 0 2>/dev/null || exit 0
fi

# Wide window that captures every seed row. Format: YYYY-MM-DDThh:mm:ssZ.
TO_ISO=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
FROM_ISO=$(date -u -d '15 days ago' +'%Y-%m-%dT%H:%M:%SZ')

# Helper: GET an analytics endpoint with the admin token.
ana_get() {
    local path="$1"
    local extra="${2:-}"
    curl_test -s -H "Authorization: Bearer $TOKEN" \
        "https://${HULA_HOST}/api/v1/analytics/${path}?server_id=testsite-seed&filters.from=${FROM_ISO}&filters.to=${TO_ISO}${extra}"
}

# --- Summary ---
summary=$(ana_get summary)
if ! echo "$summary" | grep -q '"visitors"'; then
    echo "        summary-body: $(echo "$summary" | head -c 400)"
    echo "        ch-seed-count: $(docker exec hula-e2e-hula-clickhouse-1 clickhouse-client --user hula --password hula -q "SELECT count() FROM hula.events WHERE server_id='testsite-seed'" 2>&1)"
fi
assert_contains "$summary" '"visitors"' "summary: visitors field present"
# 10 distinct visitors in the seed.
vis=$(echo "$summary" | grep -oE '"visitors":\s*"?[0-9]+"?' | head -1 | grep -oE '[0-9]+')
assert_eq "$vis" "10" "summary: visitors == 10"
pv=$(echo "$summary" | grep -oE '"pageviews":\s*"?[0-9]+"?' | head -1 | grep -oE '[0-9]+')
assert_eq "$pv" "20" "summary: pageviews == 20"

# --- Timeseries ---
ts=$(ana_get timeseries "&filters.granularity=day")
assert_contains "$ts" '"buckets"' "timeseries: buckets field present"
bucket_count=$(echo "$ts" | grep -oE '"pageviews":\s*"?[0-9]+"?' | wc -l)
if [ "$bucket_count" -ge 1 ]; then
    pass "timeseries: returned $bucket_count bucket(s)"
else
    fail "timeseries: no buckets returned" "response: $(echo "$ts" | head -c 300)"
fi

# --- Pages ---
pages=$(ana_get pages)
assert_contains "$pages" '"rows"' "pages: rows field present"
# "/" should be the top page (appears in 9 events).
top_page=$(echo "$pages" | grep -oE '"key":\s*"[^"]*"' | head -1 | grep -oE '"[^"]*"$' | tr -d '"')
assert_eq "$top_page" "/" "pages: top page is /"

# --- Sources ---
sources=$(ana_get sources)
assert_contains "$sources" '"rows"' "sources: rows field present"
# Direct channel has most rows; the top key should include 'Direct'.
assert_contains "$sources" '"Direct"' "sources: Direct channel present"

# --- Geography ---
geography=$(ana_get geography)
assert_contains "$geography" '"rows"' "geography: rows field present"
assert_contains "$geography" '"US"' "geography: US row present"
assert_contains "$geography" '"DE"' "geography: DE row present"
# Percent must be > 0 for at least one row.
assert_contains "$geography" '"percent"' "geography: percent field present"

# --- Devices ---
devices=$(ana_get devices)
assert_contains "$devices" '"device_category"' "devices: device_category field present"
assert_contains "$devices" '"browser"' "devices: browser field present"
assert_contains "$devices" '"os"' "devices: os field present"
assert_contains "$devices" '"desktop"' "devices: desktop category present"

# --- Events ---
events=$(ana_get events)
assert_contains "$events" '"rows"' "events: rows field present"
# All seed rows have code=1 (PageView) → single row with count=20.
event_count=$(echo "$events" | grep -oE '"count":\s*"?[0-9]+"?' | head -1 | grep -oE '[0-9]+')
assert_eq "$event_count" "20" "events: code=1 row has count=20"

# --- FormsReport (stub) ---
forms=$(ana_get forms)
# Stub returns {} or {"rows":[]}. Either shape is an empty success.
if echo "$forms" | grep -qE '^\{(|"rows":\[\])\}$|"rows":\s*\[\]' ; then
    pass "forms: stub returns empty rows"
else
    # Tolerate an empty object too.
    if echo "$forms" | grep -q '^{}$'; then
        pass "forms: stub returns empty object"
    else
        fail "forms: stub should return empty" "response: $forms"
    fi
fi

# --- Visitors ---
visitors=$(ana_get visitors "&limit=100")
assert_contains "$visitors" '"visitors"' "visitors: visitors field present"
visitor_count=$(echo "$visitors" | grep -oE '"visitor_id":\s*"v-[0-9]+"' | sort -u | wc -l)
assert_eq "$visitor_count" "10" "visitors: 10 unique visitor_ids returned"

# --- Visitor detail ---
# Look up v-001 (has 3 events, 2 sessions).
vp=$(curl_test -s -H "Authorization: Bearer $TOKEN" \
    "https://${HULA_HOST}/api/v1/analytics/visitor/v-001?server_id=testsite-seed")
assert_contains "$vp" '"visitor_id"' "visitor profile: visitor_id field present"
assert_contains "$vp" '"v-001"' "visitor profile: correct visitor returned"
assert_contains "$vp" '"timeline"' "visitor profile: timeline field present"

# --- Realtime ---
rt=$(curl_test -s -H "Authorization: Bearer $TOKEN" \
    "https://${HULA_HOST}/api/v1/analytics/realtime?server_id=testsite-seed")
assert_contains "$rt" '"active_visitors_5m"' "realtime: active_visitors_5m field present"

# --- CSV export ---
csv_pages=$(curl_test -s -H "Authorization: Bearer $TOKEN" \
    "https://${HULA_HOST}/api/v1/analytics/pages?server_id=testsite-seed&filters.from=${FROM_ISO}&filters.to=${TO_ISO}&format=csv")
# Expect a header row (key,visitors,pageviews) and at least one data line.
if echo "$csv_pages" | head -1 | grep -qE "^(key|bounce_rate|pageviews|visitors|percent|entrances|exits|unique_pageviews|avg_time)" ; then
    pass "pages CSV: header row present"
else
    fail "pages CSV: missing header row" "first line: $(echo "$csv_pages" | head -1)"
fi
data_lines=$(echo "$csv_pages" | wc -l)
if [ "$data_lines" -ge 2 ]; then
    pass "pages CSV: $data_lines line(s) (header + data)"
else
    fail "pages CSV: no data rows" "response: $(echo "$csv_pages" | head -c 200)"
fi

# --- Rate limit ---
# Burst 10 + sustained 0.5/s. The e2e `curl_test` wrapper spins up a
# fresh container per call, which takes ~1 second each and lets the
# bucket refill between requests. To exercise the limiter, send all
# 20 calls from a SINGLE runner-container shell so they fire in rapid
# succession.
rate_429=$(dc run --rm -T test-runner sh -c "
    count=0
    for i in \$(seq 1 20); do
        sc=\$(curl -sk -o /dev/null -w '%{http_code}' \
            -H 'Authorization: Bearer $TOKEN' \
            'https://${HULA_HOST}/api/v1/analytics/summary?server_id=testsite-seed&filters.from=${FROM_ISO}&filters.to=${TO_ISO}')
        if [ \"\$sc\" = '429' ]; then count=\$((count + 1)); fi
    done
    echo \$count
" 2>/dev/null | tr -d ' \r\n' | tail -c 5)
if [ -n "$rate_429" ] && [ "$rate_429" -gt 0 ] 2>/dev/null; then
    pass "rate limit: 429s observed under burst ($rate_429 / 20)"
else
    fail "rate limit: no 429 observed after 20 rapid calls"
fi
