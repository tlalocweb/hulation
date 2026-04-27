# Phase 5b — Mobile App (detailed plan)

Phase 5a shipped the backend (mobile APIs + notifier + WS realtime +
prefs UI). Phase 5b builds the two native apps that consume it:
**iOS** in Swift/SwiftUI and **Android** in Kotlin/Jetpack Compose,
both talking to a shared **Rust core** — `libhula-mobile` — in a
separate repository.

> **Architecture constraint (user-provided):** UI stays native. Core
> logic (HTTP + WebSocket + auth + device registration + push payload
> parsing + offline queue) lives in a Rust crate distributed as an
> `.xcframework` (iOS) and an `.aar` (Android). FFI is generated,
> not hand-written — **UniFFI** is the chosen tool.

Related docs: `PLAN_OUTLINE.md` §Phase 5b, `PHASE_5A_STATUS.md`,
`UI_PRD.md` §9 (Mobile app). The Phase-5a §9.0 addendum is the
authoritative endpoint contract this phase freezes against.

---

## 1. Context and scope

### 1.1 Why Rust core + native UI

- **One source of truth** for the wire protocol, auth flow, device
  registration, push deep-link parsing, offline queue. Bugs get
  fixed once; both platforms benefit.
- **Native UX** — SwiftUI + Compose hit the platform idioms
  (haptics, accessibility, share sheets, widgets later) that a
  cross-platform RN/Flutter layer struggles with.
- **Audit surface is narrow.** The Rust crate is ~2 kLoC of I/O +
  serialization; everything else is UI and lives in the obvious
  native spot. Reviewing a single crate across iOS + Android is
  cheaper than reviewing two parallel Swift/Kotlin stacks.
- **Future leverage.** A desktop app (macOS / Linux) could reuse
  the same core. Not in scope for 5b but the architecture doesn't
  preclude it.

### 1.2 What 5b must deliver

Two shippable-to-beta-testers apps with:

- **Login** via the same SSO + admin-bearer flow the web UI uses.
- **Dashboard** — KPI cards + sparkline for the selected server.
- **Report detail** — Pages / Sources / Geography / Devices
  drill-downs paginated for phone.
- **Realtime** feed — live events via WS, with a paused-on-background
  state.
- **Alerts inbox** — list of recent fires with deep-link from push.
- **Settings** — notification prefs + registered devices + sign-out.

Plus the plumbing the apps need to exist:

- **`libhula-mobile` repo** — Rust crate + UniFFI bindings + CI that
  publishes signed `.xcframework` + `.aar` artifacts.
- **Push delivery working end-to-end** against the Phase-5a notifier
  on both platforms.
- **Deep-link routing** from notification tap → Alerts inbox
  pre-filtered on the fired alert.
- **CI for both apps** producing signed beta builds (TestFlight
  internal + Firebase App Distribution).

### 1.3 Out of scope (→ Phase 5c / 6)

- Public App Store / Play Store submission (beta only; public
  release is a decision after beta feedback).
- In-app configuration of goals / reports / alerts / users —
  **read-only** in v1 by design.
- Widgets, Shortcuts, Wear OS, CarPlay, App Clips, Instant Apps.
- In-app purchases.
- App-level E2E encryption of analytics data (TLS to hula is
  sufficient; on-device DB stays in plain SQLite inside the app
  sandbox).
- WebAuthn / passkey sign-in (SSO via OAuth flow is the path).

### 1.4 Repo layout

Three repos, coordinated via tagged releases:

```
github.com/tlalocweb/hulation        (this repo — backend, unchanged)
github.com/tlalocweb/libhula-mobile  (new — Rust core + UniFFI bindings + CI)
github.com/tlalocweb/hula-mobile     (new — contains ios/ and android/ sub-projects)
```

The `hula-mobile` repo holds both native app projects as peer
directories, not separate repos. Two apps that share an underlying
library typically end up with paired release cadence; a single repo
cuts the version-drift coordination cost.

**Dependency flow** — no cycles:

```
hulation       →   (publishes)   → OpenAPI v2 dump + proto files
libhula-mobile →   (consumes)    → OpenAPI → generated models;
                   (publishes)   → .xcframework + .aar via GitHub
                                  Releases or internal artifact store
hula-mobile    →   (consumes)    → libhula-mobile artifacts
                   (publishes)   → TestFlight + Firebase App
                                  Distribution builds
```

---

## 2. `libhula-mobile` — Rust crate design

### 2.1 Crate layout

```
libhula-mobile/
├── Cargo.toml                     # crate-type = ["cdylib", "staticlib"]
├── uniffi.toml                    # UniFFI generator config
├── src/
│   ├── lib.rs                     # uniffi::setup_scaffolding!() + module tree
│   ├── api/
│   │   ├── mod.rs                 # HulaClient — the entry point FFI sees
│   │   ├── http.rs                # reqwest-based HTTP wrapper
│   │   ├── auth.rs                # LoginAdmin / LoginWithCode flows
│   │   ├── devices.rs             # RegisterDevice / Unregister / ListMyDevices
│   │   ├── summary.rs             # MobileSummary / MobileTimeseries
│   │   ├── alerts.rs              # ListAlerts / ListAlertEvents (read-only)
│   │   ├── notify.rs              # Get/Set NotificationPrefs
│   │   └── realtime.rs            # tokio-tungstenite WS subscriber
│   ├── model/                     # serde types mirroring pkg/apispec
│   ├── push/
│   │   ├── mod.rs                 # parse_push_payload(json) → DeepLink
│   │   └── deeplink.rs            # hula://alert/<id> parser
│   ├── storage/
│   │   ├── mod.rs                 # SecureStorage trait (native implements)
│   │   └── cache.rs               # rusqlite SQLite cache for offline
│   ├── error.rs                   # thiserror enum surfaced across FFI
│   └── uniffi_bindings.udl        # only if we use UDL; else proc-macros
├── tests/
│   ├── integration_http.rs        # hits a mock hulation over httpmock
│   └── integration_cache.rs       # tempdir SQLite round-trips
└── ci/
    ├── build-ios.sh               # cargo build + lipo + xcframework
    └── build-android.sh           # cargo ndk-compile + AAR package
```

### 2.2 Public FFI surface

UniFFI turns the Rust interface into ergonomic Swift / Kotlin. The
surface is narrow on purpose — everything else stays hidden.

```rust
// Object — one per authenticated session. Native keeps a singleton.
pub struct HulaClient { … }

impl HulaClient {
    pub fn new(base_url: String, secure: Arc<dyn SecureStorage>) -> Result<Self, HulaError>;

    // --- Auth ---
    pub async fn login_admin(&self, username: String, password: String) -> Result<LoginResult, HulaError>;
    pub async fn login_with_code(&self, code: String, onetime_token: String, provider: String) -> Result<LoginResult, HulaError>;
    pub async fn list_providers(&self) -> Result<Vec<AuthProvider>, HulaError>;
    pub async fn sign_out(&self);

    // --- Summary / timeseries ---
    pub async fn mobile_summary(&self, server_id: String, preset: String) -> Result<MobileSummary, HulaError>;
    pub async fn mobile_timeseries(&self, server_id: String, preset: String, granularity: String) -> Result<MobileTimeseries, HulaError>;

    // --- Devices / push ---
    pub async fn register_device(&self, platform: Platform, push_token: String, fingerprint: String, label: Option<String>) -> Result<Device, HulaError>;
    pub async fn unregister_device(&self, device_id: String) -> Result<(), HulaError>;
    pub async fn list_my_devices(&self) -> Result<Vec<Device>, HulaError>;

    // --- Alerts ---
    pub async fn list_alerts(&self, server_id: String) -> Result<Vec<Alert>, HulaError>;
    pub async fn list_alert_events(&self, server_id: String, alert_id: String, limit: u32) -> Result<Vec<AlertEvent>, HulaError>;

    // --- Notification prefs ---
    pub async fn get_prefs(&self, user_id: String) -> Result<NotificationPrefs, HulaError>;
    pub async fn set_prefs(&self, prefs: NotificationPrefs) -> Result<NotificationPrefs, HulaError>;

    // --- Realtime subscription ---
    // Returns a handle; native polls poll_event() or hooks a callback.
    pub fn realtime_subscribe(&self, server_id: String) -> Result<Arc<RealtimeStream>, HulaError>;
}

pub struct RealtimeStream { … }

impl RealtimeStream {
    // Native calls this in a loop (or on a background thread); the
    // Rust side blocks until an event arrives or the stream closes.
    // Timeout-aware so the native side can cancel on background.
    pub async fn next_event(&self) -> Result<Option<RealtimeEvent>, HulaError>;
    pub fn close(&self);
}

// --- Push payload parsing (pure function — no client needed) ---
pub fn parse_deep_link(url: String) -> Option<DeepLink>;
pub fn parse_push_payload(json: String) -> Option<DeepLink>;
```

### 2.3 Async strategy

UniFFI 0.28+ supports Rust `async fn` directly with Swift's
`async/await` and Kotlin `suspend fun`. No blocking adapter layer.

Tokio runtime is bundled via `uniffi::runtime::tokio` feature. Single
multi-threaded runtime per `HulaClient` instance; WebSocket + HTTP
share it.

### 2.4 Secure-storage trait

The Rust crate **never** touches Keychain or Keystore directly — it
takes a native-implemented `SecureStorage` at construction. UniFFI's
`interface` mechanism generates the matching Swift protocol +
Kotlin interface.

```rust
#[uniffi::export(with_foreign)]
pub trait SecureStorage: Send + Sync {
    fn get(&self, key: String) -> Option<String>;
    fn set(&self, key: String, value: String);
    fn remove(&self, key: String);
}
```

Native implementations:
- **iOS**: Keychain via `Security.framework` — `kSecClassGenericPassword`
  with `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`.
- **Android**: `EncryptedSharedPreferences` (AES-GCM under Keystore).

This keeps Rust portable and keeps crypto decisions on the
well-audited native side.

### 2.5 Offline cache

`rusqlite` + a tiny DAO layer. Scope for v1:

- Last-known `MobileSummary` per server (keyed `(user_id, server_id,
  preset)`), with TTL. Dashboard shows cached numbers while the live
  fetch is in flight.
- Queued outbound calls for endpoints safe to retry after
  network regain: `SetNotificationPrefs`, `RegisterDevice` (token
  rotation), `UnregisterDevice`. Each queued call has an idempotency
  key so retries are safe if the first attempt did reach hula.
- Bearer token is **never** cached in SQLite — lives only in
  SecureStorage.

### 2.6 Error model

Single `HulaError` enum surfaces across FFI. Variants: `Network`,
`Timeout`, `Unauthorized`, `Forbidden`, `NotFound`, `Server(u16)`,
`Invalid(String)`, `Cancelled`. Native maps to alerts / banners.

---

## 3. FFI & distribution pipeline

### 3.1 UniFFI specifics

- Proc-macro-style bindings (no UDL file) — less surface area,
  compiler-validated.
- `#[uniffi::export]` on the `HulaClient` methods; `#[derive(uniffi::Record)]`
  on the data types; `#[derive(uniffi::Enum)]` on `HulaError` /
  `Platform`.
- The generated Kotlin ships as a `.kt` file under
  `libhula-mobile/target/uniffi/` alongside the AAR.
- The generated Swift ships as a `.swift` file packaged inside the
  `.xcframework` as a bundled resource plus a modulemap.

### 3.2 iOS packaging

`.xcframework` with three slices:

| Slice            | Architectures     | Use                             |
|------------------|-------------------|---------------------------------|
| iOS device       | arm64             | Physical iPhone / iPad          |
| iOS sim          | arm64 + x86_64    | Apple-silicon + Intel macs      |
| macOS (optional) | arm64 + x86_64    | Unused in 5b; reserved for 5c   |

Build script `ci/build-ios.sh`:
1. `cargo build --target aarch64-apple-ios --release`
2. `cargo build --target aarch64-apple-ios-sim --release`
3. `cargo build --target x86_64-apple-ios --release`
4. `lipo -create` the two sim slices into one universal `libhula_mobile.a`.
5. `xcodebuild -create-xcframework` wrapping the device + universal-sim
   libraries into `Hula.xcframework`.
6. Copy the generated Swift bindings next to it.
7. Zip + GPG-sign + publish to the release.

### 3.3 Android packaging

AAR with three arch slices (arm64-v8a + armeabi-v7a + x86_64):
1. `cargo ndk -t arm64-v8a build --release`
2. Same for `armeabi-v7a` and `x86_64` (Android emulator).
3. Gradle `buildSrc` module wraps the three `.so` files into an AAR
   with the UniFFI-generated Kotlin source set.
4. Publish to a Maven repo (GitHub Packages; or plain artifact
   upload for v1 since there's only one consumer).

### 3.4 Versioning

`libhula-mobile` tags semver (`v0.1.0`, `v0.2.0`, ...) and pins
**major** version to the Phase-5a contract. Breaking changes bump
the major; each `hula-mobile` build pins a specific version.

Compatibility window: `libhula-mobile` v0.x accepts the Phase-5a
endpoint shapes. When hula v2 ships a `/api/mobile/v2/*` surface,
`libhula-mobile` v1 drops v1 support (with a 6-month deprecation
window).

### 3.5 CI (libhula-mobile repo)

GitHub Actions matrix:

- `test` — `cargo test --all-features` on ubuntu-latest.
- `clippy` — `cargo clippy -- -D warnings`.
- `fmt` — `cargo fmt --check`.
- `ios` — on macos-14 runner. Runs the xcframework build; uploads
  artifact.
- `android` — on ubuntu-latest with Android NDK. Runs the AAR
  build; uploads artifact.
- `release` — on tag push: signs + publishes both artifacts to
  GitHub Releases.

---

## 4. iOS app (`hula-mobile/ios/`)

### 4.1 Tech stack

- **Swift 6** with strict concurrency.
- **SwiftUI** as the primary UI layer; UIKit only where SwiftUI
  doesn't yet suffice (rare for a content-first app).
- **Observable** (Swift 5.9+) for view-model state. No Combine —
  Swift Concurrency (`async/await`, `AsyncStream`) maps 1:1 to the
  Rust library's async surface.
- **Swift Package Manager** for library dependencies. `libhula-mobile`
  ships as a binary target referencing the signed `.xcframework`.
- **Deployment target**: iOS 17. Phone + iPad (adaptive layouts).

### 4.2 Dep list

Deliberately short:
- `libhula-mobile` (binary target)
- `swift-log` (structured logging)
- Apple APIs: `UserNotifications`, `BackgroundTasks`,
  `Security.framework` (Keychain)

No Alamofire, SwiftyJSON, Moya, etc. The Rust crate is the HTTP
layer; Swift never touches `URLSession` for hula endpoints.

### 4.3 Screen hierarchy

```
App root (TabView)
├── Dashboard (NavigationStack)
│   ├── ServerPicker (sheet)
│   ├── ReportDetail — Pages / Sources / Geography / Devices
│   └── RealtimeFeed (navigation push)
├── Alerts (NavigationStack)
│   ├── AlertEventList
│   └── AlertDetail
└── Settings (NavigationStack)
    ├── NotificationPrefs
    ├── DeviceList (read-only + forget-this-device)
    └── About / SignOut
```

### 4.4 Login flow

- Launch → check Keychain for valid bearer.
- If absent / expired: present `LoginView`.
  - List SSO providers via `HulaClient.list_providers()`.
  - SSO tap: open provider URL in `ASWebAuthenticationSession`;
    intercept the `hula://auth/callback?code=…&state=…` redirect;
    call `HulaClient.login_with_code()`.
  - Admin break-glass: username + password form →
    `HulaClient.login_admin()`.
- Token lands in Keychain via the `SecureStorage` trait.

### 4.5 Push notifications

- `UIApplication.didRegisterForRemoteNotificationsWithDeviceToken`
  → hex-encode → `HulaClient.register_device(.apns, token, fingerprint,
  label)`.
- `UNUserNotificationCenterDelegate.didReceive` → parse payload via
  `parse_push_payload(json)` → navigate to the deep-link target.
- Background APNs: don't fetch data from notifications in v1; just
  route on tap. (Background fetch can be a 5c add-on.)

### 4.6 Biometric re-auth

- Face ID / Touch ID required before revealing the dashboard on
  cold launch, configurable in Settings (default **off** to keep
  the first-run friction low).

---

## 5. Android app (`hula-mobile/android/`)

### 5.1 Tech stack

- **Kotlin 2.0** (K2 compiler) + coroutines for async.
- **Jetpack Compose** for UI; no view system.
- **Material 3** theming.
- **Hilt** for DI.
- `libhula-mobile` consumed via a local `libs/` AAR in v1;
  Maven-central-equivalent publishing is a follow-up.
- **Min SDK**: 26 (Android 8) — covers ~97% of active devices as
  of early 2026.

### 5.2 Dep list

- `libhula-mobile.aar`
- `androidx.compose:compose-bom:*` + runtime/ui/material3/navigation
- `androidx.security:security-crypto` (EncryptedSharedPreferences)
- `com.google.firebase:firebase-messaging` (FCM)
- `androidx.browser:browser` (Custom Tabs for SSO)

No Retrofit / OkHttp hand-configured — Rust handles HTTP.

### 5.3 Screen hierarchy

Same information architecture as iOS, rendered in Compose:

```
Scaffold(BottomNavigation)
├── DashboardScreen → NavHost with child screens
├── AlertsScreen
└── SettingsScreen
```

`ViewModel` layer holds a `HulaClient` reference, exposes `StateFlow`s
that the composables collect via `collectAsStateWithLifecycle()`.

### 5.4 Login flow

- `HulaClient.list_providers()` → render SSO buttons.
- SSO tap: launch Custom Tabs to the provider URL; listen for the
  `hula://auth/callback` redirect via an `intent-filter` on the
  MainActivity.
- Admin: same bearer flow.

### 5.5 Push notifications

- `FirebaseMessagingService.onNewToken` →
  `HulaClient.register_device(.fcm, token, ANDROID_ID-based fingerprint,
  label=Build.MODEL)`.
- `FirebaseMessagingService.onMessageReceived` when the app is
  foreground: parse + route. Background: system delivers the
  notification; tap opens MainActivity with the deep-link payload.

### 5.6 Biometric

`androidx.biometric:biometric` gates the app on cold launch when
the user opts in via Settings.

---

## 6. Endpoint → screen mapping

| Phase-5a endpoint | libhula-mobile method | iOS screen(s) | Android screen(s) |
|-------------------|------------------------|---------------|-------------------|
| `/api/v1/auth/admin` | `login_admin()` | Login | Login |
| `/api/v1/auth/providers` | `list_providers()` | Login | Login |
| `/api/v1/auth/code` | `login_with_code()` | Login (SSO callback) | Login |
| `/api/mobile/v1/summary` | `mobile_summary()` | Dashboard | Dashboard |
| `/api/mobile/v1/timeseries` | `mobile_timeseries()` | Dashboard chart | Dashboard chart |
| `/api/mobile/v1/devices` (POST) | `register_device()` | APNs token hook | FCM token hook |
| `/api/mobile/v1/devices` (GET) | `list_my_devices()` | Settings > Devices | Settings > Devices |
| `/api/mobile/v1/devices/{id}` (DELETE) | `unregister_device()` | Settings > Forget | Settings > Forget |
| `WSS /api/mobile/v1/events` | `realtime_subscribe()` → `next_event()` | Realtime feed | Realtime feed |
| `/api/v1/alerts/{server_id}` | `list_alerts()` | Alerts list | Alerts list |
| `/api/v1/alerts/{server_id}/{id}/events` | `list_alert_events()` | Alert detail | Alert detail |
| `/api/v1/notify/prefs/{user_id}` | `get_prefs()` / `set_prefs()` | Settings > Notifications | Settings > Notifications |
| `/api/v1/analytics/*` | *(not in v1)* | — | — |

Phase-1 analytics (`/pages`, `/sources`, `/geography`, `/devices`) are
**not** surfaced via the mobile-tuned API yet. A Phase-5c follow-up
adds them or the app calls the web endpoints directly; decision
deferred.

---

## 7. Stage breakdown

Serial dependencies: L1 → L2 → L3 → L6 before I1/A1 can integrate
anything. I1/A1 → rest of their platforms. Integration stages (X1,
X2) gate the beta release.

### Library stages (`libhula-mobile` repo)

### Stage 5b.L1 — Rust crate scaffold + UniFFI setup

**Goal**: empty crate that compiles to Swift + Kotlin bindings for
both platforms.

- `cargo new --lib libhula-mobile`; `crate-type = ["cdylib", "staticlib"]`.
- UniFFI proc-macro setup with one trivial `#[uniffi::export]` fn
  (`hello_world()`) to prove the pipeline.
- Cross-compile scripts produce `.xcframework` + `.aar` artifacts.
- GitHub Actions CI running `cargo test / clippy / fmt` on every
  push.

**Acceptance**: an example iOS SwiftUI app + an example Android
Compose app can call `HulaClient.hello_world()` and render the
returned string.

**Size**: 4 days.

---

### Stage 5b.L2 — HTTP client + auth

**Goal**: login + bearer-token management end-to-end.

- `reqwest` (async, rustls) wired up with a configurable base URL,
  TLS verification, and per-request JSON body.
- `HulaClient::new(base_url, secure_storage)`.
- `login_admin`, `login_with_code`, `list_providers`, `sign_out`.
- Bearer token written/read through the native `SecureStorage`
  trait. Every subsequent request appends `Authorization: Bearer`.
- `HulaError::Unauthorized` triggers a native re-login hook.
- Integration tests against `httpmock` verifying the correct
  request shapes + Authorization header behavior.

**Size**: 5 days.

---

### Stage 5b.L3 — Summary / timeseries / alerts / prefs / devices

**Goal**: every non-WS endpoint the app needs.

- Implement all methods from §2.2 except `realtime_subscribe`.
- Types derived via UniFFI from `serde`-backed structs.
- Payload-size assertions in tests: MobileSummary response
  deserialises from the exact fixture shape Phase-5a ships.
- Error mapping: HTTP 4xx → typed variants; 5xx → `Server(u16)`.

**Size**: 4 days.

---

### Stage 5b.L4 — WebSocket realtime subscriber

**Goal**: `realtime_subscribe()` returning an `AsyncStream`-friendly
handle both platforms can consume.

- `tokio-tungstenite` with TLS; same bearer header on the upgrade
  request.
- `RealtimeStream.next_event()` — async fn that surfaces
  `Ok(Some(evt))`, `Ok(None)` (stream closed cleanly), or a typed
  error.
- `close()` cancels pending reads.
- Keepalive / reconnect: library auto-reconnects with exponential
  backoff on transient disconnects (observed via a `StreamEvent`
  enum that native can choose to surface to the user as a "live /
  reconnecting" chip).
- Integration test against a local toy WS echo server.

**Size**: 4 days.

---

### Stage 5b.L5 — Push parsing + deep-link router

**Goal**: pure functions that turn push payloads into navigation
intents.

- `parse_push_payload(json) -> Option<DeepLink>`.
- `parse_deep_link(url) -> Option<DeepLink>`.
- `DeepLink` enum: `Alert(server_id, alert_id)`, `Dashboard(server_id)`,
  `Unknown`.
- Unit tests covering all Phase-5a push shapes (the evaluator's
  alert envelope) plus malformed inputs.

**Size**: 1.5 days.

---

### Stage 5b.L6 — Offline cache

**Goal**: last-known summary + outbound retry queue.

- `rusqlite` with a versioned migration file.
- `cache.put_summary(server_id, preset, summary)`, `cache.get_summary(…)`.
- `queue.enqueue(RetryableCall)`, `queue.drain(client)` that
  replays queued writes when the network recovers.
- Idempotency key on each queued call; client-side dedup so replay
  after partial failure is safe.
- Size cap (default 5 MB); LRU eviction on put when full.

**Size**: 3 days.

---

### Stage 5b.L7 — Release pipeline

**Goal**: tagged commit → signed `.xcframework` + `.aar` on the
release page.

- GitHub Actions job `release` on tag push.
- Code-signs the xcframework with a developer ID (not distribution
  cert — that's the app-bundle step, not the framework).
- Publishes artifacts with checksums; consumers verify the SHA in
  their package manifest.
- CHANGELOG.md updated per release.

**Size**: 2 days.

---

### iOS app stages (`hula-mobile/ios/`)

### Stage 5b.I1 — Xcode project + libhula-mobile integration

- New SwiftUI app target, iOS 17 deployment.
- Depend on `libhula-mobile` via SPM + binary target URL.
- Wire Keychain `SecureStorage` conformance; smoke test a
  `HulaClient.hello_world()` call.

**Acceptance**: empty-ish app launches on simulator, prints the
library's hello to the console.

**Size**: 2 days.

---

### Stage 5b.I2 — Login + SSO

- `LoginView` renders admin + provider list.
- `ASWebAuthenticationSession` for SSO flow; URL-scheme redirect to
  `hula://auth/callback`.
- Token persisted in Keychain via the `SecureStorage` impl.
- Error handling: `HulaError.Unauthorized` → show an inline banner
  + stay on LoginView.

**Size**: 3 days.

---

### Stage 5b.I3 — Dashboard

- TabView shell with Dashboard / Alerts / Settings.
- `DashboardView` shows KPI cards + sparkline (SwiftUI `Chart`).
- Pull-to-refresh.
- Server picker sheet listing the caller's allowed servers.
- Empty / loading / error states.

**Size**: 4 days.

---

### Stage 5b.I4 — Realtime + report detail

- Pull the first versions of Pages / Sources / Geography / Devices
  drill-downs (read from cached Phase-1 endpoints directly since
  the mobile-tuned variants are 5c work).
- `RealtimeFeedView` consuming an `AsyncStream<RealtimeEvent>`
  wrapped around `HulaClient.realtime_subscribe`.
- Pause on background (`.onChange(of: scenePhase)`); auto-resume on
  foreground.

**Size**: 4 days.

---

### Stage 5b.I5 — Alerts inbox + deep-link routing

- `AlertsListView` → `ListAlerts` + most recent `AlertEvent`.
- Push-tap deep-link navigates directly to the matching alert
  detail, pre-selected.
- Read-only (no edit / delete in v1 to match the "consume, don't
  configure" scope).

**Size**: 3 days.

---

### Stage 5b.I6 — APNs registration + Settings

- Request notifications permission on Dashboard first-render.
- Pass the APNs device token to `HulaClient.register_device()`.
- Settings screen: notification prefs form (`get_prefs`/`set_prefs`),
  device list, sign-out.

**Size**: 3 days.

---

### Stage 5b.I7 — Beta polish + TestFlight

- App icon, launch screen, asset catalog, localization scaffolding
  (English only for v1).
- Accessibility audit — VoiceOver labels on every interactive
  element.
- Archive + upload to App Store Connect → TestFlight internal
  group.

**Size**: 3 days.

---

### Android app stages (`hula-mobile/android/`)

Same shape as iOS. Sizes roughly parallel; some items take slightly
longer on Android due to permissions model + emulator quirks.

### Stage 5b.A1 — Project + libhula-mobile integration

- Android Studio project, Compose, Material 3.
- Include the local `.aar`; wire the UniFFI-generated Kotlin package.
- `EncryptedSharedPreferences`-backed `SecureStorage` implementation.
- Smoke test `HulaClient.hello_world()`.

**Size**: 2 days.

---

### Stage 5b.A2 — Login + SSO

- `LoginScreen` composable.
- Custom Tabs launch + `intent-filter` callback handler.
- Admin form.

**Size**: 3 days.

---

### Stage 5b.A3 — Dashboard

- `DashboardScreen` with Compose KPI cards + sparkline (use the
  built-in `Canvas` — a full charting library is overkill for a 12-
  point line).
- Pull-to-refresh, server picker, states.

**Size**: 4 days.

---

### Stage 5b.A4 — Realtime + report detail

- `RealtimeScreen` consuming a `Flow<RealtimeEvent>` backed by the
  Kotlin side-effect wrapper around the Rust stream.
- Lifecycle-aware pause/resume via `repeatOnLifecycle`.
- Report detail composables.

**Size**: 4 days.

---

### Stage 5b.A5 — Alerts inbox + deep-link

- `AlertsScreen` + deep-link activity result handling.
- Same read-only semantics as iOS.

**Size**: 3 days.

---

### Stage 5b.A6 — FCM registration + Settings

- Firebase project setup (matches the Phase-5a `HULA_FCM_PROJECT_ID`).
- `FirebaseMessagingService` registration with hula.
- `SettingsScreen` composable.

**Size**: 3 days.

---

### Stage 5b.A7 — Beta polish + Firebase App Distribution

- App icon, splash, adaptive icons.
- Accessibility audit (TalkBack).
- CI → Firebase App Distribution.

**Size**: 3 days.

---

### Cross-cutting stages

### Stage 5b.X1 — API contract freeze + OpenAPI dump

**Goal**: pin the Phase-5a endpoint shapes at `v1`.

- Run `protoc-gen-openapiv2` over the mobile + notify protos;
  publish the resulting JSON file to this repo's docs dir.
- `libhula-mobile` vendors a copy of the OpenAPI at its current
  semver; version bumps are explicit + reviewed.
- Any post-freeze hula change has to ship an additive change (new
  endpoint or optional field) — breaking changes bump the hula
  `/api/mobile/v2/` prefix, on a 6-month deprecation timeline.

**Size**: 1.5 days. Lives in the hulation repo, not libhula-mobile.

---

### Stage 5b.X2 — Integration test against staging hula

**Goal**: confirm every path works end-to-end against a running
hula, not a mock.

- `hula-mobile/integration-tests/` — Swift XCTest and Kotlin
  instrumented test targets that drive the apps through login →
  dashboard → alert → push → deep-link on a configurable staging
  host.
- Runs in CI against an ephemeral docker-compose stack (reuse
  `test/e2e/fixtures/` from this repo).
- Mobile push delivery verified end-to-end against dev APNs +
  dev FCM using throwaway credentials tied to a synthetic target.

**Size**: 4 days.

---

### Stage 5b.X3 — `PHASE_5B_STATUS.md` + v1 ship checklist

**Goal**: sign-off document + first-release checklist.

- Screens shipped vs. planned.
- Bundle sizes + cold-launch time (< 2s target on a midrange
  device).
- Known issues list.
- TestFlight + Firebase App Distribution invite lists / procedure.
- Feedback channel documented.

**Size**: 1.5 days.

---

## 8. Timeline

| Stage            | Size  | Track    | Cumulative |
|------------------|-------|----------|------------|
| 5b.L1            | 4 d   | Library  | 4          |
| 5b.L2            | 5 d   | Library  | 9          |
| 5b.L3            | 4 d   | Library  | 13         |
| 5b.L4            | 4 d   | Library  | 17         |
| 5b.L5            | 1.5 d | Library  | 18.5       |
| 5b.L6            | 3 d   | Library  | 21.5       |
| 5b.L7            | 2 d   | Library  | 23.5       |
| 5b.I1            | 2 d   | iOS      | 25.5       |
| 5b.I2            | 3 d   | iOS      | 28.5       |
| 5b.I3            | 4 d   | iOS      | 32.5       |
| 5b.I4            | 4 d   | iOS      | 36.5       |
| 5b.I5            | 3 d   | iOS      | 39.5       |
| 5b.I6            | 3 d   | iOS      | 42.5       |
| 5b.I7            | 3 d   | iOS      | 45.5       |
| 5b.A1            | 2 d   | Android  | 47.5       |
| 5b.A2            | 3 d   | Android  | 50.5       |
| 5b.A3            | 4 d   | Android  | 54.5       |
| 5b.A4            | 4 d   | Android  | 58.5       |
| 5b.A5            | 3 d   | Android  | 61.5       |
| 5b.A6            | 3 d   | Android  | 64.5       |
| 5b.A7            | 3 d   | Android  | 67.5       |
| 5b.X1            | 1.5 d | Cross    | 69         |
| 5b.X2            | 4 d   | Cross    | 73         |
| 5b.X3            | 1.5 d | Cross    | 74.5       |

Single-threaded total: **~75 working days (~15 weeks)**.

The PLAN_OUTLINE's "~4 weeks" estimate for Phase 5b assumed an
RN / Flutter single-codebase build. The Rust-core + two-native-UIs
architecture is noticeably more upfront work — the shared library
pays back when a third platform joins (desktop) or when the wire
protocol changes and both apps get the fix for free.

### 8.1 Parallelisation

With three contributors (one per track after L6 ships):

- Library: L1 → L7 single-threaded = ~23 days.
- iOS + Android run in parallel after L6 lands: iOS track ~20d,
  Android ~22d.
- Critical path: Library → longer-of-(iOS, Android) → cross-cutting.

Calendar time drops to **~8 weeks** under that staffing.

---

## 9. Risks + open items

- **UniFFI async support on iOS**: UniFFI's Swift async binding uses
  Swift structured concurrency, but older runtime versions had
  rough edges. Pinning to UniFFI 0.28+ and iOS 17 mitigates.
- **APNs + FCM credentials in CI**: beta-distribution CI needs
  signed Apple Developer + Firebase service-account secrets. Set up
  via GitHub Actions encrypted secrets scoped to the release
  workflow only.
- **Deep-link collision** with any future `hula://` schemes used
  elsewhere. Namespace explicitly: `hula://alert/<id>` +
  `hula://dashboard/<server_id>` are the two routes; everything else
  rejects.
- **WebSocket over cellular battery cost**: the app's Realtime
  screen keeps a WS open while in foreground. Backgrounding must
  close it (or the OS will kill it anyway). Re-connect on
  foreground.
- **Binary size**: an xcframework carrying a Tokio runtime + rustls
  + reqwest + SQLite is ~8–12 MB compressed. Within the App Store
  cellular-download limit (200 MB) but worth measuring once the
  cross-compile lands.
- **Keychain across reinstall**: iOS Keychain entries survive app
  reinstall by default — that's actually what we want (don't force
  a fresh login). Document it so nobody "fixes" it.
- **SSO redirect scheme registration**: both apps must declare
  `hula` as a URL scheme; Info.plist + `intent-filter` respectively.
  Matches the Phase-3 /analytics/login callback format.

---

## 10. Sign-off checklist (Phase 5b → v1 beta ship)

- [ ] `libhula-mobile` v0.1.0 tagged and artifacts published.
- [ ] iOS app archive passes `xcrun altool --validate-app`.
- [ ] Android app `assembleRelease` produces a signed APK/AAB.
- [ ] `cargo test` + `clippy -D warnings` + `fmt --check` green.
- [ ] iOS XCTest + Android instrumented tests green against the
      staging hula.
- [ ] Push-notification end-to-end verified on a physical iPhone
      and an Android phone.
- [ ] Deep-link tap from a push opens the correct Alert detail.
- [ ] Cold-launch time < 2s on iPhone 14 / Pixel 7.
- [ ] `PHASE_5B_STATUS.md` written.
- [ ] TestFlight invite list populated; first 5 testers acknowledged
      they received a build.
- [ ] Firebase App Distribution invite list populated.

---

## 11. What happens after Phase 5b

Phase 5c (not planned yet — sequencing depends on beta feedback)
options:

1. **Store submission** — App Store + Play Store public release
   with the privacy + data-handling disclosures each platform
   requires.
2. **Widgets / live activities** (iOS) — "current visitors" lock-
   screen widget, Live Activity during a detected spike.
3. **Wear OS / watchOS** companion — push notifications + KPI
   glance.
4. **In-app configuration** — move goals / reports / alerts editing
   from web-only to the mobile app. Needs UX design work; the
   "consume, don't configure" v1 scope is deliberate to keep the
   surface reviewable.
5. **Web push** from the Svelte UI — reuses the Phase-5a notifier
   plumbing + a service-worker registration in `web/analytics/`.
