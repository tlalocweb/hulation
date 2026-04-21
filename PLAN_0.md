# Phase 0 — Foundations (detailed plan)

This plan expands Phase 0 from `PLAN_OUTLINE.md`. The scope has grown
significantly beyond the outline: in addition to the analytics data
foundation, Phase 0 now carries a major architectural migration — adopting
izcr's RBAC + SSO code and moving Hula's admin API surface onto gRPC +
grpc-gateway.

> **This phase is load-bearing.** Every later phase (APIs, UI, mobile) assumes
> the gRPC apispec, the RBAC permission annotations, and the SSO provider
> interface that land here. Nothing downstream ships until Phase 0 is done.

Related docs: `UI_PRD.md`, `PLAN_OUTLINE.md`.

---

## 1. Context and scope

### 1.1 Why this phase is bigger than the outline suggested

The outline's Phase 0 was "data foundation" — enrich events, add user/server
ACL, define materialized views. That's still in scope, but two architecture
decisions now land here first:

1. **Copy-and-adopt izcr's RBAC and SSO code** into Hula. We never import
   from `../izcr`; we copy files, rename packages to `go.izuma.io/hulation/…`,
   and adapt. Hula already adopted izcr's TOTP code, so this continues an
   existing pattern.
2. **Transition Hula's admin APIs to gRPC + grpc-gateway**, following izcr's
   unified-server pattern. This means: proto definitions per service, a
   single HTTPS port serving both gRPC and REST, permission requirements
   declared as proto annotations, a single authware middleware.

### 1.2 What stays on plain HTTP (NOT migrating to gRPC)

Confirmed with the user:

- **WebDAV endpoints** — staging-mount, staging-update, and the custom
  `PATCH X-Update-Range` / `X-Patch-Format: diff` surface. WebDAV is a
  standard protocol; wrapping it in gRPC buys nothing and would break
  interoperability with WebDAV clients.
- **Visitor-tracking endpoints** — `/v/hello`, `/v/helloiframe`,
  `/v/hellonoscript`, `/v/sub/:formid`, and the served
  `/scripts/hello.js` / `/scripts/forms.js`. These are called from
  untrusted browsers using form-encoded bodies, query strings, and cookies.
  They stay on Fiber with their current shape.
- **Static site serving** — the per-host `/*` handler that serves built
  sites. Not an API.
- **`/hulastatus`** — unauthenticated health check. Keep as plain HTTP for
  probe simplicity.

Everything else — auth, users, forms CRUD, lander CRUD, site build,
staging-build, bad-actor admin, future analytics — becomes gRPC with a
REST gateway.

### 1.3 Goals

- Adopt izcr-style RBAC and SSO without creating an import dependency on
  the izcr repo.
- Land a unified gRPC/REST listener that serves both protocols on the
  primary HTTPS port.
- Convert every admin API to proto, with permission requirements declared
  inline on each RPC.
- Finish the analytics data-foundation work from the outline so Phase 1
  has rich events to query.

### 1.4 Explicit non-goals

- No UI work (Phase 2).
- No analytics query APIs beyond stubs (Phase 1).
- No email reports, goals, or alerts (Phase 3–4).
- No tenant concept. Izcr's RBAC supports tenants; we adopt the data
  model but Hula's deployment model has a single tenant. We keep the
  columns so a later phase can flip multi-tenant on, but every row starts
  in a single implicit tenant.

---

## 2. Design decisions

These are decisions I'm making now so the plan is concrete. Push back on
any of them before implementation starts.

| # | Decision | Why |
|---|----------|-----|
| D1 | **Single proto toolchain copied from izcr**: `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` + `protoc-gen-grpc-gateway` + `protoc-gen-gotag` + `protoc-gen-validate`. Versions pinned in `external-versions.env` (mirror izcr's). | Proven; exact tooling Hula operators already have from izcr. |
| D2 | **UA parser**: `github.com/ua-parser/uap-go`. | Actively maintained; regex list syncs with upstream. |
| D3 | **Events schema migration**: option (b) from the earlier discussion — explicit versioned DDL, drop GORM `AutoMigrate` for the `events` table, backfill old rows via `INSERT … SELECT` during migration. GORM stays for non-event tables for now. | PRD asks for reviewable DDL; events is the hot path that needs an explicit schema. |
| D4 | **Session ID derivation**: at-ingest, with an in-memory `visitor_id → (last_event_at, session_id)` cache. Cold-start looks up the last event once. 30-minute inactivity threshold. | Reads are rare (cache hits dominate); raw `events.session_id` stays queryable without a JOIN. |
| D5 | **SSO providers**: adopt izcr's OIDC provider infrastructure and register Google, GitHub, Microsoft as three OIDC provider instances configured from `config.yaml`. Hula's admin (`root`) user stays as local username+password+TOTP. | Exactly what the PRD asks for and matches izcr's existing pattern. |
| D6 | **Referrer-channel classification**: hardcoded table in `pkg/analytics/referrer/channels.go`, admin-editable list deferred to a later phase. | No reason to ship config for a list that changes once a year. |
| D7 | **TTL on raw `events`**: config-driven, default 395 days. `config.yaml` key: `analytics.events_ttl_days`. | Operators need to tune without editing DDL. |
| D8 | **`user_server_access` keying**: `server_id` is the string ID from `config.yaml`. Orphan ACL rows (server removed from config) are harmless and garbage-collected lazily on next admin action. | Servers aren't a DB table; no FK to enforce. |
| D9 | **Package layout**: all adopted izcr code lands under `pkg/` (new top-level directory in Hula), renamed to `go.izuma.io/hulation/pkg/...`. Existing `model/`, `handler/`, `badactor/`, etc. stay where they are. | Keeps izcr-style code visibly grouped and separable from Hula's legacy layout, which avoids cross-pollution. |
| D10 | **Proto package path**: `hulation.v1.<service>`; Go import path `go.izuma.io/hulation/pkg/apispec/v1/<service>`. Mirrors izcr. | Predictable, greppable. |
| D11 | **Single port for gRPC + REST**: serve both on the existing admin HTTPS listener. gRPC detected via `Content-Type: application/grpc`; everything else goes through the gateway / Fiber. | One TLS cert, one port; matches izcr's unified-server pattern. |
| D12 | **Fiber stays — for now** for the three non-migrating surfaces (WebDAV, visitor tracking, static). The unified server front-ends everything; paths that aren't a registered gRPC/gateway route fall through to Fiber via a passthrough handler. | Avoids rewriting WebDAV and the visitor tracking bounce-map logic in this phase. |

---

## 3. Stages inside Phase 0

Phase 0 is internally staged. Each stage ends at a reviewable, green-tests
checkpoint. Do them in this order — each depends on the previous.

- **0.1 Proto toolchain & layout** (1–2 days)
- **0.2 Copy izcr apiobjects + annotation protos** (2 days)
- **0.3 Copy izcr authware package** (3 days)
- **0.4 Copy izcr auth provider system, wire OIDC for Google/GitHub/Microsoft** (3–4 days)
- **0.5 Define Hula apispec protos for every migrating endpoint** (3 days)
- **0.6 Unified gRPC + REST + Fiber-passthrough server** (3 days)
- **0.7 Port handlers from Fiber to gRPC service implementations** (5–7 days)
- **0.8 Migrate hulactl to generated gRPC clients** (2 days)
- **0.9 Analytics data foundation** (3–4 days)
- **0.10 `user_server_access` ACL + Phase-1 wiring** (1–2 days)
- **0.11 Tests & docs** (2 days)

Calendar estimate: **5–6 weeks** with focused work. Originally 1 week in
the outline — that estimate only covered stage 0.9.

---

## 4. Stage 0.1 — Proto toolchain and repo layout

**Goal**: Hula can compile `.proto` files the same way izcr does.

**Layout to introduce**:

```
pkg/                                          ← NEW top-level package tree
├── apiobjects/v1/                            ← adopted from izcr, renamed
│   ├── user.proto
│   ├── rbac.proto
│   ├── auth_provider.proto
│   └── ...
├── apispec/v1/                               ← Hula's gRPC services
│   ├── auth/auth.proto
│   ├── forms/forms.proto
│   ├── landers/landers.proto
│   ├── site/site.proto
│   ├── staging/staging.proto                 ← build/reload only; NOT WebDAV
│   ├── badactor/badactor.proto
│   ├── status/status.proto
│   └── analytics/analytics.proto             ← stubs only in Phase 0
├── server/
│   ├── authware/                             ← adopted from izcr
│   ├── unified/                              ← adopted from izcr (adapted)
│   └── providers/                            ← adopted from izcr
└── ...
protoext/                                     ← NEW; mirrors izcr/protoext
├── google/api/...                            ← vendored google proto options
└── izuma/auth/permission.proto               ← permission annotation extension
scripts/                                      ← NEW directory
├── install-protoc.sh                         ← copied from izcr
└── gen-protos.sh
external-versions.env                         ← NEW; pins protoc/plugin versions
```

**Makefile additions** (mirror izcr's proto targets):

```makefile
PROTOBUF_FOLDERS = protoext/izuma/auth pkg/apiobjects/v1 pkg/apispec/v1 \
                   pkg/server/authware/proto
PROTOBUF_SRCS = $(shell find $(PROTOBUF_FOLDERS) -name '*.proto' 2>/dev/null)
PROTOBUF_OUTS = $(PROTOBUF_SRCS:.proto=.pb.go)

protobuf: $(PROTOBUF_OUTS)

pkg/apispec/%.pb.go: pkg/apispec/%.proto
	@protoc -I. -I./protoext -I.bin/include \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative \
		$<
	@protoc -I. -I./protoext -I.bin/include \
		--gotag_out=paths=source_relative:. $<
# … (mirror pkg/apiobjects and protoext/izuma/auth rules from izcr's Makefile)
```

**`external-versions.env`** — pin:

```sh
PROTOC_VERSION=25.1
PROTOC_GEN_GO_VERSION=v1.34.2
PROTOC_GEN_GO_GRPC_VERSION=v1.5.1
PROTOC_GEN_GRPC_GATEWAY_VERSION=v2.22.0
PROTOC_GEN_GOTAG_VERSION=v0.9.0
```

**Checkpoint**: `make protobuf` runs cleanly against an empty protos set;
`install-protoc.sh` installs everything into `.bin/`.

---

## 5. Stage 0.2 — Copy izcr apiobjects + annotation protos

**Goal**: Hula has the apiobjects needed by auth/RBAC, and the proto
annotation extensions used by permission-based authorization.

**What to copy** (from `../izcr` into `/home/ubuntu/work/hulation/`):

| Source (izcr) | Destination (hula) | Adaptation |
|--------------|-------------------|------------|
| `protoext/google/api/*.proto` | `protoext/google/api/*.proto` | verbatim |
| `protoext/izuma/auth/permission.proto` | `protoext/izuma/auth/permission.proto` | verbatim |
| `pkg/server/authware/proto/annotations.proto` | `pkg/server/authware/proto/annotations.proto` | verbatim |
| `pkg/apiobjects/v1/user.proto` | `pkg/apiobjects/v1/user.proto` | rename `go_package` to `go.izuma.io/hulation/pkg/apiobjects/v1`; proto package `hulation.v1.apiobjects` |
| `pkg/apiobjects/v1/rbac.proto` | `pkg/apiobjects/v1/rbac.proto` | same renames; **strip** tenant/project-specific fields Hula won't use in v1 (keep columns but leave comments that they're reserved) |
| `pkg/apiobjects/v1/tokens.proto` | `pkg/apiobjects/v1/tokens.proto` | rename |
| `pkg/apiobjects/v1/robot_account.proto` | — | **skip**; not needed for Hula |
| `pkg/apiobjects/v1/rollout.proto` and registry-related | — | **skip** |

**Delta to apply to `rbac.proto`**: Hula's scope hierarchy is
`system → server`, not `system → tenant → project`. Decision: keep izcr's
`RoleAssignment.scope_type` field (values: `"system"`, `"server"`;
reserve `"tenant"` and `"project"` values in a comment). Rename
`tenant_uuid` → `scope_uuid` in Hula's copy; the string value carries
whatever scope ID applies (server ID for `server` scope, empty for
`system`).

**Permissions vocabulary**: Hula starts with a narrower catalog than
izcr. In `pkg/server/authware/permissions/catalog.go`:

```
superadmin.user.create / delete / modify / list / read
superadmin.server.list         # list configured virtual servers
superadmin.role.*              # manage custom roles
superadmin.permissions.*

server.{server_id}.read                # view analytics for this server
server.{server_id}.build.trigger       # kick a production build
server.{server_id}.staging.build       # kick a staging build
server.{server_id}.forms.*             # form CRUD for this server
server.{server_id}.landers.*           # lander CRUD for this server
server.{server_id}.badactor.manage     # badactor admin for this server
server.{server_id}.*                   # wildcard
```

These become the permission strings referenced in proto annotations
(`option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.create"] }`).

**Checkpoint**: `make protobuf` produces `*.pb.go` for every file under
`pkg/apiobjects/v1/`. Existing Go tests still pass (nothing imports these
yet).

---

## 6. Stage 0.3 — Copy the authware package

**Goal**: Hula has izcr's authware middleware, claims structure,
permission resolver, and OPA-backed policy evaluation.

**What to copy**:

| Source (izcr) | Destination (hula) | Adaptation |
|---------------|-------------------|------------|
| `pkg/server/authware/middleware.go` | `pkg/server/authware/middleware.go` | rename imports; remove tenant/project-specific code paths (leave placeholder comments) |
| `pkg/server/authware/claims.go` | `pkg/server/authware/claims.go` | rename imports; trim tenant-role map to a simpler `ServerRoles map[server_id][]role` |
| `pkg/server/authware/grpcutils.go` | `pkg/server/authware/grpcutils.go` | verbatim (after rename) |
| `pkg/server/authware/regoutils.go` | `pkg/server/authware/regoutils.go` | verbatim (after rename) |
| `pkg/server/authware/permissions/` | `pkg/server/authware/permissions/` | verbatim, trim catalog to Hula's |
| `pkg/server/authware/policy/` | `pkg/server/authware/policy/` | copy the OPA policy file; adapt to Hula's scope hierarchy (`system → server`) |
| `pkg/server/authware/agentauth.go` | — | **skip**; Hula has no agents |

**What to delete/gut** during copy:

- All references to `ProjectRoleAssignment`, `Tenants`, multi-tenant
  enforcement. Keep the *fields* on the Claims struct so a later phase can
  re-enable them without reshuffling, but the middleware checks them as
  no-ops.
- Registry-specific permission resolution (`repository.*`, `artifact.*`).

**New file** `pkg/server/authware/scopes.go`:

```go
// ResolveScopeFromRequest inspects a gRPC request's method descriptor and
// the request message itself to extract the {server_id} used for
// server-scoped permissions. Equivalent to izcr's tenant_uuid/project_uuid
// resolution but narrower.
```

**Integration with Hula's existing OPA middleware**: Hula already has
`middleware/opa.go`. We keep that for the Fiber passthrough path, but the
unified server uses the copied authware exclusively. Stage 0.7 removes
the legacy `wrapWithOpa` per-endpoint as each endpoint is migrated.

**Checkpoint**: authware package compiles; a standalone unit test
validates a token and resolves a simple `server.{server_id}.*`
permission. No production code uses authware yet.

---

## 7. Stage 0.4 — Copy auth provider system; wire OIDC for Google/GitHub/Microsoft

**Goal**: Hula can authenticate a user via Google, GitHub, or Microsoft
OIDC, plus its existing local admin login.

**What to copy**:

| Source (izcr) | Destination (hula) | Adaptation |
|---------------|-------------------|------------|
| `pkg/server/authware/provider/providers.go` | `pkg/server/authware/provider/providers.go` | rename imports; drop dex/keycloak providers from the switch |
| `pkg/server/authware/provider/base/` | `pkg/server/authware/provider/base/` | verbatim after rename |
| `pkg/server/authware/provider/internal/` | `pkg/server/authware/provider/internal/` | adapt to Hula's user store (ClickHouse `users` table, not izcr's store.common) |
| `pkg/server/authware/provider/oidc/` | `pkg/server/authware/provider/oidc/` | verbatim after rename |
| `pkg/server/authware/provider/keycloak/` | — | **skip** |
| `pkg/server/authware/provider/dex/` | — | **skip** |
| `pkg/api/v1/auth/authimpl.go` | `pkg/api/v1/auth/authimpl.go` | strip tenant endpoints; keep login/whoami/users/TOTP |
| `pkg/api/v1/auth/totp.go` | (already exists) | Hula already has TOTP — verify parity, pull any upstream fixes |

**`config.yaml` additions**:

```yaml
auth:
  providers:
    - name: google
      type: oidc
      display_name: Google
      discovery_url: https://accounts.google.com/.well-known/openid-configuration
      client_id: ${HULA_GOOGLE_CLIENT_ID}
      client_secret: ${HULA_GOOGLE_CLIENT_SECRET}
      redirect_url: https://${HULA_HOST}/api/v1/auth/callback/google
      scopes: [openid, email, profile]
      icon_url: /analytics/icons/google.svg
    - name: github
      type: oidc
      display_name: GitHub
      discovery_url: https://token.actions.githubusercontent.com/.well-known/openid-configuration
      # GitHub isn't strictly OIDC-compliant at the top level — the oidc
      # provider implementation handles GitHub's quirks via a type=github
      # subclass. See pkg/server/authware/provider/oidc/github.go.
      client_id: ${HULA_GITHUB_CLIENT_ID}
      client_secret: ${HULA_GITHUB_CLIENT_SECRET}
      redirect_url: https://${HULA_HOST}/api/v1/auth/callback/github
      scopes: [read:user, user:email]
      icon_url: /analytics/icons/github.svg
    - name: microsoft
      type: oidc
      display_name: Microsoft
      discovery_url: https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration
      client_id: ${HULA_MICROSOFT_CLIENT_ID}
      client_secret: ${HULA_MICROSOFT_CLIENT_SECRET}
      redirect_url: https://${HULA_HOST}/api/v1/auth/callback/microsoft
      scopes: [openid, email, profile]
      icon_url: /analytics/icons/microsoft.svg
```

**GitHub quirk**: GitHub's OAuth isn't fully OIDC. We'll add a small
`github.go` under `provider/oidc/` that wraps the OIDC provider,
substituting GitHub's `/user/emails` call for the `email` claim. This is
a ~100-line shim.

**User provisioning model** (same as PRD §3.1):

- Admin pre-provisions users by email via `CreateUser`.
- First SSO login with a matching email populates `users.auth_provider`
  and links the external subject ID.
- SSO login with an unknown email is rejected with a clear message.
- The local admin (`root`) user remains password+TOTP.

**Checkpoint**: a hulactl user logs in via Google OIDC flow end-to-end in
the e2e harness; the JWT issued carries the right permissions; local
admin still works.

---

## 8. Stage 0.5 — Define apispec protos for every migrating endpoint

**Goal**: Every admin endpoint has a proto definition with
`google.api.http` for REST gateway mapping and
`izuma.auth.permission` annotations.

**Protos to author** (under `pkg/apispec/v1/`):

### 8.1 `auth/auth.proto`

Port from izcr's `apispec/v1/auth/auth.proto`, keeping:

- `LoginAdmin`, `LoginWithSecret`, `LoginOIDC`, `LoginWithCode`
- `TotpSetup`, `TotpVerifySetup`, `TotpValidate`, `TotpDisable`, `TotpStatus`
- `WhoAmI`, `RefreshToken`, `ListAuthProviders`
- `ListUsers`, `CreateUser`, `PatchUser`, `DeleteUser`, `GetUser`, `SearchUsers`
- `UpdateUserPassword`, `SetUserSysAdmin`
- `InviteUser`, `ValidateEmail`, `ResendValidationEmail`, `SetInitialPassword`
- `RequestPasswordReset`, `ValidatePasswordResetToken`
- `GetMyPermissions`, `CheckUserPermission`, `GetUserPermissions`

Drop: anything tenant-scoped (`ListUsersAsTenant`, etc).

### 8.2 `forms/forms.proto`

```proto
service FormsService {
  rpc CreateForm(CreateFormRequest) returns (CreateFormResponse) {
    option (google.api.http) = { post: "/api/v1/forms" body: "*" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.create"] };
  }
  rpc ModifyForm(ModifyFormRequest) returns (ModifyFormResponse) {
    option (google.api.http) = { patch: "/api/v1/forms/{form_id}" body: "*" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.modify"] };
  }
  rpc DeleteForm(DeleteFormRequest) returns (DeleteFormResponse) {
    option (google.api.http) = { delete: "/api/v1/forms/{form_id}" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.delete"] };
  }
  rpc ListForms(ListFormsRequest) returns (ListFormsResponse) {
    option (google.api.http) = { get: "/api/v1/forms" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.list"] };
  }
  rpc GetForm(GetFormRequest) returns (GetFormResponse) {
    option (google.api.http) = { get: "/api/v1/forms/{form_id}" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.forms.read"] };
  }
}
```

### 8.3 `landers/landers.proto`

Same shape as forms — `CreateLander`, `ModifyLander`, `DeleteLander`,
`ListLanders`, `GetLander`, with `server.{server_id}.landers.*`
permissions.

### 8.4 `site/site.proto` (production site build)

```proto
service SiteService {
  rpc TriggerBuild(TriggerBuildRequest) returns (TriggerBuildResponse) {
    option (google.api.http) = { post: "/api/v1/site/{server_id}/build" body: "*" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.build.trigger"] };
  }
  rpc GetBuildStatus(GetBuildStatusRequest) returns (GetBuildStatusResponse) {
    option (google.api.http) = { get: "/api/v1/site/builds/{build_id}" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.build.read"] };
  }
  rpc ListBuilds(ListBuildsRequest) returns (ListBuildsResponse) {
    option (google.api.http) = { get: "/api/v1/site/{server_id}/builds" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.build.list"] };
  }
}
```

### 8.5 `staging/staging.proto` (staging-build ONLY — WebDAV stays separate)

```proto
service StagingService {
  rpc StagingBuild(StagingBuildRequest) returns (StagingBuildResponse) {
    option (google.api.http) = { post: "/api/v1/staging/{server_id}/build" body: "*" };
    option (izuma.auth.permission) = { needs: ["server.{server_id}.staging.build"] };
  }
  // NOTE: staging-update and staging-mount remain WebDAV (HTTP) and are
  // NOT part of this service.
}
```

### 8.6 `badactor/badactor.proto`

`ListBadActors`, `ManualBlock`, `EvictBadActor`, `ListAllowlist`,
`AddToAllowlist`, `RemoveFromAllowlist`, `BadActorStats`,
`ListSignatures`. Permission: `server.{server_id}.badactor.manage`
(except `ListSignatures` which is system-scoped).

### 8.7 `status/status.proto`

```proto
service StatusService {
  rpc Status(StatusRequest) returns (StatusResponse) {
    option (google.api.http) = { get: "/api/v1/status" };
    // authenticated but no specific permission required
  }
  rpc AuthOk(AuthOkRequest) returns (AuthOkResponse) {
    option (google.api.http) = { get: "/api/v1/auth/ok" };
  }
}
```

### 8.8 `analytics/analytics.proto` (Phase-0 stubs only)

Defines the *message types* for summary/timeseries/pages/etc. with empty
implementations. Full implementations arrive in Phase 1. Placing the
skeleton in Phase 0 locks down the shape so UI design work can start in
parallel.

**Checkpoint**: `make protobuf` generates `*.pb.go`, `*.pb.gw.go`, and
`*_grpc.pb.go` for every service. No handler logic yet.

---

## 9. Stage 0.6 — Unified gRPC + REST + Fiber-passthrough server

**Goal**: A single HTTPS listener serves gRPC, grpc-gateway REST, WebDAV,
visitor tracking, and static files on the same port with the same TLS
cert.

**What to copy from izcr**:

- `pkg/server/unified/server.go` → `pkg/server/unified/server.go`
- `pkg/server/grpc/server.go` → `pkg/server/grpc/server.go`
- `pkg/server/http/gateway.go` → `pkg/server/http/gateway.go`

**Adapt**:

- **Routing rules**: the unified server's ServeHTTP already splits gRPC
  (by `Content-Type: application/grpc`) from HTTP. On the HTTP side we
  add a **Fiber passthrough** for paths that aren't part of the REST
  gateway:
  - `/v/*` (visitor tracking)
  - `/scripts/*` (static scripts)
  - WebDAV paths: `/<webdav-prefix>/*` (from config)
  - `/hulastatus`
  - Per-host site serving (`/*` for configured site hostnames)

- **Static-site host routing**: izcr's unified server handles "registry
  hostnames" separately; we generalize this to "site hostnames from
  `config.yaml`" so per-server hosts serve the built static site, while
  the primary admin host serves the gRPC/REST/WebDAV/visitor API.

**Integration points** in `server/run.go` (Hula's existing startup):

```go
// Before: Fiber-only listener
// After:
unifiedSrv, err := unified.NewServer(&unified.Config{
    Address:     ":443",
    TLSCertFile: cfg.CertFile,
    TLSKeyFile:  cfg.KeyFile,
    GRPCServerOptions: []grpc.ServerOption{
        grpc.UnaryInterceptor(authware.UnaryInterceptor()),
        grpc.StreamInterceptor(authware.StreamInterceptor()),
    },
})
// Register each gRPC service:
authspec.RegisterAuthServiceServer(unifiedSrv.GRPC(), authimpl.New(...))
formsspec.RegisterFormsServiceServer(unifiedSrv.GRPC(), formsimpl.New(...))
// … etc.

// Gateway registration for REST:
authspec.RegisterAuthServiceHandlerServer(ctx, unifiedSrv.GatewayMux(), ...)

// Fiber passthrough for WebDAV / visitor / static:
unifiedSrv.SetFallbackHandler(fiberApp.Handler())
```

**Checkpoint**: the server boots with no gRPC services registered;
existing Fiber routes still serve correctly through the passthrough; a
reflection query (`grpcurl ... list`) against the server returns empty
(but succeeds).

---

## 10. Stage 0.7 — Port handlers from Fiber to gRPC service implementations

**Goal**: Every API endpoint listed in stage 0.5 has a working gRPC
implementation wired up; the corresponding legacy Fiber route is removed.

**Order (easiest → hardest, so integration patterns stabilize early)**:

1. `status.Status` / `status.AuthOk` — trivial; validates authware wiring
2. `auth.*` (all of it — login, TOTP, user CRUD, invite, reset) —
   largest service but mostly a straight port from izcr's `authimpl.go`
   plus Hula's existing handler code
3. `badactor.*` — stateless reads over in-memory + ClickHouse
4. `forms.*` + `landers.*` — CRUD with schema validation
5. `site.*` (build triggers) — involves the build coordinator
6. `staging.StagingBuild` — builder container coordination

**Per-endpoint port checklist**:

- Move handler body from `handler/<name>.go` into a new
  `pkg/api/v1/<service>/<service>impl.go`.
- Translate Fiber ctx access (`ctx.Params(...)`, `ctx.Query(...)`,
  `ctx.BodyParser(...)`) into proto request fields.
- Translate Fiber status codes (`ctx.Status(400).SendString(msg)`) into
  gRPC status errors (`status.Error(codes.InvalidArgument, msg)`) — the
  gateway converts these back to the right HTTP status codes.
- Remove the matching `router.go` route registration.
- Update the e2e test suite to call the new REST path (most paths
  change: `/api/form/...` → `/api/v1/forms/...`).

**hulactl implication**: every ported endpoint breaks the existing
hulactl client code. Stage 0.8 handles the migration.

**Backwards compat?** None. The existing API surface is not public; only
hulactl and the test harness call it. We rename everything to `/api/v1/`
and accept the single-release break.

**Checkpoint**: `make test-e2e` passes end-to-end against the new
endpoints. Legacy handlers and routes deleted (but the Fiber app still
handles WebDAV + visitor tracking).

---

## 11. Stage 0.8 — Migrate hulactl to generated gRPC clients

**Goal**: `hulactl` speaks gRPC (or REST-through-gateway) against the new
API surface instead of the hand-written `client/` package.

**What changes**:

- `client/*.go` becomes a thin wrapper around generated gRPC stubs from
  `pkg/apispec/v1/*/`. For each old helper (`FormCreate`, `BuildTrigger`,
  etc.), call the corresponding gRPC method.
- Credential loading (`hulactl.yaml` token) stays; the token is passed
  as `authorization: Bearer <token>` metadata on every gRPC call.
- The interactive auth flow (`hulactl auth <url>`) calls `LoginAdmin` or
  `LoginOIDC` as appropriate.

**Decision**: hulactl talks **gRPC** directly (not REST). Less marshaling
overhead, native streaming if we need it later. The REST gateway is for
browsers and curl.

**Checkpoint**: the e2e harness swaps `hulactl` for the new binary; all
42 suites still pass.

---

## 12. Stage 0.9 — Analytics data foundation

**Goal**: Every raw event is rich enough to answer any question the PRD
asks.

**Tasks**:

### 12.1 Events schema (D3)

Author `pkg/store/clickhouse/schema/events_v1.sql`:

```sql
CREATE TABLE IF NOT EXISTS events
(
    id               String,
    belongs_to       String,                  -- visitor_id
    session_id       String,
    server_id        String,                  -- NEW; denormalized from host
    code             UInt64,
    data             String,
    method           String,
    url              String,
    url_path         String,
    host             String,
    referer          String,                  -- NEW
    referer_host     String,                  -- NEW; parsed domain
    channel          LowCardinality(String),  -- NEW; Direct/Search/Social/Referral/Email
    utm_source       String,                  -- NEW
    utm_medium       String,                  -- NEW
    utm_campaign     String,                  -- NEW
    utm_term         String,                  -- NEW
    utm_content      String,                  -- NEW
    browser          LowCardinality(String),  -- NEW
    browser_version  String,                  -- NEW
    os               LowCardinality(String),  -- NEW
    os_version       String,                  -- NEW
    device_category  LowCardinality(String),  -- NEW; mobile/tablet/desktop/bot
    country_code     LowCardinality(String),  -- NEW
    region           String,                  -- NEW
    city             String,                  -- NEW
    when             DateTime64(3),
    from_ip          String,
    browser_ua       String,
    created_at       DateTime64(3),
    updated_at       DateTime64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(when)
ORDER BY (when, server_id, belongs_to, id)
TTL toDateTime(when) + toIntervalDay({{ .EventsTTLDays }})
SETTINGS index_granularity = 8192;
```

**Migration from the current AutoMigrate'd table**:

1. Create `events_v1` with new schema.
2. `INSERT INTO events_v1 (...) SELECT ... FROM events` — populates new
   columns with empty defaults for old rows.
3. `RENAME TABLE events TO events_legacy_{date}, events_v1 TO events;`
4. After a 30-day shakedown, drop `events_legacy_*`.

Migration script lives at
`pkg/store/clickhouse/migrations/0001_events_v1.sql` and runs on
startup via a new migration runner (mirrors what izcr does).

### 12.2 Ingest enrichment

Add a `pkg/analytics/enrich/` package:

- `enrich.ReferrerChannel(refURL string) (channel, host string)`
- `enrich.UTM(url string) UTMFields`
- `enrich.UserAgent(ua string) UAFields` (uap-go wrapper)
- `enrich.SessionID(visitorID string, now time.Time) string` — uses a
  `sync.Map` cache keyed by `visitor_id` with last-seen timestamps; cold
  start queries `max(when)` for that visitor.

Wire from `handler/visitor.go` — three call sites (Hello, HelloIframe,
HelloNoScript), each constructs the event; enrich immediately before
`ev.CommitTo(...)`.

**Referer capture**: read `ctx.Referer()` (already available — see
`handler/visitor.go:333`). Currently only logged; persist it.

### 12.3 `ipinfo` cache extension

The `ipinfo_cache` table already has `region` and `city` columns (see
`badactor/ipinfo.go:254-268`), and the API already returns them. The
enrichment step reads `region` + `city` from `GetIPInfo(ip)` and writes
them onto the event.

### 12.4 Materialized views

`pkg/store/clickhouse/migrations/0002_events_mvs.sql`:

- `mv_events_hourly` — `SummingMergeTree`, `GROUP BY`
  `(toStartOfHour(when), server_id, url_path, referer_host, country_code, device_category)`, summing `visitors_count`, `pageviews_count`.
- `mv_events_daily` — same with `toStartOfDay`.
- `mv_sessions` — `AggregatingMergeTree`, one row per
  `(server_id, session_id)`, aggregating `min(when)` → session_start,
  `max(when)` → session_end, `count()` → events_in_session.

### 12.5 TTL

Hardcoded in the DDL via the `{{ .EventsTTLDays }}` template var, rendered
from `config.yaml` at migration time. Materialized views get no TTL.

**Checkpoint**: events flowing in the e2e harness end up in `events` with
every new column populated. A ClickHouse query
`SELECT DISTINCT browser FROM events` returns non-empty results.

---

## 13. Stage 0.10 — `user_server_access` ACL + Phase-1 wiring

**Goal**: multi-user, multi-server access control is working end-to-end;
a non-admin user sees only their authorized servers.

**Tasks**:

- Create `user_server_access` table:

  ```sql
  CREATE TABLE IF NOT EXISTS user_server_access
  (
      user_id    String,
      server_id  String,
      role       LowCardinality(String),  -- "viewer" | "manager"
      granted_at DateTime64(3),
      granted_by String
  )
  ENGINE = ReplacingMergeTree(granted_at)
  ORDER BY (user_id, server_id);
  ```

- Populate from the RBAC layer: when a `RoleAssignment` with
  `scope_type="server"` is granted to a user, the resolver also writes a
  `user_server_access` row for fast lookup. (Redundant with
  `role_assignments` but the join is hot enough in analytics queries
  that denormalizing is worth it.)

- In the JWT `Claims` struct, populate `AllowedServers []string` at
  token-issue time from `user_server_access`. Every analytics
  API call intersects requested `server_ids[]` with
  `claims.AllowedServers` server-side.

- Admin (`superadmin.*`) bypasses: `claims.IsSuperadmin` ⇒ all servers
  allowed.

- New gRPC methods on `AuthService` (add to `auth.proto` in stage 0.5):

  ```proto
  rpc GrantServerAccess(GrantServerAccessRequest)
      returns (GrantServerAccessResponse) {
    option (google.api.http) = { post: "/api/v1/auth/access" body: "*" };
    option (izuma.auth.permission) = { needs: ["superadmin.user.modify"] };
  }
  rpc RevokeServerAccess(RevokeServerAccessRequest)
      returns (RevokeServerAccessResponse) {
    option (google.api.http) = {
      delete: "/api/v1/auth/access/{user_id}/{server_id}"
    };
    option (izuma.auth.permission) = { needs: ["superadmin.user.modify"] };
  }
  rpc ListServerAccess(ListServerAccessRequest)
      returns (ListServerAccessResponse) {
    option (google.api.http) = { get: "/api/v1/auth/access" };
    option (izuma.auth.permission) = { needs: ["superadmin.user.list"] };
  }
  ```

**Checkpoint**: a test user granted `role=viewer` on only `server_a`
gets a JWT with `AllowedServers=["server_a"]`; trying to access
`server_b`'s endpoints returns 403.

---

## 14. Stage 0.11 — Tests and docs

### 14.1 e2e harness additions

New suites under `test/e2e/suites/`:

- `13-grpc-smoke.sh` — `grpcurl -d '{}' host:443 hulation.v1.status.StatusService/Status` returns 200 via gRPC.
- `14-rest-gateway.sh` — same endpoints reachable at `/api/v1/status` via curl (REST gateway).
- `15-sso-google.sh` — full Google OIDC flow using a mock OIDC provider container (spin up `dexidp/dex` configured as a fake Google).
- `16-rbac.sh` — create user, grant server access, verify their JWT only sees the right servers.
- `17-analytics-foundation.sh` — seed a visitor, trigger a pageview, assert the new columns (`browser`, `os`, `device_category`, `session_id`, `channel`, …) are populated in ClickHouse.
- `18-events-migration.sh` — start with old-schema data, run the migration, confirm rows are preserved and the new columns default correctly.

Existing suites 01–12 are updated to hit the `/api/v1/` REST gateway
paths (or the new hulactl, which handles the path change
transparently).

### 14.2 Docs

- `pkg/apispec/v1/README.md` — how to add a new gRPC service (mirrors
  izcr's proto/service conventions).
- `pkg/server/authware/README.md` — how permissions resolve, the
  `{server_id}` placeholder mechanism, the OPA policy file.
- Update `DEPLOYMENT.md` with the new `auth.providers` section and the
  required `HULA_*_CLIENT_ID`/`HULA_*_CLIENT_SECRET` env vars.
- Update `test/ABOUT.md` with the new suites.

---

## 15. File change summary

| Area | Action | What |
|------|--------|------|
| `pkg/apiobjects/v1/*.proto` | **NEW** (copied + adapted from izcr) | User, RBAC, tokens, auth_provider apiobjects |
| `pkg/apispec/v1/auth/auth.proto` | **NEW** | Auth service gRPC spec |
| `pkg/apispec/v1/forms/forms.proto` | **NEW** | Forms service |
| `pkg/apispec/v1/landers/landers.proto` | **NEW** | Landers service |
| `pkg/apispec/v1/site/site.proto` | **NEW** | Site build service |
| `pkg/apispec/v1/staging/staging.proto` | **NEW** | Staging build (not WebDAV) |
| `pkg/apispec/v1/badactor/badactor.proto` | **NEW** | Bad-actor admin |
| `pkg/apispec/v1/status/status.proto` | **NEW** | Status + AuthOk |
| `pkg/apispec/v1/analytics/analytics.proto` | **NEW** | Phase-1 skeleton |
| `pkg/server/authware/**` | **NEW** (copied + adapted from izcr) | Middleware, claims, permissions, OPA policy |
| `pkg/server/authware/provider/{base,internal,oidc}/**` | **NEW** (copied + adapted) | Provider system; Google/GitHub/Microsoft OIDC |
| `pkg/server/unified/server.go` | **NEW** (copied + adapted) | Unified HTTPS server |
| `pkg/server/grpc/server.go` | **NEW** (copied) | gRPC server builder |
| `pkg/server/http/gateway.go` | **NEW** (copied) | grpc-gateway mux |
| `pkg/api/v1/auth/authimpl.go` | **NEW** (copied + adapted) | Auth service implementation |
| `pkg/api/v1/{forms,landers,site,staging,badactor,status}/*impl.go` | **NEW** | gRPC implementations ported from `handler/` |
| `pkg/analytics/enrich/**` | **NEW** | UA, referrer, UTM, session enrichment |
| `pkg/store/clickhouse/schema/events_v1.sql` | **NEW** | Explicit events DDL |
| `pkg/store/clickhouse/migrations/0001_events_v1.sql` | **NEW** | Migration from old events table |
| `pkg/store/clickhouse/migrations/0002_events_mvs.sql` | **NEW** | Materialized views |
| `pkg/store/clickhouse/migrations/runner.go` | **NEW** | Startup migration runner |
| `protoext/**` | **NEW** (copied from izcr) | Vendored google.api + izuma.auth annotations |
| `Makefile` | **Update** | Add `protobuf` target mirror of izcr's |
| `external-versions.env` | **NEW** | Pin protoc, plugin versions |
| `scripts/install-protoc.sh` | **NEW** (copied) | Protoc + plugin installer |
| `server/run.go` | **Update** | Replace Fiber-only listener with unified server; keep Fiber for WebDAV + visitor + static |
| `router/router.go` | **Update** | Strip all `/api/*` admin routes (now handled by gRPC); keep WebDAV, visitor, static |
| `handler/{auth,forms,lander,sitedeploy,staging,api}.go` | **Delete** | Superseded by gRPC impls (except staging WebDAV parts) |
| `handler/{visitor,redirect,common,scripts}.go` | **Keep** | Still served via Fiber |
| `handler/staging.go` | **Trim** | Keep WebDAV handlers; remove build/reload (now gRPC) |
| `client/*.go` | **Replace** | Thin wrapper over generated gRPC clients |
| `model/tools/hulactl/main.go` | **Update** | Call the new gRPC client |
| `config/config.go` | **Update** | Add `auth.providers[]`, `analytics.events_ttl_days` |
| `test/e2e/suites/13-grpc-smoke.sh` → `18-events-migration.sh` | **NEW** | New test suites |
| `DEPLOYMENT.md`, `test/ABOUT.md` | **Update** | Document new env vars and test suites |

---

## 16. Open questions

Decisions that are genuinely ambiguous and deserve a user call before we
start:

1. **Streaming vs. unary for `GetBuildStatus`?** The current build-status
   API is polled. A gRPC streaming version (`stream BuildStatusUpdate`)
   would let hulactl tail build progress in real time. Default: stay
   polled in Phase 0, revisit later.
2. **Where does the OPA policy live — in-tree or in config?** izcr
   embeds it at build time. Same for Hula? (Recommended: yes, embed.)
3. **OAuth redirect URL / callback port**: the redirect_url in the SSO
   config must be reachable from the user's browser. In a single-node
   deployment that's fine; in dev it gets awkward. Recommend:
   `http://localhost:8443/...` pattern for dev, documented in SETUP.md.
4. **Schema version checkpointing**: izcr doesn't run a migration framework
   per se — it uses `CREATE TABLE IF NOT EXISTS` everywhere. Hula's
   events migration needs a real rename, so we ship a minimal migrations
   runner (a `schema_migrations` table + a list of applied filenames).
   Recommend: ship the runner here; Phase 1+ uses it for any new
   schema.
5. **Does `hulactl auth` still support password or does it require SSO?**
   Recommend: password-only for the built-in `root` user; all other users
   can *only* auth via SSO. This matches the PRD.

---

## 17. Size estimate and risk

| Stage | Estimate | Risk |
|-------|----------|------|
| 0.1 Proto toolchain | 1–2 days | Low |
| 0.2 Copy apiobjects | 2 days | Low |
| 0.3 Copy authware | 3 days | Medium — OPA policy is subtle |
| 0.4 Provider + OIDC | 3–4 days | Medium — GitHub quirk |
| 0.5 Define protos | 3 days | Low |
| 0.6 Unified server | 3 days | Medium — routing edge cases |
| 0.7 Port handlers | 5–7 days | **Highest** — breadth of code, per-endpoint bugs |
| 0.8 hulactl migration | 2 days | Low |
| 0.9 Data foundation | 3–4 days | Medium — event migration |
| 0.10 ACL + Phase-1 wiring | 1–2 days | Low |
| 0.11 Tests and docs | 2 days | Low |

**Total: 28–34 working days (~6 calendar weeks) for one engineer.**

**Major risks**:

- **Port breadth (stage 0.7)**: ~15 endpoints, each with its own bugs.
  Mitigate by porting the smallest first (status) and using it to shake
  out the authware/unified-server integration before tackling the fat
  ones.
- **OIDC provider quirks**: GitHub's non-OIDC OAuth and Microsoft's
  multi-tenant endpoint (`/common/v2.0/`) need careful handling. izcr
  has solved this; copy exactly.
- **Events table migration**: `INSERT … SELECT` over a large existing
  table can be slow and I/O-heavy. Run during a maintenance window;
  keep the old table around for 30 days as a rollback path.

---

## 18. Checkpoints for pull-request granularity

Each of these is a reviewable PR:

1. Stage 0.1 — toolchain + empty layout
2. Stage 0.2 — apiobjects protos + generated code
3. Stage 0.3 — authware package, standalone unit tests
4. Stage 0.4a — internal provider (password login)
5. Stage 0.4b — OIDC provider + Google
6. Stage 0.4c — GitHub shim
7. Stage 0.4d — Microsoft
8. Stage 0.5 — all apispec protos, no implementations
9. Stage 0.6 — unified server, Fiber passthrough, no new endpoints live
10. Stage 0.7a — status + auth-ok gRPC
11. Stage 0.7b — auth service gRPC (login, users, TOTP, invite)
12. Stage 0.7c — forms + landers gRPC
13. Stage 0.7d — site + staging-build gRPC
14. Stage 0.7e — badactor gRPC
15. Stage 0.8 — hulactl on generated stubs
16. Stage 0.9a — events schema + migration
17. Stage 0.9b — ingest enrichment
18. Stage 0.9c — materialized views + TTL
19. Stage 0.10 — user_server_access + Phase-1 ACL hooks
20. Stage 0.11 — e2e suites + docs

20 PRs is a lot, but each one is small, reviewable, and shippable
independently. We can batch some (e.g., 0.4a-d as one PR) if reviewer
bandwidth is tighter than implementer bandwidth.
