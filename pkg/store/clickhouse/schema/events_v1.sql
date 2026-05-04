-- Explicit DDL for the visitor-events table. Replaces GORM's
-- AutoMigrate-generated schema with reviewable, versioned SQL.
--
-- Templating: {{ .EventsTTLDays }} is substituted by the migration runner
-- from config.GetConfig().Analytics.EventsTTLDays (default 395 — ~13 months).
--
-- Engine choice rationale:
--   MergeTree with PARTITION BY month — hot paths filter by date range;
--   monthly partitions keep scans small and make retention trivial.
--   ORDER BY (when, server_id, belongs_to, id) sorts data by time-first
--   (the dominant access pattern), secondarily by server (for multi-
--   tenant filtering), and then by visitor for per-visitor timelines.
--   id last to break ties between events in the same millisecond.

CREATE TABLE IF NOT EXISTS events_v1
(
    id               String,
    belongs_to       String,                   -- visitor_id (FK to visitors)
    session_id       String,                   -- derived at ingest; see pkg/analytics/enrich/session.go
    server_id        LowCardinality(String),   -- denormalized from host; enables per-server filtering without a JOIN
    code             UInt64,                   -- EventCode* from model/event.go
    data             String,
    method           LowCardinality(String),   -- "hello" / "helloiframe" / "hellonoscript"
    url              String,
    url_path         String,
    host             LowCardinality(String),
    referer          String,                   -- raw Referer header
    referer_host     LowCardinality(String),   -- parsed domain
    channel          LowCardinality(String),   -- Direct / Search / Social / Referral / Email
    search_term      String,                   -- q= extracted when channel=Search
    utm_source       LowCardinality(String),
    utm_medium       LowCardinality(String),
    utm_campaign     String,
    utm_term         String,
    utm_content      String,
    gclid            String,
    fbclid           String,
    browser          LowCardinality(String),
    browser_version  String,
    os               LowCardinality(String),
    os_version       String,
    device_category  LowCardinality(String),   -- mobile / tablet / desktop / bot / unknown
    is_bot           UInt8,                    -- 0 or 1
    country_code     LowCardinality(String),
    region           LowCardinality(String),
    city             String,
    -- Network identity sourced from the badactor ipinfo cache (ip-api.com
    -- backed). Migration 0005 adds these to existing deployments. Empty
    -- string when the IP hasn't been resolved yet (first event for a
    -- fresh IP); subsequent events pick up the cache hit.
    asn              LowCardinality(String),
    isp              LowCardinality(String),
    org              LowCardinality(String),
    when             DateTime64(3),
    from_ip          String,
    browser_ua       String,
    -- Phase 4c.1 — consent state at write time. 0 = no consent, 1 = consent.
    -- Migration 0004 also runs ALTER TABLE ADD COLUMN IF NOT EXISTS so
    -- existing deployments pick up the columns without recreating the table.
    consent_analytics UInt8 DEFAULT 0,
    consent_marketing UInt8 DEFAULT 0,
    created_at       DateTime64(3),
    updated_at       DateTime64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(when)
ORDER BY (when, server_id, belongs_to, id)
TTL toDateTime(when) + toIntervalDay({{ .EventsTTLDays }})
SETTINGS index_granularity = 8192;
