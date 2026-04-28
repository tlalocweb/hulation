# Phase 4c — Privacy, Consent, and Forwarders (detailed plan)

A second late-cycle insert between Phase 4b (chat) and Phase 5b
(mobile app). Phase 4c closes the three compliance/forwarding gaps
that the visitor-tracking analysis surfaced — consent state on
events + GPC respect, server-side forwarders to Meta CAPI / GA4
Measurement Protocol, and an opt-in cookieless mode. None of these
change the on-the-wire shape of `events` for downstream consumers
that don't care; they're additive columns and per-server toggles.

Related docs: `PLAN_OUTLINE.md`, `BACKLOG.md` (the cookie↔email
identity item complements 4c.2 forwarder identity), the visitor-
tracking analysis in this conversation's notes.

Strategic framing: hula's already on the right side of the
post-third-party-cookie shift — first-party domain, server-side
collection, no third-party pixels. What it lacks is the
**compliance and onward-forwarding** layer the modern stack now
expects. Phase 4c lands that layer without forking the existing
data model.

---

## 1. Context and scope

### 1.1 What Phase 0–4 already delivered that 4c reuses

- **First-party tracking** — `handler/visitor.go` (Hello /
  HelloIframe / HelloNoScript) plus `scripts/hello.js` is the
  collection path. Same-origin endpoint, custom script filename
  (`hello_script_filename`) — already evades filterlist-based
  blockers. 4c only adds branches inside this path; it doesn't
  rewrite it.
- **Visitor cookies** — `handler/utils.go:GetOrSetVisitor` issues
  `<prefix>_hello` (JS-readable) + `<prefix>_helloss` (HttpOnly,
  ITP-resistant). Cookieless mode (4c.3) bypasses this function and
  derives the visitor id server-side; cookie mode is unchanged.
- **Visitor hooks** — `config.Server.Hooks.SubmitToHooksOnNewVisitor`
  + the existing webhook plumbing already fires on visitor events.
  4c.2 reuses the dispatch surface and adds typed adapters
  (`pkg/forwarder/*`) instead of duplicating event fan-out.
- **Geo enrichment** — `enrichEventFromBounce` (`handler/visitor.go:67`)
  already injects country/region/city/device/is_bot. The forwarder
  payload builders consume the enriched event, so they get geo for
  free.
- **GDPR erasure** — `pkg/api/v1/analytics/forget.go` already deletes
  per-visitor events + audits the action. 4c extends `ForgetVisitor`
  to also fan out the delete to forwarders that support it (Meta
  Deletion API, GA4 User Deletion API) and to the new
  `consent_log` audit bucket.

### 1.2 What Phase 4c must deliver

**Consent surface (stage 4c.1)**

- Two new boolean columns on `events`: `consent_analytics`,
  `consent_marketing`. Default-false in cookieless mode and any flow
  where consent isn't collected; default-true when a CMP signals
  affirmative consent.
- `Sec-GPC` HTTP header recognised at the edge as a binding opt-out
  for marketing consent (analytics may still proceed under
  legitimate-interest depending on per-server config).
- `consent` field accepted on `/v/hello` POST body, populated by the
  page's CMP via a small `window.hula.setConsent({analytics,
  marketing})` API exposed by hello.js.
- Per-server `consent_mode` config: `"opt_in"` | `"opt_out"` |
  `"off"` (default `"off"` — backwards-compatible with current
  behavior; switching to `opt_in` requires affirmative consent
  before any event row is written).
- New Bolt bucket `consent_log` capturing
  (`server_id`, `visitor_id`, `timestamp`, `state`, `source`)
  tuples for audit. Append-only; ForgetVisitor purges per visitor.

**Forwarders (stage 4c.2)**

- New package `pkg/forwarder/` with `Forwarder` interface (`Send(ctx,
  Event) error`, `Deletion(ctx, visitorID) error`) plus a
  `Composite` that fans out to every configured target.
- Two adapters: `meta_capi.go` (Meta Conversions API, hashed-PII
  payload, Pixel ID + access token), `ga4_mp.go` (GA4 Measurement
  Protocol, measurement ID + api_secret).
- Per-server `forwarders:` config list. Each entry: `kind` (one of
  `meta_capi` / `ga4_mp`), credentials (env-var indirected), event
  mapping (which hula event codes map to which forwarder events).
- Async dispatch: forwarders fire on a per-server worker queue
  (size-bounded, drop-with-warn under sustained backpressure). A
  forwarder failure must NEVER block the primary event write.
- Consent gate: events with `consent_marketing=false` are silently
  skipped by ad-platform forwarders (Meta CAPI is marketing).
  Analytics-only forwarders (none in v1) would gate on
  `consent_analytics`.
- ForgetVisitor extended: per-forwarder deletion fan-out, with
  graceful fall-through if the upstream API errors (logged, not
  retried — the local row is gone, that's the legally-required
  half).

**Cookieless mode (stage 4c.3)**

- New per-server `tracking_mode: "cookie" | "cookieless"` (default
  `"cookie"` — additive, no migration).
- Cookieless flow: the `_hello` / `_helloss` cookies are not set;
  hula derives a daily-rotating visitor id at the server from
  `HMAC-SHA256(per_server_salt || daily_epoch, IP || UA)`, truncated
  to 128 bits. Same visitor on the same day → same id; same visitor
  next day → new id. Cross-day stitching is impossible by design —
  that's the privacy property (CNIL-cleared Matomo cookieless uses
  the same shape).
- Per-server salt generated at config-validate time, stored in Bolt
  bucket `cookieless_salts`. Salt rotation is operator-triggered
  via `hulactl rotate-cookieless-salt <server_id>`; rotating the
  salt has the effect of making yesterday's visitors unrecognizable
  starting today (correct behavior for "I want everyone forgotten").
- Identity branch lives in `handler/utils.go:GetOrSetVisitor` —
  early-return derived visitor without touching cookie state.
  Downstream code paths are unchanged because they key on
  `visitor.ID` regardless of how the id was minted.
- Per-server admin UI badge: when `tracking_mode=cookieless`, the
  Visitors page shows a "Cookieless mode — daily-rotating ids,
  cross-day stitching disabled" banner so operators don't get
  confused looking for repeat visitors.

---

## 2. Stage breakdown

### Stage 4c.1 — Consent state + GPC respect

**Storage.**
- ClickHouse migration `0004_consent_v1.sql`:
  ```sql
  ALTER TABLE events
    ADD COLUMN consent_analytics UInt8 DEFAULT 0,
    ADD COLUMN consent_marketing UInt8 DEFAULT 0;
  ```
- Materialized views that aggregate `events` need recompiles only
  if they sum/group by these columns (none in v1; future filters on
  consent state get a new MV in 4c.4).
- Bolt bucket `consent_log` with `StoredConsent{ServerID, VisitorID,
  TS, Analytics bool, Marketing bool, Source string}`. Source is one
  of `"gpc_header"`, `"cmp_payload"`, `"default_opt_out"`.

**Server.**
- `handler/visitor.go:Hello` and `HelloIframe` and `HelloNoScript`:
  parse `Sec-GPC: 1` header, parse `consent` field on the body,
  resolve effective state by precedence
  (CMP-explicit > GPC-header > server-default).
- Effective state tagged onto the event via two new
  `BMIndexConsentAnalytics` / `BMIndexConsentMarketing` baton
  entries; written through `enrichEventFromBounce`.
- Per-server `consent_mode` config in `config.Server`:
  - `"off"` (default): GPC respected for marketing, analytics
    always proceeds (legitimate-interest read of pageviews).
  - `"opt_in"`: no event row written until both flags are true.
    Hello returns `204 No Content` with a hint header
    (`Hula-Consent-Required: 1`) so the client CMP can react.
  - `"opt_out"`: events always written; consent flags reflect
    state at time of write; no gating.

**Client (hello.js).**
- New `window.hula.setConsent({analytics, marketing})` API. Idempotent;
  the next `/v/hello` POST carries the latest state.
- Detect `navigator.globalPrivacyControl`; surface as
  `consent: {marketing: false, source: "gpc"}` if the CMP hasn't
  spoken yet.
- Document the integration recipe for the three common CMPs
  (Cookiebot, OneTrust, Klaro) in `DEPLOYMENT.md`.

**E2e suite 35-consent.sh.**
- POST `/v/hello` with `Sec-GPC: 1` → asserts the resulting event row
  has `consent_marketing=0`, `consent_analytics=1` (default `"off"`
  mode).
- With `consent_mode: opt_in` configured for the test server: POST
  without consent → 204 + no event row; subsequent POST with
  `{"consent":{"analytics":true,"marketing":true}}` → 200 + event
  row with both flags set.
- ForgetVisitor purges the consent_log rows alongside events.

### Stage 4c.2 — Server-side forwarders (Meta CAPI + GA4 MP)

**Package layout.**
```
pkg/forwarder/
  forwarder.go       // interface + Composite
  meta_capi.go       // Meta Conversions API adapter
  ga4_mp.go          // GA4 Measurement Protocol adapter
  worker.go          // per-server bounded queue + retry policy
  forwarder_test.go  // round-trip + consent-gate + dead-letter
```

**Wiring.**
- Boot: `server/run_unified.go` builds a `forwarder.Composite` per
  server from the new `forwarders:` config block, registers it in a
  global `forwarder.SetForServer(serverID, composite)`.
- Dispatch: `handler/visitor.go:enrichEventFromBounce` (or one
  layer up where the event is committed to ClickHouse) emits the
  event to `forwarder.GetForServer(serverID).Send(ctx, ev)` on a
  background goroutine. Bounded queue size in config (default 1024
  events per server).
- Backpressure: queue full → log a metric, drop the forward. Never
  block the visitor request. Operators see drops on the dashboard
  alongside `events_per_minute`.

**Adapter notes.**

`meta_capi.go`:
- POST `https://graph.facebook.com/v19.0/<pixel_id>/events` with
  body `{data: [{event_name, event_time, action_source: "website",
  user_data: {client_ip_address, client_user_agent, em: hashed_email,
  fbc, fbp}, custom_data: {currency, value}}]}`.
- `em` = SHA256(lowercased trimmed email) when chat-email-↔-cookie
  identity is available (Phase-4b chat sessions provide this when
  the visitor has used chat — the BACKLOG item we already filed
  becomes the upstream feed).
- `fbp` / `fbc` cookies optional, picked up if the customer wants
  Meta-pixel parity; not required.
- 24-hour token-leak detection: response code 190 → log + alert
  + suspend forwarder for that server.

`ga4_mp.go`:
- POST `https://www.google-analytics.com/mp/collect?measurement_id=
  <id>&api_secret=<secret>` with the payload shape GA4 expects
  (`{client_id, events: [{name, params: {page_location, page_title,
  ...}}]}`).
- `client_id` = visitor's hula id (v4 UUID-shape, GA4-compatible).

**Config.**
```yaml
forwarders:
  - kind: meta_capi
    pixel_id: "{{env:META_PIXEL_ID}}"
    access_token: "{{env:META_CAPI_TOKEN}}"
    test_event_code: "{{env:META_TEST_CODE,optional}}"
    event_map:
      page_view: "PageView"
      form_submit: "Lead"
  - kind: ga4_mp
    measurement_id: "{{env:GA4_MEASUREMENT_ID}}"
    api_secret: "{{env:GA4_API_SECRET}}"
    event_map:
      page_view: "page_view"
      form_submit: "generate_lead"
```

**Consent gate.** Each adapter declares which consent signals gate
its dispatch (`requires_consent: marketing` for Meta CAPI;
`requires_consent: analytics` for GA4 MP). Composite skips adapters
whose required consent is `false` on the event.

**E2e suite 36-forwarders.sh.**
- Stand up a tiny http-recorder sidecar in the docker-compose stack
  that pretends to be Meta + GA4. Configure forwarders pointing at
  the recorder. POST `/v/hello`, assert recorder saw the expected
  payload shape with the right consent signal in mind. Test that
  `consent_marketing=false` events do NOT reach the Meta endpoint
  but DO reach GA4. Test ForgetVisitor → recorder sees the deletion
  RPCs.

### Stage 4c.3 — Cookieless tracking mode

**Package.**
```
pkg/visitorid/
  cookieless.go         // Derive(salt, when, ip, ua) -> uuid
  cookieless_test.go    // determinism + day-rotation + tamper
```

`Derive` = `HMAC-SHA256(salt || day_epoch_yyyymmdd, ip || "\x00" || ua)`
truncated to 16 bytes, formatted as a v4-shaped UUID for downstream
compatibility. Same input + same day = same id; same input + next
day = new id.

**Salt management.**
- `cookieless_salts` Bolt bucket: `{server_id -> 32-byte random}`.
- Generated lazily on first cookieless event for a server; never
  re-generated automatically.
- Operator-rotated via `hulactl rotate-cookieless-salt <server_id>`
  → Phase-4c CLI command. Rotation purges yesterday's stitching
  ability (correct).

**Handler branch.**
- New helper `MintVisitor(ctx, hostconf) -> visitor`:
  - If `hostconf.TrackingMode == "cookieless"`: skip cookies, derive
    id, return a transient `model.Visitor` with that id (no
    persistent identity row).
  - Else: existing `GetOrSetVisitor` path.
- Hello / HelloIframe / HelloNoScript switch from `GetOrSetVisitor`
  to `MintVisitor`. Returned visitor flows through unchanged.
- Persistent visitor row in Bolt is skipped in cookieless mode
  (events are the source of truth; visitors are derived).
  ForgetVisitor in cookieless mode is best-effort: it deletes
  events for `visitor_id`, but a visitor returning tomorrow with a
  different derived id is intrinsically a new visitor (privacy
  property).

**UI.**
- `web/analytics/src/routes/visitors/+page.svelte`: when the
  selected server's `tracking_mode === "cookieless"`, show a
  one-line banner explaining what changed. Don't expose the salt
  or the daily-rotation mechanic — operators don't need to see it,
  and surfacing the salt would invite incorrect interpretation.

**E2e suite 37-cookieless.sh.**
- POST `/v/hello` from two different IPs → two different
  `visitor_id` values.
- POST twice from the same IP/UA → same `visitor_id` (within the
  same day).
- Mock the system clock to advance 24h, POST again → new
  `visitor_id`.
- Assert no `Set-Cookie` header on responses in cookieless mode.

### Stage 4c.4 — Status doc + sign-off

- `PHASE_4C_STATUS.md` capturing the consent-state, forwarder
  payloads, and cookieless-mode mechanics + per-stage e2e sigil.
- `DEPLOYMENT.md` updates: consent_mode operator guide, forwarders
  credentials chapter, cookieless-mode trade-offs.
- `UI_PRD.md` extended: visitors-page cookieless banner copy.
- Three new e2e suites green: 35-consent.sh, 36-forwarders.sh,
  37-cookieless.sh.

---

## 3. Cross-cutting concerns

### 3.1 Compliance posture

- Consent log is append-only, ForgetVisitor-purgeable. No "did we
  ever process this person?" gaps in the audit trail.
- GPC is treated as a binding opt-out for marketing per the
  emerging US state-law consensus (CCPA / CPRA / Colorado /
  Connecticut). Analytics-under-legitimate-interest is the
  default; operators can flip to strict opt-in via `consent_mode:
  opt_in` when EU traffic dominates.
- Cookieless mode is the answer to "what if our jurisdiction won't
  let us drop a cookie even with consent?" Salt + daily rotation
  + no persistent identity row = the configuration CNIL has
  already cleared for Matomo. Hula picks up the same shape
  intentionally.

### 3.2 Backward compatibility

- Default `consent_mode: off` means existing customers see no
  behavior change — events flow as before; the new boolean columns
  default-zero and are ignored by every existing query.
- Default `tracking_mode: cookie` means existing visitors keep
  their cookie-based identities; cookieless is opt-in per server.
- `forwarders:` defaulting to empty list means existing customers
  have no outbound traffic — opt-in only.
- ClickHouse migration is forward-only, additive. `events`
  rolling-build remains compatible with the previous version.

### 3.3 Security

- Forwarder credentials live in env vars, never in YAML. The same
  `{{env:NAME}}` indirection the existing config uses.
- Meta access tokens are device-grant-flow, expire on operator
  rotation, never written to logs (redact at capture).
- Cookieless salts are HMAC keys, treated like the OPAQUE OPRF
  seed (32 random bytes, persisted in Bolt, never logged).

### 3.4 Performance

- Forwarder dispatch is per-server async with a bounded queue.
  Sustained back-pressure drops events with a metric tick rather
  than blocking the request path. Same shape as the existing
  hooks plumbing.
- Cookieless `Derive` is one HMAC + truncate per request — sub-µs.
  No DB round-trip in the cookieless hot path; events still write
  through the same ClickHouse pipeline.
- Consent log writes go to Bolt (non-fatal on error); does not
  block the event write.

### 3.5 Migration / rollout order

The three stages are independent; recommended order:

1. **4c.1 Consent + GPC** — lands the data-model addition first so
   forwarder consent-gating in 4c.2 has columns to read. Lowest
   risk, no behavior change with default config.
2. **4c.2 Forwarders** — depends on 4c.1's consent columns; opt-in
   per server; touches the visitor hot path only via async
   dispatch.
3. **4c.3 Cookieless** — fully orthogonal to 4c.1 and 4c.2. Can ship
   in parallel if a second body is available.

Estimated calendar: 4c.1 ≈ 1 wk, 4c.2 ≈ 1.5 wk, 4c.3 ≈ 1 wk
(serial: ~3.5 wk). Both 4c.1 and 4c.3 are clean; 4c.2 has the most
external dependencies (Meta + GA4 API surfaces) and benefits from
the http-recorder sidecar in the e2e harness so we don't need real
ad-platform credentials in CI.

---

## 4. Out of scope for 4c

- B2B reverse-IP firmographics enrichment (Leadfeeder / Clearbit
  shape). Worthwhile but a separate phase — needs an enrichment
  provider integration and its own pricing dimension.
- Universal ID 2.0 / data-clean-room participation. Builds on the
  cookie↔email association already in BACKLOG; would land in a
  later phase as an opt-in identity layer.
- Multi-touch attribution / MMM / incrementality. Customer's BI
  surface, not hula's.
- HubSpot CRM forwarder. Requires the OAuth flow + property-mapping
  UX; valid as a 4c.5 follow-up if any customer asks before phase
  freezes.
