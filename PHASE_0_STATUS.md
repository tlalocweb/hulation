# Phase 0 — Execution Status

Branch: `roadmap/phase0`
Plan: `PLAN_0.md`

## Completed stages

### Stage 0.3a — Storage (raft+bolt) + tune ✅
Commit: `6a29567`

- `pkg/store/common/` — Storage interface, verbatim from izcr.
- `pkg/store/raft/` — full RaftStorage + BoltBackend + RaftNode + InformerFactory from izcr. Single-node usage today, multi-node path preserved for the future (user direction).
- `pkg/tune/` — tunable parameters, IZCR_ env vars renamed to HULA_.
- Deps added: hashicorp/raft v1.7.3, raft-boltdb, bbolt.

ClickHouse remains for analytics/badactor/web-traffic; Bolt is always present; Hula can run without ClickHouse.

### Stage 0.3b (WIP — does not compile) — Authware copied ⚠️
Commit: `5773626`

- `pkg/server/authware/{middleware,claims,grpcutils,regoutils,project_access}.go` — copied + import-renamed, no further adaptation.
- `pkg/server/authware/permissions/{catalog,resolver,roles}.go` + `cache/{types,cache}.go` — same.
- `pkg/server/authware/policy/*.rego` — same.
- `pkg/utils/hashtree.go` — copied; `MatchWithWildcards` rewritten against upstream hashicorp/go-immutable-radix/v2 (izcr's fork adds a radix-level method unavailable upstream; the rewrite walks dot-segments with `Get()` — correct, slightly slower).
- `pkg/apiobjects/v1/rbac.proto` — tenant_uuid + project_uuid fields restored with a comment mapping hula's `server_id` onto `tenant_uuid`. Keeps authware code compiling without renames.

**Remaining work for 0.3c** to make authware compile:
1. Port apiobjects adapters from izcr: SessionAdapter, UserAdapter, rootuser, plus decision on RegistryUser.
2. Bridge `app.GetConfig()` ↔ `config.GetConfig()` (authware assumes the latter namespace; hula exposes the former in `app/app.go:218`).
3. Add `Hostname` and `RegistryHostnames` fields to `config.Config` if missing.
4. `go build ./pkg/server/authware/...` → exit 0; basic unit test passes.

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

## Remaining stages

| Stage | Estimate | Notes |
|-------|----------|-------|
| 0.3c authware glue (compile + tests) | 1–2 days | Port apiobjects adapters (Session/User/rootuser/RegistryUser), bridge config namespace, get `go build ./pkg/server/authware/...` green. Biggest unblocker for 0.4+. |
| 0.4 Auth providers (Google/GitHub/Microsoft OIDC) | 3–4 days | After 0.3c. |
| 0.5 Apispec protos (auth, forms, landers, site, staging, badactor, status, analytics) | 3 days | Only depends on 0.2 (apiobjects). Could actually start now in parallel. |
| 0.6 Unified server + drop Fiber | 3 days | After 0.3c (middleware). |
| 0.7 Port handlers to gRPC services | 5–7 days | Biggest-breadth stage; ~15 endpoints. |
| 0.8 Migrate hulactl to gRPC clients | 2 days | After 0.7. |
| 0.9 Analytics data foundation | 3–4 days | Mostly independent; could be parallelized with 0.3c–0.6. |
| 0.10 user_server_access ACL | 1–2 days | After 0.3c (claims structure). |
| 0.11 Tests + docs + sign-off | 2 days | Closes the phase. |

**Remaining effort**: ~22–27 working days.

## Recommended next session plan

1. **Stage 0.3c first** — finish the authware wire-up. Budget ~1 focused day.
   - Copy these adapters from `../izcr/pkg/apiobjects/v1/`: `useradapter.go`, `userutils.go`, `tokenadapter.go`, `rootuser.proto`, `rootuseradapter.go`, plus the index-name constants from `config.go`. Rename imports.
   - Decision on RegistryUser: port as-is (keeps middleware.go untouched) OR gate middleware's registry paths behind a build tag.
   - Create a tiny `config/aliases.go` in hula that exposes `GetConfig()` as an alias for `app.GetConfig()`, and add `Hostname` + `RegistryHostnames` fields to the `Config` struct. Or just edit authware's few call sites to use `app.GetConfig()`.
   - Iterate on `go build ./pkg/server/authware/...` until green.
   - Then `go test ./pkg/server/authware/...` (import izcr's `authware_permission_test.go` if feasible).

2. **Stage 0.5 can run in parallel** — defining the apispec protos (auth, forms, landers, site, staging, badactor, status, analytics stubs) only depends on the already-landed apiobjects. No authware blocker.

3. **Stage 0.9 also parallelizable** — touches `handler/visitor.go`, `badactor/ipinfo.go`, `model/event.go`. Independent of gRPC/authware.

4. **Stages 0.4 → 0.6 → 0.7 → 0.8 → 0.10 → 0.11** in that strict order afterward.

## Pre-existing issues noticed during execution

- `store/bolt.go` has references to undefined types (`FindRequest`, `FlagRequest`, etc.) causing `go build ./...` to fail at HEAD. Pre-exists Phase 0 work. Unrelated but should be triaged before Phase 1.
