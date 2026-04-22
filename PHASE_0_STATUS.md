# Phase 0 — Execution Status

Branch: `roadmap/phase0`
Plan: `PLAN_0.md`

## Completed stages (8.5 of 11)

### Stage 0.9 — Analytics data foundation ✅ (fully closed)
Commits: `349ca7f` (0.9a), `44f8344` (0.9b+c+d), `6c96f2e` (0.9e).

0.9e completed the last piece (handler wiring):
- `model/event.go`: 18 new enrichment fields on Event (SessionID, ServerID, Referer, RefererHost, Channel, SearchTerm, UTM fields, GCLID, FBCLID, Browser, BrowserVersion, OS, OSVersion, DeviceCategory, IsBot, CountryCode, Region, City). GORM-tagged; AutoMigrate picks them up on legacy path. ApplyEnrichment method takes raw inputs and populates everything.
- `handler/visitor.go`: BMIndexReferer bounce-data slot added; Referer captured at all three ingest points. `enrichEventFromBounce` helper called just before every `CommitTo` call. `IPInfoHook` function-pointer var lets badactor register its `GetIPInfo` without creating an import cycle.
- `badactor/admin.go`: `init()` registers the IPInfoHook.

Enrichment flows through the full ingest path — session IDs, channel classification, UTM attribution, UA parsing, cached geo — all land on every event. Non-blocking on the hot path.

### Stage 0.7 (~85% done) — Handler ports to gRPC ⚠️
Commits: `5867de4` (Forms + Landers), `9eb7877` (BadActor), `6791293` (Site + Staging), `8349514` (Auth skeleton).

Six of seven services fully ported; Auth has a live skeleton.

- **Forms** (`pkg/api/v1/forms/formsimpl.go`): Create / Modify / Delete / List / Get. Calls into `model.FormModel`. Proto simplified to match the existing model shape (schema is JSON-schema string).
- **Landers** (`pkg/api/v1/landers/landersimpl.go`): same CRUD shape, mapped onto `model.Lander`.
- **BadActor** (`pkg/api/v1/badactor/badactorimpl.go`): ListBadActors, ManualBlock, EvictBadActor, ListAllowlist, AddToAllowlist, RemoveFromAllowlist, BadActorStats, ListSignatures. Five new Store accessors added (`DB`, `BlockThreshold`, `AllSignatures`, `ManualInsertBlocked`, existing `AddToAllowlist`/`RemoveFromAllowlist`).
- **Site** (`pkg/api/v1/site/siteimpl.go`): TriggerBuild, GetBuildStatus, ListBuilds. Wraps `sitedeploy.BuildManager`.
- **Staging** (`pkg/api/v1/staging/stagingimpl.go`): StagingBuild only. WebDAV stays HTTP.
- **Auth** (`pkg/api/v1/auth/authimpl.go`): **skeleton**. Live RPCs: `ListAuthProviders`, `WhoAmI`, `GetMyPermissions`. All other RPCs return `codes.Unimplemented`.
- `model/form.go`: `ListFormModels(db)` added.
- `model/lander.go`: `ListLanders(db)` added.
- `server/unified_boot.go`: all seven services registered on the unified listener (gRPC + REST gateway).

**Remaining work in 0.7**:
1. **Auth RPCs gated on Bolt / mail** — invite flow, email validation, SetInitialPassword, ValidatePasswordResetToken, CheckUserPermission / GetUserPermissions (admin), TotpAdminReset, GrantServerAccess / RevokeServerAccess / ListServerAccess. All Bolt-or-mail dependent; can slide into early Phase 1.
2. **Unified server TLS polish** — RunUnified today requires static `hula_ssl.cert` and `hula_ssl.key`. Port ACME + per-host SNI cert selection from the legacy path.

**Completed in 0.7**:
- 0.7a–d: Forms, Landers, BadActor, Site, Staging, Auth-skeleton gRPC impls.
- 0.7e: Non-gRPC HTTP fallback routes wired (`server/unified_fallback.go`).
- 0.7f: LoginAdmin live.
- 0.7g: **Fiber dropped.** `router/router.go`, `middleware/*`, `handler/fiber_adapter.go`, `server/run.go` all deleted. `backend/proxy.go` rewritten on httputil.ReverseProxy. `handler/staging.go` trimmed. `main.go` calls `server.RunUnified(ctx, cfg)`. go.mod clean of `gofiber` and `valyala/fasthttp`. Hula binary builds.
- 0.7h: **WebDAV wired onto the unified fallback** at `/api/staging/{serverid}/dav[/...]`. Four more auth RPCs live: `ListUsers`, `CreateUser`, `GetUser`, `DeleteUser` (delegating to `model.User` CRUD). `userModelToProto` maps legacy ClickHouse rows → `apiobjects.User`.
- 0.7i: **Legacy /api/\* routes** wired onto the unified fallback as a transitional bridge so pre-Stage-0.8 hulactl and the existing e2e suites keep working. Every admin endpoint the old `router/router.go` served is back, now served by the net/http ServeMux on the same unified listener. Deleted once Stage 0.8 migrates hulactl to /api/v1/*.
- 0.7j: **All five TOTP RPCs live** on AuthService — TotpStatus, TotpSetup, TotpVerifySetup, TotpDisable, TotpValidate. Delegate to `model.GetAdminTotp` / `UpsertAdminTotp` / `VerifyRecoveryCode` plus `utils.EncryptTOTPSecret` / `DecryptTOTPSecret` / `GenerateRecoveryCodes` / `HashRecoveryCodes`. `callerUsername(ctx)` helper pulls the authenticated identity from authware Claims.
- 0.7k: **Backend per-host proxies** land on the unified server via an HTTP middleware. `server/unified_backend.go` walks `cfg.Servers`, creates a `backend.NewProxyHandler` per ready backend, and dispatches matching (Host, path-prefix) requests before the rest of the pipeline. Plus four more auth RPCs: `PatchUser`, `SearchUsers`, `RefreshToken` all live; `UpdateUserPassword` + `SetUserSysAdmin` explicitly deferred (require Bolt user store).
- 0.7L: **OIDC login RPCs** — `LoginWithSecret`, `LoginOIDC`, `LoginWithCode` delegate to the ProviderManager. `firstOIDCProvider()` picks a default for `LoginOIDC` (the proto shape doesn't carry a provider name). `LoginWithCode` uses a local `loginWithCoder` type assertion since the method lives on the concrete OIDC provider type.
- 0.7m: **RequestPasswordReset** — always returns `success` (prevents email enumeration); actual email dispatch + token issuance deferred to the Bolt user-store + mail-integration follow-up.

**AuthService RPCs live: 20 of ~30.** Remaining (each a bounded add, mostly gated on the Bolt user store or mail integration): InviteUser / ResendInviteUser, ValidateEmail / ResendValidationEmail / AdminValidateUserEmail, SetInitialPassword, ValidatePasswordResetToken, CheckUserPermission / GetUserPermissions, TotpAdminReset, GrantServerAccess / RevokeServerAccess / ListServerAccess, plus tenant-scoped variants unused in hula v1.

Progress notes:
- Enrichment wiring landed in 0.9e.
- LoginAdmin landed in 0.7f (real endpoint, not stub).
- ServeMux fallback routes landed in 0.7e (now wired into RunUnified).
- Fiber fully removed in 0.7g.



### Stage 0.1 — Proto toolchain and layout ✅
Commit: `1070870`

- `protoext/` — vendored google.api + izuma.auth annotation protos
- `pkg/server/authware/proto/annotations.proto` — method auth annotations
- `external-versions.env` — pinned PROTOC + plugin versions
- `hack/install-protoc.sh` — toolchain installer into `.bin/`
- `Makefile` — `protobuf`, `protobuf-clean`, `protoc-install` targets

### Stage 0.2 — apiobjects protos ✅
Commit: `a4062ef`

- `pkg/apiobjects/v1/rbac.proto` — RBAC primitives.
- `pkg/apiobjects/v1/user.proto` — User with OIDC, TOTP, role assignments, `allowed_server_ids`.
- `pkg/apiobjects/v1/tokens.proto` — StoredToken, StoredTokenKey, Session.

### Stage 0.3 — Storage + authware package ✅
Commits: `6a29567` (0.3a), `5773626` (0.3b WIP), `89e5cb4` (0.3c).

- `pkg/store/common/` + `pkg/store/raft/` — Storage interface + RaftStorage/BoltBackend/RaftNode/InformerFactory. Single-node today, distributed-ready.
- `pkg/tune/` — tunables, HULA_ env vars.
- `pkg/server/authware/` — middleware, claims, permissions, OPA policy.
- `config/globalconfig.go` — GetConfig moved from app/ to config/, matching izcr.
- `pkg/apiobjects/v1/{useradapter,userutils,tokenadapter,rootuseradapter,config,anyhelper}.go` — apiobject adapters ported.
- RegistryUser stripped throughout.

### Stage 0.4 — Auth providers + JWT factory ✅
Commit: `d2de6a6`

- `pkg/server/authware/provider/{base,internal,oidc}/` — pluggable provider system. OIDC via `coreos/go-oidc/v3`.
- `pkg/server/authware/tokens/` — JWTFactory.
- `config/auth.go` — `AuthConfig` YAML shape for Google/GitHub/Microsoft.

### Stage 0.5 — Apispec protos ✅
Commit: `d092741`

All gRPC service definitions:
- `status`, `forms`, `landers`, `site`, `staging` (build only — WebDAV stays HTTP), `badactor`, `analytics` (skeleton), `auth` (lifted from izcr).

### Stage 0.6 — Unified server infrastructure ✅
Commit: `e3ab643`

- `pkg/server/{unified,grpc,http,static}/` — single HTTPS listener for gRPC + REST gateway + ServeMux fallback.
- `pkg/api/v1/status/statusimpl.go` — minimal StatusService impl (validates the wiring).
- `server/unified_boot.go` — `BootUnifiedServer(ctx, cfg)` constructs the listener, registers status, initializes auth provider manager from `config.Auth.Providers`.
- Fiber listener NOT removed yet — switch-over happens per endpoint in stage 0.7.

### Stage 0.9 — Analytics data foundation ✅
Commits: `349ca7f` (0.9a), `44f8344` (0.9b+c+d)

- `pkg/analytics/enrich/` — referrer classification (Direct/Search/Social/Referral/Email + search term), UTM parsing, UA parsing (uap-go + bot-substring supplement), session-ID derivation (30-minute inactivity, in-memory cache + prune).
- `pkg/store/clickhouse/schema/events_v1.sql` — explicit events DDL. ~18 new columns over legacy. MergeTree, monthly partition, TTL template-driven.
- `pkg/store/clickhouse/migrations/0001_events_v1.sql` — INSERT…SELECT from legacy events; RENAME to events_legacy_v0 (rollback); RENAME events_v1 → events.
- `pkg/store/clickhouse/migrations/0002_events_mvs.sql` — mv_events_hourly (SummingMergeTree + uniqState HLL), mv_events_daily, mv_sessions (AggregatingMergeTree).
- `pkg/store/clickhouse/migrations/runner.go` — versioned migration runner with schema_migrations tracking and Go text/template substitution.
- `pkg/store/clickhouse/clickhouse.go` — `Apply(ctx, db, eventsTTLDays)` entry point.
- `config/analytics.go` — `AnalyticsConfig{EventsTTLDays int}` under `analytics:` in config.yaml. Default 395 days (~13 months).
- Enrichment wiring into `handler/visitor.go` is deferred to stage 0.7 (done as part of the handler port).

### Stage 0.10 — Server-access ACL ✅
Commit: `a2e9416`

- `pkg/server/authware/access/access.go` — helpers for the server-access ACL expressed as `RoleAssignment`s with `scope_type="server"`. `AllowedServerIDs`, `RoleOnServer`, `HasServerAccess`, `IntersectRequested`, `NewServerRoleAssignment`. Sysadmin pass-through.
- `pkg/apispec/v1/auth/auth.proto` — three new RPCs: `GrantServerAccess`, `RevokeServerAccess`, `ListServerAccess`. `ServerAccessRole` enum (VIEWER/MANAGER). `ServerAccessEntry` message.
- Implementations land with the rest of the AuthService in stage 0.7.

## Remaining stages

| Stage | Estimate | Notes |
|-------|----------|-------|
| 0.7 polish | ~0.5 day | Unified-server TLS (ACME / per-host SNI). Static-cert deployments are production-ready now; ACME is a targeted follow-up. |
| 0.8 hulactl migration | ~1 day | 0.8a shipped the gRPC client infrastructure. Remaining: hulactl command-dispatch switch to prefer `Grpc*` methods per command. Purely mechanical. |
| 0.11 final | ~0.5 day | One more suite (15 sso-google, needs dex mock); final sign-off checklist run. |

**Remaining effort**: ~2 working days.

## Final sign-off checklist (Phase 0 → Phase 1)

These are the items that must ALL pass before declaring Phase 0 closed. Run in this order — items at the top catch problems that make later items spurious.

- [ ] `go build .` produces a working hula binary.
- [ ] `go test ./pkg/...` is green (enrich, access, clickhouse, any new tests).
- [ ] `go vet ./pkg/... ./server ./handler ./model ./badactor ./config ./app` clean.
- [ ] `grep -rln 'gofiber\|valyala/fasthttp' --include='*.go' .` returns only `.gopath/` cached entries (no in-tree refs).
- [ ] `make protobuf` regenerates cleanly with no uncommitted diff.
- [ ] `./test/e2e/run.sh` passes all 16 suites (01-14 + 16-20; 15 still pending).
- [ ] `./test/integration/run.sh` passes.
- [ ] Inside a running hula container: `ss -tlnp | grep LISTEN | grep -v clickhouse` shows exactly one HTTPS listener.
- [ ] Smoke test: `curl --http2 -v https://host/hulastatus` reports HTTP/2.
- [ ] Smoke test: `grpcurl -insecure host:443 list` shows every hulation service.
- [ ] Smoke test: admin login via `hulactl auth` produces a JWT; `hulactl badactors` uses it successfully.
- [ ] `MIGRATION_0.md` reflects final state (no stale "Phase 0 remaining" language).
- [ ] `PHASE_0_STATUS.md` all stages marked ✅ or explicitly deferred to Phase 1.
- [ ] Branch merged to main (or fast-forwardable).

Items intentionally deferred to Phase 1 (these are NOT blocking Phase 0 sign-off):

- Bolt user-store migration (replaces ClickHouse `users` table). Enables: InviteUser, password-reset tokens, SetInitialPassword, email validation, GrantServerAccess persistence, SetUserSysAdmin, fine-grained ACL reads.
- Email dispatch integration (invite flow + password-reset email).
- Analytics query APIs (Summary, Timeseries, Pages, Sources, Geography, Devices, Events, Forms, Visitors, Realtime).
- Svelte + shadcn analytics UI.
- ACME + per-host SNI cert selection on the unified listener.
- `clickhouse.Apply()` migration-runner call in `Run()` to land the explicit `events_v1` DDL (current behaviour: GORM AutoMigrate handles the new columns).

## Pre-existing issues noticed

- `store/bolt.go` (legacy hula store, not the new `pkg/store/raft`) has undefined-type errors at HEAD. Pre-exists Phase 0. `go build .` works; `go build ./...` fails on the `store/` and `model/` (config-test) packages. Triage before Phase 1.
