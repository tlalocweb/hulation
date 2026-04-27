# Phase 1 — Execution Status (COMPLETE)

**Status: all 7 stages landed. `./test/e2e/run.sh` → 99/99, `./test/integration/run.sh` → 29/29.**

Summary of the Phase-1 analytics API rollout:
- 11 AnalyticsService RPCs live at `/api/v1/analytics/*` (Summary, Timeseries, Pages, Sources, Geography, Devices, Events, FormsReport (stub), Visitors, Visitor, Realtime).
- Query builder (`pkg/analytics/query`) composes parameterised ClickHouse SQL with per-request server_id ACL injection, automatic source-table selection (raw `events` / `mv_events_hourly` / `mv_events_daily`), and filter chip composition.
- Materialized-view runner is now wired into boot; `mv_events_hourly` + `mv_events_daily` + `mv_sessions` are created on first startup.
- Admin callers are treated as superadmin for analytics — they can query any server_id (including seed fixtures). Non-admin callers route through `pkg/server/authware/access` (empty until the Bolt user store ships; matches the plan's deferral).
- CSV export via `?format=csv` on every table endpoint; emits RFC-4180 CSV with `Content-Disposition: attachment`.
- Per-user rate limiter (10 burst, 30/min sustained) guards the analytics surface; bursts get 429 with `Retry-After`.
- Realtime queries are cached for 5 seconds per server_id so dashboard polling doesn't stampede ClickHouse.
- New e2e suite `21-analytics.sh` with deterministic seed fixture asserts golden numbers across every RPC.

Deferred to Phase 3 (explicitly not blocking Phase 1 or Phase 2):
- Goals CRUD (`POST/PATCH/DELETE /api/analytics/goals`).
- Scheduled reports CRUD.
- User-server ACL admin RPCs (GrantServerAccess / RevokeServerAccess / ListServerAccess still Unimplemented).
- FormsReport body — requires form submissions to be persisted as events with a discoverable form_id in the `data` column (handler/form.go doesn't yet emit those).
- Compare-window delta math in Summary (the four `*_delta_pct` fields currently return -1 sentinels).
- mv_sessions read-path (Summary still reads raw `events`).

---

Branch: `roadmap/phase1`
Plan: `PLAN_1.md`

## Completed stages (7 of 7)

### Stage 1.1 — Proto audit + regen ✅
Commit: `1097da9`

Additive proto changes against PRD §12, no field-number reuse:

- `Filters`: added `browser` (12), `os` (13), `channel` (14), `utm_source` (15), `utm_medium` (16), `utm_campaign` (17), `region` (18), `city` (19).
- `TableRow`: added per-report columns so a single shared row shape covers Pages, Sources, Geography, Events, FormsReport — `unique_pageviews`, `avg_time_on_page_seconds`, `entrances`, `exits` (Pages); `pages_per_visit`, `goal_conv_rate` (Sources); `percent` (Geography); `count`, `unique_visitors`, `first_seen`, `last_seen` (Events); `submits`, `conversion_rate`, `avg_time_to_submit_seconds` (FormsReport).
- Request messages: added `format` (CSV export) on all table requests; `limit`+`offset` on Pages, Sources, Visitors; `group_by` on Sources.
- `VisitorEvent`: added `ip` (7) for the profile timeline.

`make protobuf` regenerates cleanly. `go build .` green.

### Stage 1.2 — Query builder + ClickHouse MV wire-up ✅
Commit: `ceb6880`

New package `pkg/analytics/query`:

- `Builder` type enforces two invariants: every query is parameterised (no user-input concatenation into SQL), and every query carries a `server_id IN (...)` ACL clause. Builder fails fast (`ErrNoACL`) if the handler forgot to install an ACL.
- `WithAllowedServerIDs(ids)` installs the caller's allowed list; `WithSuperadmin()` flags admin callers who bypass the intersection (added in stage 1.7 — see below).
- Methods: `BuildSummary`, `BuildTimeseries`, `BuildTable(dim)`, `BuildVisitors`, `BuildVisitor`, `BuildRealtime`, `BuildEvents`, `BuildDevices`, `BuildGeography`, `BuildSources`.
- `Dimension` enum (`DimPath`, `DimRefererHost`, `DimChannel`, `DimCountry`, `DimRegion`, `DimDeviceCategory`, `DimBrowser`, `DimOS`, `DimEventCode`, `DimFormID`) with `isOnMV` flag driving source-table selection.
- `pickSource` boundary rules:
  - range ≤ 48h + hour granularity → raw `events`
  - range ≤ 14d + hour/day granularity → `mv_events_hourly_state`
  - range > 14d OR week granularity → `mv_events_daily_state`
  - any chip referencing an off-MV column (utm_*, event_code, goal, browser, os, region, city) → raw `events`

Unit tests (`pkg/analytics/query/builder_test.go`): ACL-missing / bad-range / parseable-time error paths; ACL intersection (empty set emits `IN ('')`); source-table boundary at 1h / 10d / 90d × hour/day/week; off-MV-dim force raw; `BuildVisitor` returns two parameterised queries; `BuildRealtime` returns four queries; injection smoke — malicious `country` chip lands as a bound param, not in the SQL string.

Boot wiring: `preloadSharedSubsystems` in `server/run_unified.go` now calls `clickhouse.Apply()` (the migration runner wired in Phase 0 but not invoked). Non-fatal on failure — analytics queries fall back to raw `events` when MVs are absent.

### Stage 1.3 — Summary + Timeseries RPCs live ✅
Commit: `9fe588c`

- `pkg/api/v1/analytics/analyticsimpl.go` implements `Server` with `Summary` and `Timeseries`. Uses a per-call `Builder` pre-loaded with the caller's ACL via the `aclLookup` hook.
- `server/analytics_acl.go`: admin/root roles get every configured server_id; non-admin callers route through `authware/access.AllowedServerIDs` intersected with the configured server list.
- `server/unified_boot.go`: registers `AnalyticsService` on both the gRPC server and the REST gateway mux.
- Integration harness (`test/integration/run.sh`) added three analytics smokes: `/api/v1/analytics/summary` with admin JWT returns visitors + pageviews; `/timeseries` returns a buckets array; without JWT → 401.

Summary SQL uses a GROUP-BY-session inner subquery (one row per session) so the outer aggregation can compute bounce rate and avg duration cleanly — the original window-function shape was rejected by ClickHouse ("column not under aggregate function").

Deltas (compare window) return -1 sentinels; wiring compare into the builder is a Phase-1 follow-up.

### Stage 1.4 — Table reports ✅
Commit: `ac558da`

Six RPC implementations in `pkg/api/v1/analytics/reports.go`:

- `Pages` — `BuildTable(DimPath)`, honours `Limit`/`Offset`.
- `Sources` — `BuildSources` picks `DimChannel`/`DimRefererHost`/`DimUtmSource` based on the `GroupBy` field (default: channel).
- `Geography` — `BuildGeography` switches to `DimRegion` when `Filters.Country` is set, else `DimCountry`. Post-processes rows to populate `percent` (share of total pageviews in the response).
- `Devices` — three parallel queries (`device_category`, `browser`, `os`) packed into a single `DevicesResponse`.
- `Events` — `BuildEvents` returns code-grouped rows with `count`, `unique_visitors`, `first_seen`, `last_seen`.
- `FormsReport` — stub returning empty `Rows` (ACL gate still runs). Blocked on form-submission event emission (see deferred list).

Shared `queryTable` helper handles the `(key, visitors, pageviews)` scan across four of the six RPCs.

### Stage 1.5 — Visitors + Visitor detail + Realtime ✅
Commit: `1ca705e`

- `Visitors` — paginated directory from `BuildVisitors`. Projects first/last-seen, sessions, pageviews, events, top_country, top_device. Default limit 50, cap 1000.
- `Visitor` — pair of queries (summary header + event timeline, capped at 1000 rows). Returns `codes.NotFound` when the id has no events in the 400-day retention window.
- `Realtime` — four queries (active_visitors_5m, recent events, top pages, top sources). 5-second per-server cache guarded by a package-level `sync.Mutex`-indexed map so dashboard polling doesn't stampede ClickHouse.

### Stage 1.6 — CSV export + rate limiting ✅
Commit: `19df784`

New `server/analytics_csv.go` attaches `analyticsHTTPMiddleware` (scoped to `/api/v1/analytics/*` paths only):

- **CSV export**: triggered by `?format=csv`. Captures the gateway JSON body, walks for the first array key (`rows`/`buckets`/`visitors`/`recent`/`top_pages`/`top_sources`), emits RFC-4180 CSV with a header row. Devices (three parallel arrays) flattens into one CSV with a `kind` column. Summary (no arrays) emits a `key,value` CSV. Response headers: `Content-Type: text/csv; charset=utf-8`, `Content-Disposition: attachment; filename=<report>-<yyyymmdd>.csv`. Error responses pass through as JSON.
- **Rate limiter**: `golang.org/x/time/rate` per-user token bucket keyed on `authware.Claims.Username` (anon key for missing claims). Burst 10, sustained 0.5 qps (30/min). Over-quota → 429 with `Retry-After: 2`.

Added `golang.org/x/time` to go.mod via `go mod tidy`.

### Stage 1.7 — e2e suite 21 + fixture seed ✅
Commit: `ecdb78e`

- `test/e2e/fixtures/analytics-seed.sh` — deterministic INSERT into `hula.events` tagged `server_id='testsite-seed'`. Idempotent via `ALTER TABLE … DELETE WHERE server_id=…` before insert. Fixture shape: 10 distinct visitors, 20 pageviews over 14 days + 8 hours, 5 url_paths, 2 countries, 3 device categories, 3 channels.
- `test/e2e/suites/21-analytics.sh` — runs the seed then fires every analytics RPC, asserting golden numbers across the REST gateway: Summary (visitors=10, pageviews=20), Timeseries (buckets present), Pages (top page is `/`), Sources (Direct channel present), Geography (US + DE rows, percent populated), Devices (desktop/mobile/tablet represented, browser + os arrays returned), Events (code=1 row with count=20), Visitors (10 unique ids), Visitor profile (timeline populated), Realtime (active_visitors_5m present), CSV export (header row + ≥1 data row), Rate limiter (>0 × 429 on 20 rapid calls from a single runner container).

Fixes surfaced while bringing up suite 21:
- **Superadmin ACL gap**: `Builder.WithAllowedServerIDs` intersected the caller's allowed list with the requested server_id, which meant admin callers asking for `testsite-seed` (or any server not in the config) got an empty ACL and zero rows. Added `Builder.WithSuperadmin()` so admin callers skip the intersection; the builder still populates `server_id IN (…)` from the request so queries stay tenant-scoped. `aclLookup` returns a new `ACLResolution{Allowed, Superadmin}` struct.
- **Docker exec flag**: seed script initially used `docker exec -T` (only valid for `docker compose exec`); switched to plain `docker exec`.
- **Rate-limit test harness**: individual `dc run --rm test-runner curl ...` spins up a fresh container per call (~1s latency), which let the bucket refill between requests. Moved all 20 iterations into a single runner shell so they actually race.

---

## Final sign-off checklist (Phase 1 → Phase 2)

Verified in this session:

- [x] All 7 stages landed on `roadmap/phase1`.
- [x] `go build -o hula .` produces a working binary (~54MB).
- [x] `go test ./pkg/analytics/...` green (query builder unit tests).
- [x] `make protobuf` regenerates cleanly (no uncommitted diff).
- [x] `./test/integration/run.sh` → 29 passed, 0 failed (includes the 1.3 analytics smoke).
- [x] `./test/e2e/run.sh` → 99 passed, 0 failed (20 legacy + 1 new).
- [x] `hulactl` still builds.
- [x] `PHASE_1_STATUS.md` written.

Remaining work — NOT blocking Phase 1 → Phase 2 handoff:

- [ ] `UI_PRD.md` §12 endpoint table update to match shipped paths / field names (e.g., `/api/analytics/*` in PRD vs `/api/v1/analytics/*` in the actual spec).
- [ ] Branch merged to main (or fast-forwardable).
- [ ] Phase-2 team review of the JSON response shapes before UI scaffolding starts.

## Deferred to Phase 3

Items intentionally out of scope — see PLAN_1.md §1.3:

- Goals CRUD (`POST/PATCH/DELETE /api/analytics/goals`). Requires a goals-config table in BoltDB + `is_goal` computation in the enrichment pipeline.
- Scheduled reports CRUD (`/api/analytics/reports/*`).
- User-server ACL admin RPCs (`GrantServerAccess` / `RevokeServerAccess` / `ListServerAccess`).
- FormsReport body — requires form-submission events with an extractable form_id in the `data` column.
- Compare-window delta math in Summary (the four `*_delta_pct` fields return -1 sentinels).
- mv_sessions read-path (Summary still reads raw `events` for bounce + duration).

## Pre-existing issues

- `store/bolt.go` (legacy umputun/remark42-derived store, not imported by the hula binary) still has undefined-type errors at HEAD. Pre-existed Phase 0 and Phase 1; `go build .` works, `go build ./...` fails only on that orphan package. Triage before the store rewrite in Phase 3+.
