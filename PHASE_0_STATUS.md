# Phase 0 — Execution Status

Branch: `roadmap/phase0`
Plan: `PLAN_0.md`

## Completed stages

### Stage 0.1 — Proto toolchain and layout ✅
Commit: `1070870`

- `protoext/` — vendored google.api + izuma.auth annotation protos
- `pkg/server/authware/proto/annotations.proto` — method auth annotations
- `external-versions.env` — pinned PROTOC + plugin versions
- `hack/install-protoc.sh` — toolchain installer into `.bin/`
- `Makefile` — `protobuf`, `protobuf-clean`, `protoc-install` targets

**Verification**: `make protobuf` regenerates cleanly. `go build ./protoext/... ./pkg/...` exits 0.

### Stage 0.2 — apiobjects protos ✅
Commit: `a4062ef`

- `pkg/apiobjects/v1/rbac.proto` — RBAC primitives (AuthProviderRef, PrincipalRef, RoleDefinition, RoleAssignment, PermissionGrant, PermissionSource). Scope narrowed to (system, server); "tenant"/"project" reserved as future `scope_type` values.
- `pkg/apiobjects/v1/user.proto` — User message with OIDC identity, TOTP, role assignments, password reset, email validation. Adds hula-specific `allowed_server_ids`. RegistryUser dropped.
- `pkg/apiobjects/v1/tokens.proto` — StoredToken, StoredTokenKey, Session.

**Verification**: same as 0.1.

## Remaining stages (9 of 11)

| Stage | Estimate | Notes |
|-------|----------|-------|
| 0.3 Authware package | 3 days | ~5000 LoC to copy + adapt. Depends on porting izcr's `store/common.Storage` interface or rewriting against gorm. The single biggest blocker for downstream stages. |
| 0.4 Auth providers (Google/GitHub/Microsoft OIDC) | 3–4 days | Depends on 0.3. |
| 0.5 Apispec protos (auth, forms, landers, site, staging, badactor, status, analytics) | 3 days | Depends on 0.2 (apiobjects referenced by message types). |
| 0.6 Unified server + drop Fiber | 3 days | Depends on 0.3 (middleware). |
| 0.7 Port handlers to gRPC services | 5–7 days | Biggest-breadth stage; ~15 endpoints. |
| 0.8 Migrate hulactl to gRPC clients | 2 days | After 0.7. |
| 0.9 Analytics data foundation | 3–4 days | Mostly independent; could be parallelized with 0.3–0.6 by a second engineer. |
| 0.10 user_server_access ACL | 1–2 days | After 0.3 (claims structure). |
| 0.11 Tests + docs + sign-off | 2 days | Closes the phase. |

**Remaining effort**: ~25–30 working days.

## Recommended next session plan

1. **Stage 0.3 first** — it unblocks 0.4, 0.6, 0.7, 0.10. Budget a full day.
   - Start by deciding on the storage adapter question: copy izcr's `store/common` interface, or port authware to use gorm directly. I'd lean toward copying `store/common` behind a hulation-owned interface so the authware code can be lifted with minimal rewriting.
   - Then copy `middleware.go`, `claims.go`, `grpcutils.go`, `regoutils.go`, `permissions/`, `policy/*.rego`.
   - Trim tenant/project branches. Keep claims fields for forward-compat.
   - End with `go test ./pkg/server/authware/...` passing.

2. **Stage 0.9 can run in parallel** if a second engineer is available — it touches `handler/visitor.go`, `badactor/ipinfo.go`, and `model/event.go` but doesn't depend on the gRPC/authware work.

3. **Stages 0.5 → 0.6 → 0.7 → 0.8 → 0.10 → 0.11** in that strict order afterward.

## Pre-existing issues noticed during execution

- `store/bolt.go` has references to undefined types (`FindRequest`, `FlagRequest`, etc.) causing `go build ./...` to fail at HEAD. Pre-exists Phase 0 work. Unrelated but should be triaged before Phase 1.
