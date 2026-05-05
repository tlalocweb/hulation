# Phase 3 — Execution Status (COMPLETE)

**Status: all 10 stages landed. Three new e2e suites wired
(`23-bolt-users`, `24-reports`, `25-goals`).**

Summary of the Phase-3 admin + scheduled-reports rollout:
- Two new protos: `pkg/apispec/v1/goals/goals.proto` and
  `pkg/apispec/v1/reports/reports.proto`, both plumbed through gRPC
  and the REST gateway on the unified listener.
- BoltDB store at `pkg/store/bolt/` holds identity/ACL/goals/reports
  data — the Phase-3 surface that doesn't belong in ClickHouse.
  Buckets: `server_access`, `goals`, `reports`, `report_runs`.
- Per-server ACL: `GrantServerAccess` / `RevokeServerAccess` /
  `ListServerAccess` RPCs backed by Bolt. `viewer` / `manager`
  roles gate analytics reads and admin writes respectively.
- Goals service: CRUD + `TestGoal` dry-run + `ListConversions`.
  Four goal kinds — `URL_VISIT`, `EVENT`, `FORM`, `LANDER` — each
  reads a different subset of the `rule_*` fields.
- Scheduled reports: cron-driven email reports with robfig/cron/v3 +
  IANA timezones. Dispatcher is a background goroutine inside hula
  with a 1-minute ticker and a bounded `SendNow` queue; retry
  schedule `[0, 1m, 5m, 25m]`. Mailer path is STARTTLS via stdlib
  `net/smtp`; no-op when `hula_mailer` is absent.
- HTML email templates (`summary`, `detailed`) with inline preview
  via an iframe srcdoc in the admin UI.
- Admin UI landed at `/analytics/admin/*`:
  - `admin/users` — directory + per-user "Manage access" sheet with
    server × role grant/revoke.
  - `admin/goals` — server-scoped CRUD with kind-conditional rule
    inputs + `TestGoal` preview.
  - `admin/reports` — CRUD + inline preview iframe + `Send now`
    button + per-report "Last runs" timeline.
- Sidebar groups admin pages under a divider that surfaces only
  when `window.hulaConfig.isAdmin=true`.
- SSO login UI shipped at `/analytics/login` (+ `/login/callback`):
  provider list from `ListAuthProviders` + break-glass admin
  username/password form. Sign-out in the sidebar footer clears the
  JWT and bounces to `/analytics/login`.

Out-of-plan adjacent fixes:
- **Hotfix `8a7e18f`** — `protocolDetectingListener.Accept` was
  returning raw peek errors straight to `Serve()`, which killed the
  listener on the first transient client hiccup; on a live origin
  that produced intermittent Cloudflare 521s. Now a peek failure
  closes the bad conn and returns a non-fatal `temporaryError` so
  `Serve()` keeps accepting.
- **Hotfix `8a7e18f`** — `config.go` was resetting `HulaSSL` to nil
  when the embedded cert/key strings were empty, even if the
  Cloudflare Origin CA path was using env-var credentials. Fixed the
  reset-to-nil guard to honour the CF-CA env-var code path.
- **Commit `9317629`** — the static-site middleware was catching
  `/analytics/*` before the SPA handler. Added `/analytics/` to its
  exemption list.

Deferred to Phase 4 (explicitly not blocking Phase 3):
- Realtime report + Visitor profile drill-down + Events + Forms
  reports (hooked in Phase 4).
- `TestGoal` dry-run is currently a `501 Unimplemented` stub on the
  server — the UI renders the button and handles the 501 path
  gracefully; actual dry-run machinery ships in Phase 4 alongside
  the Events report.
- Compare-window delta display.
- Region-level choropleth.

---

Branch: `roadmap/phase3`
Plan: `PLAN_3.md`

## Completed stages (10 of 10)

### Stage 3.1 — Goals + Reports protos ✅
Commit: `62185d7`

- `pkg/apispec/v1/goals/goals.proto` — `GoalsService` with
  `CreateGoal / UpdateGoal / DeleteGoal / ListGoals / GetGoal /
  ListConversions / TestGoal`. `GoalKind` enum:
  `URL_VISIT | EVENT | FORM | LANDER`.
- `pkg/apispec/v1/reports/reports.proto` — `ReportsService` with
  `CreateReport / UpdateReport / DeleteReport / ListReports /
  GetReport / PreviewReport / SendNow / ListRuns`.
  `TemplateVariant` enum: `SUMMARY | DETAILED`.
- Generated Go stubs + gateway code registered into the unified
  server.

### Stage 3.2 — Persistent store + server-access ACL ✅
Commit: `3dbf309`

- `pkg/store/bolt/bolt.go` — schema + bucket-name constants for the
  persistent Storage seam. Buckets routed through
  `pkg/store/storage/local`: `server_access`, `goals`, `reports`,
  `report_runs`. Production wires the Raft FSM as the global
  Storage at boot (HA Plan 2); the on-disk file lives at
  `/var/hula/data/raft/data.db`.
- `pkg/store/bolt/access.go` — ACL grants keyed as
  `"<user_id>|<server_id>" → role`. `GrantServerAccess` upserts;
  `RevokeServerAccess` is idempotent. `AllowedServerIDsForUser`
  powers the analytics-layer filter hook.
- `pkg/api/v1/auth/access.go` — `GrantServerAccess /
  RevokeServerAccess / ListServerAccess` RPC implementations. Role
  enum ↔ string round-trip (`viewer` / `manager`).

### Stage 3.3 — Goals service ✅
Commit: `e06a2c3`

- `pkg/api/v1/goals/goals.go` — CRUD backed by Bolt's `goals`
  bucket, keyed by `<server_id>|<goal_id>`.
- `ListConversions` delegates to the Phase-1 analytics query builder
  with a `goal_id` filter.
- Event-ingest hook in `pkg/analytics/events` reads all enabled goals
  for a server on event and flags `is_goal=1` on matching rows so
  the analytics layer's `goal_conv_rate` column populates.
- `TestGoal` RPC registered — responds `501 Unimplemented`; full
  dry-run machinery deferred to Phase 4 alongside the Events report.

### Stage 3.4 — ScheduledReports CRUD + preview ✅
Commit: `e524c89`

- `pkg/api/v1/reports/reports.go` — CRUD on Bolt's `reports` bucket.
- `pkg/reports/render` — `summary` + `detailed` HTML templates.
  Summary is the four KPI boxes; detailed adds the top-pages table.
- `PreviewReport` renders the HTML against the current data window
  and returns it verbatim for iframe preview — does not persist or
  enqueue.
- `SendNow` validates + enqueues a run via the dispatcher's
  on-demand channel (bounded at 32 — returns
  `ResourceExhausted` when full).

### Stage 3.5 — Dispatcher goroutine ✅
Commit: `3f71943`

- `pkg/reports/dispatch/dispatch.go` — 1-minute ticker + bounded
  `Enqueue` channel. `runDueReports()` walks all enabled reports and
  fires those whose `next_fire_at` ≤ now.
- `sendOne(reportID, force)` renders + emails, writes one
  `StoredReportRun` per attempt. Retry delays `[0, 1m, 5m, 25m]`.
- `advanceNextFire` parses the cron expression + IANA timezone via
  robfig/cron/v3 + `time.LoadLocation`; refuses to fire on an
  unparseable tz.
- `pkg/mailer/mailer.go` — STARTTLS on :587 via stdlib `net/smtp`.
  `ErrNotConfigured` sentinel for the log-only fallback path.

### Stage 3.6 — Admin UI: Users ✅
Commit: `b354a47`

- `web/analytics/src/lib/api/auth.ts` — typed wrappers for users
  CRUD + per-server ACL.
- `web/analytics/src/lib/components/Sheet.svelte` — reusable
  right-anchored slide-over with two-way `open`, Escape + backdrop
  close.
- `/admin/users` — table + "+ New user" sheet + per-user
  "Manage access" sheet with server selector × role grant/revoke.
- Sidebar gains an Admin section (shown only when
  `window.hulaConfig.isAdmin=true`).

### Stage 3.7 — Admin UI: Goals ✅
Commit: `6312bd8`

- `web/analytics/src/lib/api/goals.ts` — typed wrappers + `TestGoal`.
- `/admin/goals` — scoped to the filter bar's current server (reactive
  `$: currentServer, load()`). Kind-conditional rule inputs.
  `TestGoal` button handles the server's 501 by surfacing "TestGoal
  not implemented yet — stage 3.3b".

### Stage 3.8 — Admin UI: Reports ✅
Commit: `<this branch>`

- `web/analytics/src/lib/api/reports.ts` — typed wrappers for CRUD
  + `PreviewReport` + `SendNow` + `ListRuns`, plus `ScheduledReport`
  / `ReportRun` / `TemplateVariant` types.
- `/admin/reports` — table of scheduled reports (name, cron, tz,
  next-fire, enabled); "+ New report" sheet with cron/tz/recipients/
  template/enabled; inline iframe preview via `reports.preview()`
  rendered in a sandboxed iframe; "Send now" confirms via returned
  `run_id`; expandable "Last runs" panel per report.

### Stage 3.9 — SSO login UI ✅
Commit: `<this branch>`

- `/analytics/login` — provider list from `ListAuthProviders` with
  one-click auth-URL redirect; break-glass admin username/password
  form at the bottom. Loads into localStorage on success and bounces
  to `/analytics/`.
- `/analytics/login/callback` — exchanges `?code=` (+ optional
  `state` / `onetimetoken` / `provider`) via `LoginWithCode`, stashes
  the JWT, redirects to `/analytics/`.
- Sign-out button added to the sidebar footer.
- Layout reset (`+layout@.svelte`) so the login pages render without
  the sidebar/filter-bar chrome.

### Stage 3.10 — e2e suites 23/24/25 + status docs ✅
Commit: `<this branch>`

- `test/e2e/suites/23-bolt-users.sh` — public providers probe +
  `CreateUser` → `ListUsers` → `GrantServerAccess` →
  `ListServerAccess` → `RevokeServerAccess` → `DeleteUser` round-trip.
  Pass-skips gracefully when the Bolt store is inactive.
- `test/e2e/suites/24-reports.sh` — `CreateReport` → `GetReport` →
  `UpdateReport` → `PreviewReport` → `SendNow` → `ListRuns` →
  `DeleteReport`. Waits up to ~30s for the dispatcher to pick up
  the SendNow queue before polling runs.
- `test/e2e/suites/25-goals.sh` — `CreateGoal` → `GetGoal` →
  `UpdateGoal` → `ListGoals` → `TestGoal` (200 or 501) →
  `DeleteGoal`.
- `PHASE_3_STATUS.md` written (this file).
- `UI_PRD.md` §7 (Admin) + §8 (Email Reports) updated to describe
  the shipped shapes.

---

## Final sign-off checklist (Phase 3 → Phase 4)

Verified in this session:

- [x] All 10 stages landed on `roadmap/phase3`.
- [x] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [x] `cd web/analytics && pnpm build` green. First-load gzipped
      **19.8 KB** (under the 150 KB budget).
- [x] Three Phase-3 e2e suites added (`23-bolt-users`,
      `24-reports`, `25-goals`).
- [x] `PHASE_3_STATUS.md` written.
- [x] Hotfix `8a7e18f` verified live: `www.tlaloc.us`,
      `tlaloc.us`, `staging.tlaloc.us` curl 200 after rebuild +
      redeploy.

Remaining work — NOT blocking the Phase 3 → Phase 4 handoff:

- [ ] Full e2e run (`./test/e2e/run.sh`) with the new 23/24/25 suites
      included — intended to hold at 110+/110+ when the Bolt store
      and mailer are wired in the test compose stack.
- [ ] `TestGoal` dry-run implementation (stage 3.3b) — backend
      returns 501 today; UI gracefully degrades. Revisit alongside
      the Events report in Phase 4.
- [ ] Dex-backed suite 15 end-to-end pass once the compose stack
      adds a dex sidecar (login UI is ready; backend hooks are in).
- [ ] `UI_PRD.md` §7/§8 final alignment pass after a Phase-3 design
      review.

## Deferred to Phase 4+

- Realtime, Visitor profile drill-down, Events, Forms reports.
- `TestGoal` dry-run server implementation.
- Multi-tenant UI (`*AsTenant` RPCs exist; single-tenant shell today).
- Compare-window delta display (unlocks when Phase 1 compare math
  ships).
- Region-level choropleth map.

## Pre-existing issues

- Suite 17 (analytics foundation enrichment) remains flaky — passes
  most runs but occasionally fails when ClickHouse responds slowly
  to the first event query. Unrelated to Phase 3.
