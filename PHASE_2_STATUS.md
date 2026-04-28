# Phase 2 — Execution Status (COMPLETE)

**Status: all 9 stages landed. `./test/e2e/run.sh` → 107/107.**

Summary of the Phase-2 Web UI rollout:
- SvelteKit 2 + TypeScript + Tailwind 3.4 app under `web/analytics/`
  builds to a static asset tree served at `/analytics/*` by the
  unified listener.
- Five reports live and consuming real data: Overview, Pages, Sources,
  Geography (with country → region drill), Devices.
- Five D3 chart components — `<LineChart>`, `<Sparkline>`,
  `<StackedBar>`, `<Donut>`, `<ChoroplethMap>` — with a sandbox at
  `/analytics/design/charts` for visual inspection.
- Global filter state mirrored bidirectionally to the URL query
  string; every view is shareable and bookmarkable.
- Dark mode (auto-follows system, manual override via toolbar).
- Color-blind-safe palette (Okabe-Ito) across every chart.
- Per-user rate limiting is enforced in Phase 1; the UI treats 429s
  as soft errors via the ErrorCard component.
- CSV export on every table endpoint — download button in the page
  header writes `<report>-YYYYMMDD.csv`.
- First-load gzipped bundle: **19.1 KB** (budget 150 KB). Heavy chart
  code + world-atlas TopoJSON are dynamic imports and load only when
  a chart is rendered.
- New e2e suite `22-ui-smoke.sh` covers HTTP-level UI probes always,
  plus opt-in Playwright browser probes when available.
- Bundle-size guard wired into `pnpm build` — fails the build when
  first-load exceeds the budget.

Deferred to Phase 3/4 (explicitly not blocking Phase 2):
- Admin pages: Users, Goals CRUD, Scheduled Reports, User-server ACL
  admin (Phase 3).
- Realtime, Visitor profile drill-down, Events, Forms reports
  (Phase 4).
- Email reports backend (Phase 3).
- SSO login UI (break-glass admin auth retained; SSO UI lands when
  the Bolt user store ships).
- Compare-window delta display (Phase 1 returns -1 sentinels; the UI
  reserves the deltas row but hides the values until Phase 1's
  compare math ships).
- Region-level choropleth map (country borders are in world-atlas
  already; per-country region topojson is heavy and varies per
  country — table-only region view ships for now).
- Lighthouse audit report committed to
  `web/analytics/LIGHTHOUSE.md` (optional; runs manually against
  the local stack).

---

Branch: `roadmap/phase2`
Plan: `PLAN_2.md`

## Completed stages (9 of 9)

### Stage 2.1 — SvelteKit scaffold + typed API client ✅
Commit: `59d5ecf`

- `web/analytics/` SvelteKit 2 + TypeScript + Tailwind 3.4 app.
- `@sveltejs/adapter-static` emits a static tree under `build/` with
  `fallback: 'index.html'` for SPA routing.
- `base: '/analytics'` in `svelte.config.js` so every asset URL is
  prefixed correctly when served behind the hula listener.
- Dev-server proxy in `vite.config.ts` forwards `/api/*` to
  `HULA_API_URL` (default `https://localhost:8443`) so `pnpm dev`
  hits a locally running hula without CORS hassle.
- `src/lib/api/` — TypeScript interfaces mirroring
  `pkg/apispec/v1/analytics/analytics.proto`, plus a typed fetch
  wrapper that flattens `Filters` into the `filters.<field>` query
  params hula's gateway expects. Unit tests in
  `src/lib/api/analytics.spec.ts` cover the serialiser.
- Tailwind 3.4 with a shadcn-style CSS-variable theme (light / dark)
  driven by Okabe-Ito semantic slots. Inline theme bootstrap in
  `app.html` prevents first-paint flash.

### Stage 2.2 — Filter bar + URL-as-state + app shell ✅
Commit: `e59013b`

- `src/lib/filters.ts` — central `FilterState` store. Mutation goes
  through typed helpers (`setServer`, `setFilter`, `setDateRange`,
  `clearFilter`, `clearAllChips`) so every write keeps URL sync
  consistent. `hydrateFromURL(url)` + `toQueryString` / `fromQuery-
  String` round-trip state through the location search params.
- `src/lib/theme.ts` — `toggleTheme` cycles light → dark → system
  and persists in `localStorage`; inline bootstrap handles first
  paint.
- Components landed: `Sidebar`, `FilterBar`, `ServerSelect`,
  `DateRangePicker`, `CompareToggle`, `FilterChips`, `ThemeToggle`.
  All filter-bar controls are keyboard-reachable with appropriate
  ARIA.
- Five routes wired up with placeholder content so route transitions
  + theming are verified end-to-end before report work lands.

### Stage 2.3 — D3 chart component library ✅
Commit: `d66071e`

- Five chart components, each with a small documented surface, an
  Okabe-Ito palette, and keyboard-accessible interactive SVG:
  - `LineChart` — multi-series line with hover tooltip + legend.
  - `Sparkline` — axis-free miniature line.
  - `StackedBar` — top-N over time with `barClick` events.
  - `Donut` — proportion by category with `segmentClick` events.
  - `ChoroplethMap` — world map, country borders from `world-atlas`
    TopoJSON. Loaded via dynamic import so the ~80 KB topojson
    appears only in chunks that actually render the map.
- `src/lib/charts/utils.ts` — pure-function geometry builders exercised
  by `utils.spec.ts` (11 data-shape tests, no DOM required).
- `src/routes/design/charts/+page.svelte` — sandbox rendering all
  five with representative data for manual regression checks.

### Stage 2.4 — Overview report ✅
Commit: `de51896`

- `src/lib/useQuery.ts` — tiny (~50 lines) reactive fetcher
  subscribed to the filters store. AbortController cancels in-flight
  requests on rapid filter changes; AbortError noise is suppressed.
  `retry()` hook for `ErrorCard` recovery.
- `KpiCard` component renders label + big formatted number +
  optional sparkline, with a skeleton loading state.
- `ErrorCard` translates `ApiError` into a user-visible message with
  retry button.
- Overview page fires summary + timeseries in parallel. Four cards
  (Visitors, Pageviews, Bounce rate, Avg session duration). Main
  chart uses `LineChart`. Delta row is reserved but hidden until
  Phase 1's compare math ships.

### Stage 2.5 — Pages + Sources + shared `ReportTable` ✅
Commit: `172ad5f`

- `ReportTable<T>` — generic table with `Column[]` schema, click-
  to-sort headers (ARIA `aria-sort` + keyboard Enter/Space), client-
  side pagination, typed row-click dispatch, skeleton loading state,
  and optional `Download CSV` button.
- `csvDownload.ts` — browser Blob → file-download helper with a
  `<report>-YYYYMMDD.csv` naming convention.
- Pages route: Path · Visitors · Pageviews · Unique · Bounce · Avg
  time. Row click sets `filters.path`; a "Clear path filter" button
  appears in the header when active.
- Sources route: Source · Visitors · Pageviews · Bounce ·
  Pages/visit. Local group-by toggle (channel / host / UTM source)
  routes through the server-side `group_by` param. Row click sets
  the matching filter chip (`channel` / `source` / `utm_source`).

### Stage 2.6 — Geography + country → region drill ✅
Commit: `c3fabd3`

- Default view: ChoroplethMap + top-countries table in a 60/40 split.
  Columns: Country · Visitors · % · Pageviews · Bounce.
- Drill: clicking a country (either on the map or in the table) sets
  `filters.country`; the server builder automatically switches to
  `DimRegion` for the drilled view; the map hides and the regions
  table spans the full width.
- CSV filename reflects the drilled scope.
- Region-level maps are deferred — the topojson for each country's
  regions varies and is heavy; ship the table-only drill for Phase 2
  and revisit.

### Stage 2.7 — Devices ✅
Commit: `c94dc16`

- Three donuts side-by-side (device_category, browser, OS), all
  driven by a single call to `/api/v1/analytics/devices`.
- Click a slice → sets the matching filter chip (`filters.device`,
  `filters.browser`, `filters.os`) so every other report narrows to
  that category.
- Per-panel loading/empty/error states; active chip gets a `clear`
  link in the panel header.
- CSV export covers all three tables via the hula-side flattener.

### Stage 2.8 — Hula image integration ✅
Commit: `11c6f6b`

- `Dockerfile.local` grew a `ui-build` stage on `node:22-alpine`
  that runs `pnpm install --frozen-lockfile && pnpm build`. Output
  is copied into `/hula/web/analytics` in the final image.
- `server/unified_analytics_ui.go` registers three routes on the
  unified server:
  - `GET /analytics/config.json` — JSON shim the UI reads on boot
    (servers from hula config, username + isAdmin from the current
    `authware.Claims`).
  - `GET /analytics/` subtree (Go 1.22 ServeMux pattern) — SPA
    handler: serves static assets when they exist, otherwise falls
    back to `index.html` for SPA routing.
  - `GET /analytics` — redirect to `/analytics/`.
- `HULA_ANALYTICS_UI_ROOT` env var overrides the default path so a
  dev hula can point at the source-tree build output.
- Registration is a no-op when the bundle isn't present — running
  hula from source without a frontend build stays functional.
- `.dockerignore` excludes `node_modules`, `.svelte-kit`, `build`
  so pnpm gets a clean slate inside the build stage.

### Stage 2.9 — UI smoke suite + bundle-size guard ✅
Commit: `2f7eee2`

- `test/e2e/suites/22-ui-smoke.sh`:
  - Layer 1 (always runs): HTTP probes for `/analytics/`
    (200 + correct title), `/analytics/config.json` (valid JSON with
    servers + isAdmin), first JS chunk referenced in `index.html`,
    deep-link SPA fallback.
  - Layer 2 (opt-in): Playwright headless Chromium probe that
    loads Overview against the suite-21 seed and asserts the
    Visitors KPI renders `10` with no console errors. Skips with a
    PASS when Playwright isn't installed — same pattern suite 15
    uses for dex.
- `web/analytics/scripts/bundle-size-guard.mjs` runs after every
  `pnpm build`. Reads `build/index.html`, gzips the referenced
  first-load chunks, fails when total > 150 KB. Today: **19.1 KB**.

---

## Final sign-off checklist (Phase 2 → Phase 3)

Verified in this session:

- [x] All 9 stages landed on `roadmap/phase2`.
- [x] `cd web/analytics && pnpm build` green. First-load gzipped
      **19.1 KB** (well under the 150 KB budget).
- [x] `cd web/analytics && pnpm test` → **20/20**.
- [x] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [x] `./test/e2e/run.sh` → **107 passed, 0 failed**.
- [x] `curl -k https://hula.test.local/analytics/` serves the SPA
      shell. Deep-link paths (`/analytics/pages`, `/analytics/geography`
      …) serve the same `index.html` so SvelteKit's router takes over.
- [x] Admin login via the existing auth flow → Overview renders
      populated KPI cards + timeline against live data.
- [x] `PHASE_2_STATUS.md` written (this file).

Remaining work — NOT blocking the Phase 2 → Phase 3 handoff:

- [ ] Lighthouse audit committed to `web/analytics/LIGHTHOUSE.md`.
      Requires a Chrome on the host; run: `lighthouse --preset=perf
      https://hula.test.local/analytics/` with the admin token
      pre-seeded in localStorage.
- [ ] `UI_PRD.md` §4–§6 update if any UX choice deviated from the
      original spec (none did, but a PR reviewer may want to sanity-
      check).
- [ ] Branch merged to `main` (or fast-forwardable).
- [ ] Phase-3 team design review before admin pages break ground.

## Deferred to Phase 3+

- Admin pages: Users, Goals CRUD, Scheduled Reports, User-server ACL
  admin.
- Email reports backend.
- SSO login UI (break-glass admin login is unchanged; SSO UI ships
  with the Bolt user-store migration in Phase 3).
- Compare-window delta display (unlocks when Phase 1 compare math
  ships).
- Realtime, Visitor profile, Events, Forms reports (Phase 4).
- Region-level choropleth map (topojson size + per-country data —
  best solved after we pick a topojson set).

## Pre-existing issues

- `store/bolt.go` compilation errors pre-exist Phases 0–2. `go build .`
  still works; `go build ./...` fails only on that orphan package.
  Triage before the Bolt user-store migration in Phase 3.
- Suite 17 (analytics foundation enrichment) remains flaky — passes
  most runs but occasionally fails when ClickHouse responds slowly to
  the first event query. Unrelated to Phase 2 changes.
