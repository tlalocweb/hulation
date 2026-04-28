-- Phase 4c.1 — consent state on events.
--
-- Adds two boolean columns to events recording the visitor's consent
-- state at the moment the event was written. Rationale and full
-- design in PLAN_4C.md §2 stage 4c.1.
--
--   consent_analytics  — visitor consents to non-marketing analytics
--                        (pageviews, retention metrics, internal BI).
--   consent_marketing  — visitor consents to marketing/advertising
--                        use of their data (forwarders to Meta CAPI,
--                        Google Ads conversion etc.).
--
-- Default 0 (no consent) on every existing row. The hello-handler
-- branch in `consent_mode: off` mode (the default) sets analytics=1
-- and lets Sec-GPC determine marketing — so net-new rows on
-- post-upgrade traffic populate sensibly without operator action.
--
-- Forward-only, additive. Existing queries that don't reference
-- these columns continue to work unchanged.

ALTER TABLE events
    ADD COLUMN IF NOT EXISTS consent_analytics UInt8 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS consent_marketing UInt8 DEFAULT 0;
