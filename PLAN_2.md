# Phase 2 — Web UI Core (detailed plan)

Phase 1 closed with 11 analytics RPCs live at `/api/v1/analytics/*`,
ACL enforcement, CSV export, and rate limiting. The JSON contract is
stable (PROTO-snake_case field names via `UseProtoNames=true`).
Phase 2 builds the SvelteKit dashboard that consumes that surface.

**Goal**: a working analytics dashboard a site owner uses daily. Five
reports (Overview + Pages + Sources + Geography + Devices) establish
every UI convention — filter bar, URL-as-state, chart components,
dark mode, accessibility — so Phase 3/4 can drop additional reports
into the same frame.

Related docs: `PLAN_OUTLINE.md` §Phase 2, `UI_PRD.md` §4–§6 + §10 +
§13, `PHASE_1_STATUS.md`.

---

## 1. Context and scope

### 1.1 What Phase 1 already delivered

- Every read endpoint the Overview + four Phase-2 reports need is live
  (Summary, Timeseries, Pages, Sources, Geography, Devices).
- Admin JWT path is the only auth we'll wire up in Phase 2. Non-admin
  (SSO-issued) tokens work but non-admin users see empty results until
  the Bolt user store ships in Phase 3.
- CSV export is ready: any table endpoint responds to `?format=csv`.
- Rate-limit contract is set (10 qps burst, 30/min sustained, 429 +
  `Retry-After` on exhaustion).

### 1.2 What Phase 2 ships

- **Svelte app** under `web/analytics/`, built to static assets.
- **Layout**: left sidebar (collapsible) + sticky top filter bar +
  main content pane.
- **Global filter state** mirrored bidirectionally to the URL query
  string — every view is shareable via URL.
- **Five reports**: Overview (KPI cards + timeline), Pages, Sources,
  Geography, Devices.
- **Chart library** — five reusable D3-based Svelte components
  (`<LineChart>`, `<Sparkline>`, `<StackedBar>`, `<Donut>`,
  `<ChoroplethMap>`). Heatmap is optional.
- **shadcn-svelte look** (cards, tables, dropdowns, date picker, dialog,
  sheet, command palette). Copy-in components, not a framework
  dependency.
- **Dark mode** toggle (default: follow system).
- **Keyboard nav + A11y** on filter bar, date picker, and tables.
- **Bundle budget**: < 150 KB gzipped on first load.
- **Integration with hula**: static assets bundled into `hula:local`,
  served at `/analytics/*` by the unified listener, OPA-protected
  (admin-only until Phase 3 opens to SSO users).
- **E2E regression**: a Playwright suite hitting the local stack,
  asserting the five reports render without console errors and with
  expected headline numbers against the Phase-1 seed fixture.

### 1.3 Out of scope (deferred to Phase 3/4)

- Admin pages (Users, Goals CRUD, Scheduled Reports, ACL admin) —
  Phase 3.
- Realtime page, Visitor profile drill-down, Events page, Forms page
  — Phase 4.
- Email reports backend — Phase 3.
- SSO login UI (only break-glass admin login in Phase 2).
- Compare-window delta display (Phase 1 returns sentinels; UI hides
  the deltas until Phase 1's compare math lands).

---

## 2. Stage breakdown

Nine stages. Each lands as a single commit on `roadmap/phase2` (new
branch off Phase 1). Pattern: every stage must leave `pnpm build`
green and `pnpm test` (vitest) passing.

### Stage 2.1 — Project scaffolding + build pipeline

**Goal**: an empty-but-buildable SvelteKit app with the toolchain set
up and an API client generated from the Phase-1 protos.

- `web/analytics/` — SvelteKit + TypeScript + Tailwind.
- Vite config tuned for static-adapter output (`@sveltejs/adapter-static`)
  into `web/analytics/build/` (which hula later bundles into the image).
- Tailwind + shadcn-svelte theme setup. Dark-mode toggle via
  `prefers-color-scheme` + manual override.
- TypeScript API client generator: a small Go `tools/genjsonclient`
  program (or a `protoc-gen-openapiv2` → OpenAPI → `openapi-typescript`
  pipeline — whichever is smaller) emits typed helpers for every
  analytics RPC.
- `pnpm install`, `pnpm build`, `pnpm test` all green.
- `.gitignore` ignores `node_modules/` and `build/`.

**Acceptance**:
- `cd web/analytics && pnpm install && pnpm build` produces a
  `build/` directory.
- The generated API client exposes typed helpers like
  `analytics.summary({ serverId, from, to })` that return the
  proto-snake_case JSON as TypeScript interfaces.
- Bundle-size probe passes: gzipped `build/` ≤ 20 KB for the empty
  scaffold (budget tightens per stage).

**Size**: 1.5 days.

---

### Stage 2.2 — Filter bar + URL-as-state + app shell

**Goal**: the sticky header + left sidebar + URL-synced filter state
are all visible, even though no report renders yet.

- Svelte store (`$lib/filters.ts`) holds the global Filters object.
  Writing to the store updates `window.history` via `pushState`;
  reading from the URL on mount hydrates the store.
- Top bar components:
  - Server selector (dropdown; calls a stub helper that reads
    `window.hulaConfig.servers` — baked in by hula at serve time).
  - Date-range picker (shadcn-svelte `<DatePicker>` plus preset chips:
    Today, 7d, 30d, 90d, custom).
  - Compare toggle (previous period / year-over-year / off).
  - Filter chips container (adds/removes country, device, source,
    path, channel, utm_*, browser, os chips).
- Left sidebar with five nav items (Overview, Pages, Sources,
  Geography, Devices) — active-route styling.
- Dark-mode toggle in the user menu.
- Routes exist but reports are blank placeholders.

**Acceptance**:
- Changing any filter mutates the URL; reloading the page restores
  the same filter state.
- Keyboard-only user can reach every filter control with Tab.
- Dark-mode toggle flips the theme and persists via `localStorage`.

**Size**: 2 days.

---

### Stage 2.3 — D3 chart components

**Goal**: the five Svelte + D3 chart components land with a stable
API and a Storybook-adjacent sandbox page at `/design/charts` for
manual inspection.

- `<LineChart data={...} series={['visitors','pageviews']} />` —
  multi-series line, hover tooltip, y-axis formatter, responsive
  resize.
- `<Sparkline data={...} />` — small, axis-free, inline-sized.
- `<StackedBar data={...} stacks={[...]} />` — top-N over time.
- `<Donut data={...} />` — proportion by category with a legend.
- `<ChoroplethMap data={...} geoJson={...} />` — world map coloured
  by row value, hover+click interaction. Uses a pre-bundled simplified
  world TopoJSON (~40 KB gzipped).
- All components emit Svelte events on interaction
  (`on:segmentClick`, `on:barClick`, `on:countryClick`) so report
  pages can wire drill-downs.
- Color palette: color-blind-safe (Okabe-Ito) as default; theme
  variables drive light/dark.
- Each component has a `.spec.ts` that renders with sample data
  and snapshots the SVG output.

**Acceptance**:
- `/design/charts` sandbox renders each component with representative
  data.
- Vitest snapshot suite green.
- No D3 import leaks into the route bundles: chart components use
  dynamic imports so only pages that need them pull D3.

**Size**: 3 days.

---

### Stage 2.4 — Overview report

**Goal**: the landing page. KPI cards + main timeline + recent events
(just the scaffold; actual recent-events feed is Phase 4).

- Four KPI cards: Visitors, Pageviews, Bounce rate, Avg session.
  Each card has a number, a `<Sparkline>` of the timeseries for that
  metric, and a delta placeholder (the delta row is hidden until
  Phase 1's compare math ships).
- Main `<LineChart>` of visitors + pageviews over the filter window.
- Loading state (skeleton cards) + error state (toast + retry
  button).
- Uses the API client from 2.1 — `summary` + `timeseries` fetched in
  parallel.
- Filter bar changes re-fetch.

**Acceptance**:
- `/` shows populated cards + chart against the seed fixture.
- Changing the date range re-fetches and re-renders.
- Lighthouse performance score ≥ 85 on first paint.

**Size**: 2 days.

---

### Stage 2.5 — Pages + Sources reports

**Goal**: the two table-shaped reports. A shared `<ReportTable>`
component handles both.

- `<ReportTable>`:
  - Configurable columns with per-column sort.
  - Sticky header, pagination (client-side for ≤ 1000 rows, which
    is the Phase-1 limit).
  - Row-click → add corresponding filter chip and navigate.
  - CSV export button → `fetch('/api/v1/analytics/pages?format=csv')`
    with the same filter state, triggers download.
- Pages: key=path, columns from TableRow (visitors, pageviews,
  unique_pageviews, bounce_rate, avg_time_on_page_seconds). Above the
  table, a multi-line chart (top-10 pages over time — client-side
  assembles from timeseries-per-path, or deferred to a later stage
  if the backend path isn't exposed).
- Sources: key=channel (default) or referer_host (group_by chip).
  Columns: visitors, pageviews, bounce_rate, pages_per_visit,
  goal_conv_rate.

**Acceptance**:
- Both pages render against the seed; top-row values match suite 21
  goldens.
- Row-click adds the right filter and navigates to the child report
  (e.g., clicking "/" on Pages narrows by path).
- CSV export downloads a valid CSV.

**Size**: 2.5 days.

---

### Stage 2.6 — Geography report + drill-down

**Goal**: the `<ChoroplethMap>` report with country → region drill.

- World map (60% of viewport) coloured by visitor count.
- Table (40% of viewport) with columns: country, visitors, %,
  pageviews, bounce.
- Clicking a country sets `filters.country` → re-fetches with the
  region dimension → re-renders the map at country-region level
  (when region TopoJSON is available, else falls back to a bar
  chart).
- Region TopoJSON only loaded on drill-down (dynamic import).

**Acceptance**:
- Seed fixture shows US + DE regions on the map.
- Drill-down works both on map click and row click.
- Back button restores the country view.

**Size**: 3 days.

---

### Stage 2.7 — Devices report

**Goal**: three stacked donuts backed by one API call.

- Single call to `/api/v1/analytics/devices` returns three parallel
  arrays (device_category, browser, os).
- Three `<Donut>` components side-by-side with legends.
- Click a slice → add the corresponding filter chip and narrow
  reports app-wide.
- Below each donut, a table of the same data with a drill-toggle
  (version breakdown — `browser_version` / `os_version` come from
  raw-events path, require a new BuildTable invocation; schedule
  that as a tail follow-up if it doesn't fit stage 2.7's budget).

**Acceptance**:
- Seed fixture: desktop slice ≈ 50%, mobile ≈ 40%, tablet ≈ 10%
  (per the 10-visitor distribution).
- Clicking a slice adds a filter chip; other reports pick it up.

**Size**: 1.5 days.

---

### Stage 2.8 — Hula image integration

**Goal**: the dashboard ships inside the `hula:local` Docker image and
the unified listener serves it at `/analytics/*` behind the admin
bearer token.

- `Dockerfile.local` builds `web/analytics/` and copies `build/` into
  the image at `/hula/web/analytics/`.
- `server/unified_analytics_ui.go` registers a `GET /analytics/`
  subtree handler on the unified ServeMux fallback: serves static
  assets from the embedded FS, falls back to `index.html` for SPA
  routing.
- Admin-only: the existing `AdminBearerHTTPMiddleware` covers
  `/analytics/*` too; unauthenticated requests redirect to a minimal
  `/login` page (served from the same static bundle) that posts to
  `/api/auth/login`.
- Window-config shim: hula renders a small JSON blob at
  `/analytics/config.json` with the caller's visible servers, so the
  server selector in 2.2 has real data.

**Acceptance**:
- `curl https://hula.test.local/analytics/` returns the SPA shell
  (no auth needed to see the login page).
- After login, the app loads, the server selector lists configured
  servers, and the Overview renders against live data.

**Size**: 2 days.

---

### Stage 2.9 — Playwright smoke + accessibility audit

**Goal**: regression coverage for the UI + confirm the A11y + bundle
budgets.

- Playwright running against the e2e stack — new `test/e2e/suites/
  22-ui-smoke.sh` that drives headless Chromium through Overview +
  each of the four reports, asserts:
  - Page renders without console errors.
  - KPI numbers against the suite-21 seed fixture.
  - Row click on Pages drills into the filtered view.
- Lighthouse run on Overview — performance ≥ 85, accessibility ≥ 95.
- Bundle-size guard: fail the build if gzipped first-load > 150 KB.

**Acceptance**:
- `./test/e2e/run.sh` → 100/100.
- Lighthouse report committed to `web/analytics/LIGHTHOUSE.md`.

**Size**: 2 days.

---

## 3. Timeline

| Stage | Size   | Cumulative |
|-------|--------|------------|
| 2.1   | 1.5 d  | 1.5        |
| 2.2   | 2 d    | 3.5        |
| 2.3   | 3 d    | 6.5        |
| 2.4   | 2 d    | 8.5        |
| 2.5   | 2.5 d  | 11         |
| 2.6   | 3 d    | 14         |
| 2.7   | 1.5 d  | 15.5       |
| 2.8   | 2 d    | 17.5       |
| 2.9   | 2 d    | 19.5       |

Close to **4 calendar weeks** at a sustainable pace — slightly over
PLAN_OUTLINE's "~3 weeks" estimate to account for the ChoroplethMap
drill-down in 2.6 and the image-integration plumbing in 2.8 (which
has a hula-side component).

---

## 4. Risks + open items

- **shadcn-svelte churn**: the port of shadcn from React to Svelte is
  young and APIs shift. Pin versions at stage 2.1 and vendor anything
  we extend.
- **Bundle budget**: D3 is big (~40 KB min-zipped for the subset we
  use). Dynamic imports on chart-heavy routes are non-negotiable.
- **Tailwind v4 migration**: upstream released a breaking v4 with a
  different config model. Stay on v3.4 until v4 lands a clean migration
  guide for shadcn-svelte.
- **ChoroplethMap data**: world TopoJSON at country granularity is
  fine; region-level is much bigger and varies per country. Load
  lazily on drill-down.
- **Admin-only assumption**: Phase 2 bakes in "admin sees every
  server". When SSO + per-user ACLs land in Phase 3 the window-config
  shim needs to switch to a server-side render so non-admin users
  only see their servers.

---

## 5. Sign-off checklist (Phase 2 → Phase 3)

- [ ] All 9 stages landed on `roadmap/phase2`.
- [ ] `cd web/analytics && pnpm build` produces a < 150 KB (gzipped)
      first-load bundle.
- [ ] `cd web/analytics && pnpm test` green.
- [ ] `./test/e2e/run.sh` → 100/100 (99 existing + 22-ui-smoke).
- [ ] `curl https://hula.test.local/analytics/` serves the SPA.
- [ ] Admin user lands on Overview with populated data.
- [ ] Lighthouse: perf ≥ 85, A11y ≥ 95 on Overview.
- [ ] `PHASE_2_STATUS.md` written, mirroring Phase 0/1 status docs.
- [ ] `UI_PRD.md` §4–§6 updated if any UX decision deviated from the
      original spec.

---

## 6. What happens after Phase 2

Phase 3 adds the admin surface (Users, Goals CRUD, Scheduled Reports,
ACL admin, email dispatch). Once Goals land, Overview gets a "goal
conversions" card; the Sources report's `goal_conv_rate` column lights
up; the FormsReport body ships.

Phase 4 adds the remaining four reports (Realtime, Visitor profile,
Events, Forms) onto the frame built in Phase 2 — no further chart-
library work needed beyond maybe the optional Heatmap.
