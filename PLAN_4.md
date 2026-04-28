# Phase 4 — Realtime, Visitor Profiles, Alerts (detailed plan)

Phase 3 shipped the admin-write surface + scheduled email reports.
Phase 4 fills in the rest of the UI (realtime, per-visitor drill,
events, forms), lands the threshold-alert engine, and closes the
two deliberate Phase-3 debts: `TestGoal` dry-run and the GDPR
"forget this visitor" flow.

Related docs: `PLAN_OUTLINE.md` §Phase 4, `UI_PRD.md` §6.5–§6.8
(reports) + §7 (Admin), `PHASE_3_STATUS.md`.

---

## 1. Context and scope

### 1.1 What Phase 0–3 already delivered

- **Analytics read API** covers every endpoint Phase 4 needs —
  `/api/v1/analytics/{realtime,visitors,visitor/:id,events,forms}`
  are all live and covered by suite 21. Phase 4 work is **UI wiring
  + write-side + alert engine**; no new read RPCs needed except a
  small one for the Events report's code histogram.
- **ClickHouse schema** — `events` table has `is_goal`, `session_id`,
  UA fields, city/region, UTM fields, channel, goal_id. Materialized
  views `mv_events_hourly`, `mv_events_daily`, `mv_sessions` are
  live.
- **Bolt store** — `pkg/store/bolt/` has `server_access`, `goals`,
  `reports`, `report_runs` buckets. Phase 4 adds two more:
  `alerts`, `alert_events`.
- **Dispatcher + mailer** — `pkg/reports/dispatch` + `pkg/mailer`
  already have the ticker, retry schedule, and delivery path. The
  alert engine reuses the same mailer.
- **Admin UI shell** — sidebar admin section, Sheet component, Phase 3
  pages (`/admin/users`, `/admin/goals`, `/admin/reports`). Phase 4
  adds `/admin/alerts`.

### 1.2 What Phase 4 must deliver

**Reports (UI only — backends exist):**
- Realtime page with 5-second polling; world map of recent
  pageviews; active visitors counter; streaming event list.
- Visitor detail page (`/visitors/[id]`) with full event timeline,
  aliases, and admin-gated "GDPR forget" button.
- Events report — per-code histogram + drill-down into payload.
- Forms report — submission counts, conversion rate, time-to-submit.
- Visitors table page (drill from row click lands on the visitor
  detail page from above).

**Alerts (new surface, end-to-end):**
- Protos: `AlertsService` with CRUD + enable/disable + fire-history.
- Bolt persistence (`alerts`, `alert_events` buckets) + alert rule
  evaluator goroutine.
- Rule kinds: `GOAL_COUNT_ABOVE`, `PAGE_TRAFFIC_DELTA`,
  `FORM_SUBMISSION_RATE`, `BAD_ACTOR_RATE`, `BUILD_FAILED`.
- Email delivery only (push comes in Phase 5a).
- Admin UI page `/admin/alerts` — CRUD + recent fires timeline.

**Debts closed:**
- `TestGoal` dry-run — real server implementation. Replaces the
  `501 Unimplemented` stub; admin UI already renders the result.
- GDPR visitor-forget flow — admin-gated RPC that deletes rows from
  `events`, `mv_*` aggregates, and the visitor entry.

**Out of scope (deferred to Phase 5a/5b):**
- Mobile APIs, push notifications, mobile app.
- SSE upgrade for realtime (stays on 5s polling in Phase 4).
- Web push from the browser.
- Lighthouse audit publication.

---

## 2. Stage breakdown

Each stage is a standalone PR-size slice that lands behind its own
commit. Stages 4.2–4.4 and 4.6 are independent and can be parallelised
across two contributors.

### Stage 4.1 — Realtime page

**Goal**: wire `/api/v1/analytics/realtime` into a Svelte page.

- `src/routes/realtime/+page.svelte` — polls every 5s via a small
  `onInterval` helper that pauses when the tab is hidden.
- KPI row: active visitors (last 5m), pageviews/min, recent form
  submissions.
- Mini world map (reuse `ChoroplethMap` with a thin-data layer for
  recent dots — dots fade over 60s).
- Streaming events table (last 50) — replaces in place; no new rows
  flash (WCAG 2.1 reduced-motion friendly).
- Sidebar entry above "Pages" (matches PRD ordering).

**Acceptance**:
- Page renders populated numbers against suite-21 seed.
- Polling pauses on tab hide; resumes on focus.
- No console errors over a 60s continuous poll.

**Size**: 1.5 days.

---

### Stage 4.2 — Visitor list + Visitor detail

**Goal**: `/visitors` table + `/visitors/[id]` drill.

- `/visitors/+page.svelte` — paginated table (visitor id, first seen,
  last seen, sessions, events, country, device). Row click →
  `/visitors/[id]`. CSV export.
- `/visitors/[id]/+page.svelte` — header with first-seen / last-seen
  / country / device; event timeline grouped by session; related
  aliases list (other visitor IDs sharing IP + UA fingerprint).
- Admin-only "Forget visitor (GDPR)" button (stage 4.10 ships the
  RPC; the UI button is gated on `window.hulaConfig.isAdmin=true`).
- Timeline items link to the Pages report filtered to the path, and
  to the Events report filtered to the code.

**Acceptance**:
- `/visitors` paginates 100 rows/page, sorts on `last_seen DESC`.
- `/visitors/[id]` renders the Phase-1 fixture visitor with its
  4-event timeline.
- "Forget visitor" is hidden for non-admin sessions.

**Size**: 2 days.

---

### Stage 4.3 — Events report

**Goal**: `/events` page.

- Aggregate table: event code · label · count · unique visitors · %
  of total. Reuses `ReportTable`.
- Row click → drill sheet showing top payload values (JSON-flat
  `Data.*` keys ranked by frequency) + a Sparkline of the code's
  daily count over the filter window.
- Filter chip click on a payload value narrows the main view.

**Acceptance**:
- `EventsReport` in suite-21 asserts the code histogram renders.
- Drill sheet opens via the existing `Sheet.svelte` component.

**Size**: 1.5 days.

---

### Stage 4.4 — Forms report

**Goal**: `/forms` page.

- Table: form id · submissions · conversion rate (unique visitors
  who submitted / unique visitors who viewed) · median time-to-submit.
- Drill (sheet): recent submissions list + field-fill-order chart
  (deferred — stub a placeholder in the sheet if the underlying
  per-field data isn't on the event row yet).
- CSV export.

**Acceptance**:
- Renders against the forms-test fixture rows from suite 04.
- Conversion-rate column reads the same numerator the goals
  summary row uses (no double-count).

**Size**: 1.5 days.

---

### Stage 4.5 — `TestGoal` dry-run implementation

**Goal**: close the Phase-3 debt. Replace the 501 stub with a real
dry-run against the last N days.

- `pkg/api/v1/goals/goals.go:TestGoal` — builds the same match
  predicate the ingest-path goal evaluator uses, queries ClickHouse
  for events in the requested window, counts matches without
  writing anything.
- Admin UI already renders `{would_fire, scanned_events}` — no UI
  changes needed once the 501 goes away.

**Acceptance**:
- Suite 25's TestGoal probe flips from a 501-pass-skip to a 200.
- Returned `would_fire` matches a manual ClickHouse count.

**Size**: 1 day.

---

### Stage 4.6 — Alerts protos + Bolt storage

**Goal**: wire up the alert surface.

- `pkg/apispec/v1/alerts/alerts.proto` — `AlertsService` with:
  - `CreateAlert / UpdateAlert / DeleteAlert / ListAlerts / GetAlert`
  - `ListAlertEvents` (recent fires, paginated)
- `AlertKind` enum: `GOAL_COUNT_ABOVE`, `PAGE_TRAFFIC_DELTA`,
  `FORM_SUBMISSION_RATE`, `BAD_ACTOR_RATE`, `BUILD_FAILED`.
- `Alert` message: id, server_id, name, kind, enabled, threshold
  (float64), window_minutes, target_* (goal_id / path / form_id),
  recipients[], cooldown_minutes.
- `AlertEvent` message: id, alert_id, fired_at, observed_value,
  threshold, recipients, delivery_status.
- `pkg/store/bolt/alerts.go` — two buckets (`alerts`, `alert_events`).
- `pkg/api/v1/alerts/alertsimpl.go` — CRUD skeleton (evaluator ships
  in 4.7).

**Acceptance**:
- `go test ./pkg/store/bolt/... ./pkg/api/v1/alerts/...` passes.
- REST gateway exposes `/api/v1/alerts/{server_id}` etc; suite 26
  asserts CRUD shape (happy path).

**Size**: 1.5 days.

---

### Stage 4.7 — Alert rule evaluator goroutine

**Goal**: threshold evaluator that fires alerts and emails recipients.

- `pkg/alerts/evaluator/evaluator.go` — 1-minute ticker. On each
  tick, walks all enabled alerts and calls the kind-specific
  predicate:
  - `GOAL_COUNT_ABOVE` — count of goal conversions in last
    `window_minutes` > `threshold`.
  - `PAGE_TRAFFIC_DELTA` — current window vs same window last week;
    fire when delta > threshold (or < -threshold).
  - `FORM_SUBMISSION_RATE` — submissions/min exceeds a baseline;
    designed to catch spam bursts.
  - `BAD_ACTOR_RATE` — rate of bad-actor hits/min over threshold.
  - `BUILD_FAILED` — any `build_failed` event observed in the
    window.
- Cooldown: if the same alert fired in the last `cooldown_minutes`,
  skip. Prevents alert storms.
- On fire: write an `AlertEvent` row; hand the rendered email to
  `pkg/mailer` (reused from Phase 3).
- `server/run_unified.go` starts the evaluator alongside the report
  dispatcher.

**Acceptance**:
- Unit test in `pkg/alerts/evaluator/` seeds ClickHouse with a
  known spike and asserts the evaluator fires exactly once (then
  respects cooldown).
- Suite 27 end-to-end: create a `GOAL_COUNT_ABOVE` alert with a
  low threshold, generate traffic that trips it, assert an
  `AlertEvent` row appears in ListAlertEvents.

**Size**: 3 days.

---

### Stage 4.8 — Admin UI: Alerts page

**Goal**: `/admin/alerts` page.

- `src/lib/api/alerts.ts` — typed wrappers.
- Table: name · kind · threshold · window · enabled · last fired.
- "+ New alert" sheet with kind-conditional fields (matching the
  pattern in `/admin/goals`).
- Per-row expandable "Recent fires" timeline (last 25 `AlertEvent`
  rows).
- Sidebar gains an "Alerts" entry under the Admin divider.

**Acceptance**:
- `pnpm check` clean; bundle stays under 150 KB first-load budget.
- Alert CRUD round-trips with suite 27 seed.

**Size**: 1.5 days.

---

### Stage 4.9 — GDPR visitor-forget RPC

**Goal**: admin-gated "forget this visitor" flow.

- `pkg/apispec/v1/analytics/analytics.proto` — add
  `ForgetVisitor(ForgetVisitorRequest) returns (ForgetVisitorResponse)`
  (POST `/api/v1/analytics/visitor/{visitor_id}/forget`; permission
  `server.{server_id}.admin`).
- Implementation deletes from `events` (ALTER TABLE … DELETE WHERE
  visitor_id=X, respecting the mutation semantics of ClickHouse),
  clears `mv_*` rows for the visitor, and writes an audit row to a
  new `audit_forget` bucket in Bolt (timestamp, admin_user, target
  visitor) for compliance.
- Wire the UI button from stage 4.2 to this endpoint with a confirm
  dialog.

**Acceptance**:
- Suite 28 asserts: visitor exists → call ForgetVisitor → visitor
  absent from `ListVisitors` + `VisitorDetail` → audit row
  present.

**Size**: 1.5 days.

---

### Stage 4.10 — e2e suites 26/27/28 + `PHASE_4_STATUS.md`

**Goal**: regression coverage + sign-off document.

- `test/e2e/suites/26-alerts.sh` — AlertsService CRUD + enable/
  disable + ListAlertEvents shape.
- `test/e2e/suites/27-alerts-fire.sh` — seed traffic → evaluator
  fires → AlertEvent row + email send-attempt recorded.
- `test/e2e/suites/28-visitor-forget.sh` — visitor present →
  ForgetVisitor → visitor gone + audit row.
- `PHASE_4_STATUS.md` — mirrors the Phase-3 status doc.
- `UI_PRD.md` §6.5–§6.8 (Events/Forms/Visitors/Realtime) + §7.4
  (Alerts admin, new subsection) updated to shipped shapes.

**Acceptance**:
- `./test/e2e/run.sh` → 113+/113+ green (3 new suites).
- `PHASE_4_STATUS.md` complete.

**Size**: 1.5 days.

---

## 3. Timeline

| Stage | Size    | Cumulative |
|-------|---------|------------|
| 4.1   | 1.5 d   | 1.5        |
| 4.2   | 2 d     | 3.5        |
| 4.3   | 1.5 d   | 5          |
| 4.4   | 1.5 d   | 6.5        |
| 4.5   | 1 d     | 7.5        |
| 4.6   | 1.5 d   | 9          |
| 4.7   | 3 d     | 12         |
| 4.8   | 1.5 d   | 13.5       |
| 4.9   | 1.5 d   | 15         |
| 4.10  | 1.5 d   | 16.5       |

Total: **16.5 working days** (≈ 3.3 weeks). Exceeds the 2-week
outline estimate; the extra 1.3 weeks covers the alert evaluator
complexity (cooldown/window/delta math) and the three new e2e suites.
A two-contributor split (one on 4.1–4.5, one on 4.6–4.8) compresses
calendar time to ~2 weeks.

---

## 4. Risks + open items

- **Delta-window math for `PAGE_TRAFFIC_DELTA`** — comparing the
  current window to "same window last week" needs to work across DST
  boundaries. Use `time.Time` + IANA tz, not wall-clock arithmetic.
- **ClickHouse DELETE for GDPR forget** — ALTER TABLE DELETE is a
  mutation (async). Need to verify rows are actually gone from
  reads by the time the RPC returns, or change the API to return
  "scheduled; poll" semantics. Decision in stage 4.9 design.
- **Alert email rate**: a sudden ClickHouse slowness could trigger
  every alert on the same tick. Safeguard: evaluator caps total
  fires per tick at 10 and logs-warn on throttle.
- **Realtime polling overhead**: 5s poll × many admin tabs × heavy
  realtime query. If suite-17 style slowness recurs, drop to 15s
  poll with a visible "updating" indicator.

---

## 5. Sign-off checklist (Phase 4 → Phase 5a)

- [ ] All 10 stages landed on `roadmap/phase4`.
- [ ] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [ ] `cd web/analytics && pnpm build` green; first-load gzipped
      under 150 KB.
- [ ] `./test/e2e/run.sh` → 113+/113+ green.
- [ ] `PHASE_4_STATUS.md` written.
- [ ] `UI_PRD.md` §6.5–§6.8 + §7.4 describe shipped shapes.

---

## 6. What happens after Phase 4

Phase 5a ("Mobile APIs + Notification engine") takes the Phase 4
alert events as the notification source. The `pkg/mailer` delivery
path generalises to a `pkg/notifier` that fans out to email + push
(APNs/FCM). No Phase 4 code changes for 5a — the mailer interface
stays as is; the notifier layer wraps it.
