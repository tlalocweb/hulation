# Phase 1 — Analytics API (detailed plan)

Phase 0 shipped the foundations — enriched events, materialized views,
gRPC + grpc-gateway stack, RBAC, server-access ACL, the unified HTTPS
listener. Protos for the analytics surface are already defined at
`pkg/apispec/v1/analytics/analytics.proto`; 11 RPCs declared, zero
implementations. Phase 1 fills in the bodies.

**Goal**: every query the Phase-2 UI needs is reachable at
`/api/v1/analytics/*` with JWT auth, server-scoped ACL enforcement,
filter composition, and a query layer that picks between raw `events`,
`mv_events_hourly`, and `mv_events_daily` based on range and
granularity.

Related docs: `PLAN_OUTLINE.md` §Phase 1, `UI_PRD.md` §11 + §12,
`PLAN_0.md`, `PHASE_0_STATUS.md`.

---

## 1. Context and scope

### 1.1 What Phase 0 already delivered

- `events` table in ClickHouse, enriched at ingest with: `session_id`,
  `server_id`, `channel`, `referer_host`, `utm_*`, `gclid`, `fbclid`,
  `browser`, `browser_version`, `os`, `os_version`, `device_category`,
  `is_bot`, `country_code`, `region`, `city`.
- Materialized views `mv_events_hourly` and `mv_events_daily`, defined
  in migrations; the runner exists but is not yet wired into `Run()`
  (GORM AutoMigrate handles the events table; MV registration deferred
  to stage 1.2).
- `pkg/server/authware/access` — per-server ACL helpers (UserID → list
  of allowed `server_id`), backed by BoltDB. `GrantServerAccess` /
  `RevokeServerAccess` / `ListServerAccess` RPCs still return
  `Unimplemented` but the storage layer is live.
- `AnalyticsService` protos + generated gRPC stubs + grpc-gateway
  HTTP bindings. Permission annotation
  `server.{server_id}.analytics.read` declared on every RPC.
- Admin JWT pipeline (bearer token → `authware.Claims` on ctx, both
  gRPC and REST paths) — the plumbing analytics handlers will read
  identity from.

### 1.2 What Phase 1 ships

Concretely, at end of phase:

- **Query builder** (`pkg/analytics/query/`) — composes ClickHouse SQL
  from a typed `Filters` struct. Picks source table by range+granularity.
  Produces parameterised queries (no string concat of user input).
- **11 analytics RPCs live** — Summary, Timeseries, Pages, Sources,
  Geography, Devices, Events, FormsReport, Visitors, Visitor, Realtime.
- **Server-ACL enforcement** — each RPC intersects
  `Filters.server_ids` with the caller's allowed set (admin sees all;
  non-admin sees only granted servers; unauthorised ids silently
  dropped, not 403 — matches the UI contract).
- **CSV export** — every table-shaped response endpoint accepts
  `?format=csv` and emits a valid CSV (header row, one data row per
  table row, RFC 4180 quoting).
- **Rate limiting** — per-user token bucket (10 queries/sec burst, 30/min
  sustained) on heavy endpoints to prevent scan-the-world queries.
- **e2e suite 21-analytics.sh** — seeds ClickHouse with known events
  via the ingest path, fires every RPC, asserts golden numbers.

### 1.3 Out of scope (deferred to Phase 3)

- Goals CRUD (`POST/PATCH/DELETE /api/analytics/goals`) — requires a
  goals-config table in BoltDB, and `is_goal` computation in the
  enrichment pipeline.
- Scheduled reports CRUD.
- User-server ACL CRUD (`/api/analytics/access/*`) — the GrantServerAccess
  RPC family stays `Unimplemented` this phase; UI admins will use the
  BoltDB CLI.
- Any UI (Phase 2).

---

## 2. Stage breakdown

Seven stages. Each lands as a single commit on `roadmap/phase1` (new
branch off `main` after Phase 0 merges). Pattern: every stage must
leave the repo building + all existing tests green.

### Stage 1.1 — Proto audit + regeneration

**Goal**: confirm the analytics proto covers the PRD §12 surface and
regenerate Go stubs.

- Read through `analytics.proto`; cross-check against `UI_PRD.md` §12
  (endpoint table) and §6 (report page specs). Extend messages where
  the UI spec needs a field the proto is missing (e.g. `devices` needs
  browser+os+device_category breakdowns; `geography` needs region drill
  when `Filters.country` is set; `visitor` needs `source`, `referer`,
  `landing_page`).
- Make sure `Filters` carries every chip the filter bar exposes
  (§4.123): servers, date range, granularity, compare, path, country,
  device, source, event_code, goal, UTM.
- Add a `format` field to request messages that have CSV export
  (Pages/Sources/Geography/Devices/Events/FormsReport/Visitors); `""`
  defaults to JSON.
- Run `make protobuf` — regenerate `*.pb.go`, `*_grpc.pb.go`,
  `*.pb.gw.go`.

**Acceptance**:
- `go build ./...` passes.
- `git diff pkg/apispec/v1/analytics/analytics.proto` shows only
  additive changes (no field-number reuse, no renames that break
  wire compat).
- Proto covers every PRD §12 UI-facing field.

**Size**: half a day.

---

### Stage 1.2 — Query builder + source-table picker

**Goal**: a `pkg/analytics/query` package that takes
`analyticsspec.Filters` + a "report shape" and emits a parameterised
ClickHouse query.

- `query.Builder` type. Methods per report kind: `BuildSummary`,
  `BuildTimeseries(granularity)`, `BuildTable(dimension)` (one dim:
  path, source, country, device, browser, os, event_code, form_id),
  `BuildVisitors(limit, offset)`, `BuildVisitor(id)`, `BuildRealtime()`.
- Source-table picker (`pickSource`): range ≤ 24h → `events`; range ≤
  14d and granularity ≥ `day` → `mv_events_hourly`; otherwise
  `mv_events_daily`. Rationale documented inline.
- Filter composition: every filter chip contributes an `AND clause =
  ?`; params collected in order and returned alongside the SQL.
- Server-ACL injection: `Builder.WithAllowedServerIDs([]string)` is
  mandatory — every query gets a `server_id IN (?, ?, …)` clause,
  silently intersected with the request's `server_ids`.
- Wire the **MV runner** (`clickhouse.Apply()` from Phase 0) into the
  boot path so `mv_events_hourly` / `mv_events_daily` actually exist
  at query time. Deferred from Phase 0 explicitly.
- Unit tests (`query/builder_test.go`) with table-driven cases: assert
  emitted SQL + param slice for each report kind, each filter
  combination, each source-table boundary.

**Acceptance**:
- `go test ./pkg/analytics/...` passes.
- Source-table boundary test: 1h range picks `events`; 10d `day`
  picks `mv_events_hourly`; 90d `week` picks `mv_events_daily`.
- No user input concatenated into SQL — grep for `fmt.Sprintf.*SELECT`
  returns zero hits in the query package.
- MVs are present in the ClickHouse schema after a fresh boot (verify
  via suite 18 which already probes `schema_migrations`).

**Size**: 2 days.

---

### Stage 1.3 — Summary + Timeseries RPCs

**Goal**: two simplest endpoints live end-to-end.

- `pkg/api/v1/analytics/analyticsimpl.go` — new `Server` type
  implementing `AnalyticsServiceServer`. Constructor takes a
  `query.Builder` + an ACL lookup fn.
- `Summary` RPC: visitors, pageviews, bounce rate, avg session
  duration, plus `compare` deltas when requested.
- `Timeseries` RPC: `[]TimeseriesBucket` aligned to the requested
  granularity.
- Boot-path wiring in `server/unified_boot.go` — register
  `AnalyticsService` on both the gRPC server and the REST gateway.
- Integration-level smoke in the existing `test/integration/run.sh`
  (the simpler harness, not e2e): seed one pageview, call
  `/api/v1/analytics/summary`, assert `visitors: 1`.

**Acceptance**:
- `hulactl authok` continues to work (regression check).
- `curl -H "Authorization: Bearer $TOKEN" \
    'https://hula.test.local/api/v1/analytics/summary?server_id=testsite&filters.from=2026-04-01T00:00Z&filters.to=2026-04-30T00:00Z'`
  returns a populated JSON object.
- ACL test: a user without the server's `analytics.read` permission
  gets an empty response (not 403 — matches "silently drop" contract).
- Snake_case JSON preserved (the JSONPb option set in 0.x holds).

**Size**: 1 day.

---

### Stage 1.4 — Table reports (Pages, Sources, Geography, Devices, Events, FormsReport)

**Goal**: the six read-only list endpoints from PRD §6.

- One RPC implementation per report. Each is a thin wrapper around
  `Builder.BuildTable(dim)` + a projection into the proto response
  shape.
- **Pages**: top N paths by pageviews + bounce + avg duration.
- **Sources**: channel / referer_host / utm_source breakdown (grouped
  into the `channel` dimension by default; the UI switches groupings via
  a `group_by` chip).
- **Geography**: country → region drill when `Filters.country` is set.
- **Devices**: three tables in one response — device_category,
  browser, os — so the UI doesn't round-trip three times.
- **Events**: counts by `event_code`; filterable by code.
- **FormsReport**: submissions per form ID, conversion rate vs
  pageviews on the same form's source pages.

**Acceptance**:
- All six RPCs return deterministic output against the Phase-0 seed
  fixture from integration tests.
- Pagination: Pages/Sources/Visitors accept `limit` (default 50, cap
  1000) and `offset`. Other tables return up to 100 rows with no
  pagination (UI doesn't scroll those).
- Every table RPC is < 200 lines of Go (shared helpers in the impl
  package).

**Size**: 2 days.

---

### Stage 1.5 — Visitors + Visitor detail + Realtime

**Goal**: visitor-level queries.

- **Visitors**: paginated directory. Projects `visitor_id`, first-seen,
  last-seen, pageviews, channel, country, device. Respects all filter
  chips so a manager can find "mobile visitors from Germany last week".
- **Visitor** (`/api/v1/analytics/visitor/{visitor_id}`): full event
  timeline. Returns the visitor's profile summary + all events in time
  order (capped at 1000 events — anything larger gets a pagination
  token).
- **Realtime**: active visitors in the last 5 minutes + a rolling list
  of the 50 most recent events. Queries raw `events` table (MVs are
  too coarse). Cache the server-side result for 5s — the UI polls
  every 10s and multiple users viewing the same dashboard shouldn't
  stampede ClickHouse.

**Acceptance**:
- `GET /api/v1/analytics/visitors?limit=10` returns 10 or fewer rows.
- `GET /api/v1/analytics/visitor/{id}` returns the summary + timeline.
- `GET /api/v1/analytics/realtime` responds in < 100ms when cached.
- A new visitor fires a pageview → `realtime` reflects it within 5s.

**Size**: 1.5 days.

---

### Stage 1.6 — CSV export + rate limiting

**Goal**: `?format=csv` on every table endpoint, per-user rate limit.

- gRPC-gateway content-type negotiation: add a custom Marshaler that
  emits RFC-4180 CSV when `format=csv` is on the request. Header row
  is the proto field names; one row per table entry.
- Registered at the mux level via
  `runtime.WithMarshalerOption("text/csv", csvMarshaler)` + response
  handler that rewrites `Content-Disposition` to
  `attachment; filename="<report>-<yyyymmdd>.csv"`.
- Per-user rate limiter (`golang.org/x/time/rate`) in an interceptor:
  keyed by `claims.Username`, 10 qps burst, 30/min sustained. Apply to
  all analytics RPCs (trivial protos stay exempt).
- 429 with `Retry-After` when the bucket is empty.

**Acceptance**:
- `curl '...?format=csv'` returns `Content-Type: text/csv` and a valid
  CSV body.
- Hammer test: 50 concurrent `/summary` calls from one user → some
  return 429; another user sees no throttling.

**Size**: 1 day.

---

### Stage 1.7 — e2e suite 21 + fixture seed

**Goal**: deterministic, machine-checkable regression coverage.

- `test/e2e/fixtures/analytics-seed.sh` — ClickHouse insert script
  that writes a known-quantity set of events (e.g. 10 visitors across
  3 servers, 5 pages, 2 countries, 3 devices, 20 pageviews total,
  timestamps spread over the last 14 days).
- `test/e2e/suites/21-analytics.sh` — calls every analytics RPC and
  asserts golden numbers. Example:
  - `summary?server_id=testsite&from=…&to=…` → visitors=10, pageviews=20.
  - `pages` → top row is `/` with 5 pageviews (or whatever the fixture
    gives).
  - `geography?country=DE` → returns the DE row.
  - `devices` → mobile=6, desktop=4 (matches seed).
  - `realtime` → reflects the latest seed event.
- Re-run all 20 existing e2e suites + this new one; must stay green.
- Integration-harness smoke (from 1.3) stays — faster signal than
  full e2e.

**Acceptance**:
- `./test/e2e/run.sh` prints `=== Results: 70 passed, 0 failed ===`.
- Suite 21 is deterministic: running twice in a row produces the same
  asserted values.
- Seed script is idempotent — re-running it against a dirty DB drops
  the seed rows first.

**Size**: 1.5 days.

---

## 3. Timeline

| Stage | Size     | Cumulative |
|-------|----------|------------|
| 1.1   | 0.5 days | 0.5        |
| 1.2   | 2 days   | 2.5        |
| 1.3   | 1 day    | 3.5        |
| 1.4   | 2 days   | 5.5        |
| 1.5   | 1.5 days | 7          |
| 1.6   | 1 day    | 8          |
| 1.7   | 1.5 days | 9.5        |

Roughly **two calendar weeks** at a sustainable pace, aligned with
PLAN_OUTLINE's "~1.5 weeks" estimate with some slack for the Phase-2
team's proto review (stage 1.1) and any ClickHouse query tuning that
surfaces in 1.4/1.5.

---

## 4. Risks + open items

- **MV registration lag**: if the MV runner has bugs that only surface
  in 1.2, we may need to fall back to raw `events` for all queries
  while the MVs are fixed. Acceptable performance trade-off for the
  Phase-2 UI launch; query builder already handles both paths.
- **Realtime cache invalidation**: the 5s cache in 1.5 could mask a
  stale backend during demos. Document the trade-off and provide a
  `?no_cache=true` gesture for manual testing.
- **Auth provider selection**: analytics RPCs trust the Phase-0 admin
  JWT claims. Non-admin users + per-server ACL will exercise the
  `authware/access` helpers for the first time at scale — expect to
  fix one or two edge cases in ACL intersection logic.
- **BoltDB migration of ACL data**: the phase expects the ACL table to
  already be populated. If Phase 1 lands before any non-admin users
  are provisioned, every ACL-gated test falls back to admin-all-access.
  That's fine for CI; flag as a deployment checklist item.

---

## 5. Sign-off checklist (Phase 1 → Phase 2)

- [ ] All 7 stages landed on `roadmap/phase1`.
- [ ] `go build ./... && go test ./...` passes.
- [ ] `./test/integration/run.sh` → 25/25 (no regressions).
- [ ] `./test/e2e/run.sh` → 70/70 (20 original + suite 21).
- [ ] `hulactl` builds and every admin command still works.
- [ ] `PHASE_1_STATUS.md` written, mirroring PHASE_0_STATUS.md.
- [ ] `UI_PRD.md` §12 updated to match the ACTUAL shipped endpoints
      (any proto field rename from stage 1.1).
- [ ] `MIGRATION_0.md` gets a short Phase-1 addendum covering the MV
      runner wire-up and any ClickHouse migration the ops team must
      run on upgrade.

---

## 6. What happens after Phase 1

Phase 2 (Svelte UI) picks up the analytics JSON surface and builds
dashboards on top. The UI team can start in parallel as soon as
stage 1.3 (Summary + Timeseries) is live — those two endpoints are
enough to scaffold the overview page while the rest of Phase 1 fills
in the report pages.
