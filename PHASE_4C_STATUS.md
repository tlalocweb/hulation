# Phase 4c — Execution Status (COMPLETE)

**Status: all 4 stages landed on `feature/compliance-fixes`. Three new
e2e suites (`35-consent`, `36-forwarders`, `37-cookieless`) and four
new test files (one for `handler.resolveConsent`, one each for the
`pkg/forwarder` and `pkg/visitorid` packages).**

Phase 4c closed the three compliance/forwarding gaps identified in
the visitor-tracking analysis: hula now records consent state on
events, respects the `Sec-GPC` header as a binding marketing opt-out,
fans out completed events to operator-configured Meta CAPI / GA4 MP
endpoints, and supports a per-server cookieless tracking mode that
matches the shape CNIL cleared Matomo on.

Summary of the Phase 4c rollout:

- **Storage + types**:
  - ClickHouse migration `0004_consent_v1.sql` adds two `UInt8`
    columns to the `events` table: `consent_analytics`,
    `consent_marketing`. Idempotent `ALTER TABLE ADD COLUMN IF NOT
    EXISTS` so existing deployments pick them up without recreating
    the table; `schema/events_v1.sql` updated to mirror for fresh
    deployments.
  - Two new Bolt buckets: `consent_log` (audit trail of consent
    decisions, keyed by `server_id|visitor_id|ts`) and
    `cookieless_salts` (32-byte HMAC keys, one per server,
    treated as cryptographic secrets).
  - `model.Event` gained `ConsentAnalytics` + `ConsentMarketing`
    bool fields; GORM column tags map to the new ClickHouse
    columns.

- **Consent resolution** (`handler/consent.go`):
  - `resolveConsent(hostconf, body, gpcHeader)` collapses three
    inputs (per-server `consent_mode`, optional `Sec-GPC: 1`
    header, optional CMP-supplied `consent` body field) into a
    single `ConsentDecision{State, Block}`.
  - Precedence: **CMP body > Sec-GPC header > per-server default**.
  - Three modes: `off` (default — analytics always on, GPC binds
    marketing), `opt_in` (events gated until affirmative consent
    arrives — server returns 204 + `Hula-Consent-Required: 1`),
    `opt_out` (events always written, consent flags reflect state).
  - 12-case precedence matrix in `handler/consent_test.go`.

- **Visitor handlers** (`handler/visitor.go`):
  - `Hello`, `HelloIframe`, `HelloNoScript` now resolve consent at
    request entry, stash the decision on the bounce, and apply it
    when committing the event.
  - In `opt_in` mode without affirmative consent, `Hello` returns
    204 with the hint header; the iframe handler still serves the
    iframe HTML so the embedded `hello.js` can later POST with a
    consent payload.
  - Consent-log audit row written on every event commit.

- **Client-side** (`scripts/hello.js`):
  - GPC detection on script load (`navigator.globalPrivacyControl`).
  - `window.Hula.setConsent({analytics, marketing})` API for CMP
    integration; `window.Hula.getConsent()` for introspection.
  - `/v/hello` POST body now carries the `consent` field when known.

- **Server-side forwarders** (`pkg/forwarder/`):
  - `Adapter` interface: `Name() / Purpose() / Send() / Delete()`.
  - `Composite` fans out concurrently with consent gating;
    adapters whose `Purpose` is unsatisfied by the event's consent
    flags are silently skipped.
  - `Worker` provides a per-server bounded queue with
    `DefaultQueueSize = 1024` and `DefaultSendTimeout = 5s`.
    Backpressure: queue full → `dropped` counter ticks, request
    path never blocks.
  - `MetaCAPI` adapter — POST to
    `https://graph.facebook.com/v19.0/<pixel_id>/events`. SHA256
    hashes email (lowercased + trimmed) and visitor_id; IP + UA
    sent in the clear (Meta hashes server-side). `test_event_code`
    optional. Purpose: `marketing`.
  - `GA4MP` adapter — POST to GA4 Measurement Protocol with
    `client_id` + events array. Purpose: `analytics`. `Endpoint`
    overridable (used by suite 36's http-recorder sidecar).
  - `BuildAndRegisterAll(servers)` wires forwarders at boot from
    `config.Server.Forwarders []*ForwarderConfig`.
  - `ForgetVisitor` (Phase-3 GDPR RPC) extended to fan out
    deletion to every forwarder via `Composite.Delete` —
    consent does NOT gate deletion (privacy floor, not gate).

- **Cookieless tracking mode** (`pkg/visitorid/`):
  - `Derive(salt, day, ip, ua) -> v4-shape UUID` =
    `HMAC-SHA256(salt, day || 0x00 || ip || 0x00 || ua)[:16]`,
    with v4 version + variant bits set so downstream consumers
    (GA4 MP `client_id`, ClickHouse `belongs_to`) accept it.
  - `DeriveNow` uses `time.Now().UTC()` for the day key.
  - 9-case test suite covers determinism, day rotation, IP/UA
    separation, NUL-domain-separation, salt rotation, bad inputs,
    UUID-shape conformance, UTC-zone DST safety.
  - `pkg/store/bolt/cookieless.go` provides
    `GetOrCreateCookielessSalt` (lazy 32-byte gen on first read),
    `RotateCookielessSalt`, `DeleteCookielessSalt`.
  - `handler.MintVisitor(ctx, hostconf, baton)` is the unified
    visitor-mint helper: branches on `hostconf.TrackingMode`. In
    `cookieless` mode it skips the cookie handshake entirely and
    derives a transient visitor id from (per-server salt, day,
    IP, UA). `cookie` mode delegates to the existing
    `GetOrSetVisitor`. `HelloIframe` and `HelloNoScript` now call
    `MintVisitor` instead of `GetOrSetVisitor` directly.

- **CLI**:
  - `hulactl rotate-cookieless-salt <server_id>` (`--bolt <path>`
    required) replaces a server's salt with 32 fresh random
    bytes. Yesterday's visitors become unrecognisable today —
    intentional, this is the "wipe everyone" operator action.
    Same offline-edit pattern as `forget-opaque-record` (hula
    must be stopped, caller copies the bolt out, runs the
    command, copies it back).

- **GDPR fan-out**:
  - `ForgetVisitor` now also `DeleteConsentForVisitor` (consent_log)
    and `forwarder.GetForServer(...).Delete()` (per-adapter
    deletion fan-out). Best-effort like the existing MV +
    chat-table deletes; the events `ALTER ... DELETE` remains the
    primary obligation.

## Sub-component diagram

```
                   /v/hula_hello.html (iframe)
                            |
                            v
                   handler.HelloIframe
                            |
                            +-- resolveConsent(GPC, mode)
                            |         (4c.1)
                            |
                            +-- MintVisitor(...)
                            |         (4c.3)  picks cookie | derived id
                            |
                            +-- enrich + ev.CommitTo(events) ─-> ClickHouse
                            |                                       (consent cols + visitor_id)
                            |
                            +-- recordConsent(...)                ─-> Bolt consent_log
                            |
                            +-- dispatchForwarder(...)            ─-> Worker queue
                                                                       (4c.2)
                                                                       async drain
                                                                            |
                                                                            v
                                                                    Composite.Send
                                                                            |
                                                                  +---------+---------+
                                                                  |                   |
                                                          Meta CAPI (mkt)     GA4 MP (analytics)
                                                          (consent gated)     (consent gated)
```

## E2e coverage

| Suite | Verifies |
|---|---|
| `35-consent.sh` | Sec-GPC: 1 → marketing=0; CMP body overrides; opt_in 204 hint header (when fixture provides) |
| `36-forwarders.sh` | http-recorder sidecar receives `/collect` POST with shape `{client_id, events:[{name:"page_view"}]}`; consent gate works |
| `37-cookieless.sh` | no Set-Cookie in cookieless mode; same UA → same visitor_id within day; different UA → different id; CLI command recognised |

## Files changed

### New files

- `PLAN_4C.md` — design doc for this phase
- `pkg/store/clickhouse/migrations/0004_consent_v1.sql`
- `pkg/store/bolt/consent.go` (+ bucket entry in `bolt.go`)
- `pkg/store/bolt/cookieless.go` (+ bucket entry in `bolt.go`)
- `handler/consent.go` + `handler/consent_test.go`
- `pkg/forwarder/forwarder.go`
- `pkg/forwarder/worker.go`
- `pkg/forwarder/meta_capi.go`
- `pkg/forwarder/ga4_mp.go`
- `pkg/forwarder/boot.go`
- `pkg/forwarder/forwarder_test.go`
- `pkg/visitorid/cookieless.go` + `pkg/visitorid/cookieless_test.go`
- `config/forwarder.go`
- `test/e2e/suites/35-consent.sh`
- `test/e2e/suites/36-forwarders.sh`
- `test/e2e/suites/37-cookieless.sh`

### Edited files

- `pkg/store/clickhouse/schema/events_v1.sql` (consent cols mirrored)
- `model/event.go` (ConsentAnalytics + ConsentMarketing fields)
- `config/config.go` (Server.ConsentMode, Server.Forwarders, Server.TrackingMode)
- `handler/visitor.go` (HelloMsg.Consent, BMIndexConsentDecision, dispatch hooks)
- `handler/utils.go` (MintVisitor wrapper)
- `scripts/hello.js` (GPC + setConsent + body payload)
- `pkg/api/v1/analytics/forget.go` (consent_log + forwarder fan-out)
- `server/run_unified.go` (forwarder boot wiring)
- `model/tools/hulactl/main.go` + `cmddef.go` (rotate-cookieless-salt)
- `test/e2e/fixtures/docker-compose.yaml` (forwarder-recorder sidecar; runner /etc/hosts entries)
- `test/e2e/fixtures/hula-config.yaml.tmpl` (testsite-seed + testsite-seed-cookieless servers)

## Backwards compatibility

Every Phase 4c change is opt-in:
- `consent_mode` defaults to `off` → existing customers see no
  change in event volume or shape.
- `forwarders:` defaults to empty list → no outbound traffic.
- `tracking_mode` defaults to `cookie` → existing visitor cookies
  keep working unchanged.
- ClickHouse migration is additive (`ADD COLUMN IF NOT EXISTS`);
  existing queries that don't reference the new columns are
  unaffected.

## How to verify

1. Ship to staging with default config — no behavior change observed.
2. Flip `consent_mode: off → opt_in` for a test server; reload
   hula; visit a page with no CMP — `/v/hello` returns 204 +
   `Hula-Consent-Required: 1`. Visit with CMP-supplied affirmative
   consent — event row lands with both flags = 1.
3. Add a `forwarders:` block pointing at the http-recorder sidecar;
   verify boot log `forwarder registered: server=... kind=ga4_mp`
   and the recorder receives a `/collect` POST per page-view.
4. Flip `tracking_mode: cookie → cookieless` for a test server;
   verify response headers contain no `Set-Cookie`; verify two
   page-views from the same UA on the same day produce the same
   `belongs_to`.
5. Run `hulactl --bolt <path> rotate-cookieless-salt <id>`;
   yesterday's visitors no longer recognised today.

Phase 4c is signed off. Next is Phase 5b (mobile app) per
`PLAN_OUTLINE.md`.
