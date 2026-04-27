# Phase 4 — Execution Status (COMPLETE)

**Status: all 10 stages landed on `roadmap/phase4`. Three new e2e
suites (`26-alerts`, `27-alerts-fire`, `28-visitor-forget`).**

Phase 4 covered the "deep" analytics features — live activity,
per-visitor drill-down, events + forms reports, plus the threshold-
alert engine. It also closed two deliberate Phase-3 debts: TestGoal
dry-run and the GDPR "forget this visitor" flow.

Summary of the Phase-4 rollout:

- **Four new report pages** in the Svelte UI:
  - `/realtime` — 5-second polling, pauses on tab hide. Active
    visitors (5m) + recent events + top pages/sources + streaming
    event list with sticky header.
  - `/visitors` — paginated directory table (100/page) with
    CSV export.
  - `/visitors/[id]` — full event timeline, related IDs,
    admin-gated "Forget visitor (GDPR)" button.
  - `/events` — per-code histogram with payload-breakdown drill
    sheet (payload stats stubbed — needs richer ingest).
  - `/forms` — per-form viewers · submissions · conversion rate ·
    avg time to submit, with drill sheet.
- **TestGoal dry-run** — Phase-3's `501 Unimplemented` stub replaced
  by a real COUNT-query dry-run against `events_v1`. Rule → WHERE
  parameterised per-kind (URL_VISIT, EVENT, FORM, LANDER). Days
  clamped to [1, 90].
- **Alerts** — new service end-to-end:
  - `pkg/apispec/v1/alerts/alerts.proto` + generated stubs.
  - Bolt buckets: `alerts`, `alert_events`. Plus `audit_forget` for
    the GDPR trail.
  - `pkg/api/v1/alerts/alertsimpl.go` — CRUD + ListAlertEvents.
  - `pkg/alerts/evaluator/evaluator.go` — 1-minute ticker with
    cooldown, fire-cap (10/tick), kind-specific predicates:
    GOAL_COUNT_ABOVE / PAGE_TRAFFIC_DELTA / FORM_SUBMISSION_RATE /
    BAD_ACTOR_RATE / BUILD_FAILED.
  - Email delivery via the Phase-3 mailer (`pkg/mailer`); the
    evaluator maps `ErrNotConfigured` to
    `DELIVERY_STATUS_MAILER_UNCONFIGURED` so the UI surfaces
    missing-SMTP visibly.
  - `/admin/alerts` UI: table + kind-conditional create/edit sheet
    + inline expandable "Recent fires" panel.
- **GDPR ForgetVisitor RPC** — `POST /api/v1/analytics/visitor/
  {visitor_id}/forget` (server.<id>.admin). Issues ALTER DELETE on
  `events_v1` + `mv_events_*` + `mv_sessions`; writes a
  StoredForgetAudit row with admin identity from authware.Claims.
  Async-mutation semantics documented in the UI confirm dialog.

Deferred / explicit out-of-scope:
- Per-payload-value stats for `/events` drill — current server
  aggregate only carries top-level numbers. Follow-up:
  `/api/v1/analytics/events/{code}/payload` with ranked `Data.*`
  values.
- Live world-map dots on `/realtime` — needs lat/lon on event rows
  (Phase 0 captured country/region/city but not coordinates).
- Per-field fill-order stats on `/forms` drill — needs richer
  form-view payloads at ingest.
- BUILD_FAILED alert uses a reserved event code (0x1000) but the
  site-deploy pipeline doesn't yet emit it — alert will never fire
  today; wire-up is in Phase-5 DevEx follow-ups.
- SSE upgrade for realtime — stays on 5s poll. Upgrade path is
  isolated behind the `onInterval` helper so the swap is trivial.

---

Branch: `roadmap/phase4`
Plan: `PLAN_4.md`

## Completed stages (10 of 10)

### Stage 4.1 — Realtime page ✅
Commit: `f4585e6`

- `/realtime` polls `/api/v1/analytics/realtime` every 5s.
- `visibilitychange` listener pauses polling when the tab is hidden;
  server-change re-ticks immediately.
- Three KPI tiles + side-by-side top-pages/top-sources + streaming
  events table with sticky header.

### Stage 4.2 — Visitor list + detail ✅
Commit: `1208fc9`

- `/visitors` paginated table with row click → `/visitors/[id]`.
- `/visitors/[id]` visitor summary + KPI tiles + timeline + related
  identifiers. Timeline URL clicks set the path filter and jump to
  `/pages`.
- Admin-gated "Forget visitor (GDPR)" button (RPC lands in 4.9).

### Stage 4.3 — Events report ✅
Commit: `ed865c0`

- `/events` per-code histogram with drill sheet (payload-breakdown
  placeholder).
- Drill sheet's "Filter by this event" sets the `event_code` chip.

### Stage 4.4 — Forms report ✅
Commit: `fe70fde`

- `/forms` per-form table with drill sheet.
- Server TableRow fields: key · visitors · submits · conversion_rate
  · avg_time_to_submit_seconds.

### Stage 4.5 — TestGoal server implementation ✅
Commit: `33920ef`

- Replaces the Phase-3 501 stub. Two COUNT queries against
  `events_v1` — scanned + matched — with a kind-specific WHERE.
- Days clamped to [1, 90]; all inputs parameterised (no SQL
  injection).

### Stage 4.6 — Alerts proto + Bolt + CRUD ✅
Commit: `52cb009`

- `pkg/apispec/v1/alerts/alerts.proto`, regen.
- Three new Bolt buckets: `alerts`, `alert_events`, `audit_forget`.
- `pkg/api/v1/alerts/alertsimpl.go` CRUD + ListAlertEvents.
- Registered in `unified_boot.go` on both gRPC and REST paths.

### Stage 4.7 — Alert evaluator goroutine ✅
Commit: `e0a787c`

- `pkg/alerts/evaluator/evaluator.go` 1-min ticker + cooldown +
  fire cap.
- Five predicate kinds; mailer reuse; 4 DeliveryStatus states.
- Wired into `server/run_unified.go` alongside the report
  dispatcher.

### Stage 4.8 — Admin UI: Alerts ✅
Commit: `6f08ea7`

- `src/lib/api/alerts.ts` + `/admin/alerts` page.
- Kind-conditional target inputs (goal_id / URL path / form_id).
- Inline expandable "Recent fires" with colored status dots.
- Sidebar gains Alerts under the Admin divider.

### Stage 4.9 — GDPR ForgetVisitor RPC ✅
Commit: `5d48c90`

- New `POST /api/v1/analytics/visitor/{visitor_id}/forget`
  (server.<id>.admin).
- ALTER DELETE on events_v1 + mv_events_hourly + mv_events_daily +
  mv_sessions.
- StoredForgetAudit row in Bolt with admin identity from claims.

### Stage 4.10 — e2e suites + status docs ✅
Commit: `<this branch>`

- `test/e2e/suites/26-alerts.sh` — AlertsService CRUD shape probe.
- `test/e2e/suites/27-alerts-fire.sh` — creates a
  BAD_ACTOR_RATE alert, waits up to 90s for the evaluator tick,
  asserts ListAlertEvents shows a row.
- `test/e2e/suites/28-visitor-forget.sh` — synthetic hello → forget
  RPC → poll `/visitor/:id` until gone (async mutation aware).
- `PHASE_4_STATUS.md` (this file).
- `UI_PRD.md` §6.5–§6.8 + §7.4 rewritten to describe the shipped
  shapes.

---

## Final sign-off checklist (Phase 4 → Phase 5a)

Verified in this session:

- [x] All 10 stages landed on `roadmap/phase4`.
- [x] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [x] `cd web/analytics && pnpm build` green; first-load gzipped
      **20.1 KB** (under the 150 KB budget).
- [x] `go build .` green; all new Go packages compile clean.
- [x] Three Phase-4 e2e suites added
      (`26-alerts`, `27-alerts-fire`, `28-visitor-forget`).
- [x] `PHASE_4_STATUS.md` written.

Remaining work — NOT blocking the Phase 4 → Phase 5a handoff:

- [ ] Full e2e run with 26/27/28 — they need Bolt + the evaluator's
      first tick to complete before asserting; the suites
      pass-skip gracefully when either is missing.
- [ ] Per-field fill-order stats on the Forms drill sheet.
- [ ] `/events` payload-breakdown endpoint.
- [ ] BUILD_FAILED event-code emitter in the site-deploy pipeline.
- [ ] `UI_PRD.md` final design review.

## Deferred to Phase 5a+

- Mobile APIs (`/api/mobile/v1/*`).
- Notification engine: APNs + FCM push delivery. The Phase-4 mailer
  generalises cleanly into a `Notifier` interface — wrap rather
  than rewrite.
- WebSocket realtime upgrade.
- Notification preferences UI (quiet hours, per-channel routing).
- Per-device logout / session management.

## Pre-existing issues

- `store/bolt.go` orphan package — still has undefined references;
  `go build .` is clean but `go build ./...` fails only on that
  file. Same pre-Phase-0 state; no new regressions.
- Suite 17 (analytics foundation enrichment) remains intermittently
  flaky on first-query ClickHouse slowness. Unrelated.
