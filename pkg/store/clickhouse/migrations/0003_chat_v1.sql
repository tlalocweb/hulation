-- Phase 4b — chat tables migration marker.
--
-- The schema runner applies schema/chat_v1.sql first (idempotent
-- CREATE TABLE IF NOT EXISTS), so on a fresh DB this migration is
-- effectively a no-op. The marker exists so future chat-schema
-- evolutions (denormalised columns, MV rollups, FTS upgrade to
-- the Tantivy-backed `text` index) can land as 0004_chat_*.sql /
-- 0005_chat_*.sql etc. and get tracked in schema_migrations the
-- same way the events_v1 migrations are.
--
-- Idempotent on every shape of pre-existing DB:
--   - Fresh DB: schema file already created the tables; this is
--     a no-op INSERT into a one-row helper table.
--   - DB that pre-existed phase 4b but didn't have the tables:
--     same, the schema file created them just before this runs.

CREATE TABLE IF NOT EXISTS schema_migrations_chat_v1_marker
(
    applied_at DateTime64(3) DEFAULT now64(3)
)
ENGINE = MergeTree
ORDER BY applied_at;

INSERT INTO schema_migrations_chat_v1_marker (applied_at) VALUES (now64(3));
