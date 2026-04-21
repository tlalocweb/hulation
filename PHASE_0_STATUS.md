# Phase 0 — Execution Status

Branch: `roadmap/phase0`
Plan: `PLAN_0.md`

## Completed stages (5 of 11)

### Stage 0.1 — Proto toolchain and layout ✅
Commit: `1070870`

- `protoext/` — vendored google.api + izuma.auth annotation protos
- `pkg/server/authware/proto/annotations.proto` — method auth annotations
- `external-versions.env` — pinned PROTOC + plugin versions
- `hack/install-protoc.sh` — toolchain installer into `.bin/`
- `Makefile` — `protobuf`, `protobuf-clean`, `protoc-install` targets

**Verified**: `make protobuf` regenerates cleanly; `go build ./protoext/... ./pkg/...` exits 0.

### Stage 0.2 — apiobjects protos ✅
Commit: `a4062ef`

- `pkg/apiobjects/v1/rbac.proto` — RBAC primitives. Scope narrowed to (system, server); "tenant"/"project" reserved as future `scope_type` values via `tenant_uuid`/`project_uuid` placeholders (restored in 0.3c for authware compat).
- `pkg/apiobjects/v1/user.proto` — User with OIDC, TOTP, role assignments, password reset, email validation. `allowed_server_ids` denormalized field added.
- `pkg/apiobjects/v1/tokens.proto` — StoredToken, StoredTokenKey, Session.

### Stage 0.3 — Storage + authware package ✅
Commits: `6a29567` (0.3a), `5773626` (0.3b WIP), `89e5cb4` (0.3c).

**0.3a — Storage:**
- `pkg/store/common/` — Storage interface, verbatim from izcr.
- `pkg/store/raft/` — RaftStorage + BoltBackend + RaftNode + InformerFactory. Single-node today, multi-node path preserved (per user direction).
- `pkg/tune/` — tunables, IZCR_ env vars renamed to HULA_.
- Deps: hashicorp/raft v1.7.3, raft-boltdb, bbolt.

**0.3b — Authware copied (not yet compiling):**
- `pkg/server/authware/{middleware,claims,grpcutils,regoutils,project_access}.go`
- `pkg/server/authware/permissions/{catalog,resolver,roles}.go` + `cache/{types,cache}.go`
- `pkg/server/authware/policy/*.rego`
- `pkg/utils/hashtree.go` (MatchWithWildcards rewritten against upstream iradix).

**0.3c — Authware wired and compiling:**
- Config move: `config/globalconfig.go` exposes `GetConfig`, `InitConfig`, `ReloadConfig`, `GetConfigPath`, `GetHulaOriginHost`, `GetHulaOriginBaseUrl`, `ApplyLogTagConfig`, `SetConfigForTesting`. `app/app.go` helpers now delegate. Existing 13+ `app.GetConfig()` call sites continue to work.
- `config/config.go`: `Hostname` + `RegistryHostnames` fields added.
- apiobjects adapters ported: `useradapter.go`, `userutils.go`, `tokenadapter.go`, `rootuseradapter.go`, `rootuser.proto`, `config.go`, `anyhelper.go`.
- RegistryUser stripped (hula has no OCI registry).
- `pkg/utils/argon2.go` — argon2id helpers via `alexedwards/argon2id`.
- `pkg/utils/filter/` — copied for userutils' ListUsers.

ClickHouse remains for analytics/badactor/web-traffic (optional). Bolt is always present. Hula can run without ClickHouse.

### Stage 0.4 — Auth providers (internal + OIDC) + JWT factory ✅
Commit: `d2de6a6`

- `pkg/server/authware/provider/base/baseprovider.go` — AuthProvider interface, BaseProvider stubs, YAML config decode.
- `pkg/server/authware/provider/internal/internalprovider.go` — local username + password + TOTP.
- `pkg/server/authware/provider/oidc/oidcprovider.go` — full OIDC via `coreos/go-oidc/v3`. Handles discovery, state/PKCE, callback, claim → user mapping.
- `pkg/server/authware/provider/providers.go` — ProviderManager.
- `pkg/server/authware/tokens/{factory,token,tokens}.go` — JWTFactory from izcr's `cmd/izcrd/tokens`, relocated under authware.
- `pkg/utils/randstr.go` — from izcr.
- `config/auth.go` — `AuthConfig` + `AuthProviderConfig` config types. `Config.Auth *AuthConfig` field added.
- Deps: `coreos/go-oidc/v3 v3.18.0`, `golang.org/x/oauth2 v0.36.0`.

**Verified**: `go build ./pkg/server/authware/... ./config/...` exits 0. Providers not yet registered at runtime — wiring from config.yaml → ProviderManager is stage 0.6.

### Stage 0.5 — Apispec protos ✅
Commit: `d092741`

All gRPC service definitions for endpoints migrating to gRPC + grpc-gateway. Generated Go + gRPC + gateway stubs build cleanly.

- `status/status.proto` — Status, AuthOk.
- `forms/forms.proto` — Create / Modify / Delete / List / Get per server.
- `landers/landers.proto` — same CRUD shape.
- `site/site.proto` — TriggerBuild, GetBuildStatus, ListBuilds.
- `staging/staging.proto` — StagingBuild. **WebDAV remains HTTP.**
- `badactor/badactor.proto` — list/block/evict/allowlist/stats/signatures.
- `analytics/analytics.proto` — Phase-1 SKELETON (Summary, Timeseries, Pages, Sources, Geography, Devices, Events, Forms, Visitors, Visitor detail, Realtime).
- `auth/auth.proto` — copied from izcr, retargeted to hula imports. Kept *AsTenant RPCs intact for forward compat.

Apiobjects additions:
- `tenant.proto` — minimal `TenantRole` enum (reserved; unused in v1).
- `rbac.proto` — `ProjectRoleAssignment` restored as reserved type.

Permission vocabulary (see proto `izuma.auth.permission` annotations):
- `server.{server_id}.forms.{create,modify,delete,list,read}`
- `server.{server_id}.landers.*`
- `server.{server_id}.build.{trigger,read,list}`
- `server.{server_id}.staging.build`
- `server.{server_id}.badactor.manage`
- `server.{server_id}.analytics.read`
- `superadmin.*` (auth service)

## Remaining stages (6 of 11)

| Stage | Estimate | Notes |
|-------|----------|-------|
| 0.6 Unified server + drop Fiber | 3 days | Copy izcr's `pkg/server/{unified,grpc,http}` packages. Wire ProviderManager from config. Register gRPC services (all stubs in 0.6; real impls come in 0.7). Delete Fiber imports; migrate non-gRPC endpoints (WebDAV, visitor, scripts, /hulastatus) onto `http.ServeMux` fallback. HTTP/2 verified. |
| 0.7 Port handlers to gRPC | 5–7 days | Biggest stage. 7 services × ~3 endpoints each = ~15 endpoints. Order: status → auth → badactor → forms → landers → site → staging. Legacy `handler/{auth,forms,lander,sitedeploy,api}.go` deleted. |
| 0.8 Migrate hulactl to gRPC clients | 2 days | `client/` rewritten over generated gRPC stubs; token metadata on every call. e2e suites swap to new binary. |
| 0.9 Analytics data foundation | 3–4 days | Explicit `events_v1` DDL + migration runner. UA/referrer/UTM/session enrichment. ipinfo region+city on events. MVs hourly/daily/sessions. TTL config. **Independent of 0.6–0.8; can run in parallel with a second engineer.** |
| 0.10 user_server_access ACL + Phase-1 wiring | 1–2 days | `user_server_access` table. `AllowedServers` populated on JWT claims. GrantServerAccess / RevokeServerAccess / ListServerAccess RPCs. |
| 0.11 Tests + docs + sign-off | 2 days | Update 12 existing e2e suites; add 8 new (13-grpc-smoke, 14-rest-gateway, 15-sso-google, 16-rbac, 17-analytics-foundation, 18-events-migration, 19-http2, 20-single-listener). Integration harness. DEPLOYMENT.md, test/ABOUT.md, MIGRATION_0.md. |

**Remaining effort**: ~16–20 working days.

## Recommended next-session plan

1. **Stage 0.6 first** — unblocks 0.7. Budget ~1 day.
   - Copy `pkg/server/unified/server.go`, `pkg/server/grpc/server.go`, `pkg/server/http/gateway.go` from izcr.
   - Adapt: drop registry-hostname routing; generalize to hula's per-server site hostnames.
   - `server/run.go`: replace Fiber listener with unified server. Register a minimal StatusService gRPC impl (other services land in 0.7 as gRPC stubs that error with `codes.Unimplemented`).
   - Move non-gRPC endpoints (WebDAV, visitor `/v/*`, `/scripts/*`, `/hulastatus`, per-host site serving) onto the unified server's `http.ServeMux` fallback using `handler/nethttp_adapter.go`.
   - Delete `handler/fiber_adapter.go`; `go mod tidy` drops `gofiber/fiber/v2` and `valyala/fasthttp`.
   - ProviderManager constructed from `config.GetConfig().Auth.Providers`.
   - Smoke test: `curl --http2 -v https://host/hulastatus` returns 200 over HTTP/2; `grpcurl ... list` shows registered services.

2. **Stage 0.9 in parallel** — analytics data foundation. Touches `handler/visitor.go`, `badactor/ipinfo.go`, `model/event.go`, new `pkg/analytics/enrich/`. Independent of the gRPC migration.

3. **Stages 0.7 → 0.8 → 0.10 → 0.11** in that strict order afterward.

## Pre-existing issues noticed

- `store/bolt.go` has references to undefined types (`FindRequest`, `FlagRequest`, etc.) causing `go build ./...` to fail at HEAD. Pre-exists Phase 0 work. Unrelated but should be triaged before Phase 1.
