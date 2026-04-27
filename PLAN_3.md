# Phase 3 — Admin & Scheduled Email Reports (detailed plan)

Phase 2 shipped the read-only analytics dashboard. Phase 3 turns
hula into a fully self-service system: operators provision users,
grant per-server access, define goals, and schedule recurring email
reports. It also unlocks the Phase-1 deferrals that were gated on a
Bolt user store (real ACL enforcement for non-admin users, goals-
aware `FormsReport` body, compare-window deltas).

Related docs: `PLAN_OUTLINE.md` §Phase 3, `UI_PRD.md` §7 (Admin) + §8
(Email Reports), `PHASE_2_STATUS.md`.

---

## 1. Context and scope

### 1.1 What Phase 0–2 already delivered

- **Unified HTTPS listener** with gRPC + REST gateway at `/api/v1/*`.
- **Phase-0 auth** — admin bearer JWT; authware Claims populated on
  both gRPC and REST paths.
- **RBAC skeleton** — `pkg/server/authware/access` (per-server
  RoleAssignment helpers), `pkg/server/authware` (Claims, JWT
  factory, OIDC provider manager). `AuthService` skeleton has
  `ListAuthProviders`, `WhoAmI`, `GetMyPermissions` live; a long
  list of Unimplemented RPCs gated on a Bolt-backed user store.
- **Analytics read API** — 11 RPCs live, CSV export, rate limiting.
- **Svelte UI** — five reports, app shell, URL-state, chart library,
  served at `/analytics/*`.

### 1.2 What Phase 3 ships

- **Bolt user store** — replaces the legacy `users` ClickHouse table.
  Enables every Unimplemented auth RPC (invite, password reset,
  email validation, SetInitialPassword, CheckUserPermission, TotpAdminReset,
  GrantServerAccess family, SetUserSysAdmin).
- **Goals CRUD** — new `GoalsService` proto + REST at
  `/api/v1/goals/*`. Goals are per-server (URL visit / event / form /
  lander). Ingest-time tracking writes to `events` (is_goal flag) +
  an aggregated `mv_conversions` MV.
- **Scheduled Reports CRUD** — new `ReportsService` proto + REST at
  `/api/v1/reports/*`. Preview and send-now endpoints render an
  email HTML locally and optionally push it through the dispatcher.
- **Report dispatcher goroutine** — hula-side. Loads
  `scheduled_reports` on startup, runs a 1-minute ticker, fires the
  next due report, handles per-report timezones, logs each run to
  `report_runs`, retries with exponential backoff up to 3 times.
  SMTP transport via the existing mailer config.
- **Admin UI** — three new Svelte pages:
  - Users (table + CRUD + per-user "Manage access" modal).
  - Goals (per-server CRUD).
  - Scheduled Reports (CRUD + inline preview + Send-now).
- **SSO login UI** — user-facing login page that lists the
  configured OIDC providers (Google / GitHub / Microsoft) plus the
  break-glass admin username/password form.
- **FormsReport body** — now that the goals table carries form-
  submission aggregations, the stub from Phase 1 gets a real body.
- **Summary compare deltas** — Phase-1's `*_delta_pct` fields get
  populated by comparing the current window to the "previous" or
  "previous_year" window.

### 1.3 Out of scope (deferred to Phase 4 / 5)

- Realtime page + Visitor profile drill-down + Events + Forms detail
  reports — Phase 4.
- Threshold alerts ("traffic spiked", "build failed") — Phase 4.
- Mobile app + push notifications — Phase 5a / 5b.
- Custom report builder (drag-drop widgets) — not on the roadmap.

---

## 2. Stage breakdown

Ten stages. Each lands as a single commit on `roadmap/phase3` (new
branch off Phase 2). Every stage must leave `go build ./...` + `pnpm
build` + the integration + e2e suites green.

### Stage 3.1 — Protos for goals + reports

**Goal**: define the two new gRPC services + their messages, add
gateway annotations, regenerate stubs.

- `pkg/apispec/v1/goals/goals.proto`:
  - `GoalsService` with `Create`, `Update`, `Delete`, `List`, `Get`,
    and a read-side `ListConversions(GoalID, Filters)`.
  - Goal types: URL visit (path regex), Event (event_code match),
    Form (form_id), Lander (lander_id).
- `pkg/apispec/v1/reports/reports.proto`:
  - `ReportsService` with `Create`, `Update`, `Delete`, `List`,
    `Get`, `Preview` (returns HTML), `SendNow`, `ListRuns`.
  - ScheduledReport fields: id, name, server_ids, cron, timezone,
    recipients[], template_variant (summary | detailed), filters,
    enabled.
- Permission annotations on every write RPC:
  `(izuma.auth.permission) = { needs: ["server.{server_id}.admin"] }`.
- `make protobuf` regenerates cleanly.

**Acceptance**:
- `go build ./pkg/apispec/...` green.
- Proto files lint-clean (no field-number reuse).

**Size**: 1 day.

---

### Stage 3.2 — Bolt user store + ACL

**Goal**: real user persistence so every Phase-0 `codes.Unimplemented`
auth RPC lights up.

- `pkg/store/bolt/` package (new — not the orphan
  `store/bolt.go` in the repo root). Small embedded BoltDB
  (github.com/etcd-io/bbolt) with buckets: `users`,
  `role_assignments`, `invites`, `password_reset_tokens`,
  `email_validation_tokens`.
- `model/user_bolt.go`: same interface as the legacy ClickHouse
  `model/user.go` so handlers transition cleanly.
- Wire up every previously-Unimplemented RPC: `InviteUser`,
  `ValidateEmail`, `SetInitialPassword`, `ValidatePasswordResetToken`,
  `CheckUserPermission`, `GetUserPermissions` (admin view),
  `TotpAdminReset`, `GrantServerAccess` / `RevokeServerAccess` /
  `ListServerAccess`, `SetUserSysAdmin`.
- Migration: on first boot after this stage, walk existing
  ClickHouse `users` rows + copy into Bolt. Idempotent, tracked in
  a `migrated_users` marker.
- `pkg/server/authware/access.IntersectRequested` now reads real
  ACL grants for non-admin users (currently returns empty for them).

**Acceptance**:
- `go build .` green; the legacy model code is kept as a read-path
  fallback for one release.
- Existing integration + e2e suites: admin-path 107/107 unchanged.
- New `test/e2e/suites/23-bolt-users.sh` creates a non-admin user via
  `InviteUser`, grants server access, calls `WhoAmI` +
  `GetMyPermissions` + the analytics surface and sees real ACL
  intersection.

**Size**: 3 days.

---

### Stage 3.3 — Goals service

**Goal**: goals CRUD + ingest-time tracking + `ListConversions` read
path.

- `pkg/api/v1/goals/goalsimpl.go` — CRUD + List + Get. Stored in
  Bolt (cross-references `server_id`).
- `handler/visitor.go` + `handler/form.go` + `handler/lander.go`:
  after event insertion, run goal matching; set `is_goal = 1` on
  the event when a rule fires; write a row to `conversions` table
  (or use an MV).
- New ClickHouse table `goals_v1` + migration. Matchers are regex
  for URL visit; exact match for event code / form_id / lander_id.
- Phase-1 `Sources.goal_conv_rate` column gets populated via
  existing BuildSources query once the column is live.

**Acceptance**:
- `go test ./pkg/api/v1/goals/...` green.
- e2e suite extends 21-analytics to create a goal, fire a matching
  event, assert `goal_conv_rate` ≥ 0 on Sources.
- `hulactl goals create/list/delete` mirrors the RPCs (minimal, for
  CLI ops use).

**Size**: 2 days.

---

### Stage 3.4 — Scheduled Reports service (CRUD + Preview)

**Goal**: every read-only CRUD RPC for ScheduledReport + an HTML
preview endpoint.

- `pkg/api/v1/reports/reportsimpl.go` — CRUD + List + Get +
  Preview.
- `pkg/reports/render/` — pure-Go HTML renderer using
  `html/template` + inline SVG. Runs an equivalent of the UI's
  Overview page (KPI cards + top-10 line chart) into a self-
  contained email-safe HTML document. Charts are generated with a
  small D3-adjacent Go package (or the simpler `svgo` lib) since
  email clients can't run JS.
- Preview endpoint returns the rendered HTML so the UI's preview
  modal can iframe it.
- `scheduled_reports` + `report_runs` BoltDB buckets.

**Acceptance**:
- Preview an empty report in Storybook-style sandbox; confirm the
  HTML validates against `gomarkdown/html` strict parser + passes
  a basic SVG-in-img-src email check (no remote fetches).
- `ReportsService.SendNow` returns `codes.Unimplemented` (the
  dispatcher lands in 3.5).

**Size**: 3 days.

---

### Stage 3.5 — Report dispatcher goroutine

**Goal**: recurring email delivery.

- `pkg/reports/dispatch/` — long-running goroutine started from
  `server/run_unified.go` alongside badactor + staging startup.
- On boot: load all enabled `scheduled_reports` from Bolt, compute
  `next_fire_at` per report's cron + timezone.
- 1-minute ticker: fire any reports whose `next_fire_at` is in the
  past, render via 3.4's renderer, dispatch via the existing SMTP
  config (`config.Mailer`). Log each to `report_runs` with status
  (success / failed). Exponential back-off 1m / 5m / 25m for up to
  3 retries on transient SMTP errors.
- `ReportsService.SendNow` now enqueues a one-off render + send via
  the same code path.

**Acceptance**:
- Unit tests for the scheduler (cron + timezone math).
- E2E suite 24-reports spins up a mailhog sidecar in the e2e
  compose file, creates a scheduled report with `next_fire_at` in
  the past + a test recipient, and asserts mailhog received the
  HTML body.

**Size**: 3 days.

---

### Stage 3.6 — Admin UI: Users page

**Goal**: SvelteKit `/admin/users` page backed by the Phase-3.2
Bolt store.

- `web/analytics/src/routes/admin/users/+page.svelte` — table of
  users, row-click opens a "Manage access" sheet.
- New API-client helpers for `ListUsers`, `CreateUser`, `GetUser`,
  `PatchUser`, `DeleteUser`, `GrantServerAccess` / `ListServerAccess`.
- Sidebar grows an "Admin" section visible only when
  `window.hulaConfig.isAdmin === true`.

**Acceptance**:
- Admin can add a user, grant server access, revoke it, remove the
  user. All via the UI, no CLI.

**Size**: 2 days.

---

### Stage 3.7 — Admin UI: Goals page

**Goal**: `/admin/goals` — per-server goal CRUD.

- Table + "Create goal" sheet with kind selector (URL / Event /
  Form / Lander) and per-kind configuration.
- "Test rule" button runs the goal against the last 7 days of
  events and shows how many fires it would have had.

**Acceptance**:
- Create → edit → delete a goal end-to-end.
- Test-rule returns a non-zero count against the seed fixture.

**Size**: 1.5 days.

---

### Stage 3.8 — Admin UI: Scheduled Reports page

**Goal**: `/admin/reports` — CRUD + inline preview + Send-now.

- Table + "New report" sheet with cron builder, timezone picker,
  recipients textarea, template-variant dropdown, filters preset
  (from the global FilterBar).
- Inline preview iframe pulls from `ReportsService.Preview`.
- "Send now" button + last-runs timeline per report.

**Acceptance**:
- Create a test report scheduled at `*/5 * * * *` with the admin's
  own email as recipient; preview renders correctly; Send-now
  triggers a mailhog delivery immediately.

**Size**: 2.5 days.

---

### Stage 3.9 — SSO login UI

**Goal**: `/analytics/login` page + OIDC round-trip.

- Landing page lists providers from
  `AuthService.ListAuthProviders`. Clicking a provider starts the
  OIDC flow via `AuthService.LoginOIDC`; the callback lands on
  `/analytics/login/callback` which exchanges the code via
  `AuthService.LoginWithCode`, stores the returned JWT in
  `localStorage`, and redirects to `/analytics/`.
- Break-glass admin username/password form at the bottom of the
  page (`AuthService.LoginAdmin`).
- Existing admin bearer flow continues to work.

**Acceptance**:
- E2E suite 15-sso-google (currently skip-pass without dex) flips to
  a full end-to-end pass when dex is in the compose stack.

**Size**: 2 days.

---

### Stage 3.10 — e2e suite 23/24 + `PHASE_3_STATUS.md`

**Goal**: regression coverage + sign-off document.

- `test/e2e/suites/23-bolt-users.sh` — CRUD through the new user
  RPCs + ACL intersection check against analytics.
- `test/e2e/suites/24-reports.sh` — dispatcher + mailhog probe.
- `test/e2e/suites/25-goals.sh` — goal CRUD + fire + `goal_conv_rate`
  populated.
- `PHASE_3_STATUS.md` mirrors the Phase 0/1/2 status doc.
- `UI_PRD.md` §7 (Admin) + §8 (Email Reports) updated with concrete
  shipped shapes.

**Acceptance**:
- `./test/e2e/run.sh` → 110+/110+ green (3 new suites).
- `PHASE_3_STATUS.md` complete.

**Size**: 2 days.

---

## 3. Timeline

| Stage | Size    | Cumulative |
|-------|---------|------------|
| 3.1   | 1 d     | 1          |
| 3.2   | 3 d     | 4          |
| 3.3   | 2 d     | 6          |
| 3.4   | 3 d     | 9          |
| 3.5   | 3 d     | 12         |
| 3.6   | 2 d     | 14         |
| 3.7   | 1.5 d   | 15.5       |
| 3.8   | 2.5 d   | 18         |
| 3.9   | 2 d     | 20         |
| 3.10  | 2 d     | 22         |

Roughly **4.5 calendar weeks** — heavier than the outline's "~2
weeks" because (a) the Bolt user store migration in 3.2 is gnarly in
practice and (b) the report dispatcher + SVG-for-email renderer in
3.4/3.5 is genuinely new infrastructure.

---

## 4. Risks + open items

- **BoltDB vs. ClickHouse for users**: BoltDB is single-node only.
  If hula ever needs to scale horizontally (not on the roadmap but
  possible), BoltDB is a rewrite. Mitigation: the user store lives
  behind a narrow interface in `pkg/store/bolt` so swapping for
  Postgres / CockroachDB is mechanical.
- **Email template rendering**: D3 charts don't render in email
  clients. Plan: use a small Go SVG generator (svgo + hand-built
  line chart) for email-safe output. Accept that charts look simpler
  than the web UI.
- **Timezone correctness**: cron-plus-timezone is a known hazard
  (DST transitions, leap seconds). Use `cronexpr` or `robfig/cron`
  (pre-vetted) + `time.LoadLocation` with explicit IANA names.
- **Goal ingest performance**: every event ingest now runs a goal
  rule match. Keep the rule set small and in-memory; mutex-guard
  it; recompute on Goals CRUD. Bench on the ingest hot path before
  shipping.
- **SMTP dependency**: e2e suite 24 requires a mailhog sidecar in
  the compose file. Add a `profiles: ["mail"]` service so it's
  opt-in; default e2e runs skip it.

---

## 5. Sign-off checklist (Phase 3 → Phase 4)

- [ ] All 10 stages landed on `roadmap/phase3`.
- [ ] `go build ./...` green (except pre-existing `store/bolt.go`
      orphan).
- [ ] `cd web/analytics && pnpm build` green; first-load ≤ 150 KB.
- [ ] `cd web/analytics && pnpm test` green.
- [ ] `./test/integration/run.sh` green.
- [ ] `./test/e2e/run.sh` ≥ 110/110 (23-bolt-users + 24-reports +
      25-goals).
- [ ] Admin can add a user, grant access, create a goal, schedule a
      report, receive it via mailhog — all through the UI.
- [ ] `PHASE_3_STATUS.md` written.
- [ ] `UI_PRD.md` §7 + §8 aligned with shipped shapes.
- [ ] Branch merged to `main` (or fast-forwardable).

## 6. What happens after Phase 3

Phase 4 adds the remaining four reports (Realtime, Visitor profile,
Events, Forms detail) + threshold alerts. Most chart infra is in
place from Phase 2; the new work is real-time polling, the
Visitor-profile timeline view, and the alert rule engine.
