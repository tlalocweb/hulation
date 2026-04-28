# Hula Analytics — Plan Outline

Roadmap for building out the analytics product defined in `UI_PRD.md`.
Each phase is a shippable milestone; downstream phases have hard
dependencies on upstream ones.

**This document is the outline only.** Each phase will be expanded into
its own detailed plan document before work starts on that phase. Anything
below is intentionally summary-level.

Stack baseline (reused across all phases):

- **Backend**: existing hula (Go), ClickHouse, OPA auth
- **Web UI**: Svelte + SvelteKit, shadcn-svelte component look
- **Charts**: D3.js (custom components; not a wrapper library)
- **Mobile**: decided in Phase 5 prep (recommendation: native iOS/Android
  with a Kotlin-Multiplatform shared client, or Flutter if single-codebase is
  preferred; locked in at the start of Phase 5b)

---

## Phase 0 — Foundations (gRPC/RBAC/SSO + Data Foundation)

**See `PLAN_0.md` for the detailed plan.** The scope grew beyond the original
outline — Phase 0 now includes a major architectural migration in addition
to the data-foundation work:

1. Adopt izcr's RBAC and SSO code by copy-and-adapt (never import from izcr).
2. Transition every admin API (except WebDAV and visitor-tracking) to
   gRPC + grpc-gateway, with permission requirements declared inline as
   proto annotations.
3. Enrich visitor events and stand up materialized views so later phases
   have rich, queryable history.

**Why first**: the UI, mobile app, and analytics APIs in later phases all
sit on top of the RBAC/SSO model and gRPC apispec landed here. Event
enrichment also needs to land now so existing traffic starts accumulating
in the richer schema immediately — by the time the UI ships, there's real
history to display.

**In scope (summary — full detail in PLAN_0.md)**

Architectural migration:

- Adopt the proto toolchain from izcr (`protoc` + plugins; `make protobuf`).
- Copy izcr's `apiobjects`, `authware`, `auth provider`, and unified
  (gRPC + REST gateway) server into `pkg/...`, renamed to Hula's import path.
- Register Google, GitHub, and Microsoft as OIDC providers; Hula's local
  admin (`root`) user stays as username+password+TOTP break-glass.
- Define `pkg/apispec/v1/` protos for every admin API (auth, forms,
  landers, site, staging-build, badactor, status, analytics stubs) with
  inline permission annotations.
- Port Fiber handlers to gRPC service implementations; remove legacy
  `wrapWithOpa` per-endpoint wrappers. REST clients reach the same
  methods via the grpc-gateway at `/api/v1/*`.
- Migrate `hulactl` to generated gRPC clients.
- **WebDAV (`staging-update`, `staging-mount`, PATCH variants) and
  visitor-tracking endpoints (`/v/*`, `/scripts/*`) stay on Fiber** —
  they keep their existing HTTP shape.

Analytics data foundation:

- Capture `Referer` header; parse into channel (Direct/Search/Social/
  Referral/Email) and referrer host.
- UA-parse events at ingest (uap-go): browser, browser version, OS, OS
  version, device category.
- Extend the `ipinfo` cache usage to persist region and city onto events.
- Derive and store `session_id` per event (30-minute inactivity boundary).
- Parse UTM params from landing URLs.
- Define explicit `events` DDL (drop GORM AutoMigrate for this table);
  migrate old rows into the new schema.
- Materialized views: `mv_events_hourly`, `mv_events_daily`, `mv_sessions`.
- Config-driven TTL on raw `events` (default 395 days).
- Add `user_server_access` table; populate `AllowedServers` on the JWT
  claim so Phase 1 analytics APIs can enforce per-server ACLs.

**Out of scope**

- Any UI.
- Analytics query APIs (Phase 1).
- Goals, scheduled reports, notifications.

**Key deliverables**

- `pkg/apispec/v1/**/*.proto` with generated Go + gRPC + gateway code.
- `pkg/server/authware/**` with permission resolver and OPA policy.
- `pkg/server/unified/` — single HTTPS listener for gRPC + REST +
  Fiber-passthrough (WebDAV / visitor / static).
- Google/GitHub/Microsoft SSO working end-to-end in the e2e harness.
- New e2e suites: `13-grpc-smoke`, `14-rest-gateway`, `15-sso-google`,
  `16-rbac`, `17-analytics-foundation`, `18-events-migration`.
- Explicit `events_v1.sql` DDL + migration runner for schema changes.
- MV definitions as versioned SQL files.

**Dependencies**: none.

**Size estimate**: ~6 calendar weeks (28–34 working days) — see PLAN_0.md
for the stage-by-stage breakdown and PR-granularity checkpoints.

---

## Phase 1 — Analytics API

**Goal**: expose every query the UI will need as JSON endpoints, with ACL
enforcement and date-range/filter composition.

**Why second**: the UI is going to be a thin client over these APIs. Shape
the contract first and test it against fixture data; UI development is much
smoother when the APIs are stable.

**In scope**

- All read endpoints in PRD §11 implemented behind OPA auth:
  - `/api/analytics/summary`
  - `/api/analytics/timeseries`
  - `/api/analytics/{pages,sources,geography,devices,events,forms}`
  - `/api/analytics/visitors` + `/api/analytics/visitor/:id`
  - `/api/analytics/realtime`
- Query-layer component that picks source table automatically (raw `events`
  vs `mv_events_hourly` vs `mv_events_daily`) based on range and granularity.
- Filter composition: every endpoint accepts the same set of filter params
  (server, date range, path, country, device, source, event code, goal).
  Filters compose AND-wise.
- Server ACL enforcement: JWT → user ID → intersect requested `serverIds[]`
  with allowed servers; silently drop unauthorized.
- Rate-limit heavy queries per user (prevent scan-the-world requests).
- CSV export endpoint: any table query can set `?format=csv`.
- Fixture-based integration tests (extend the e2e harness with a new
  `13-analytics.sh` suite that seeds events and asserts query results).

**Out of scope**

- Write endpoints (goals CRUD, reports CRUD) — those come in Phase 3.
- Any UI.

**Key deliverables**

- New `handler/analytics/*.go` package (or split per-report handler files).
- Query builder that emits ClickHouse SQL against the right source table.
- Test fixtures: a ClickHouse seed script that inserts known events;
  golden-file assertions for API outputs.
- OpenAPI 3 spec (or a Go struct-based equivalent) so the UI team has a
  stable contract.

**Dependencies**: Phase 0 (needs the enriched schema, MVs, ACL table).

**Size estimate**: ~1.5 weeks.

---

## Phase 2 — Web UI Core

**Goal**: a working dashboard that a site owner uses daily. Ships Overview +
the four highest-value reports. Establishes all UI conventions.

**In scope**

- **Framework**: SvelteKit project under `web/analytics/`. Built to static
  assets served from `/analytics/` by hula (OPA-protected route).
- **Components**: shadcn-svelte look — cards, tables, dropdowns, dialog,
  sheet, command-palette. Copy the look, not necessarily the exact library —
  we'll vendor the components we actually use.
- **Charts**: D3.js-based, written as Svelte components:
  - `<LineChart>` — timeline, multi-series.
  - `<StackedBar>` — top-N over time.
  - `<Donut>` — device categories, OS.
  - `<ChoroplethMap>` — world/country maps.
  - `<Sparkline>` — KPI cards.
  - `<Heatmap>` (optional, nice to have).
- **Layout & navigation**: left sidebar (collapsible), sticky global filter
  bar with server selector, date-range picker, compare toggle, and filter
  chips.
- **Shareable state**: every filter combo is mirrored to the URL query
  string; the Svelte store hydrates from URL on load.
- **Reports shipped in this phase**:
  - Overview
  - Pages
  - Sources
  - Geography
  - Devices
- **Accessibility**: keyboard navigation on the filter bar, date picker, and
  tables; semantic landmarks; color-blind-safe palette as default.
- **Dark mode** toggle (shadcn gets this for free).
- **Target bundle size**: < 150 KB gzipped for initial load.

**Out of scope**

- Admin pages (users, goals, scheduled reports) — Phase 3.
- Realtime, Visitor profile drill-down, Events, Forms — Phase 4.

**Key deliverables**

- `web/analytics/` SvelteKit project that builds to static assets.
- A set of reusable D3-powered chart components with a clear API.
- Integration with hula: static assets bundled into `hula:local` image,
  served at `/analytics/*`, SPA routing handled by a catch-all.
- Screenshot-based visual regression test in the e2e harness (optional but
  nice — Playwright against the local stack).

**Dependencies**: Phase 1 APIs are stable.

**Size estimate**: ~3 weeks.

---

## Phase 3 — Admin & Scheduled Email Reports

**Goal**: Hula operators can provision users, grant per-server access, define
goals, and schedule recurring email reports.

**In scope**

- **Write endpoints** for:
  - `POST/PATCH/DELETE /api/analytics/goals/*`
  - `POST/PATCH/DELETE /api/analytics/reports/*`
  - `POST /api/analytics/access/*` (grant / revoke server access)
  - `POST /api/analytics/reports/:id/preview` (render email HTML)
  - `POST /api/analytics/reports/:id/send-now`
- **Admin pages** in the Svelte UI:
  - **Users** — table, CRUD, per-user "Manage access" modal.
  - **Goals** — per-server goal CRUD (URL visit / event / form / lander).
  - **Scheduled Reports** — CRUD + inline preview + send-now.
- **Report dispatcher** — goroutine in hula that:
  - Loads `scheduled_reports` on startup.
  - Runs a 1-minute ticker, fires reports whose `next_fire_at` has elapsed.
  - Handles timezones correctly (per-report tz).
  - Renders HTML via go `html/template`; inlines SVG charts via our own
    D3-in-headless-browser path or a simpler SVG generator.
  - Sends via existing SMTP/mailgun config.
  - Logs each run to `report_runs`; retries with exponential backoff up to 3
    times on failure.
- **Goal tracking** — on every event ingest, check against configured goals;
  record a conversion row (may be a materialized view).

**Out of scope**

- Mobile push notifications (Phase 5a).
- Alert-style notifications ("traffic spiked", "build failed") — those live
  in Phase 4 (threshold alerts) or Phase 5a (delivery via push).

**Key deliverables**

- Admin UI pages (Users, Goals, Reports).
- Working email dispatcher with retry + timezone handling.
- Email HTML templates: summary and detailed variants.
- New tables: `goals`, `scheduled_reports`, `report_runs`.

**Dependencies**: Phase 2 (UI scaffold and components).

**Size estimate**: ~2 weeks.

---

## Phase 4 — Realtime, Visitor Profiles, Alerts

**Goal**: the "deep" analytics features — live activity, per-visitor
drill-down, events and forms reports, plus in-product alerts.

**In scope**

- **Realtime view** — Svelte page that:
  - Polls `/api/analytics/realtime` every 5s (or upgrades to SSE — decide
    during implementation based on effort).
  - Live world map of recent pageviews (D3-animated dots).
  - Streaming list of recent events.
  - Active visitors counter.
- **Visitor profile page** — click any visitor row → drill-down page with:
  - Full event timeline (each event: time, URL, referrer, IP, country,
    device).
  - Related IPs, cookies, aliases.
  - "GDPR forget" button (admin only) — permanently deletes visitor and
    all their events.
- **Events report** — per event code and per `Data` payload; drill-down
  histograms.
- **Forms report** — per-form conversion rate, submission counts,
  time-to-submit.
- **Threshold alerts** — "notify me when":
  - A goal's daily conversion count crosses a threshold.
  - Traffic to a page drops/rises > X% vs the same day last week.
  - A form starts getting submissions it didn't used to (spam-like).
  - The build endpoint fails.
  - The bad-actor rate spikes above X/min.
  - Alerts are stored in an `alerts` table; fired alerts are written to an
    `alert_events` table (polling basis, 1-minute tick).
  - Delivery channels in this phase: **email only**. (Push notifications
    come in Phase 5a.)
- **CSV export** shipped on every table (if not already done in Phase 2).

**Out of scope**

- Push notifications (deferred to Phase 5a).
- Mobile UI (Phase 5b).

**Key deliverables**

- Realtime + Visitor + Events + Forms reports in the UI.
- `alerts` table, alert-rule engine, email delivery.
- Visitor-delete flow (full removal from `visitors`, `events`, MVs).

**Dependencies**: Phase 2 (UI components), Phase 3 (dispatcher reused for
alert emails).

**Size estimate**: ~2 weeks.

---

## Phase 5a — Mobile APIs & Notification Engine

**Goal**: surface everything the mobile app needs as a stable API, and build
a cross-channel notification engine (push + email) so the Phase 4 alerts can
reach a phone.

**In scope**

- **Mobile-tuned APIs**:
  - `POST /api/mobile/register` — device registration (APNs token for iOS,
    FCM token for Android), linked to the authenticated user.
  - `POST /api/mobile/unregister` — on logout or uninstall.
  - `GET /api/mobile/summary` — compact summary payload (fewer fields,
    smaller charts, pre-rendered sparkline data).
  - `GET /api/mobile/timeseries?compact=true` — bucketized down to what
    fits a phone screen.
  - Endpoint contract explicitly versioned (e.g., `/api/mobile/v1/...`) so
    we can evolve without breaking installed apps.
- **Auth flow**:
  - Mobile users authenticate via `/api/auth/login` (same as web).
  - TOTP supported (same flow).
  - Refresh-token pattern or long-lived JWT (decide during phase).
- **Push notification service**:
  - Supports both **APNs** (Apple) and **FCM** (Google/Firebase).
  - Abstract `Notifier` interface with APNs and FCM implementations.
  - Integrates with the Phase 4 alert engine: when an alert fires, look up
    all devices registered to recipients, fan out push notifications via
    the appropriate provider.
  - Delivery tracking: `notification_sends` table with outcome per send.
  - Dead-device handling: if APNs/FCM reports a bad token, mark device
    unregistered and stop sending.
- **Realtime channel**:
  - WebSocket endpoint `/api/mobile/v1/events` (authenticated) that streams
    live events to subscribed mobile clients — used for the "activity feed"
    page on the phone.
  - Optionally a lighter polling endpoint as a fallback for clients on
    bad networks.
- **Notification preferences**:
  - Per-user preferences UI in the web admin: which alert types deliver
    via push vs email vs both, quiet hours, timezone.
  - Stored in `notification_prefs` table.
- **Security**:
  - Device tokens are treated as secrets (never logged, stored encrypted
    at rest using hula's existing TOTP encryption key mechanism).
  - Sessions per device so we can "log out of this device" remotely.

**Out of scope**

- The mobile app itself (Phase 5b).
- Web push (browser push notifications) — can be a later add-on that
  reuses the same dispatcher interface.

**Key deliverables**

- `notifier` package with APNs and FCM backends.
- New tables: `mobile_devices`, `notification_sends`, `notification_prefs`.
- `/api/mobile/v1/*` endpoints with fixture-based tests.
- Admin UI for notification preferences.
- Documented API contract (OpenAPI or equivalent).

**Dependencies**: Phase 4 (alert engine fires the events we deliver).

**Size estimate**: ~2 weeks.

---

## Phase 5b — Mobile App

**Goal**: native(ish) mobile app that an operator uses to check analytics on
the go and receive push alerts.

**Tech choice (locked in at start of phase)**:

- **Recommended**: **React Native** with TypeScript.
  - Single codebase for iOS + Android.
  - Large component ecosystem (including charting libs).
  - Easy to share TS types with the web UI's analytics API types.
- **Alternative**: **Flutter** (Dart) — one codebase, beautiful animations,
  native-feeling. Better performance than RN in some cases.
- **Alternative**: **Native** (Swift + Kotlin) with a shared Kotlin-
  Multiplatform networking layer. Best fidelity, double the engineering time.

Decision criteria: team's existing expertise and whether we want to reuse
our D3 chart components (RN makes this painful; Flutter has its own charts;
native is the hardest to share).

**In scope**

- **App features**:
  - Login / TOTP flow against hula API.
  - Server picker (same ACL model as web).
  - Dashboard: KPI cards, timeline sparkline, top pages/sources/countries.
  - Realtime feed: streaming pageviews and events.
  - Alerts inbox: list of past alerts with deep-link to the affected report.
  - Push-notification landing: tapping a push opens the relevant report
    pre-filtered.
  - Goal and scheduled-report viewing (config is web-only).
- **Push notifications**:
  - APNs on iOS (configure app bundle ID + provisioning).
  - FCM on Android (Firebase project + service account).
  - Deep-link routes so `open hula://alert/123` navigates to the alert.
- **Offline-friendly**:
  - Cache last-fetched summary so opening the app shows something
    immediately even on flaky networks.
  - Retry queue for auth-related requests.
- **Distribution**:
  - TestFlight (iOS) and Firebase App Distribution or internal APK
    (Android) for beta testers.
  - Public App Store / Play Store publishing is a separate follow-up
    decision after beta.

**Out of scope**

- Editing goals, reports, or access rules from the app (web-only in v1).
- Complex drill-down reports (web is the deep-dive surface).
- Widget / Shortcuts / Wear OS integrations.

**Key deliverables**

- React Native (or chosen stack) app with ≥ 5 reviewed core screens:
  Login, Dashboard, Report detail, Realtime, Alerts.
- Signed iOS + Android builds distributed via beta channels.
- Push notification integration (E2E from hula alert → phone lockscreen).
- App privacy / store listing drafts (not submission — that's later).

**Dependencies**: Phase 5a (APIs + notification engine).

**Size estimate**: ~4 weeks.

---

## Timeline Summary

| Phase | What | Size | Runs in parallel with |
|-------|------|------|------------------------|
| 0 | Foundations (gRPC/RBAC/SSO + data enrichment) | 6 wk | — |
| 1 | Analytics API | 1.5 wk | — |
| 2 | Web UI core | 3 wk | (design prep for 3 can run late-in-2) |
| 3 | Admin + email reports | 2 wk | — |
| 4 | Realtime + drill-downs + alerts | 2 wk | — |
| 5a | Mobile APIs + notifications | 2 wk | (app design prep for 5b) |
| 5b | Mobile app | 4 wk | — |

Strict serial path total: ~20.5 weeks. Calendar time reduces if designers
and mobile engineers can work ahead in parallel with backend phases.

## Cross-cutting concerns (addressed in every phase)

- **Multi-tenancy / ACL** — every phase checks server access on both read
  and write.
- **e2e test coverage** — each phase adds a suite under `test/e2e/suites/`.
- **Docs** — each phase updates `DEPLOYMENT.md` and `test/ABOUT.md`.
- **Migration safety** — schema changes use forward-only DDL; no app
  deployment ever removes a column that's being read by the current
  previous version.
