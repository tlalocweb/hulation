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
1. **Auth Unimplemented RPCs** — user CRUD, TOTP, invite, password-reset, RefreshToken, OIDC login, GrantServerAccess family. LoginAdmin (0.7f) is the template: delegate to existing `model.*` helpers. Full Bolt user-store wiring is a separate effort — hula's legacy ClickHouse user table works for Phase-0 auth.
2. **Unified server TLS/routing polish** — RunUnified today requires `hula_ssl.cert` and `hula_ssl.key` (static files). Port ACME + per-host SNI cert selection from the legacy path. Wire backend per-host proxies + WebDAV route (`/api/staging/{server_id}/dav`) on the ServeMux fallback.

**Completed in 0.7**:
- 0.7a–d: Forms, Landers, BadActor, Site, Staging, Auth-skeleton gRPC impls.
- 0.7e: Non-gRPC HTTP fallback routes wired (`server/unified_fallback.go`).
- 0.7f: LoginAdmin live.
- 0.7g: **Fiber dropped.** `router/router.go`, `middleware/*`, `handler/fiber_adapter.go`, `server/run.go` all deleted. `backend/proxy.go` rewritten on httputil.ReverseProxy. `handler/staging.go` trimmed. `main.go` calls `server.RunUnified(ctx, cfg)`. go.mod clean of `gofiber` and `valyala/fasthttp`. Hula binary builds.

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

## Remaining stages (2 of 11)

| Stage | Estimate | Notes |
|-------|----------|-------|
| 0.7 finishers | 1–2 days | Auth Unimplemented RPCs (user CRUD + TOTP + invite + password-reset + RefreshToken + OIDC login + GrantServerAccess family); unified-server TLS polish (ACME / per-host SNI); register WebDAV + backend per-host proxies on the fallback mux. |
| 0.8 Migrate hulactl to gRPC clients | 2 days | After 0.7. |
| 0.11 Tests + docs + sign-off | 2 days | Update 12 existing e2e suites; add 8 new; update integration harness; DEPLOYMENT.md, test/ABOUT.md, MIGRATION_0.md. |

**Remaining effort**: ~5–6 working days.

## Recommended next-session plan

1. **Stage 0.7** — focused work for ~1 week. Start with the smallest services (status, badactor) to stabilize the unified-server + authware dispatch, then the CRUD ones (forms, landers), then the build-triggering ones (site, staging-build), ending with auth (biggest). Flip Fiber off when only non-gRPC endpoints (WebDAV, visitor, scripts, /hulastatus) remain; migrate those onto the unified ServeMux fallback and delete `handler/fiber_adapter.go` + the `gofiber`/`fasthttp` go.mod entries.

   Key wiring points to remember:
   - `server/unified_boot.go` registers services. Add one `RegisterXService` line per ported service.
   - Claims population: `pkg/server/authware/tokens/factory.go` NewJWTClaimsCommit — set `claims.AllowedServers` from `access.AllowedServerIDs(user)`.
   - Enrichment: `handler/visitor.go` Hello/HelloIframe/HelloNoScript — call `enrich.ClassifyReferrer`, `enrich.ParseUTM`, `enrich.ParseUA`, `enrich.SessionIDForVisitor` just before `ev.CommitTo(...)`. Write into events_v1 columns.
   - ClickHouse schema: call `clickhouse.Apply(ctx, db, cfg.Analytics.EventsTTLDays)` once in server startup before any event write.

2. **Stage 0.8** after 0.7 — rewrite `client/` over generated gRPC stubs, keep hulactl's on-disk `hulactl.yaml` format, token in `authorization: Bearer …` metadata.

3. **Stage 0.11** — update e2e + integration harnesses for the new `/api/v1/*` paths; new suites for grpc-smoke, rest-gateway, sso-google (dex mock), rbac, analytics-foundation, events-migration, http2, single-listener. Close with the sign-off checklist in PLAN_0.md §14.4.

## Pre-existing issues noticed

- `store/bolt.go` has references to undefined types (`FindRequest`, `FlagRequest`, etc.) causing `go build ./...` to fail at HEAD. Pre-exists Phase 0 work. Triage before Phase 1.
