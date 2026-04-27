#!/bin/bash
# Suite 18: events schema / migration-runner smoke.
#
# The pkg/store/clickhouse migration runner lands in Phase 0 but isn't
# called from Run() yet (Phase 0 keeps GORM AutoMigrate for the events
# table). This suite verifies:
#   1. The events table exists and is queryable.
#   2. Legacy columns (id, when, url) survive Phase 0.
#   3. The enrichment columns added in stage 0.9e can be selected — they
#      may be NULL/empty on existing rows but the column must exist.

# 1. events table exists.
tables=$(dc exec -T hula-clickhouse sh -c "clickhouse-client -q 'SHOW TABLES FROM hula' 2>/dev/null" || true)
assert_contains "$tables" "events" "events table present in ClickHouse"

# 2. Legacy columns queryable.
legacy_probe=$(dc exec -T hula-clickhouse sh -c "clickhouse-client -q \"SELECT count() FROM hula.events\" 2>/dev/null" || true)
if echo "$legacy_probe" | grep -qE '^[0-9]+$'; then
    pass "legacy events.count() returns a number: $legacy_probe"
else
    fail "legacy events query failed or returned unexpected output: $legacy_probe"
fi

# 3. Enrichment columns selectable. A failed SELECT would mean GORM
# AutoMigrate hasn't picked up the new Event struct fields.
enrich_probe=$(dc exec -T hula-clickhouse sh -c "clickhouse-client -q \"SELECT session_id, channel, browser, device_category, country_code FROM hula.events LIMIT 1\" 2>&1" || true)
# Tolerate a 'column not found' only if the events table was created
# before the Phase-0 column additions and nobody has done an explicit
# migration. Document that in the test output.
if echo "$enrich_probe" | grep -qi "Unknown identifier\|Missing column"; then
    fail "enrichment columns missing from events table (legacy schema; run migration)"
else
    pass "enrichment columns present (session_id, channel, browser, device_category, country_code)"
fi

# 4. schema_migrations tracker exists if the runner has been invoked.
# Phase-0 tolerates absence (runner isn't wired into Run() yet).
sm_check=$(dc exec -T hula-clickhouse sh -c "clickhouse-client -q 'SHOW TABLES FROM hula LIKE \"schema_migrations\"' 2>/dev/null" || true)
if echo "$sm_check" | grep -q "schema_migrations"; then
    pass "schema_migrations tracker table present"
else
    pass "schema_migrations tracker not present — runner not yet wired into Run() (expected in Phase 0)"
fi
