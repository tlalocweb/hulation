# Phase 5a — Mobile APIs & Notification Engine (detailed plan)

Phase 4 shipped the threshold-alert engine with email-only delivery.
Phase 5a turns that into a cross-channel notification surface (email
+ push) and exposes the mobile-tuned REST/WebSocket surface the
Phase-5b mobile app will consume. No mobile app is built here —
that's 5b; this phase delivers the backend the app will connect to
plus the notification prefs UI that operators use to route alerts.

Related docs: `PLAN_OUTLINE.md` §Phase 5a, `PHASE_4_STATUS.md`,
`UI_PRD.md` §8.2 (Alerts evaluator) + §9 (Mobile app scope).

---

## 1. Context and scope

### 1.1 What Phase 0–4 already delivered

- **Alert evaluator** (`pkg/alerts/evaluator`) with five predicate
  kinds + cooldown + throttle + delivery-status enum that already
  carries a `RETRYING` state reserved for the Phase-5a push-retry
  machinery.
- **Mailer** (`pkg/mailer`) — clean `Send(ctx, Message) error`
  interface. Phase 5a wraps this in a Notifier that also speaks
  APNs + FCM.
- **Bolt store** with nine buckets. Phase 5a adds three:
  `mobile_devices`, `notification_sends`, `notification_prefs`.
- **Unified HTTPS listener** — REST + gRPC + ServeMux fallback.
  WebSocket endpoints register on the ServeMux fallback via the
  same mechanism visitor-tracking uses (no gRPC streaming
  complication).
- **Admin UI** shell with four admin pages; the notification-prefs
  UI adds a fifth.

### 1.2 What Phase 5a must deliver

**Storage + types:**
- Three new Bolt buckets with accessors: `mobile_devices`,
  `notification_sends`, `notification_prefs`.
- `StoredDevice` carries APNs/FCM tokens (encrypted at rest — reuse
  the TOTP key machinery).

**Protos + REST:**
- `pkg/apispec/v1/mobile/mobile.proto` — Register / Unregister /
  Summary / Timeseries.
- `pkg/apispec/v1/notify/notify.proto` — NotificationPreferences
  CRUD + TestNotification (admin-only; fires a test push).

**Notifier package:**
- `pkg/notifier/notifier.go` — `Notifier` interface (`Deliver(ctx,
  Envelope) error`) + composite implementation that fans out to
  Email + APNs + FCM.
- `pkg/notifier/apns` + `pkg/notifier/fcm` backends (stdlib HTTP to
  APNs HTTP/2 endpoint + FCM HTTP v1 endpoint; no vendor SDKs — keeps
  the surface tight + auditable).
- Dead-token handling: on `410 Gone` (APNs) or `INVALID_ARGUMENT`
  (FCM), mark the device unregistered and stop sending.

**Evaluator integration:**
- Phase-4 evaluator currently calls `mailer.Send` directly. After
  5a, it calls `notifier.Global().Deliver(envelope)`. The envelope
  carries the recipient list (resolved from notification_prefs) +
  per-channel body variants (short-form for push, HTML for email).
- `DeliveryStatus` enum gets populated with `RETRYING` on first
  push transport error; the notifier handles backoff internally.

**Realtime WebSocket:**
- `/api/mobile/v1/events` authenticated WebSocket. Streams live
  event objects shaped like `VisitorEvent` — the mobile app's
  realtime feed page consumes this.
- Implementation: `gorilla/websocket` + a small in-process pub/sub
  (one goroutine per connection; broadcast via buffered channels
  from the ingest hook).

**Admin UI:**
- `/admin/notifications` — per-user preferences table: email ·
  push · quiet-hours start/end · timezone. Row click opens a sheet
  with a "Test notification" button.

**Documentation:**
- `docs/mobile-api.md` — stable contract (or OpenAPI v2 via the
  existing grpc-gateway `--openapiv2_out` plugin already installed
  in `.bin/`).

**Out of scope (→ Phase 5b / 6):**
- The mobile app itself (React Native / Flutter — 5b).
- Web push (service-worker flow in the Svelte UI).
- Per-alert-type routing granularity (current model: "push"/"email"
  are global flags; fine-grained routing ships with 5b's UI work).
- Multi-device per-user encryption key rotation (needed when device
  tokens leak — follow-up).

---

## 2. Stage breakdown

Size targets below are standalone PR-sized slices. Stages 5a.1–5a.4
are serial (proto → storage → notifier → evaluator wire-up). 5a.5
and 5a.7 can fork off in parallel.

### Stage 5a.1 — Protos + Bolt storage

**Goal**: contract + persistence surface.

- `pkg/apispec/v1/mobile/mobile.proto` — `MobileService`:
  - `RegisterDevice(RegisterDeviceRequest) → Device`
  - `UnregisterDevice(UnregisterDeviceRequest) → ack`
  - `MobileSummary(MobileSummaryRequest) → MobileSummaryResponse`
    (compact — 4 KPIs + 12 sparkline points)
  - `MobileTimeseries(MobileTimeseriesRequest) → MobileTimeseriesResponse`
- `pkg/apispec/v1/notify/notify.proto` — `NotifyService`:
  - `GetNotificationPrefs` / `SetNotificationPrefs`
  - `TestNotification` (admin — fires a synthetic envelope to the
    caller's email + push endpoints).
- `pkg/store/bolt/mobile.go`: `StoredDevice` / `StoredNotificationSend`
  / `StoredNotificationPrefs` + Put/Get/Delete/List.
- `make protobuf` to regen Go + gateway.

**Acceptance**: `go build .` clean; suites 29/30 (next stage skeleton)
compile.

**Size**: 1.5 days.

---

### Stage 5a.2 — Device token encryption

**Goal**: never store APNs/FCM tokens in plaintext at rest.

- `pkg/mobile/tokenbox/tokenbox.go` — Seal / Open helpers wrapping
  `crypto/aes` GCM. Key derived from the same master TOTP key used
  by `authware` (single key reduces operator surface).
- `StoredDevice.TokenCipherBlob []byte` replaces plaintext
  `APNsToken` / `FCMToken` strings at the storage boundary.
- `RegisterDevice` accepts a plaintext token in the request (it's
  arriving over TLS already); seals before persisting; opens on
  the way to the notifier.
- Unit tests: round-trip + tamper detection (GCM authentication
  prevents silent corruption).

**Acceptance**: `go test ./pkg/mobile/tokenbox/...` green; no
plaintext token appears in the Raft FSM bbolt file after registration.

**Size**: 1 day.

---

### Stage 5a.3 — Notifier package (APNs + FCM backends)

**Goal**: cross-channel delivery with dead-token handling.

- `pkg/notifier/notifier.go`:
  - `type Envelope struct { Subject, HTML, ShortText string;
    Recipients []DeviceAddr }`
  - `type DeviceAddr struct { Email; PushToken; Platform
    ("apns" | "fcm"); UserID }`
  - `type Notifier interface { Deliver(ctx, Envelope)
    (DeliveryReport, error) }`
  - Composite `MultiNotifier` — fans out to registered backends;
    aggregates per-recipient results into a `DeliveryReport`.
- `pkg/notifier/apns/apns.go` — HTTP/2 to `api.push.apple.com`.
  JWT-signed (ES256) header token; rotates every ~40m.
- `pkg/notifier/fcm/fcm.go` — HTTP POST to the FCM v1 endpoint with
  an OAuth2 service-account token.
- Dead-token sentinel: `errors.Is(err, notifier.ErrDeadToken)` →
  caller marks the device row unregistered.
- Reuses `pkg/mailer` as the email backend (no rewrite).

**Acceptance**: unit tests stub the transport and assert the correct
headers + body; one integration test that hits a real APNs sandbox
(opt-in when credentials are available).

**Size**: 3 days.

---

### Stage 5a.4 — Evaluator → Notifier wire-up

**Goal**: Phase-4 evaluator uses the Notifier instead of the mailer
directly.

- `pkg/alerts/evaluator/evaluator.go` — `fire()` now builds an
  `Envelope` (subject + HTML + short text derived from the alert
  kind) and calls `notifier.Global().Deliver()`.
- Recipient list resolution:
  1. Start with `Alert.Recipients` (emails).
  2. For each recipient email, look up the user + their devices in
     Bolt; append `DeviceAddr` entries for enabled push channels.
  3. Apply `NotificationPrefs` (quiet hours, muted kinds) at the
     envelope boundary.
- `DeliveryStatus` on the `StoredAlertEvent` becomes a composite
  summary (`success` when any channel delivered; `retrying` when
  only transient errors; `failed` when all channels rejected).

**Acceptance**: suite 27 still passes (evaluator still fires + writes
AlertEvent row). New `27b-alerts-push.sh` suite asserts a push
envelope is handed to the notifier when a device is registered.

**Size**: 2 days.

---

### Stage 5a.5 — Mobile-tuned APIs

**Goal**: compact JSON payloads sized for a phone screen.

- `pkg/api/v1/mobile/mobileimpl.go`:
  - `MobileSummary` returns `{visitors, pageviews,
    bounce_rate, avg_session_duration, sparkline: [12 numbers]}`
    — deliberately tiny.
  - `MobileTimeseries` buckets to 24 datapoints for daily views; 12
    for hourly.
- Both reuse the Phase-1 query builder; the mobile layer is a
  projection, not a re-query.
- Versioning: endpoints sit under `/api/mobile/v1/*` so future
  breaking changes increment `v2` without breaking installed apps.

**Acceptance**: `28-mobile-api.sh` (now: `29`) asserts
payload shape + byte size < 4 KB for Summary.

**Size**: 1.5 days.

---

### Stage 5a.6 — WebSocket realtime feed

**Goal**: `/api/mobile/v1/events` streams live events.

- `server/mobile_ws.go` — `gorilla/websocket` upgrade handler. Auth
  via the `Authorization: Bearer` header on the upgrade request.
- Pub/sub: one goroutine holds a bounded buffered channel per
  connection; the visitor-tracking ingest hook broadcasts each new
  event through a central fan-out (non-blocking — slow clients get
  their buffer filled + connection closed).
- Keepalive: ping/pong every 25s; idle timeout 60s.
- Metrics: `active_ws_connections` gauge + `ws_messages_sent` counter
  exposed on `/hulastatus`.

**Acceptance**: suite 30 opens a WS, injects a synthetic event via
`/v/hello`, asserts the WS frame arrives within 1s.

**Size**: 2.5 days.

---

### Stage 5a.7 — Notification prefs UI

**Goal**: `/admin/notifications` page + per-user prefs.

- `web/analytics/src/lib/api/notify.ts` — typed wrappers.
- `/admin/notifications` page: table (user email · push-enabled ·
  email-enabled · quiet hours · timezone · last-device-seen).
- Row click → sheet with:
  - Email/Push toggles.
  - Quiet-hours start/end time pickers (IANA tz-aware).
  - Device list (platform + last-seen timestamp) with a
    "forget device" button that triggers Unregister.
  - "Send test notification" button → calls `TestNotification`.
- Default prefs for new users: email on, push off until a device
  registers, no quiet hours.

**Acceptance**: `pnpm check` clean; bundle stays within the 150 KB
first-load budget. Suite 31 smoke-tests CRUD shape.

**Size**: 2 days.

---

### Stage 5a.8 — e2e suites 29/30/31 + `PHASE_5A_STATUS.md`

**Goal**: regression coverage + sign-off.

- `test/e2e/suites/29-mobile-api.sh` — Register / Summary /
  Timeseries / payload-size guard.
- `test/e2e/suites/30-mobile-ws.sh` — WS upgrade + event
  propagation round-trip.
- `test/e2e/suites/31-notify-prefs.sh` — prefs CRUD + TestNotification
  response.
- Legacy 27-alerts-fire stays green (evaluator still writes events
  after the notifier switch).
- `PHASE_5A_STATUS.md` mirrors Phase-4 status layout.
- `UI_PRD.md` §9 (Mobile app) gets a new §9.0 subsection describing
  the backend surface the 5b app consumes.

**Acceptance**:
- `./test/e2e/run.sh` → 116+/116+ green.
- `PHASE_5A_STATUS.md` complete.

**Size**: 1.5 days.

---

## 3. Timeline

| Stage | Size   | Cumulative |
|-------|--------|------------|
| 5a.1  | 1.5 d  | 1.5        |
| 5a.2  | 1 d    | 2.5        |
| 5a.3  | 3 d    | 5.5        |
| 5a.4  | 2 d    | 7.5        |
| 5a.5  | 1.5 d  | 9          |
| 5a.6  | 2.5 d  | 11.5       |
| 5a.7  | 2 d    | 13.5       |
| 5a.8  | 1.5 d  | 15         |

Total: **15 working days** (~3 weeks). Matches the outline's 2-week
estimate within tolerance; the extra ~5 days are the APNs/FCM
backend (underestimated in the outline) and the WebSocket fan-out.

Single-threaded runs 15d; two-contributor split (one on 5a.1–5a.4 +
5a.6, one on 5a.5 + 5a.7) compresses to ~10 calendar days.

---

## 4. Risks + open items

- **APNs provisioning**: staging-env APNs cert/key pair may not be
  easy to come by during CI; suite 30 pass-skips when
  `HULA_APNS_TEAM_ID`/`HULA_APNS_KEY_PATH` envs are absent.
- **FCM service-account secret size**: ~2 KB JSON; encrypt at rest
  alongside device tokens — do not drop it in a plain config.
- **WS backpressure**: a bursty visitor-tracking stream can saturate
  slow clients. Current design drops the connection on buffer
  overflow (defensible — the mobile app reconnects). If that turns
  out too aggressive, swap to coalescing ("you missed N events").
- **Push payload size**: APNs caps at 4 KB, FCM at 4 KB for data
  messages. The Envelope's `ShortText` field must stay under 200
  chars for margins; evaluator's rendered short-text already fits.
- **Token rotation**: FCM tokens change over time on the client; the
  mobile app (5b) needs a refresh loop that calls `RegisterDevice`
  on every app open. Server-side: accept re-registration as
  idempotent keyed by `(user_id, device_fingerprint)`.

---

## 5. Sign-off checklist (Phase 5a → Phase 5b)

- [ ] All 8 stages landed on `roadmap/phase5a`.
- [ ] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [ ] `cd web/analytics && pnpm build` green; first-load gzipped
      under 150 KB.
- [ ] `go build ./...` clean (or `go build .` + known-orphan
      `store/bolt.go` documented).
- [ ] `./test/e2e/run.sh` → 116+/116+ green (+3 suites).
- [ ] `PHASE_5A_STATUS.md` written.
- [ ] `UI_PRD.md` §9 updated to reference the shipped backend.
- [ ] `docs/mobile-api.md` (or OpenAPI output) published.

---

## 6. What happens after Phase 5a

Phase 5b builds the mobile app (React Native recommended) against the
surface this phase delivers. The app's screens — Login / Dashboard /
Report / Realtime / Alerts — each map to one or two Phase-5a
endpoints, with push via the APNs/FCM backends already in place.
No Phase-5a code changes should be needed for 5b — the contract is
the freeze point.
