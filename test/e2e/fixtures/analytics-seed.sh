#!/bin/bash
# analytics-seed.sh — insert a deterministic set of rows into
# hula.events for the Phase-1 analytics suite (21-analytics).
#
# Idempotent: the seed tags its rows with server_id='testsite-seed' so
# `DELETE WHERE server_id='testsite-seed'` wipes them cleanly before
# the new batch lands. Run before every invocation of 21-analytics.sh
# if you want repeatable golden-file assertions.
#
# Inserted shape:
#   - 10 distinct visitors
#   - 20 pageview events spread across 14 days, all within the
#     retention window
#   - 5 distinct url_paths
#   - 2 countries (US, DE)
#   - 3 device_category values (desktop, mobile, tablet)
#   - 3 channel values (Direct, Search, Referral)
#
# All timestamps are `NOW()` minus offsets of 1h..14d so the suite's
# from/to window of (now - 15d, now + 1h) captures every row.
#
# Usage: analytics-seed.sh [clickhouse-container-name]
#   Default container name: hula-e2e-hula-clickhouse-1

set -euo pipefail

CH_CONTAINER="${1:-hula-e2e-hula-clickhouse-1}"
SERVER_ID='testsite-seed'

# Wipe any prior seed rows first.
docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula -q \
    "ALTER TABLE hula.events DELETE WHERE server_id = '${SERVER_ID}'" >/dev/null 2>&1 || true

# Wait briefly for the mutation to commit — ClickHouse mutations are
# async but this one is cheap.
sleep 1

# The events table (GORM-created) has a relatively wide column set.
# We populate the subset the analytics queries actually read.

SEED_OUTPUT=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula --multiquery -q "
INSERT INTO hula.events (id, belongs_to, session_id, server_id, code, data, method, url, url_path, host, referer, referer_host, channel, search_term, utm_source, utm_medium, utm_campaign, from_ip, country_code, region, city, browser, browser_version, os, os_version, device_category, is_bot, browser_ua, when)
VALUES
    -- 10 visitors, 20 pageviews. code=1 = EventCodePageView.
    ('e-01', 'v-001', 's-001', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.1', 'US', 'CA', 'San Francisco', 'Chrome',  '120', 'macOS',  '14', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 14 DAY),
    ('e-02', 'v-001', 's-001', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/about',   '/about',   'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.1', 'US', 'CA', 'San Francisco', 'Chrome',  '120', 'macOS',  '14', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 14 DAY + INTERVAL 1 MINUTE),
    ('e-03', 'v-002', 's-002', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', 'https://www.google.com/', 'google.com', 'Search', 'hula analytics', '', '', '', '10.0.0.2', 'US', 'NY', 'New York',  'Safari',  '17',  'iOS',    '17', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 10 DAY),
    ('e-04', 'v-003', 's-003', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/pricing', '/pricing', 'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.3', 'DE', 'BY', 'Munich',        'Firefox', '121', 'Linux',  '',   'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 9 DAY),
    ('e-05', 'v-003', 's-003', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.3', 'DE', 'BY', 'Munich',        'Firefox', '121', 'Linux',  '',   'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 9 DAY + INTERVAL 3 MINUTE),
    ('e-06', 'v-004', 's-004', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', 'https://example.com/',    'example.com', 'Referral', '', '', '', '', '10.0.0.4', 'US', 'WA', 'Seattle',     'Chrome',  '120', 'Windows','10', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 7 DAY),
    ('e-07', 'v-005', 's-005', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/docs',    '/docs',    'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.5', 'DE', 'BE', 'Berlin',        'Chrome',  '120', 'Android','14', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 5 DAY),
    ('e-08', 'v-005', 's-005', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/docs/api','/docs/api','site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.5', 'DE', 'BE', 'Berlin',        'Chrome',  '120', 'Android','14', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 5 DAY + INTERVAL 2 MINUTE),
    ('e-09', 'v-006', 's-006', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.6', 'US', 'CA', 'San Jose',      'Safari',  '17',  'iPadOS', '17', 'tablet',  0, 'Mozilla/5.0',           now() - INTERVAL 4 DAY),
    ('e-10', 'v-007', 's-007', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/about',   '/about',   'site.test.local', 'https://www.google.com/', 'google.com', 'Search', 'hula',            '', '', '', '10.0.0.7', 'US', 'TX', 'Austin',    'Chrome',  '120', 'Windows','11', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 3 DAY),
    ('e-11', 'v-008', 's-008', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.8', 'DE', 'HH', 'Hamburg',       'Firefox', '121', 'macOS',  '14', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 2 DAY),
    ('e-12', 'v-008', 's-008', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/pricing', '/pricing', 'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.8', 'DE', 'HH', 'Hamburg',       'Firefox', '121', 'macOS',  '14', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 2 DAY + INTERVAL 5 MINUTE),
    ('e-13', 'v-009', 's-009', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/docs',    '/docs',    'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.9', 'US', 'MA', 'Boston',        'Chrome',  '120', 'Linux',  '',   'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 1 DAY),
    ('e-14', 'v-010', 's-010', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.10','US', 'IL', 'Chicago',       'Safari',  '17',  'iOS',    '17', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 1 HOUR),
    ('e-15', 'v-001', 's-011', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/docs',    '/docs',    'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.1', 'US', 'CA', 'San Francisco', 'Chrome',  '120', 'macOS',  '14', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 3 HOUR),
    ('e-16', 'v-002', 's-012', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/pricing', '/pricing', 'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.2', 'US', 'NY', 'New York',      'Safari',  '17',  'iOS',    '17', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 4 HOUR),
    ('e-17', 'v-004', 's-013', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/about',   '/about',   'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.4', 'US', 'WA', 'Seattle',       'Chrome',  '120', 'Windows','10', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 5 HOUR),
    ('e-18', 'v-006', 's-014', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.6', 'US', 'CA', 'San Jose',      'Safari',  '17',  'iPadOS', '17', 'tablet',  0, 'Mozilla/5.0',           now() - INTERVAL 6 HOUR),
    ('e-19', 'v-010', 's-015', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/pricing', '/pricing', 'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.10','US', 'IL', 'Chicago',       'Safari',  '17',  'iOS',    '17', 'mobile',  0, 'Mozilla/5.0',           now() - INTERVAL 7 HOUR),
    ('e-20', 'v-007', 's-016', '${SERVER_ID}', 1, '', 'hello', 'https://site.test.local/',        '/',        'site.test.local', '', '',           'Direct',   '', '', '', '', '10.0.0.7', 'US', 'TX', 'Austin',        'Chrome',  '120', 'Windows','11', 'desktop', 0, 'Mozilla/5.0',           now() - INTERVAL 8 HOUR);
" 2>&1)
SEED_EXIT=$?
if [ "$SEED_EXIT" -ne 0 ] || [ -n "$SEED_OUTPUT" ]; then
    echo "  seed INSERT exit=$SEED_EXIT output:"
    echo "$SEED_OUTPUT" | sed 's/^/    /'
fi

SEED_COUNT=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula -q \
    "SELECT count() FROM hula.events WHERE server_id='${SERVER_ID}'" 2>&1)
echo "  seeded ${SEED_COUNT} pageview events for server_id='${SERVER_ID}' (target: 20)"
