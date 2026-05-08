# Tech note: ClickHouse system-log volume on idle hula deployments

**Status:** observed in production, fix shipped to `tlaloc-deploy-site`.
**Audience:** operators running hula on a single-node ClickHouse with default
config.

## TL;DR

ClickHouse's stock config is tuned for busy multi-tenant clusters and turns on
a dozen self-observability tables that log every query, metric tick, merge,
and stack-sample. On a hula deployment that serves "very little" actual
analytics traffic, those system tables grow ~150 MB per 15 minutes from
idle alone — orders of magnitude larger than the user-facing data they
exist to observe. Disable them, or accept that the bulk of `ch_data/` will
be CH telemetry.

## What we saw

Operator nuked `ch_data/` and `ch_logs/`, restarted the stack. Fifteen
minutes later — site receiving "very very little" real traffic — `ch_data/`
was 142 MB. After three days at this rate it's at the multi-GB range that
shows up in `df` checks.

`SELECT … FROM system.parts` (without the `WHERE active` filter, so we
see inactive parts as well as live ones) showed:

| Table                              | active+inactive | rows | parts |
|------------------------------------|-----------------|------|-------|
| `system.metric_log`                | 26.7 MB         | 9 K (live)  | 108 inactive |
| `system.trace_log`                 | 16.9 MB         | 647 K (inactive) | 108 |
| `system.text_log`                  | 15.1 MB         | 367 K (inactive) | 108 |
| `system.part_log`                  | 2.4 MB          | 15 K (inactive) | 108 |
| `system.query_log`                 | 2.2 MB          | 7,856 (inactive) | 108 |
| `system.asynchronous_metric_log`   | 1.9 MB          | 2.2 M (inactive) | 120 |
| `system.processors_profile_log`    | 1.2 MB          | 25 K (inactive) | 108 |
| `system.histogram_metric_log`      | 0.9 MB          | 147 K (inactive) | 108 |
| **`hula.events`** (user data)      | **8 KB**        | **2 rows**       | 2   |
| **`hula.visitors`**                | **4 KB**        | **4 rows**       | 1   |
| **`hula.visitor_cookies`**         | **3 KB**        | **4 rows**       | 1   |

User analytics: ~17 KB across four tables and 12 rows. CH self-telemetry:
the rest. The "108 inactive parts" cliff is the smoking gun — every merge
of `metric_log` writes a row to `part_log` describing the merge, which
itself triggers a merge that writes another `part_log` row. CH's GC
eventually catches up but the steady-state on-disk footprint is large
because every part takes its own 4 KB-aligned directory full of small
files (primary.idx, columns.bin, checksums.txt, etc).

The hula healthcheck (`SELECT 1` every 5 s) alone produces ~17 K
`query_log` rows/day. Multiply by all the metric/trace/text/part log
tickers running in parallel and you get the storage pattern above.

## Fix

Drop a config.d override into the ClickHouse container that suppresses the
relevant tables. The shipped default config has each one declared at the
top level; the `<X remove="1"/>` form is ClickHouse's documented way of
nulling out an inherited entry from `/etc/clickhouse-server/config.xml`.

`ch_config/config.d/disable-system-logs.xml`:

```xml
<?xml version="1.0"?>
<clickhouse>
    <metric_log remove="1"/>
    <asynchronous_metric_log remove="1"/>
    <trace_log remove="1"/>
    <text_log remove="1"/>
    <part_log remove="1"/>
    <query_log remove="1"/>
    <query_thread_log remove="1"/>
    <query_views_log remove="1"/>
    <session_log remove="1"/>
    <processors_profile_log remove="1"/>
    <histogram_metric_log remove="1"/>
    <opentelemetry_span_log remove="1"/>
    <asynchronous_insert_log remove="1"/>
    <crash_log remove="1"/>
    <backup_log remove="1"/>
    <error_log remove="1"/>
    <logger>
        <level>warning</level>
        <size>50M</size>
        <count>3</count>
    </logger>
</clickhouse>
```

Mount it in the compose's `hula-clickhouse` service:

```yaml
volumes:
  - ./ch_data:/var/lib/clickhouse
  - ./ch_logs:/var/log/clickhouse-server
  - ./ch_config/config.d:/etc/clickhouse-server/config.d:ro
```

Then `docker compose up -d hula-clickhouse` (the running container needs
to be recreated to re-read config.d). Verify after restart with:

```sql
SELECT name FROM system.tables WHERE database = 'system' AND name LIKE '%_log'
```

— the disabled ones should not appear.

To reclaim space already accumulated under the old config, either:

1. **Surgical** — `TRUNCATE TABLE IF EXISTS system.<each table>;` for every
   row in the table above, then wait a minute for CH to drop the inactive
   parts.
2. **Nuclear** — stop the container, `rm -rf ch_data ch_logs`, restart.
   Loses the `hula.*` user tables (rebuild on next event ingest from the
   migrations the hula binary runs at startup).

The user-data tables (`hula.events`, `hula.visitors`, `hula.visitor_cookies`,
the `mv_*` materialized views) are governed by the
`pkg/store/clickhouse/migrations` package — they re-create on first hula
boot against an empty database, so the nuclear option is genuinely safe
when the operator has accepted that ~12 rows of analytics history will
disappear.

## What `ch_logs/` is

Separate from `ch_data/` — the `/var/log/clickhouse-server/` bind mount.
Default config has the server log rotate at 1 GB and keep 10 files —
i.e., a 10 GB cap that's only reached if you actually break things, but
the fully-populated state on a healthy server is still a few GB of `INF`
chatter. The `<logger>` block above caps it at 50 MB × 3 files = 150 MB
and downgrades to `warning` level so the routine boot/shutdown noise
stops landing.

## When NOT to apply this

These system tables are how you debug ClickHouse-side problems. If
you're operating a multi-tenant or high-traffic install, leave the
defaults — and put `ch_data/` on a partition that can absorb the
growth. The fix above is for the tlaloc-style "single-node, single-
tenant, mostly-idle" shape where the observability noise dominates the
real analytics by 4+ orders of magnitude.

`query_log` specifically is the only one some operators want to keep
for short-term debugging; in that case, drop the `<query_log
remove="1"/>` line and instead add a TTL:

```xml
<query_log>
    <ttl>event_date + INTERVAL 7 DAY DELETE</ttl>
</query_log>
```

so the table self-prunes weekly.

## Reproduction reference

Diagnostic query (works on any running CH with admin or `hula`-user
access):

```sql
SELECT database, table, active,
       formatReadableSize(sum(bytes_on_disk)) AS size,
       sum(rows) AS rows,
       count() AS parts
FROM system.parts
GROUP BY database, table, active
ORDER BY sum(bytes_on_disk) DESC
LIMIT 20
```

If the top entries are `system.*_log` and the `active=0` columns are
fat (many parts each), this note applies.
