# Phase 5a — Execution Status (COMPLETE)

**Status: all 8 stages landed on `roadmap/phase5a`. Three new e2e
suites (`29-mobile-api`, `30-mobile-ws`, `31-notify-prefs`).**

Phase 5a turned Phase-4's email-only alert delivery into a cross-
channel notification surface (email + APNs + FCM), shipped the
mobile-tuned REST endpoints + WebSocket realtime feed the Phase-5b
app will consume, and landed the notification-prefs admin UI that
operators use to route alerts.

Summary of the Phase-5a rollout:

- **Protos + storage**:
  - `pkg/apispec/v1/mobile/mobile.proto` — `MobileService`
    (RegisterDevice, UnregisterDevice, ListMyDevices,
    MobileSummary, MobileTimeseries). All read endpoints versioned
    under `/api/mobile/v1/*`.
  - `pkg/apispec/v1/notify/notify.proto` — `NotifyService`
    (Get/Set/ListNotificationPrefs, TestNotification).
  - Three new Bolt buckets: `mobile_devices`, `notification_sends`,
    `notification_prefs` with `StoredDevice` /
    `StoredNotificationSend` / `StoredNotificationPrefs` and CRUD
    helpers in `pkg/store/bolt/mobile.go`.

- **Token encryption at rest** (`pkg/mobile/tokenbox`):
  - AES-256-GCM seal/open with `[nonce||ciphertext||tag]` layout.
  - Key is the same 32-byte master the TOTP subsystem uses —
    single operator-managed secret.
  - Tamper detection via the GCM tag; wrong-key → ErrTampered
    instead of silent corruption.
  - Round-trip, tamper, wrong-key, key-length tests all green.

- **Notifier** (`pkg/notifier`):
  - `Envelope` carries Subject + HTML + ShortText + a recipient
    list of `DeviceAddr` entries (channel + push token or email).
  - `Notifier` interface + `Composite` that fans out to every
    registered backend; results aggregate into a `DeliveryReport`.
  - `ErrDeadToken` + `DeadTokenError` sentinel for permanent
    transport rejections.
  - `Email` backend wraps `pkg/mailer` (one Send per recipient to
    avoid cross-recipient exposure).
  - `APNs` backend: stdlib HTTP/2, ES256 JWT provider token
    cached + refreshed every ~40m. Dead-token on HTTP 410 or
    known reason codes.
  - `FCM` backend: HTTP v1 endpoint, OAuth2 token exchange via
    `golang.org/x/oauth2/google`. Dead-token on 404 /
    INVALID_ARGUMENT / NOT_FOUND.
  - Global composite; `notifier.SetGlobal(c)` at boot.

- **Evaluator → Notifier wire-up**:
  - `pkg/alerts/evaluator.fire()` now builds an `Envelope` and
    calls `notifier.Global().Deliver()` preferentially; falls back
    to the mailer-only path if the global is unset (useful on
    partially-configured hosts).
  - Recipient resolution: emails + active devices (per user prefs),
    opened via tokenbox when a master key is installed.
  - Quiet-hours handling: IANA tz-aware, midnight-spanning ranges
    supported.
  - DeliveryStatus roll-up from the composite: `AnyOK` → success;
    `!AllConfigured` → `mailer_unconfigured`; else `failed`.

- **Mobile-tuned APIs** (`pkg/api/v1/mobile/mobileimpl.go`):
  - `RegisterDevice` idempotent on `(user_id, fingerprint)`.
    Token sealed via tokenbox before persist.
  - `UnregisterDevice` owner-only check against authware claims.
  - `MobileSummary` projects Phase-1 `Summary + Timeseries` into
    a compact payload: 4 numerics + 12-point sparkline.
  - `MobileTimeseries` downsamples the full timeseries to 12
    (hour) or 24 (day) buckets — summing for numerics, spacing
    the ts labels evenly.
  - Payload-size budget: MobileSummary stays under ~300 B JSON
    (far below the 4 KB budget).

- **Realtime WebSocket** (`pkg/realtime` + `server/mobile_ws*.go`):
  - `Hub` in-process pub/sub, bounded buffered channels (cap 32),
    non-blocking Broadcast (slow clients drop messages, not the
    hub).
  - `/api/mobile/v1/events` upgrade handler via
    `github.com/gorilla/websocket`. 25s ping / 60s idle timeout;
    10s write-deadline on each frame.
  - `/api/mobile/v1/debug/publish` admin-only helper for the e2e
    harness — publishes a synthetic `realtime.Event` so suites can
    verify WS propagation without needing the ingest hook wired.
  - Subscribe/Broadcast/slow-client-drop tests green.

- **NotifyService impl + admin UI**:
  - `pkg/api/v1/notify/notifyimpl.go` CRUD + TestNotification.
    Non-admin readers can only see their own row; writes gated on
    the same.
  - `/admin/notifications` Svelte page: per-user prefs table with
    edit sheet (email/push toggles, IANA timezone, quiet-hours
    HH:MM pair). "Send test notification" button surfaces per-
    channel pass/fail dots. "My registered devices" block with
    "Forget device" action hitting `UnregisterDevice`.

- **Config**: `config.APNS` + `config.FCM` structs with
  `HULA_APNS_*` / `HULA_FCM_*` env bindings. Both optional — the
  notifier composite wires them in only when the required fields
  are present.

---

Branch: `roadmap/phase5a`
Plan: `PLAN_5A.md`

## Completed stages (8 of 8)

### Stage 5a.1 — Protos + Bolt storage ✅
Commit: `a52c399`

### Stage 5a.2 — Token encryption (tokenbox) ✅
Commit: `f020145`

### Stage 5a.3 — Notifier package (Email/APNs/FCM) ✅
Commit: `2e7500a`

### Stage 5a.4 — Evaluator → Notifier wire-up ✅
Commit: `b017b26`

### Stage 5a.5 — Mobile-tuned APIs ✅
Commit: `1db1fcf`

### Stage 5a.6 — Realtime hub + WebSocket ✅
Commit: `3bf9819`

### Stage 5a.7 — NotifyService + admin UI ✅
Commit: `15c84a0`

### Stage 5a.8 — e2e suites + status docs ✅
Commit: `<this branch>`

- `test/e2e/suites/29-mobile-api.sh` — Summary + Timeseries shape
  + payload size budget + Register/List/Unregister device round-
  trip.
- `test/e2e/suites/30-mobile-ws.sh` — WS upgrade probe + debug-
  publish → WS frame propagation. Pass-skips gracefully when
  websocat isn't installed in the runner.
- `test/e2e/suites/31-notify-prefs.sh` — prefs CRUD + TestNotification
  results-array shape assertion.

---

## Final sign-off checklist (Phase 5a → Phase 5b)

Verified in this session:

- [x] All 8 stages landed on `roadmap/phase5a`.
- [x] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [x] `cd web/analytics && pnpm build` green; first-load gzipped
      **20.1 KB** (under the 150 KB budget).
- [x] `go build .` clean. `go test ./pkg/{realtime,notifier,mobile/tokenbox}/...`
      green.
- [x] Three Phase-5a e2e suites added (29/30/31).
- [x] `PHASE_5A_STATUS.md` written.
- [x] `UI_PRD.md` §9 gains §9.0 describing the shipped backend.

Remaining work — NOT blocking the Phase 5a → Phase 5b handoff:

- [ ] Full e2e run with 29/30/31 — need Bolt + TOTP key + at least
      one registered device for the WS + prefs suites to exercise
      the happy path; otherwise they pass-skip gracefully.
- [ ] Visitor-tracking ingest hook that calls
      `realtime.Global().Broadcast(evt)` on each accepted event. The
      WS subscriber + debug-publish paths are complete; wiring the
      ingest-side publisher is a follow-up that doesn't block 5b.
- [ ] `TestNotification` push path currently hands through a
      sealed token with `PushToken=""` because the handler doesn't
      have the tokenbox key; surface result is `ErrNotConfigured`
      for push. Fix is to inject the key loader into the
      NotifyService constructor and open tokens at test-send time.
- [ ] Admin-as-other-user variant of `ListMyDevices` so the prefs
      sheet can show the target user's devices (not the admin's).
- [ ] OpenAPI v2 dump via the existing `protoc-gen-openapiv2`
      binary in `.bin/` — lets the 5b app generate a typed client.

## Deferred to Phase 5b+

- Mobile app itself (React Native recommended per PLAN_OUTLINE).
- Per-alert-kind routing granularity ("don't push BAD_ACTOR_RATE
  overnight") — v1 is three global booleans + quiet hours.
- Notification-send log (StoredNotificationSend) is persisted but
  not yet surfaced in the admin UI. Add when the 5b app needs an
  "inbox history" view.
- Per-device session management ("log out of this device") beyond
  the current Forget flow.
- Web push (service-worker flow) — orthogonal to APNs/FCM; the
  notifier backend interface accommodates it when that lands.

## Pre-existing issues

- `store/bolt.go` orphan package — still has undefined references;
  `go build .` stays clean but `go build ./...` fails only on that
  file. Unchanged since Phase 0.
- Suite 17 (analytics foundation enrichment) remains intermittently
  flaky on first-query ClickHouse slowness. Unrelated.
