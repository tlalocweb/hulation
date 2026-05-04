-- Phase-1 follow-up — network identity (ASN / ISP / Org) on events.
--
-- The ipinfo cache (badactor/ipinfo.go) already pulls full geo + ASN
-- + ISP + Org for every IP it sees, but earlier enrichment only
-- forwarded country/region/city onto the event row. This migration
-- adds the remaining three columns so the visitor analytics surface
-- can show "who owns this IP" alongside country/city.
--
-- Defaults to empty string on every existing row. Net-new traffic
-- post-upgrade picks up the populated values via the IPInfoHook
-- async-on-miss path; the FIRST event for a brand-new IP still
-- has empty ASN/ISP/Org (cache miss → async lookup → next event
-- finds it). Same shape as the country/region/city behavior.
--
-- Forward-only, additive. Existing queries that don't reference
-- these columns continue to work unchanged.

ALTER TABLE events
    ADD COLUMN IF NOT EXISTS asn LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS isp LowCardinality(String) DEFAULT '',
    ADD COLUMN IF NOT EXISTS org LowCardinality(String) DEFAULT '';
