-- Phase 4b — visitor chat tables.
--
-- Two tables:
--   chat_sessions  — one row per chat session (visitor opened the
--                    widget, passed Turnstile + email-verifier, got
--                    a chat token from POST /api/v1/chat/start).
--                    ReplacingMergeTree so we can update
--                    last_message_at / message_count / closed_at /
--                    assigned_agent_id by INSERTing a new version
--                    instead of running async ALTER MUTATIONs.
--   chat_messages  — one row per message (back-and-forth N times
--                    between visitor and agent = 2N rows).
--                    Plain MergeTree with two skip indexes on
--                    `content` for FTS:
--                      tokenbf_v1: word-level "find chats mentioning pricing"
--                      ngrambf_v1: substring "find chats with @example.com"
--                    When ClickHouse 25.x stabilises the Tantivy-
--                    backed `text` index we replace both with one
--                    inverted index; the SearchMessages RPC contract
--                    is unchanged.
--
-- Templating: {{ .ChatRetentionDays }} is substituted by the runner
-- from config.GetConfig().Chat.RetentionDays (default 365).

CREATE TABLE IF NOT EXISTS chat_sessions
(
    id                UUID,
    server_id         LowCardinality(String),
    visitor_id        String,
    visitor_email     String,
    -- denormalised so the All-Chats list page renders without a
    -- JOIN against `events`. Backfilled at session-start from the
    -- visitor's most-recent event row.
    visitor_country   LowCardinality(String) DEFAULT '',
    visitor_device    LowCardinality(String) DEFAULT '',
    visitor_ip        IPv6                   DEFAULT toIPv6('::'),
    user_agent        String                 DEFAULT '',
    started_at        DateTime64(3),
    closed_at         Nullable(DateTime64(3)),
    last_message_at   DateTime64(3),
    message_count     UInt32                 DEFAULT 0,
    -- enum mirrors model/chat.go SessionStatus
    -- queued    — created, no agent assigned yet
    -- assigned  — agent picked, awaiting their per-session WS open
    -- open      — agent's per-session WS is in (or has been in) the
    --             session; conversation is live
    -- closed    — explicitly closed by either side
    -- expired   — token expired or stale-cleanup ticker reaped it
    status            LowCardinality(String) DEFAULT 'queued',
    -- routing: empty = unassigned/queued; set when next-available
    -- routing matches the session to an admin or when an admin
    -- explicitly takes a session from the queue. Resets to '' when
    -- a session is released back to the queue (admin clicked
    -- "Release", or grace period expired after agent disconnect).
    assigned_agent_id LowCardinality(String) DEFAULT '',
    assigned_at       Nullable(DateTime64(3)),
    -- arbitrary JSON the client can attach (page URL, referrer, …)
    meta              String                 DEFAULT ''
)
ENGINE = ReplacingMergeTree(last_message_at)
ORDER BY (server_id, started_at, id)
TTL toDateTime(started_at) + INTERVAL {{ .ChatRetentionDays }} DAY DELETE;

CREATE TABLE IF NOT EXISTS chat_messages
(
    id            UUID,
    session_id    UUID,
    server_id     LowCardinality(String),
    visitor_id    String,
    -- direction = visitor | agent | system | bot
    --   visitor — sent by the website visitor over /chat/ws
    --   agent   — sent by an admin (REST PostAdminMessage or
    --             agent-WS); sender_id holds the username
    --   system  — server-generated (session opened, agent_joined,
    --             closed by inactivity, …); sender_id empty
    --   bot     — reserved for the future bot auto-responder
    direction     LowCardinality(String),
    sender_id     String         DEFAULT '',
    content       String,
    `when`        DateTime64(3),
    INDEX idx_content_token (content) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 4,
    INDEX idx_content_ngram (content) TYPE ngrambf_v1(3, 32768, 3, 0) GRANULARITY 4
)
ENGINE = MergeTree
ORDER BY (server_id, session_id, `when`)
TTL toDateTime(`when`) + INTERVAL {{ .ChatRetentionDays }} DAY DELETE;
