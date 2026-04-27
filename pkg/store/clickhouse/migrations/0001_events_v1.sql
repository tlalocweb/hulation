-- Migration: legacy `events` → `events_v1`.
--
-- Runs once on first startup after Phase 0 stage 0.9b lands. Idempotent:
-- the runner tracks applied migrations in `schema_migrations` and skips
-- anything already seen.
--
-- Strategy:
--   1. Create events_v1 from pkg/store/clickhouse/schema/events_v1.sql
--      (runner applies the schema file first).
--   2. INSERT … SELECT from legacy `events`, defaulting new columns.
--   3. Rename legacy table for rollback; rename events_v1 → events.
--
-- Step 1 is handled by the runner before executing this file. The
-- statements below are steps 2 and 3.

INSERT INTO events_v1
    (id, belongs_to, session_id, server_id, code, data, method, url,
     url_path, host, referer, referer_host, channel, search_term,
     utm_source, utm_medium, utm_campaign, utm_term, utm_content,
     gclid, fbclid, browser, browser_version, os, os_version,
     device_category, is_bot, country_code, region, city, when,
     from_ip, browser_ua, created_at, updated_at)
SELECT
    id,
    belongs_to,
    '' AS session_id,           -- old rows predate session tracking
    '' AS server_id,             -- backfilled lazily on next event per host
    code,
    data,
    method,
    url,
    url_path,
    host,
    '' AS referer,
    '' AS referer_host,
    'Direct' AS channel,         -- conservative default for old rows
    '' AS search_term,
    '' AS utm_source,
    '' AS utm_medium,
    '' AS utm_campaign,
    '' AS utm_term,
    '' AS utm_content,
    '' AS gclid,
    '' AS fbclid,
    '' AS browser,
    '' AS browser_version,
    '' AS os,
    '' AS os_version,
    'unknown' AS device_category,
    0 AS is_bot,
    '' AS country_code,
    '' AS region,
    '' AS city,
    when,
    from_ip,
    browser_ua,
    created_at,
    updated_at
FROM events;

RENAME TABLE events TO events_legacy_v0;
RENAME TABLE events_v1 TO events;
