-- Materialized views for hot-path analytics queries.
--
-- The query layer picks the source table based on the requested date
-- range and granularity:
--   - ≤ 48 hours, hourly granularity     → mv_events_hourly
--   - ≤ 14 days, daily granularity       → mv_events_daily
--   - > 14 days                          → mv_events_daily
--   - session-aware queries              → mv_sessions
--   - drill-down into a single event     → events (raw)

CREATE TABLE IF NOT EXISTS mv_events_hourly_state
(
    bucket_hour     DateTime,
    server_id       LowCardinality(String),
    url_path        String,
    referer_host    LowCardinality(String),
    country_code    LowCardinality(String),
    device_category LowCardinality(String),
    channel         LowCardinality(String),
    pageviews       UInt64,
    visitors_hll    AggregateFunction(uniq, String)
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(bucket_hour)
ORDER BY (bucket_hour, server_id, url_path, referer_host, country_code, device_category, channel);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_events_hourly
TO mv_events_hourly_state
AS
SELECT
    toStartOfHour(when) AS bucket_hour,
    server_id,
    url_path,
    referer_host,
    country_code,
    device_category,
    channel,
    count() AS pageviews,
    uniqState(belongs_to) AS visitors_hll
FROM events
WHERE is_bot = 0
GROUP BY bucket_hour, server_id, url_path, referer_host, country_code, device_category, channel;


CREATE TABLE IF NOT EXISTS mv_events_daily_state
(
    bucket_day      Date,
    server_id       LowCardinality(String),
    url_path        String,
    referer_host    LowCardinality(String),
    country_code    LowCardinality(String),
    device_category LowCardinality(String),
    channel         LowCardinality(String),
    pageviews       UInt64,
    visitors_hll    AggregateFunction(uniq, String)
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(bucket_day)
ORDER BY (bucket_day, server_id, url_path, referer_host, country_code, device_category, channel);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_events_daily
TO mv_events_daily_state
AS
SELECT
    toDate(when) AS bucket_day,
    server_id,
    url_path,
    referer_host,
    country_code,
    device_category,
    channel,
    count() AS pageviews,
    uniqState(belongs_to) AS visitors_hll
FROM events
WHERE is_bot = 0
GROUP BY bucket_day, server_id, url_path, referer_host, country_code, device_category, channel;


-- mv_sessions: one row per (server_id, session_id) aggregating start/end
-- times, pageview count, and entry/exit paths.
CREATE TABLE IF NOT EXISTS mv_sessions_state
(
    server_id           LowCardinality(String),
    session_id          String,
    visitor_id          String,
    session_start       AggregateFunction(min, DateTime64(3)),
    session_end         AggregateFunction(max, DateTime64(3)),
    pageviews           UInt64,
    entry_path          AggregateFunction(argMin, String, DateTime64(3)),
    exit_path           AggregateFunction(argMax, String, DateTime64(3)),
    entry_referer_host  AggregateFunction(argMin, String, DateTime64(3)),
    entry_channel       AggregateFunction(argMin, String, DateTime64(3)),
    country_code        LowCardinality(String),
    device_category     LowCardinality(String),
    utm_source          LowCardinality(String),
    utm_medium          LowCardinality(String),
    utm_campaign        String
)
ENGINE = AggregatingMergeTree
ORDER BY (server_id, session_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_sessions
TO mv_sessions_state
AS
SELECT
    server_id,
    session_id,
    belongs_to AS visitor_id,
    minState(when) AS session_start,
    maxState(when) AS session_end,
    count() AS pageviews,
    argMinState(url_path, when) AS entry_path,
    argMaxState(url_path, when) AS exit_path,
    argMinState(referer_host, when) AS entry_referer_host,
    argMinState(channel, when) AS entry_channel,
    any(country_code) AS country_code,
    any(device_category) AS device_category,
    any(utm_source) AS utm_source,
    any(utm_medium) AS utm_medium,
    any(utm_campaign) AS utm_campaign
FROM events
WHERE is_bot = 0 AND session_id != ''
GROUP BY server_id, session_id, belongs_to;
