# Hula Analytics UI — Product Requirements Document

## 1. Context

Hula already collects detailed visitor-tracking data (page views, form
submissions, lander hits, visitor identity via cookies, IPs with country
enrichment) and writes it to ClickHouse. What's missing is the product layer
on top: query APIs, a dashboard UI, scheduled email reports, and a
per-user/per-server access model.

This document specifies the UI and supporting backend for a privacy-respecting
web-analytics product in the spirit of **Plausible** and **Fathom**, with
selected drill-down capabilities inspired by **Google Analytics 4** — but
staying opinionated and focused rather than exhaustive.

## 2. Goals & Non-Goals

### Goals

- Give site owners answers to **"how many visitors, from where, to which pages,
  via which sources, doing what"** without leaving the page.
- Support **pivot-and-drill** across the standard axes: time, page, source,
  geography, device, event, visitor.
- Respect **multi-tenancy**: a user sees only the servers they're authorized for.
- Deliver **scheduled email reports** (daily / weekly / monthly) per-server
  or consolidated.
- Self-host-friendly: no external dependencies at runtime (no SaaS analytics
  providers). Everything runs against the local ClickHouse.

### Non-Goals (explicitly out of scope for v1)

- **E-commerce revenue attribution** (GA-style). Goals/conversions yes, $ no.
- **Cross-device stitching** via logged-in user IDs. We track visitor cookies
  only; linking to user identity is a manual "alias" feature that already
  exists in the model.
- **A/B testing / experiments**.
- **Public share links** to dashboards (may come in v2).
- **Custom dimensions / custom user properties** beyond what the event schema
  already allows (the `Data` string payload on events).

## 3. Personas & Access Model

| Persona | Description | Access |
|---------|-------------|--------|
| **Admin** | Hula operator (the `admin` user from `config.yaml`) | Sees all servers; manages users; configures scheduled reports; can configure goals and access rules |
| **Site operator** | A user granted access to one or more servers | Sees analytics for their servers only; can view but not configure |
| **Report recipient** | Email address on a scheduled report | No UI / app access; receives email only |

### 3.1 Authentication — SSO

Users sign in via **Single Sign-On**, not a local username/password. Supported
identity providers at launch:

- **Google** (Google Workspace or consumer Gmail)
- **GitHub**
- **Microsoft / Azure AD**

A drop-in SSO framework (to be specified separately) handles the OAuth 2.0 /
OIDC flow for each provider and returns a verified email + external subject
ID. Hula's job is to:

- Map the external identity to a local `users` row (matched by email).
- Create the user on first login if admin has pre-provisioned them by email.
- Reject login if the email isn't in `users` (no open self-signup by default;
  admin-provisions emails ahead of time).
- Issue a hula JWT as it does today, so downstream middleware (OPA) is
  unchanged.

The local `admin` user stays as a break-glass account (username + password +
optional TOTP) for cases where SSO is misconfigured.

### 3.2 Access enforcement

Access is enforced by a new join table **`user_server_access`**:

```
users (id, email, ...)               ← already exists in hula
user_server_access (user_id, server_id, role)
servers                              ← from hula config, not a DB table
```

`role` is one of `viewer` (default) or `manager` (future — can configure
goals for a server). The `admin` user implicitly has `manager` on all servers
and is not represented in this table.

At every API call, the backend resolves the JWT's user ID → the list of
server IDs they can see, and intersects that with any server filter in the
request. Unauthorized server IDs are silently dropped from results.

## 4. Information Architecture

Top-level navigation (left sidebar, collapsible):

```
┌─────────────────┐
│  Hula Analytics │
├─────────────────┤
│ ▸ Overview      │  ← landing page, same as Plausible's dashboard
│ ▸ Realtime      │  ← live activity (polls every 5s)
│                 │
│ REPORTS         │
│ ▸ Pages         │
│ ▸ Sources       │
│ ▸ Geography     │
│ ▸ Devices       │
│ ▸ Events        │
│ ▸ Forms         │
│ ▸ Visitors      │
│                 │
│ ADMIN           │
│ ▸ Goals         │  ← admins / managers only
│ ▸ Users         │  ← admins only
│ ▸ Reports       │  ← scheduled email reports
└─────────────────┘
```

### Server selector

Above every page, a **server dropdown** (multi-select when useful). Admins see
all servers; other users see only their authorized servers. The selection
persists across reports via localStorage.

### Global filters (sticky header)

Every report page has a sticky header with:

1. **Date range** — Today, Yesterday, Last 7d, Last 30d, Last 90d, Month to
   date, Year to date, Custom. Compare-to-previous-period toggle.
2. **Server scope** — see above.
3. **Compose filters chips** — click on any row in any table to add a filter
   chip (e.g., "Country = Germany", "Page = /pricing", "Device = Mobile").
   Filters apply across all subsequent navigation.
4. **Clear all / save current view**.

Compose filters are how **drill-down** works. Clicking a country in the
Geography report adds "Country = X" to the filter bar, and every subsequent
report (Pages, Sources, etc.) is now restricted to that country.

## 5. Overview (Dashboard)

Default landing. One-page summary.

```
┌────────────────────────────────────────────────────────────────┐
│ [Server ▾]  [Last 7d ▾]  [Compare: 7d prior ▾]  [Filter +]     │
├────────────────────────────────────────────────────────────────┤
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐         │
│  │ VISITORS │  │PAGE VIEWS│  │BOUNCE RT │  │AVG SESS. │         │
│  │   1,234  │  │   8,421  │  │   42%    │  │  1m 32s  │         │
│  │   ▲ 12%  │  │  ▲ 18%   │  │  ▼ 3%    │  │  ▲ 8%    │         │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘         │
├────────────────────────────────────────────────────────────────┤
│  Timeline (visitors & pageviews, stacked line chart)           │
│  Granularity: auto (hourly ≤48h, daily ≤90d, weekly > 90d)     │
│  Range-compare: previous period shown as dashed line           │
├────────────────────────────────────────────────────────────────┤
│  TOP PAGES (5)         │  TOP SOURCES (5)   │  TOP COUNTRIES   │
│  /              3,211  │  Direct      4,120 │  🇺🇸 US    421  │
│  /pricing         912  │  google.com  1,890 │  🇩🇪 DE    218  │
│  /blog/...        421  │  ...              │  ...            │
│                        │                    │                  │
│  [→ All pages]         │  [→ All sources]  │  [→ All countries]│
├────────────────────────────────────────────────────────────────┤
│  TOP DEVICES           │  GOALS (if any)                       │
│  Mobile        61%     │  Signup form submit    132  (+12%)    │
│  Desktop       35%     │  Trial click           54   (-3%)     │
│  Tablet         4%     │  [→ All events]                       │
└────────────────────────────────────────────────────────────────┘
```

**KPI cards** show current value, delta vs. comparison period, and a
5-point sparkline.

Each "Top N" card:
- Row click → adds that item as a filter chip and refreshes the page.
- "→ All X" link → navigates to that report with the same filters applied.

## 6. Report Pages

Every report follows the same template:

```
┌────────────────────────────────────────────────────────────────┐
│ [sticky filter bar]                                            │
├────────────────────────────────────────────────────────────────┤
│  Main chart for this dimension (varies by report)              │
├────────────────────────────────────────────────────────────────┤
│  Table: one row per value of the dimension                     │
│  Columns: configurable — visitors, pageviews, bounce, time     │
│  Click row = add filter + drill down                           │
│  Sort by any column; CSV export                                │
└────────────────────────────────────────────────────────────────┘
```

### 6.1 Pages

- **Dimension**: URL path.
- **Chart**: top 10 pages over time (multi-line).
- **Columns**: Path · Visitors · Pageviews · Unique pageviews · Bounce rate ·
  Avg time on page · Entrances · Exits.
- **Drill-down**: click a page → filter applied; reports now show only
  sessions that included that page. Also adds a "Per-page detail" panel:
  top referrers into this page, exit-destination pages, on-page events.

### 6.2 Sources

- **Dimension**: Referrer host (parsed from `Referer` header — **new**).
- **Sub-dimensions**: Channel (Direct, Search, Social, Referral, Email),
  Referrer URL, UTM source/medium/campaign (if URL had them at landing).
- **Chart**: top sources stacked-bar over time.
- **Columns**: Source · Visitors · Bounce · Pages/visit · Goal conv. rate.
- **Drill-down**: click "google.com" → shows UTM breakdowns + landing pages.

### 6.3 Geography

- **Dimension**: Country (two-letter code, already in `badactor.IPInfo`).
- **Secondary**: Region, City (**new** — requires richer IP enrichment).
- **Chart**: world map heatmap (top-left 60% of viewport) +
  top-countries table (right 40%).
- **Drill-down**: click country → regions for that country.
- **Columns**: Country · Visitors · % · Pageviews · Bounce.

### 6.4 Devices

Three stacked reports on one page:

- **Device type**: Mobile / Tablet / Desktop (parsed from UA — **new**).
- **Browser**: Chrome / Firefox / Safari / … (parsed from UA — **new**).
- **OS**: macOS / Windows / iOS / Android / Linux (parsed from UA — **new**).

Each with donut chart + table; drill into browser version or OS version.

### 6.5 Events (`/events`)

Shipped in Phase 4. Per-event-code histogram with drill sheet.

- **Columns**: Event code · Count · Unique visitors · % of total.
  Backed by `GET /api/v1/analytics/events`; "% of total" is computed
  client-side against `pageviews` to avoid an extra round-trip.
- **Row click** opens a right-anchored Sheet with:
  - Fire count + unique-visitor count summary.
  - A "Filter by this event" button that sets the `event_code` filter
    chip so every other report narrows to that code.
- **Deferred**: ranked `Data.*` payload histogram (needs a dedicated
  `/api/v1/analytics/events/{code}/payload` endpoint — sheet renders
  an explicit "follow-up" placeholder until then).
- CSV export via `analytics.csv('events')`.

### 6.6 Forms (`/forms`)

Shipped in Phase 4.

- **Columns**: Form · Viewers · Submissions · Conversion rate · Avg time
  to submit. Backed by `GET /api/v1/analytics/forms`.
- **Drill sheet** shows first-seen / last-seen + the numeric detail
  fields. Per-field fill-order is flagged as a follow-up (needs
  richer `StartForm` payloads at ingest — still emitted but not yet
  captured with per-field state).

### 6.7 Visitors (`/visitors`, `/visitors/[id]`)

Shipped in Phase 4. Two pages:

`/visitors` — paginated directory table.
- **Columns**: Visitor · Last seen · First seen · Sessions · Pageviews
  · Country · Device.
- Server-side pagination at 100/page; Page N of M footer.
- Row click → `/visitors/[id]` drill.
- CSV export.

`/visitors/[id]` — per-visitor detail.
- Header: first-seen / last-seen / sessions / pageviews summary.
- Three KPI tiles: Country · Device · Email.
- **Timeline** — chronological event list (time · code · URL · country
  · device). URL clicks set the `path` filter chip and jump to
  `/pages` so the operator can narrow every report to that URL.
- **Related identifiers** block: IPs · aliases · cookies.
- **Admin-only "Forget visitor (GDPR)" button** — shown only when
  `window.hulaConfig.isAdmin=true`. Calls
  `POST /api/v1/analytics/visitor/{id}/forget` (stage 4.9). UI shows a
  confirm dialog that explicitly says "irreversible" — the backend
  mutation is async-queued against ClickHouse so the row may not
  disappear synchronously.

### 6.8 Realtime (`/realtime`)

Shipped in Phase 4.

- Polls `GET /api/v1/analytics/realtime` every 5 seconds.
- **`visibilitychange`** listener pauses polling when the tab is
  hidden and resumes on focus. A live/paused dot in the header
  shows the state.
- **KPI row**: Active visitors (5m) · Recent events · Top country.
- **Top-pages / top-sources** side-by-side lists (key + visitors
  count, reverse-sorted).
- **Streaming events table** — last 50 server-returned events with
  time · code · URL · country · device. Sticky header; 28rem max
  height; replaces in place (no flash / animation — WCAG 2.1
  reduced-motion friendly).
- **Deferred** from the original spec: D3-animated world-map dots
  of current-pageview geo points. The current event row has country
  but no lat/lon — a dot map needs ingest-time geocoordinate
  enrichment.

## 7. Admin Pages

Admin pages live under `/analytics/admin/*` and surface in the sidebar
under an "Admin" divider that shows only when
`window.hulaConfig.isAdmin === true` (populated from `/analytics/config.json`
on boot). Non-admin sessions never see the links.

Shipped in Phase 3.

### 7.1 Users (`/admin/users`)

Directory + per-user ACL, backed by `AuthService` over the REST gateway.

- **Table columns**: email · username · created_at.
  Populated by `POST /api/v1/auth/users/list` (no filter → full list).
- **"+ New user"** button opens a right-anchored slide-over sheet
  (`Sheet.svelte`) with email (required) + username (optional). Submits to
  `POST /api/v1/auth/users`.
- **Per-row "Manage access"** opens a second sheet that lists the user's
  current grants (`GET /api/v1/auth/access?user_id=<id>`) alongside a
  grant widget (server selector × role dropdown: `viewer` | `manager`).
  Grant → `POST /api/v1/auth/access`; revoke → `DELETE
  /api/v1/auth/access/{user_id}/{server_id}`.
- **Delete user** → `POST /api/v1/auth/user/delete` with
  `{identity: <uuid-or-email>}`.

Deferred from the original spec: "last login" column (not yet tracked),
TOTP-status column (needs a dedicated RPC), password-reset button (Phase 4,
alongside a mail-out flow).

### 7.2 Goals (`/admin/goals`)

Per-server CRUD over `GoalsService`. Scope follows the top filter bar's
current server (`$: currentServer, load()` reactive guard); the page shows
an empty state until a server is picked.

- **Table columns**: name · kind · rule summary · enabled.
  Populated by `GET /api/v1/goals/{server_id}`.
- **"+ New goal"** opens a sheet with:
  - Name (required), description (optional).
  - **Kind** (`URL_VISIT` · `EVENT` · `FORM` · `LANDER`).
  - Kind-conditional rule input: URL-path regex, event code (int64), form ID,
    or lander ID.
  - Enabled checkbox.
  - **Test rule (7d)** button → `POST /api/v1/goals/{server_id}/test`. The
    server currently returns `501 Unimplemented`; the UI renders
    "(TestGoal not implemented yet — stage 3.3b)" in that case. Full
    dry-run math lands in Phase 4 alongside the Events report.
- Create → `POST /api/v1/goals/{server_id}` (body: `{goal: …}`).
- Patch → `PATCH /api/v1/goals/{server_id}/{goal_id}`.
- Delete → `DELETE /api/v1/goals/{server_id}/{goal_id}`.

**Value** (numeric weight) from the original spec is not yet exposed in the
UI — reserved for Phase 4's revenue-features work.

### 7.3 Scheduled Reports (`/admin/reports`)

Per-server CRUD over `ReportsService`, including inline preview + send-now.

- **Table columns**: name · cron · timezone · recipients (first two + "+N")
  · next fire · enabled.
- **"+ New report"** opens a sheet with:
  - Name (required).
  - **Cron** (5-field: `min hr dom mon dow`, parsed by robfig/cron/v3).
    Default `0 9 * * 1` (Mondays 09:00).
  - **Timezone** — IANA string (`Intl.DateTimeFormat().resolvedOptions()
    .timeZone` prefilled).
  - **Recipients** — comma- or space-separated emails, normalized on save.
  - **Template** — `SUMMARY` (four KPI boxes) or `DETAILED` (KPIs + top
    pages).
  - Enabled checkbox.
- Create → `POST /api/v1/reports/{server_id}` (body: `{report: …}`);
  response includes the dispatcher-computed `next_fire_at`.
- **Preview** button opens a second sheet with a sandboxed iframe rendered
  from `POST /api/v1/reports/{server_id}/{report_id}/preview` (`html` +
  `subject` fields). The iframe uses `sandbox=""` (full isolation) and
  `srcdoc=…` to render without navigation.
- **Send now** → `POST /api/v1/reports/{server_id}/{report_id}/send-now`;
  the alert shows the returned `run_id`, and if the "Runs" panel is open
  for that report it reloads.
- **Runs** (inline expandable panel) → `GET /api/v1/reports/{server_id}/
  {report_id}/runs?limit=25`. Lists started_at · attempt · status, colored
  dot by status (`success` · `retrying` · anything-else).
- Delete → `DELETE /api/v1/reports/{server_id}/{report_id}`.

The original spec's "Scope — one or more servers" is scoped down to the
single current server (matching the rest of the admin UI's per-server
model). Multi-server reports can be created by cloning a report config
across servers via the UI.

### 7.4 Alerts (`/admin/alerts`)

Shipped in Phase 4. Per-server threshold alert rules + recent fires.

- **Table columns**: Name · Kind · Threshold · Window · Last fired ·
  Enabled. Backed by `GET /api/v1/alerts/{server_id}`.
- **"+ New alert"** opens a sheet with:
  - Name (required).
  - **Kind** (one of five):
    - `GOAL_COUNT_ABOVE` — fires when conversions for Target Goal in
      the last `window_minutes` exceed `threshold`.
    - `PAGE_TRAFFIC_DELTA` — current-window pageviews on Target Path
      vs the same window one week ago; fires when `|delta_pct| >
      threshold`. Skips when prior-window is zero (avoids spurious
      infinite-delta on new pages).
    - `FORM_SUBMISSION_RATE` — submissions/min to Target Form above
      threshold (spam-burst detector).
    - `BAD_ACTOR_RATE` — is_bot hits/min above threshold.
    - `BUILD_FAILED` — any `build_failed` code in window (threshold
      ignored).
  - Kind-conditional Target input: Goal ID / URL path / Form ID.
  - Numeric `threshold` + `window_minutes` + `cooldown_minutes`.
  - Recipients (comma- or space-separated emails).
  - Enabled checkbox.
- Create → `POST /api/v1/alerts/{server_id}`.
- Update → `PATCH /api/v1/alerts/{server_id}/{alert_id}` (preserves
  server-side `last_fired_at`).
- Delete → `DELETE /api/v1/alerts/{server_id}/{alert_id}`.
- **Inline "Recent fires" panel** per alert, shown when the row's
  "Fires" button is clicked. Backed by `GET .../events?limit=25`.
  Each fire shows a colored dot (success · retrying · failed ·
  `mailer_unconfigured`) + observed value + threshold + error text
  when present.

Evaluator details — see §8.2.

## 8. Backend engines

### 8.1 Email Reports dispatcher

Shipped in Phase 3 as `pkg/reports/dispatch` + `pkg/reports/render` +
`pkg/mailer`. Dispatcher lives as a background goroutine inside hula.

- **Startup**: `dispatch.Start(ctx, mailer)` — starts a 1-minute ticker and
  an `Enqueue` channel (cap 32) for on-demand `SendNow` requests. No-op
  when the Bolt store isn't present; mailer is log-only when `hula_mailer`
  isn't configured.
- **Scheduling**: each tick walks all enabled reports in the `reports`
  bucket and calls `sendOne` for any whose `next_fire_at ≤ now`. Cron + IANA
  timezone parsed via robfig/cron/v3 + `time.LoadLocation`; reports with an
  unparseable tz are refused at create time.
- **Rendering**: `pkg/reports/render` emits HTML templates —
  `TEMPLATE_VARIANT_SUMMARY` (four KPI boxes) or `TEMPLATE_VARIANT_DETAILED`
  (KPIs + top-pages table). Same query path as the UI (Phase-1 analytics
  RPCs) so email numbers match the dashboard byte-for-byte.
- **Delivery**: `pkg/mailer/mailer.go` — STARTTLS on :587 via stdlib
  `net/smtp`. `ErrNotConfigured` sentinel for the log-only fallback path
  (useful in dev + on first boot before the operator wires SMTP).
- **Persistence**: one `StoredReportRun` row per attempt (id, report_id,
  started_at, finished_at, status, attempt, error, recipients[]).
- **Retry**: `[0, 1m, 5m, 25m]` on failure (max 4 attempts). Each attempt
  writes its own `StoredReportRun` so the admin UI's runs panel shows the
  full trail.
- **Preview**: `PreviewReport` renders the same HTML but skips the mailer
  and does not persist — pure template + current-data-window render for
  the iframe preview in the admin UI.

Server-side chart rendering (the original spec's "attach as CIDs" for the
Detailed variant) is currently inline KPI + tables only — chart PNGs are
deferred to Phase 5a alongside the mobile notification engine.

### 8.2 Alerts evaluator

Shipped in Phase 4 as `pkg/alerts/evaluator`. Runs as a sibling
goroutine to the report dispatcher (started in `server/run_unified.go`).

- **Startup**: `evaluator.Start(ctx, mailer, db)` — starts a 1-minute
  ticker. Fires once at boot so operators don't wait a full minute to
  see activity after a restart.
- **Evaluation**: each tick walks all enabled alerts in the Bolt
  `alerts` bucket. For each, runs the kind-specific predicate against
  `events_v1` and — if true + cooldown elapsed + throttle budget left
  — writes a `StoredAlertEvent` row and hands a rendered email to the
  mailer.
- **Cooldown**: `CooldownMinutes` per alert (safety default 60m when
  zero). `LastFiredAt` persists on the alert row so subsequent ticks
  short-circuit.
- **Throttle**: `maxFiresPerTick = 10`. Log-warns on throttle so a
  ClickHouse slowness spike that trips many rules at once can't
  overload the mailer.
- **Delivery status** (mapped to `DeliveryStatus` proto enum):
  - `SUCCESS` — mailer returned nil.
  - `MAILER_UNCONFIGURED` — SMTP wasn't set up; the alert still
    persisted as a fire for the admin UI, just not sent.
  - `FAILED` — mailer returned a non-sentinel error; `error` column
    holds the message.
  - `RETRYING` — reserved (Phase 5a adds push-channel retries).
- **Persistence**: `StoredAlertEvent` in Bolt's `alert_events` bucket.
  One row per fire attempt, ordered by `FiredAt DESC` for the admin
  UI's "Recent fires" panel.
- **Predicates** (current set):
  - `goal_count_above` — COUNT(is_goal=1 AND goal_id=X) in window >
    threshold.
  - `page_traffic_delta` — current pageviews to path vs same window
    one week ago; fires when `|delta_pct|` > threshold. Skips if the
    prior-window pageview count is zero.
  - `form_submission_rate` — submissions/min (code=0x20 +
    payload contains form_id) > threshold.
  - `bad_actor_rate` — `is_bot=1` hits/min > threshold.
  - `build_failed` — any `code = 0x1000` event in window. Threshold
    ignored; fires on first observation. (The site-deploy pipeline
    doesn't yet emit this code — wiring is a Phase-5 follow-up.)

Delivery reuses the Phase-3 mailer directly. Phase 5a generalises to
a `Notifier` interface that wraps the mailer and adds push channels
without touching evaluator code.

## 9. Mobile App

The web dashboard is the primary surface. A mobile app complements it for
on-the-go monitoring and push notifications.

### 9.0 Mobile backend surface (shipped in Phase 5a)

The backend the 5b mobile app consumes is already in tree. Summary of
what's live:

- **Auth**: same bearer-token flow as the web UI. No mobile-specific
  login endpoint — the app hits `/api/v1/auth/admin` (or an OIDC
  provider flow) and caches the returned JWT locally.
- **Device registration**:
  - `POST /api/mobile/v1/devices` — idempotent on (user_id, device
    fingerprint). Body carries `platform` (APNS / FCM) + plaintext
    `token` + optional `label`. Server seals the token at rest via
    AES-GCM (see `pkg/mobile/tokenbox`).
  - `DELETE /api/mobile/v1/devices/{device_id}` — owner-only.
  - `GET /api/mobile/v1/devices` — caller's own devices; last-seen
    descending.
- **Compact analytics**:
  - `GET /api/mobile/v1/summary?server_id=…&preset=…` — 4 numerics
    + a 12-point sparkline. < 300 B JSON.
  - `GET /api/mobile/v1/timeseries?server_id=…&preset=…&granularity=…`
    — downsampled to 12 (hour) or 24 (day) buckets.
- **Realtime feed**:
  - `WSS /api/mobile/v1/events?server_id=…` — authenticated WebSocket
    upgrade. Streams JSON event frames. 25s ping / 60s idle timeout;
    slow clients get dropped (bounded buffered channels).
- **Notifications**:
  - `GET / PATCH /api/v1/notify/prefs/{user_id}` — email/push
    booleans + IANA timezone + quiet hours.
  - Alert delivery via the notifier composite: emails always, push
    via APNs (iOS) or FCM (Android) when creds are configured.
  - Dead-token handling: APNs 410 / FCM NOT_FOUND marks the device
    inactive server-side so the app sees it disappear from
    `ListMyDevices` on next refresh.
- **Deep-link scheme** for alert pushes: `hula://alert/<alert_id>` —
  app is expected to route to the alerts-inbox detail screen. Web
  fallback path `/analytics/admin/alerts` also works.

Full protos live at `pkg/apispec/v1/mobile/mobile.proto` and
`pkg/apispec/v1/notify/notify.proto`. OpenAPI v2 dump is pending
(stage 5a.8 follow-up).

### 9.1 Scope & feel

- **Platforms**: iOS and Android.
- **Not a reskin** of the web responsive view — it's a purpose-built
  app focused on *consuming* analytics and reacting to alerts, not
  *configuring* them. Configuration (goals, scheduled reports, users)
  stays web-only.

### 9.2 Screens

| Screen | Purpose |
|--------|---------|
| Login | SSO via Google / GitHub / Microsoft; OAuth flow in a system browser (ASWebAuthenticationSession on iOS, Custom Tabs on Android) |
| Server picker | Same ACL as web; remembered across launches |
| Dashboard | KPI cards, timeline sparkline, top pages / sources / countries for the selected server and date range |
| Report detail | Tap any top-N card to drill into that report (paginated, filter chips work the same) |
| Realtime | Live visitors count; scrolling feed of recent events; mini map of activity |
| Alerts inbox | Chronological list of past alerts; tap to jump to the relevant report pre-filtered |
| Settings | Notification preferences; signed-in identity; sign-out-this-device |

### 9.3 Push notifications

Alerts defined in the web UI (Phase 4) can deliver via push:

- **iOS**: Apple Push Notification service (APNs).
- **Android**: Firebase Cloud Messaging (FCM).

Each notification carries a deep-link URL (`hula://alert/<id>`) so tapping
the notification opens the relevant report pre-filtered.

**Notification types** (v1):

- Goal conversion threshold reached.
- Traffic spike / drop vs. same day last week.
- Site build failed (production or staging).
- Bad-actor block rate spike.
- Form submission anomaly (zero submissions for N hours on a form that
  usually gets traffic).

Preferences are per-user, per-alert-type: push, email, or both. Quiet
hours and timezone are respected.

### 9.4 Authentication

- SSO providers are the same as web (Google / GitHub / Microsoft).
- Device token (APNs/FCM) is registered against the user's account on
  first launch + after every login.
- Logging out of a device revokes that device's notification registration.
- Admin can force-revoke a device from the web UI.

### 9.5 Offline behavior

- Last-fetched summary is cached locally so cold-start shows something
  immediately.
- Pending write actions (mark alert as read, change preference) queue and
  retry when network returns.
- "Stale" indicator shown on any metric older than 5 minutes without a
  successful refresh.

### 9.6 Tech choice (recommended)

Locked at the start of the mobile-app implementation phase. Candidates:

- **React Native** with TypeScript — shares types with the web API; large
  ecosystem. **Recommended for launch.**
- **Flutter** — single codebase, native-quality UI.
- **Native** (Swift + Kotlin with a shared Kotlin-Multiplatform networking
  layer) — best fidelity, highest cost.

Decision criteria: team's existing expertise, urgency of launch, desire to
share code with the web UI.

## 10. Chart & Visualization Spec

| Chart | Tech | Use |
|-------|------|-----|
| Line / stacked line | **D3.js** wrapped in a `<LineChart>` Svelte component | Overview timeline, top-N over time |
| Bar / stacked bar | D3 Svelte component | Sources by channel, devices |
| Donut | D3 Svelte component | Device category, OS |
| World map | D3 `d3-geo` + natural-earth TopoJSON | Geography |
| Sparkline | Tiny D3 Svelte component, inline SVG | KPI cards |
| Heatmap | D3 Svelte component | Hour-of-day / day-of-week activity |
| Sankey / flow | D3 `d3-sankey` (optional v2) | User flow between pages |

All charts are **client-side rendered** and authored as first-party Svelte
components on top of D3 primitives — **not** a chart-library wrapper. This
keeps bundle size tight and lets the same source components be reused by
the mobile app (ported to React Native SVG or Flutter CustomPainter, or
delivered as server-rendered SVG snapshots embedded in the mobile UI).

Email reports reuse the same components server-side, rendered to static
SVG (embedded in HTML) — no headless browser dependency.

## 11. Data Pipeline & Schema Additions

### 10.1 Enrichment at ingest

Currently a visitor event goes straight to the `events` table with raw
`FromIP` and `BrowserUA`. We'll add a thin enrichment layer in the handler
before insert:

- **UA parse** — use `github.com/mssola/user_agent` or `github.com/ua-parser/uap-go`.
  Compute `Browser`, `BrowserVersion`, `OS`, `OSVersion`, `DeviceCategory`.
- **Referrer parse** — pull `Referer` header (**new — currently not captured**).
  Classify into channel (Direct / Search / Social / Referral / Email).
  Parse UTM params from the landing URL query string.
- **Geo enrich** — call existing `badactor.FormatIPInfoCached` but also
  extend `IPInfo` with `Region` and `City` (the ip-api.com endpoint returns
  these already — just persist them).
- **Session ID** — group events for the same `VisitorID` with gaps < 30 min
  into the same session. Derived, stored on the event row.

All new fields are added as columns to the `events` table. Older rows get
defaults / NULLs; reports cope with missing values gracefully.

### 10.2 New tables

| Table | Columns | Engine |
|-------|---------|--------|
| `user_server_access` | user_id, server_id, role, granted_at | ReplacingMergeTree(granted_at) |
| `goals` | id, server_id, name, type, matcher (JSON), created_at | ReplacingMergeTree |
| `scheduled_reports` | id, name, recipients, server_ids, frequency, format, timezone, next_fire_at, active | ReplacingMergeTree |
| `report_runs` | id, report_id, fired_at, ok, error | MergeTree ORDER BY fired_at |

### 10.3 Materialized views (pre-aggregates)

For responsive dashboards without scanning the full `events` table:

- `mv_events_hourly` — grouped by hour, server, path, referrer host, country,
  device_category. Populated via ClickHouse materialized view on `events`.
- `mv_events_daily` — same at day granularity. For ranges > 14 days, queries
  hit this view.
- `mv_sessions` — one row per session, aggregated at session-close time.

These are invisible to the UI; the query layer picks the right source table
based on requested date range and granularity.

## 12. Backend API

All endpoints require JWT auth (admin bearer token today; Phase-2 adds
SSO-issued tokens). Every endpoint takes a mandatory `server_id` query
param. Table-shaped endpoints additionally accept `filters.from`,
`filters.to` (ISO 8601), `filters.granularity` (hour|day|week), a
`filters.compare` flag (previous period / year-over-year — wired but
deltas currently return -1 sentinels), and the full chip set:
`filters.country`, `filters.device`, `filters.source`,
`filters.path`, `filters.event_code`, `filters.goal`,
`filters.browser`, `filters.os`, `filters.channel`,
`filters.utm_source`, `filters.utm_medium`, `filters.utm_campaign`,
`filters.region`, `filters.city`. Filters compose AND-wise on the
ClickHouse query.

Every table-shaped endpoint accepts `?format=csv` for CSV export
(RFC 4180, `Content-Disposition: attachment`).

Paths shipped by Phase 1 (all under `/api/v1/*`, served by grpc-gateway
from the `AnalyticsService` proto; browser JSON field names are
proto-snake_case because the gateway is configured with
`UseProtoNames=true`):

| Endpoint | Returns | Phase |
|----------|---------|-------|
| `GET /api/v1/analytics/summary` | KPI scalars (visitors, pageviews, bounce_rate, avg_session_duration_seconds, plus `*_delta_pct` sentinels) | 1 |
| `GET /api/v1/analytics/timeseries` | `{ buckets: [{ ts, visitors, pageviews }] }` | 1 |
| `GET /api/v1/analytics/pages` | `{ rows: [...] }` — accepts `limit` (default 50, cap 1000) + `offset` | 1 |
| `GET /api/v1/analytics/sources` | `{ rows: [...] }` — accepts `group_by` (`channel`/`referer_host`/`utm_source`), `limit`, `offset` | 1 |
| `GET /api/v1/analytics/geography` | `{ rows: [...] }` — country by default; drills to region when `filters.country` is set; rows carry a `percent` field | 1 |
| `GET /api/v1/analytics/devices` | `{ device_category: [...], browser: [...], os: [...] }` — three parallel tables | 1 |
| `GET /api/v1/analytics/events` | `{ rows: [...] }` — per-code count, unique_visitors, first_seen, last_seen | 1 |
| `GET /api/v1/analytics/forms` | `{ rows: [] }` — **stub**; body pending form-submission events with a discoverable form_id | 1 (stub) |
| `GET /api/v1/analytics/visitors` | Paginated directory (`limit`, `offset`) | 1 |
| `GET /api/v1/analytics/visitor/{visitor_id}` | Profile header + up-to-1000-event timeline + related ips/cookies/aliases | 1 |
| `GET /api/v1/analytics/realtime` | Active visitors in last 5 min + 50 recent events + top pages + top sources (5-second server-side cache) | 1 |
| `GET /api/v1/analytics/goals` | Goal counts and conversion rates | Phase 3 |
| CRUD `/api/v1/analytics/goals/*` | Goal config | Phase 3 |
| CRUD `/api/v1/analytics/reports/*` | Scheduled report config | Phase 3 |
| `POST /api/v1/analytics/reports/{id}/preview` | Render a report for inline preview | Phase 3 |
| `POST /api/v1/analytics/reports/{id}/send-now` | Force-fire a report | Phase 3 |
| CRUD `/api/v1/analytics/access/*` | User-server ACL admin | Phase 3 |

All filter chips in the UI become query params. Example:

```
GET /api/v1/analytics/pages
    ?server_id=tlaloc
    &filters.from=2026-04-14T00:00:00Z
    &filters.to=2026-04-21T00:00:00Z
    &filters.country=DE
    &filters.device=mobile
    &limit=50
```

Auth and ACL:
- Admin JWTs (role `admin` or `root`) are treated as superadmin; they
  can query any `server_id`.
- Non-admin callers are intersected against `pkg/server/authware/access`
  (per-user RoleAssignment records). Phase 1 ships this path but the
  store is empty until the Bolt user rewrite lands in Phase 3, so
  non-admin queries effectively return no rows today.

Rate limiting:
- Per-user token bucket keyed on the authware claim username.
- Burst 10 qps, sustained 30/min.
- Over-quota responses: 429 with `Retry-After: 2`.

## 13. UI Tech Stack

Phase 2 shipped the web UI. Code lives under `web/analytics/` and
is served from hula at `/analytics/*`.

- **Framework**: **SvelteKit 2** + TypeScript + `@sveltejs/adapter-
  static`. Compiled output is ~19 KB gzipped on first load.
- **Component look**: shadcn-svelte-style — CSS-variable theme in
  `src/app.css`; components vendored under `$lib/components/` and
  `$lib/charts/`.
- **Charts**: hand-built D3 + Svelte components. Five shipped in
  Phase 2: `<LineChart>`, `<Sparkline>`, `<StackedBar>`, `<Donut>`,
  `<ChoroplethMap>`. Sandbox at `/analytics/design/charts`.
- **CSS**: Tailwind 3.4 + the shadcn CSS-variable theme.
- **Auth**: admin bearer token via the existing Phase-0 login path.
  SSO login UI ships in Phase 3 with the Bolt user-store migration.
- **Build output**: static assets bundled into the `hula:local`
  Docker image (Dockerfile.local ui-build stage) and served from
  `/hula/web/analytics/` by the unified listener. Path is overridable
  via the `HULA_ANALYTICS_UI_ROOT` env var for dev-mode hula.
- **State**: URL-as-state — every filter chip + date range is
  mirrored bidirectionally to `?filters.*` query params. Reloading
  any page restores the exact view.
- **Palette**: Okabe-Ito color-blind-safe defaults across every
  chart and KPI card.
- **Dark mode**: auto-follows `prefers-color-scheme`; manual
  override via the toolbar toggle, persisted in `localStorage`.
- **Bundle budget**: 150 KB gzipped first-load, enforced in CI by
  `scripts/bundle-size-guard.mjs` after every `pnpm build`. Heavy
  chart code (D3 scales + world-atlas TopoJSON) is dynamic-
  imported and only loads on the relevant routes.
- **Mobile app**: separate codebase (React Native recommended;
  see §9.6).

## 14. Implementation Phases

See `PLAN_OUTLINE.md` for the high-level phase-by-phase roadmap (Phase 0
through Phase 5b). Each phase will be detailed in its own plan document
before kickoff.

## 15. Success Criteria

- A site owner can answer "How many people visited my pricing page from
  Germany on mobile last week?" in **under 30 seconds** of clicking.
- Weekly emails land within 5 minutes of their scheduled time with correct
  data 99.9% of the time.
- Dashboard first-paint < 500 ms for a 7-day range (thanks to materialized
  views).
- A non-admin user *cannot* see servers they're not authorized for — ever,
  via any endpoint (enforced server-side, not just hidden in UI).
- Page size: the JS bundle for the SPA is < 150 KB gzipped.

## 16. Open Questions

1. **Cookie consent** — the current visitor cookie is set without a consent
   gate. If served to EU users, we likely need a consent mode. Should we
   ship a consent-aware mode (drop the cookie only after consent, rely on
   IP/UA heuristics for anonymous tracking)? Plausible sidesteps by not
   using cookies at all. Open.
2. **Data retention policy** — raw events grow fast. Default TTL of 13
   months on the raw `events` table + indefinite on the materialized views?
3. **Goal backfill** — when a goal is created, should its counts apply
   retroactively to historical data? Probably yes (it's just a query
   filter), but we should make this explicit.
4. **Is the UI served from the hula-core listener only, or also on each
   server's host?** Decision: admin UI only on hula-core. Per-server hosts
   serve only the site, not the analytics UI.
