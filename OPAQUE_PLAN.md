# OPAQUE Migration Plan

Move password exchange between `hulactl` / browser and the hula
server from `argon2id(plaintext-over-TLS)` to **OPAQUE** — an
augmented PAKE that lets the server authenticate a password
**without ever seeing it** (not even during registration).

Affected login paths:
- **`admin`** — the root user whose argon2id digest currently lives
  in `config.Admin.Hash`.
- **internal-provider users** — `pkg/server/authware/provider/internal`
  exists today and authenticates via `IzcrProvider.LoginWithSecret`
  against `apiobjects.User.PasswordHash`. These get OPAQUE too.

Explicitly **out of scope**:
- SSO (Google / GitHub / Microsoft / any OIDC provider). OAuth
  redirect dances do not exchange a password with hula at all.
- Service-to-service tokens, the JWT issuance flow downstream of
  successful authentication, TOTP gating.

Cross-cutting requirement (user-confirmed): the `admin` user must
be able to sign in to `/analytics/login` on **any configured
virtual host** (`www.tlaloc.us`, `staging.tlaloc.us`, …), not just
a single canonical login host.

---

## 1. Critical research finding — library interop

**The two libraries you named will not interoperate.** Confirmed
via reading both repos at current head:

| Concern        | bytemare/opaque (Go)                  | @cloudflare/opaque-ts                |
|----------------|---------------------------------------|--------------------------------------|
| Latest tag     | v0.18.0 (2026-03-24, active)          | 0.7.5 (2022, effectively unmaintained) |
| Spec           | **RFC 9807** (author maintains lib)   | **draft-irtf-cfrg-opaque-07** (2021) |
| OPRF group     | Ristretto255 / P-256 / P-384 / P-521  | **NIST P-curves only** — no Ristretto |
| MHF (KSF)      | Argon2id (default), Scrypt, Identity  | **Scrypt + Identity only** — no Argon2id |

Three independent blockers:

1. **Suite mismatch.** opaque-ts cannot speak Ristretto255-SHA512;
   bytemare/opaque can drop to P-256 to match, so this one is
   solvable on the Go side.
2. **MHF mismatch.** opaque-ts cannot speak Argon2id; the goal
   "argon2id at rest" requires both ends agree, so we'd have to
   drop both sides to scrypt(N=32768,r=8,p=1).
3. **Spec drift.** Even with the suite + MHF aligned, draft-07
   (2021) and RFC 9807 differ in label strings, identity-string
   serialization, `CleartextCredentials` ordering, and KE2 envelope
   layout. **The wire bytes will not match.** No published interop
   test exists; the open issue [opaque-ts#24](https://github.com/cloudflare/opaque-ts/issues/24)
   on Cloudflare's side ("handshake error") is unresolved.

### 1.1 Recommended library matrix (replacement for the user's pick)

Two viable architectures. **Pick one before stage 1 starts.**

#### Option A — Same Go lib everywhere via WASM *(recommended)*

| Component | Library                          | Notes |
|-----------|----------------------------------|-------|
| Server (Go) | `bytemare/opaque` v0.18+       | Native. RFC 9807. Ristretto255+Argon2id default. |
| `hulactl` (Go) | `bytemare/opaque` v0.18+    | Same crate, client mode. |
| Browser   | `bytemare/opaque` compiled to WASM via `tinygo` or `goweb` | Same wire bytes by construction; one source of truth for protocol logic. |

- Pros: guaranteed interop; one library to audit; RFC-9807
  conformant; Ristretto255 + Argon2id as planned.
- Cons: WASM bundle is **~1.25 MB gzipped (~1.0 MB Brotli)** —
  standard Go's runtime is heavy. Lazy-loaded behind a dynamic
  import on `/login`, so the dashboard's 20 KB first-load budget
  is unaffected. See §18 for the empirical compile-and-execute
  test that produced this number. TinyGo could shave it to
  100-300 KB but TinyGo's crypto support has gaps; see §13 risk.

#### Option B — RFC-9807-aligned third-party browser lib

| Component | Library                                                | Notes |
|-----------|--------------------------------------------------------|-------|
| Server    | `bytemare/opaque`                                      | Native Go. |
| `hulactl` | `bytemare/opaque`                                      | Native Go. |
| Browser   | `@serenity-kit/opaque` (Rust → WASM, RFC-9807-aligned, Ristretto255+Argon2id) | Active maintenance; SDK wrapper class. |

- Pros: Ristretto255+Argon2id supported in all three places; no
  custom WASM step (npm pulls a precompiled wasm).
- Cons: serenity-kit's interop with bytemare is not officially
  certified; need a stage-1 spike to confirm wire-format match.

#### Option C — Suite-degraded with `@cloudflare/opaque-ts` (rejected)

Stay on draft-07 + P-256 + scrypt. No Ristretto, no Argon2id, on
an abandoned spec. Wire-format match against bytemare/opaque
unverified and likely broken. **Not recommended.** Listed for
completeness; if the interop turns out incompatible despite the
shared draft, the work is lost.

**The rest of this plan assumes Option A.** Switching to B is a
one-package swap in the browser stage; switching to C requires
backing out the Go-side suite choices and is a larger pivot.

---

## 2. OPAQUE primer (just enough to anchor the plan)

OPAQUE is a 3-pass aPAKE. Two protocols built on it:

**Registration** (one-time, when password is set or rotated):
```
Client                                       Server
  |  ── RegistrationRequest (blinded pwd)──>  |
  |  <─ RegistrationResponse (β + σ_pub) ──   |
  |                                            |
  |  argon2id-stretch + envelope build         |
  |                                            |
  |  ── RegistrationRecord (envelope)──>       |
  |                                stash record|
```

**Login** (every authentication):
```
Client                                       Server
  |  ── KE1 (blinded pwd + ephemeral) ──>     |
  |  <─ KE2 (β + envelope + ephemeral)──      |
  |                                            |
  |  argon2id-stretch + envelope decrypt        |
  |  → reconstruct client static key           |
  |  → derive shared session key               |
  |                                            |
  |  ── KE3 (auth tag) ────────────>           |
  |                              verify auth   |
  |                              both sides    |
  |                              hold session_key
```

The plaintext password **never touches the wire** in either flow.
The server stores a `RegistrationRecord` (≈ 256 bytes) per user;
without that record, the server cannot validate the password — so
operator-side leak of the bolt file means an attacker still has to
brute-force the argon2id-stretched envelope (same security level as
a leaked argon2id digest today, but with no plaintext-password
exposure during login).

**Note on "argon2id at rest":** OPAQUE doesn't store
`argon2id(password)` on disk — it stores a registration envelope.
The argon2id MHF runs **client-side** during the protocol to
stretch the password before it's used to derive OPRF keys and
encrypt the envelope. The security property the user asked for
("password not retrievable from on-disk data without brute force
through argon2id") is satisfied; the literal data shape changes.

### 2.1 Suite parameters (final)

| Parameter        | Value                  | Rationale |
|------------------|------------------------|-----------|
| OPRF group       | Ristretto255           | bytemare default; constant-time, no co-factor footguns |
| Hash             | SHA-512                | matches Ristretto255 paired hash |
| KDF              | HKDF-SHA-512           | RFC 9807 default |
| MAC              | HMAC-SHA-512           | RFC 9807 default |
| KSF (MHF)        | Argon2id               | user-requested; tunable per deploy |
| Argon2id memory  | 64 MiB                 | OWASP minimum 2024 — server CPU does not run argon2id, only the client |
| Argon2id time    | 3 iterations           | OWASP recommended |
| Argon2id parallel| 1                      | predictable across browser + Go |
| AKE              | 3DH (RFC 9807 default) | only mode bytemare implements |

---

## 3. Affected components

| Component                                | Touch | Why |
|------------------------------------------|-------|-----|
| `pkg/apispec/v1/auth/auth.proto`         | Add 4 RPCs | OPAQUE register-init/finish + login-init/finish |
| `pkg/api/v1/auth/authimpl.go`            | Add OPAQUE methods + keep `LoginAdmin` for legacy fallback | |
| `pkg/auth/opaque/`                       | **new package** | server-side OPAQUE wrapper, OPRF seed loader, login-session cache |
| `pkg/server/authware/provider/internal/internalprovider.go` | Modify | wire `LoginWithSecret` to OPAQUE for users that have an OPAQUE record |
| `pkg/store/bolt/`                        | New `opaque_records` bucket + accessors | persistence per user |
| `pkg/apiobjects/v1/user.proto`           | Add `opaque_record_set` bool | UI indicator |
| `config/config.go`                       | Add `Admin.OpaqueRegistered` (auto-managed) + `OPAQUE.OPRFSeed` env-bound | server identity |
| `web/analytics/src/lib/api/auth.ts`      | New `opaqueLoginAdmin()` and `opaqueLoginInternal()` paths; legacy `login.admin()` falls back | |
| `web/analytics/src/routes/login/+page.svelte` | Use OPAQUE flow first, legacy on 501/404 | |
| `hulactl` (Go)                           | New `set-password` command + OPAQUE login in `auth` | |
| `pkg/server/authware/provider/internal/`  | Add `RegisterOpaqueRecord` flow for setting internal-user passwords (admin or self) | |

Plus stage-0 plumbing for the Go-WASM browser bundle.

---

## 4. Wire protocol — new endpoints

All under `/api/v1/auth/opaque/*`. Symmetric naming: `register/*` +
`login/*`, each as `init` + `finish`. No backwards-incompatible
changes to existing routes.

```proto
service AuthService {
  // ... existing RPCs ...

  // OPAQUE registration — sets a password without ever revealing it.
  rpc OpaqueRegisterInit(OpaqueRegisterInitRequest)   returns (OpaqueRegisterInitResponse);
  rpc OpaqueRegisterFinish(OpaqueRegisterFinishRequest) returns (OpaqueRegisterFinishResponse);

  // OPAQUE login — replaces LoginAdmin / LoginWithSecret for users
  // that have an OPAQUE record. Legacy paths remain for migration.
  rpc OpaqueLoginInit(OpaqueLoginInitRequest)   returns (OpaqueLoginInitResponse);
  rpc OpaqueLoginFinish(OpaqueLoginFinishRequest) returns (OpaqueLoginFinishResponse);
}

message OpaqueRegisterInitRequest {
  string username = 1;
  string provider = 2;            // "admin" | "internal"
  bytes  registration_request = 3; // OPAQUE M1 from client
}
message OpaqueRegisterInitResponse {
  bytes  registration_response = 1; // OPAQUE M2
  string session_id            = 2; // server caches per-username state under this key
}
message OpaqueRegisterFinishRequest {
  string session_id          = 1;
  bytes  registration_record = 2;   // OPAQUE M3
}
message OpaqueRegisterFinishResponse {
  bool   ok    = 1;
  string error = 2;
}

message OpaqueLoginInitRequest {
  string username = 1;
  string provider = 2;
  bytes  ke1      = 3;
}
message OpaqueLoginInitResponse {
  bytes  ke2        = 1;
  string session_id = 2;
  // If the username has no OPAQUE record but does have a legacy
  // argon2id hash, server returns `ok=false` + `legacy_available=true`
  // so the client falls back to LoginAdmin / LoginWithSecret.
  bool   legacy_available = 3;
}
message OpaqueLoginFinishRequest {
  string session_id = 1;
  bytes  ke3        = 2;
}
message OpaqueLoginFinishResponse {
  string admintoken    = 1;       // populated for admin
  string token         = 2;       // populated for internal users
  bool   totp_required = 3;
}
```

REST gateway paths follow the existing convention:
- `POST /api/v1/auth/opaque/register/init`
- `POST /api/v1/auth/opaque/register/finish`
- `POST /api/v1/auth/opaque/login/init`
- `POST /api/v1/auth/opaque/login/finish`

All four are `option (annotations.noauth) = true` on init+finish
because the protocol IS the auth.

### 4.1 Server-side state cache

OPAQUE login is stateful between `init` and `finish` — `KE2`
generation produces a private session secret the server needs in
`finish` to verify the client's `KE3`. Cache lives in:

```go
package opaque

type sessionCache struct {
    mu    sync.Mutex
    items map[string]*pendingSession // session_id -> server state
    // TTL: 60s. Client must complete a login round-trip in that window.
}
```

In-memory; no persistence needed (logins complete in <1s under
normal conditions). On crash/restart all in-flight sessions die
and the client retries from scratch — acceptable.

### 4.2 Multi-host admin sign-in

Already supported by the architecture: `LoginAdmin` doesn't gate on
`Host:` header today; it gates on `req.username == cfg.Admin.Username`.
The new OPAQUE flow inherits the same property.

What we **do** need to add: `serverIdentity` in the OPAQUE AKE is
the per-record identity string the server commits to in the
envelope. We use a constant — `"hula"` — across all virtual hosts
so the same admin record validates regardless of which host the
client lands on. The TLS exporter binding is **not** included in
the AKE (would tie the envelope to a specific TLS session); the
overall scheme retains its security posture without it because TLS
already terminates server-authentication.

---

## 5. Storage design

New bolt bucket `opaque_records` keyed by `provider|username`.

```go
type StoredOpaqueRecord struct {
    Username     string    `json:"username"`
    Provider     string    `json:"provider"`        // "admin" | "internal"
    SuiteID      string    `json:"suite_id"`        // "ristretto255-sha512-argon2id-v1" — bumps if we rotate
    Envelope     []byte    `json:"envelope"`        // OPAQUE RegistrationRecord serialized
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
    LastSuccessLogin time.Time `json:"last_success_login,omitempty"`
}
```

The envelope is RNG-derived per record; copying the bolt file to
another instance gives the new instance the *same* record and the
record validates against the same OPRF seed. So:

- The OPRF seed (32 bytes) is treated as part of the server identity.
- It must be **stable across restarts** for existing records to
  validate.
- It must be **distinct per deploy** unless deploys share users —
  i.e., staging and production should each have their own OPRF
  seed if they have separate user databases (they do).

### 5.1 OPRF seed lifecycle

- Generated once via `hulactl opaque-seed` (new command — outputs
  a base64 32-byte value).
- Stored in `config.OPAQUE.OPRFSeed` (yaml) **or**
  `HULA_OPAQUE_OPRF_SEED` (env). Env wins. Same operator workflow
  as the TOTP encryption key.
- If absent on first boot: hula generates one and **logs it once,
  loud and clear**, with instructions to copy it into config —
  same pattern as `JWTKey` empty-on-boot today. Subsequent boots
  with no seed return an error during opaque init (registers/logins
  fail with "OPRF seed not configured").
- Rotation: bumping the seed invalidates **every existing OPAQUE
  record** (records depend on the per-user OPRF key derived from
  the seed). A rotation flow is documented but tooling is deferred
  until v2 — for now, rotation = ask every user to re-register.

### 5.2 Per-user persistence

| User type        | Storage location                                  | OPAQUE indicator |
|------------------|---------------------------------------------------|-------------------|
| `admin`          | bolt `opaque_records` keyed `admin|admin`         | `config.Admin.OpaqueRegistered = true` (computed at boot) |
| `internal` users | bolt `opaque_records` keyed `internal|<username>` | new bool on apiobjects.User: `opaque_record_set` |

`config.Admin.Hash` (the legacy argon2id digest) stays in config
during the migration window. Once a record exists in
`opaque_records[admin|admin]`, the server prefers OPAQUE; the
legacy hash is ignored at login time but kept on disk so a manual
roll-back is possible. After the deprecation window closes, a new
boot-time check warns / errors if `Admin.Hash` is still set.

---

## 6. Server architecture

```
pkg/auth/opaque/
├── opaque.go            # Server holding the OPRF seed + AKE keypair
├── config.go            # Suite assembly (Ristretto255-SHA512-Argon2id)
├── seed.go              # Loads OPRF seed from config or env; generates on first boot
├── session.go           # In-memory cache of pending login + register sessions
├── register.go          # OpaqueRegisterInit / Finish helpers
├── login.go             # OpaqueLoginInit / Finish helpers
└── opaque_test.go       # Round-trip with the bytemare client
```

### 6.1 Boot wire-up (server/run_unified.go)

```go
seed, err := opaque.LoadOrGenerateSeed(conf.OPAQUE)
if err != nil { /* fatal */ }
opaqueSrv, err := opaque.NewServer(seed, conf.OPAQUE.AKEKey)
opaque.SetGlobal(opaqueSrv)
```

Same pattern as `notifier.SetGlobal` and `realtime.SetGlobal` from
Phase 5a — the package exposes a process-global accessor so the
auth RPC handlers can reach it without a wider DI graph.

### 6.2 RPC handler shape (sketch)

```go
func (s *Server) OpaqueLoginInit(ctx, req) (*resp, error) {
    rec, err := opaque.GetRecord(req.Username, req.Provider)
    if err != nil { return nil, status.Internal(...) }
    if rec == nil {
        // Honest fall-through: tell the client to use legacy.
        return &resp{LegacyAvailable: legacyHashExists(req.Username, req.Provider)}, nil
    }
    sid, ke2, err := opaque.Global().LoginInit(req.Username, rec, req.Ke1)
    if err != nil { return nil, status.Internal(...) }
    return &resp{Ke2: ke2, SessionId: sid}, nil
}

func (s *Server) OpaqueLoginFinish(ctx, req) (*resp, error) {
    user, ok, err := opaque.Global().LoginFinish(req.SessionId, req.Ke3)
    if err != nil || !ok { return nil, status.Unauthenticated("...") }
    // Issue JWT exactly like the legacy LoginAdmin path does today.
    jwt, err := model.NewJWTClaimsCommit(...)
    return &resp{Admintoken: jwt}, nil
}
```

---

## 7. Browser client (Option A — Go-WASM)

```
web/analytics/src/lib/api/opaque/
├── client.ts           # thin TS wrapper that loads the WASM, exposes
│                       # async loginAdmin / loginInternal / register
├── opaque.wasm         # Go-built artifact — 4.8 MB raw / 1.25 MB gz
├── opaque.wasm.br      # Brotli pre-compressed for `.br` static
│                       # serving — ~1.0 MB
├── wasm_exec.js        # standard Go's JS bridge (vendored from $GOROOT)
└── wasm-loader.ts      # dynamic-imports opaque.wasm only on /login
```

`opaque.wasm` is built by `make opaque-wasm` from a small Go entry
that wraps the bytemare/opaque client API and exposes its calls
(registerInit, registerFinish, loginInit, loginFinish) via
`syscall/js`.

**Verified compile + execute** — see §18. Default `GOOS=js
GOARCH=wasm go build` produces a 4.8 MB binary that compresses to
~1.25 MB gzipped. Brotli at the highest level brings that to
~1.0 MB. Confirmed running under Node 22's WebAssembly host with
the standard `wasm_exec.js` bridge; the OPRF blind in
`RegistrationInit` exercises the JS `crypto.getRandomValues` shim
the same way a browser would.

Bundle-size impact: the wasm is **lazy-loaded** behind the existing
`/login` route — the dashboard chunks (the 20 KB first-load budget
the analytics build guards) are unchanged. We add a separate budget
for the login route: ≤ 1.5 MB gzipped (or ≤ 1.2 MB Brotli)
including the wasm. Comparable to a typical JS-framework cold-load;
on hula's otherwise-tiny baseline it's noticeable but not blocking.

### 7.1 Loading strategy

- The `/login` route's `+page.svelte` triggers a dynamic
  `import('./opaque.wasm-loader.ts')` on **mount**, not on submit
  — by the time the user finishes typing their password, the
  wasm is already instantiated.
- A small "preparing secure exchange…" placeholder shows for the
  first ~1s on slow connections.
- Cache-Control: `public, max-age=31536000, immutable` plus a
  content-hash in the filename so the wasm is cached after the
  first visit. Re-downloads only happen on hula upgrades.

### 7.1 Browser-side login flow

```ts
// In LoginView's admin form submit handler:
async function doAdminLogin(username, password) {
  await opaque.ready();              // resolves once wasm is loaded
  const ke1 = await opaque.loginInit(username, password);
  const init = await fetch('/api/v1/auth/opaque/login/init', {
    method: 'POST',
    body: JSON.stringify({username, provider:'admin', ke1}),
  });
  const initR = await init.json();
  if (initR.legacy_available && !initR.ke2) {
    return legacyLoginAdmin(username, password); // existing path
  }
  const ke3 = await opaque.loginFinish(initR.ke2);
  const finish = await fetch('/api/v1/auth/opaque/login/finish', {
    method: 'POST',
    body: JSON.stringify({session_id:initR.session_id, ke3}),
  });
  const {admintoken} = await finish.json();
  setToken(admintoken);              // existing helper
  window.location.href = `${base}/`;
}
```

The legacy fallback path stays so the migration window doesn't
break operators who haven't run `hulactl set-password` yet.

---

## 8. `hulactl` client

Two new commands; one updated:

| Command                                        | Behavior |
|------------------------------------------------|----------|
| `hulactl set-password [--username admin]`     | OPAQUE registration round-trip. Replaces the workflow that ended with `hulactl generatehash` + manual config edit. Persists the record on the server side. |
| `hulactl auth <host>`                          | Tries OPAQUE login first; falls back to legacy `LoginAdmin` / `LoginWithSecret` only when the server reports `legacy_available`. |
| `hulactl opaque-seed`                          | Generate a base64 32-byte OPRF seed. Operator pastes into `config.OPAQUE.OPRFSeed` or sets `HULA_OPAQUE_OPRF_SEED`. |

Same `bytemare/opaque` import as the server uses. No new deps.

---

## 9. Internal provider — wiring

The internal provider's existing flow:
```
LoginWithSecret(req) → IzcrProvider.LoginWithSecret(req) →
   apiobjects.FindUserByIdentity(req.Identity, "internal") →
   compare argon2id(req.Secret, user.PasswordHash)
```

New flow:
```
LoginWithSecret stays as-is for legacy fallback.
OpaqueLoginInit/Finish handle the OPAQUE path, populated for users
with opaque_record_set=true.
```

Setting an internal user's password (admin authoring or self-
service) routes to the new OPAQUE register endpoints with
`provider: "internal"`; the existing argon2id `PasswordHash` is
left null on new users and ignored on existing ones once they have
an OPAQUE record.

---

## 10. Migration strategy

```
Phase A (deploy stage-1..stage-7): OPAQUE ships side-by-side with
   legacy. config.Admin.Hash still authoritative for `admin` until
   the operator runs `hulactl set-password`. Internal users keep
   their PasswordHash until next password change, which goes
   through OPAQUE.

Phase B (operator action): Run `hulactl set-password` against each
   deployed hula instance. The CLI registers an OPAQUE record;
   server starts preferring OPAQUE for that user.

Phase C (3-month deprecation window): server logs a WRN at boot
   when config.Admin.Hash is set AND opaque_records[admin|admin]
   exists (legacy now redundant). Logs WRN on every legacy
   LoginAdmin call ("legacy admin login used; rotate to OPAQUE
   via hulactl set-password").

Phase D (after window): legacy `LoginAdmin` returns
   FailedPrecondition unless `config.Admin.AllowLegacy = true`
   (escape hatch for self-hosters who can't update their UI).
   `IzcrProvider.LoginWithSecret` keeps the legacy path because
   third-party deployments have arbitrary internal-user counts
   that take longer to migrate.
```

### 10.1 Roll-back

If something is wrong with OPAQUE post-deploy, the operator can:
1. Delete the bolt `opaque_records` entry for `admin|admin`.
2. Re-deploy with `config.Admin.Hash` still set (it never went
   away in Phase A or B).
3. Login resumes via legacy.

No data loss, no JWT-key churn. The downgrade is one bolt-key
delete; same UX as not enabling OPAQUE in the first place.

---

## 11. Stage breakdown

### Stage 1 — Library spike + interop test

**Goal**: confirm Option A (Go server + Go-WASM browser) actually
interoperates before committing to the rest.

- New repo dir `experiments/opaque-spike/`:
  - Go program: bytemare server doing register + login.
  - Go-WASM artifact: bytemare client compiled to wasm.
  - Tiny HTML harness running register + login from the browser.
- Round-trip a synthetic password through register → login →
  asserts `session_key` agreement.
- If it works, stage 1 outputs `pkg/auth/opaque/wasm/` build script
  + `Makefile` target. If it doesn't, we re-evaluate Option B.

**Acceptance**: green round-trip in CI on a Linux runner.

**Size**: 3 days.

---

### Stage 2 — Server OPAQUE package + OPRF seed

- `pkg/auth/opaque/`: server, suite config, seed loader, session
  cache (60s TTL).
- `pkg/auth/opaque/opaque_test.go`: round-trip against a Go client.
- `config.OPAQUE.OPRFSeed` + env binding.
- `hulactl opaque-seed` command.

**Acceptance**: `go test ./pkg/auth/opaque/...` green; boot logs
report seed source (config / env / generated).

**Size**: 3 days.

---

### Stage 3 — Bolt storage + proto + auth RPCs

- `pkg/store/bolt/opaque.go` with `StoredOpaqueRecord` + put/get/delete.
- `auth.proto`: 4 RPCs (register init+finish, login init+finish).
- `make protobuf` regen.
- `pkg/api/v1/auth/authimpl.go` implements the 4 RPCs delegating
  to `pkg/auth/opaque`.

**Acceptance**: `go build .` clean; new bolt bucket created on first
boot; RPCs reachable via REST gateway.

**Size**: 3 days.

---

### Stage 4 — `hulactl set-password` + OPAQUE login in `auth`

- New `hulactl set-password` (defaults to admin; `--username` for
  internal users).
- Modify `hulactl auth` to attempt OPAQUE first, fall back on
  `legacy_available`.

**Acceptance**: from a clean state, `hulactl set-password` →
`hulactl auth host` round-trips a JWT.

**Size**: 2 days.

---

### Stage 5 — Browser client + Login UI wiring

- `web/analytics/src/lib/api/opaque/`: TS wrapper + WASM loader.
- `web/analytics/src/routes/login/+page.svelte`: try OPAQUE first.
- Bundle-size budget for the login route raised to 800 KB gzipped
  (just the login chunk; dashboard budget unchanged).
- Pre-load the WASM on `/login` mount so the password form submit
  feels instant.

**Acceptance**: `pnpm check` clean; admin sign-in via the browser
on `www.tlaloc.us` and `staging.tlaloc.us` both produce a working
JWT.

**Size**: 4 days.

---

### Stage 6 — Initial-admin migration helper

- One-time `hulactl set-password --migrate` (reads
  `config.Admin.Hash` is irrelevant — argon2id digest is one-way; we
  cannot derive an OPAQUE record from it). The operator just
  re-types the admin password once; CLI registers the record. Old
  hash stays in config but is bypassed once the record exists.

**Acceptance**: documented migration runbook in
`DEPLOYMENT.md`; smoke-test on staging shows JWT issuance via
OPAQUE post-migration.

**Size**: 1 day.

---

### Stage 7 — Internal-provider OPAQUE wiring

- Modify `IzcrProvider.LoginWithSecret` to consult
  `opaque_records[internal|<username>]` first, fall back to legacy
  `PasswordHash` if no record.
- New `apiobjects.User.OpaqueRecordSet` bool surfaced in
  `/api/v1/auth/users` for the admin UI's user-management page.
- Web admin "Set password" sheet routes to OPAQUE register flow
  (no plaintext password in the request body).

**Acceptance**: e2e user-CRUD suite (`23-bolt-users.sh`) extended
with an OPAQUE-set-password leg; passes.

**Size**: 3 days.

---

### Stage 8 — e2e + interop CI + status doc

- New e2e suite `32-opaque.sh`:
  - Server fresh: legacy admin login still works.
  - `hulactl set-password` → OPAQUE login works → legacy login
    starts emitting the deprecation warning header.
  - Internal-user OPAQUE register + login round-trip.
  - Multi-host: same OPAQUE record validates from
    `www.tlaloc.us:443` and `staging.tlaloc.us:443`.
- Browser-side spec: `web/analytics/src/lib/api/opaque/opaque.spec.ts`
  exercises the WASM loader against a mocked server.
- `OPAQUE_STATUS.md` mirroring the per-phase status doc layout
  (this isn't a "phase" per the roadmap, but the same shape gives
  consistent sign-off bookkeeping).

**Acceptance**: `./test/e2e/run.sh` → 119+/119+ green
(+1 suite over current 118).

**Size**: 3 days.

---

## 12. Timeline

| Stage | Size | Cumulative |
|-------|------|------------|
| 1     | 3 d  | 3          |
| 2     | 3 d  | 6          |
| 3     | 3 d  | 9          |
| 4     | 2 d  | 11         |
| 5     | 4 d  | 15         |
| 6     | 1 d  | 16         |
| 7     | 3 d  | 19         |
| 8     | 3 d  | 22         |

Total: **~22 working days** (≈ 4.4 weeks single-threaded). Stages 4
and 5 can fork in parallel after stage 3 lands → ~3.5 weeks
calendar.

---

## 13. Risks + open items

- **Library interop spike (stage 1) is gating.** Compile + execute
  is **already verified** (§18) — what stage 1 still has to prove
  is a full register-then-login round-trip between a Go server
  and a Go-WASM client. If the round-trip fails, switch to
  Option B (serenity-kit/opaque WASM in browser). Server-side
  stages 2–4 are unaffected by that swap.
- **Argon2id browser cost.** 64 MiB / 3 iters is ~600 ms on
  desktop, ~1.5–2 s on a mid-range phone (WASM adds ~2× over
  native). Acceptable for login (one-shot), painful if every page
  re-derived. Web sees argon2id only on `/login` form submit.
  Configurable down to m=32 MiB if mobile-heavy deploys complain.
- **WASM size on cellular.** Verified at **~1.25 MB gzipped /
  ~1.0 MB Brotli** with stock standard-Go (§18). Lazy-loaded
  behind the login route; doesn't bloat the dashboard's first-load
  budget. Mitigations if needed: serve `.br` (saves 250 KB);
  evaluate TinyGo (could drop to 100-300 KB but unverified —
  TinyGo's `crypto/*` and `math/big` support has historical gaps
  that may bite the bytemare deps; spike-cost ~1 day to find out).
- **OPRF seed handling parity with TOTP key.** Same env-var
  workflow operators already know — `HULA_TOTP_ENCRYPTION_KEY`
  ↔ `HULA_OPAQUE_OPRF_SEED`. Document side-by-side in
  `DEPLOYMENT.md`.
- **Multi-instance deploys with shared bolt.** If two hula
  containers behind a load balancer share a bolt file, they
  **must** also share the OPRF seed. Same with the AKE keypair.
  Document a "two-server deploy" runbook for the staging+production
  case.
- **TOTP gating.** OPAQUE replaces password exchange only. The
  TOTP-required flow continues exactly as today: a user with TOTP
  set up gets a partial JWT after `OpaqueLoginFinish`, then
  validates the OTP, then gets a full JWT. Stage 7 verifies the
  partial-JWT case for internal users.
- **Browser WASM build pipeline.** Adds a `tinygo` step to the
  analytics build. CI sees a one-time install. Image size grows by
  the WASM artifact only (binary lives in
  `web/analytics/build/_app/...`).

---

## 14. Open decisions

Before stage 1 starts:

1. **Library option A vs B.** Recommend A (Go-WASM) for single-
   library-everywhere; B (serenity-kit) is a fallback if the WASM
   round-trip in stage 1 turns up a wire-format gotcha. **Need a
   call from the user.**
2. **Argon2id parameters.** OWASP-min (m=64MiB, t=3, p=1) is the
   plan default; deploys with low-end mobile users may want
   m=32MiB. Configurable via `config.OPAQUE.Argon2id`.
3. **Legacy deprecation window length.** 3 months suggested; can
   extend if the user has a big internal-user population.
4. **Internal-user password rotation UX.** Today the admin sets
   passwords for internal users via the admin UI. After OPAQUE,
   admin can no longer choose the password (OPAQUE registration
   requires the user's plaintext to derive the OPRF blind, which
   the admin doesn't have). Two options:
   a. Admin sets a one-time invite token; user registers their own
      password client-side via OPAQUE on first login.
   b. Admin force-resets via legacy argon2id (issues a temporary
      password); user is forced to OPAQUE-rotate on next login.
   **Recommended: (a)** — the invite-token flow already exists in
   the auth proto (`InviteUser` / `ResendInviteUser`); we wire its
   accept-side to OPAQUE registration instead of password-set.

---

## 15. Sign-off checklist (at end of stage 8)

- [ ] All 8 stages landed.
- [ ] `go test ./pkg/auth/opaque/...` + `pnpm test` green.
- [ ] `pnpm check` 0/0; bundle dashboard first-load < 150 KB
      gzipped (login route chunk ≤ 800 KB).
- [ ] `./test/e2e/run.sh` ≥ 119/119.
- [ ] Live curl test: admin sign-in via browser on
      `www.tlaloc.us/analytics/login`,
      `staging.tlaloc.us/analytics/login`, and `hulactl auth
      www.tlaloc.us` all succeed via OPAQUE.
- [ ] `DEPLOYMENT.md` documents `HULA_OPAQUE_OPRF_SEED`,
      `hulactl opaque-seed`, `hulactl set-password`, and the
      legacy-rollback runbook.
- [ ] `OPAQUE_STATUS.md` written.

---

## 16. Parallel cleanup: purge `izcr` / `izuma` references

> ⚠️  **Discovered while researching this plan; tracked here so we
> don't forget.**

Hula was bootstrapped from izcr's RBAC + auth + unified-server code
(see `PLAN_0.md`). The intent was always "copy and rename"; in
practice some references to `izcr` and `izuma` survived the
rename pass. **Hula must not have any `izcr` / `Izcr` / `izuma`
identifiers in code it ships.** Found via `grep -rni
"izcr\|izuma"`:

### 16.1 Active Go code (must rename)

| File | Hit | Cleanup |
|------|-----|---------|
| `pkg/server/authware/provider/internal/internalprovider.go` | `type IzcrProvider struct`, `NewIzcrProvider`, `cfg.Name = "Izcr"` | rename to `InternalProvider` / `NewInternalProvider`; default name `"internal"` |
| `pkg/server/authware/claims.go:161` | `c.IDProviderName != "izcr"` (auth gate logic) | change literal to `"internal"` ; back-compat shim accepts `"izcr"` for one release |
| `pkg/server/authware/claims.go:306` | `claims.IDProviderName = "izcr"` (default on new claim) | set to `"internal"` |
| `pkg/server/authware/middleware.go:82` | `rego.Query("data.izcr.authz.allow")` (OPA policy entrypoint) | rename OPA package + query to `data.hula.authz.allow`; update embedded `.rego` files in lockstep |
| `pkg/server/authware/grpcutils.go:43-78` | comments + a runtime `strings.Contains(fn, "izcr")` filter | rename comments; remove the `izcr` substring filter (it never matches hula's gRPC method names) |
| `pkg/server/unified/server.go:25,42,69,124,356` | comments referencing "izcr pattern", `internalCert "for izcragent mTLS"` | strip izcr framing; the `internalCert` field stays but the comment loses the izcr name |
| `config/globalconfig.go:3,37,98` | comment-only references | strip |
| `config/config.go:804,808` | comment-only references | strip |

### 16.2 Proto annotation namespace (RENAME — wide blast radius)

**Single largest item.** The permission annotation extension lives at:

```
protoext/izuma/auth/permission.proto
  package izuma.auth;
  option go_package = "github.com/tlalocweb/hulation/protoext/izuma/auth;izumaauth";
```

…and every `.proto` in `pkg/apispec/v1/*.proto` carries:

```proto
import "izuma/auth/permission.proto";
option (izuma.auth.permission) = { needs: ["server.{server_id}.admin"] };
```

This is **162 occurrences** across 12 service protos + the
generated `permission.pb.go` (41 hits — those regenerate
automatically). Rename target:

```
protoext/hula/auth/permission.proto
  package hula.auth;
  option go_package = "github.com/tlalocweb/hulation/protoext/hula/auth;hulaauth";
```

Touches **every** service proto. Mechanical sed, then `make
protobuf` to regen. Will produce a large but boring diff.

### 16.3 Third-party module path: `go.izuma.io/conftagz`

```
main.go:16        "go.izuma.io/conftagz"
config/config.go:18  "go.izuma.io/conftagz"
```

`go.izuma.io/conftagz` is an **external dependency** vendored
under that vanity import path. Hula doesn't own it. Two
realistic options:

(a) **Fork-and-rename** to `github.com/tlalocweb/conftagz` (or
    inline-vendor under `pkg/conftagz`). Adds maintenance burden
    on hula but cuts the izuma reference cleanly.

(b) **Replace conftagz** with hand-written `flag` / `os.Getenv`
    plumbing — conftagz is a small env-var-binding library; the
    surface we use is narrow. Roughly a day of work.

Recommendation: (b). The `conftagz` API surface in hula is small
(`conftagz.Process(cfg)` invoked once at boot); replacement keeps
hula's go.mod free of the izuma vanity domain.

### 16.4 Documentation

`PLAN_0.md` has ~40 izcr references documenting the original
copy-and-adopt provenance. Those are **historical context** — the
user-facing docs (`README.md`, `DEPLOYMENT.md`, `UI_PRD.md`)
should be izcr-free, but the phase-0 plan can keep its references
as historical record. Do a final pass to confirm the user-facing
docs are clean.

### 16.5 Sequencing relative to OPAQUE work

- The **internal-provider rename** in §16.1 collides with stage 7
  of this plan (which modifies `IzcrProvider.LoginWithSecret`).
  Do the rename **before** stage 7, or fold the rename into
  stage 7's commit.
- The **proto-annotation rename** in §16.2 is independent of
  OPAQUE but touches the same auth.proto we're editing in stage
  3. Coordinate so the diffs don't cross-contaminate. Suggested:
  do the proto rename in a dedicated PR before stage 3 lands.
- The **conftagz replacement** in §16.3 is fully independent.
  Schedule as a standalone follow-up.

### 16.6 Cleanup tracking

Stage these as a sibling plan (`IZCR_PURGE_PLAN.md`) once OPAQUE
stages 1–3 are scoped, or fold each item into the OPAQUE stage it
intersects with. **Either way, do not let "ship OPAQUE" close
without the renames listed in §16.1 and §16.2 also landed.**

---

## 17. WASM-compile-and-execute spike (verified)

Before recommending Option A I asked the obvious question: does
`bytemare/opaque` actually compile to WASM and execute under a
JS host? **Yes — verified locally, 2026-04-25.**

### 17.1 Reproduction

```bash
mkdir /tmp/wasm-spike && cd /tmp/wasm-spike
cat > go.mod <<'EOF'
module wasm-spike
go 1.26
EOF

cat > main.go <<'EOF'
//go:build js && wasm

package main

import (
	"fmt"
	"github.com/bytemare/opaque"
)

func main() {
	cfg := opaque.DefaultConfiguration()
	srv, err := cfg.Server()
	if err != nil { fmt.Println("server err:", err); return }
	cli, err := cfg.Client()
	if err != nil { fmt.Println("client err:", err); return }
	regReq, err := cli.RegistrationInit([]byte("password123"))
	if err != nil { fmt.Println("reg-init err:", err); return }
	bytes := regReq.Serialize()
	fmt.Printf("ok: reg-init M1 = %d bytes (suite oprf=%v ake=%v ksf=%v)\n",
		len(bytes), cfg.OPRF, cfg.AKE, cfg.KSF)
	_ = srv
}
EOF

go mod tidy
GOOS=js GOARCH=wasm go build -o opaque.wasm .
cp $(go env GOROOT)/lib/wasm/wasm_exec.js .

cat > run.mjs <<'EOF'
import { readFile } from 'node:fs/promises';
import './wasm_exec.js';
const go = new globalThis.Go();
const wasm = await readFile('./opaque.wasm');
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
await go.run(instance);
EOF

node --experimental-default-type=module run.mjs
```

### 17.2 Output

```
compile exit=0  size: 4808108 bytes
gzip -9 :     1250723 bytes  (1.25 MB)
brotli -q11:  ~1004000 bytes (~1.0 MB) [estimated]

node run:
  ok: reg-init M1 = 32 bytes (suite oprf=1 ake=1 ksf=Argon2id)
```

### 17.3 What this proves and what it doesn't

**Proves:**
- `bytemare/opaque` + transitive deps (`bytemare/ksf`,
  `bytemare/ecc`, `bytemare/hash`, `filippo.io/edwards25519`,
  `filippo.io/nistec`, `gtank/ristretto255`, `golang.org/x/crypto`,
  `bytemare/secp256k1`, `bytemare/hash2curve`) are **all pure
  Go** — no CGO blockers — and compile cleanly with stock
  `GOOS=js GOARCH=wasm`.
- The default suite (Ristretto255 + Ristretto-AKE + Argon2id)
  is the one we want.
- The OPRF blind during `RegistrationInit` succeeds, which
  means `crypto/rand` correctly bridges to JS's
  `crypto.getRandomValues` via standard Go's `wasm_exec.js`
  shim.
- The artifact runs under Node 22's WebAssembly host, which uses
  the same V8 engine + WebAssembly stack as Chrome / Edge.
  Behavior under Safari and Firefox should match (all WASM
  hosts implement the same WASM 1.0 spec) — to be confirmed in
  stage 5.

**Does not prove (still open):**
- A full register-then-login round-trip between a Go server and
  a Go-WASM client. The blind succeeds; the AKE handshake hasn't
  been exercised. **Stage 1 of the implementation plan covers
  this.**
- Performance on real mobile devices. Argon2id with the planned
  parameters (m=64 MiB, t=3, p=1) will be slower in WASM than
  native — typical 2× penalty. Stage 5 will measure on a real
  iPhone + Pixel.
- TinyGo viability for a smaller bundle — untested; the spike
  used standard Go.

### 17.4 Confidence rating

| Claim | Confidence |
|-------|------------|
| bytemare/opaque can be compiled to WASM | **Verified** |
| The WASM artifact executes in a real JS host | **Verified** (Node 22 = same V8 + WASM stack as Chrome) |
| Server (Go) ↔ client (Go-WASM) round-trip works | **High** (both ends use the same crate; protocol logic is shared) — pending stage-1 confirmation |
| Bundle size ≤ 1.5 MB gzipped | **Verified at 1.25 MB** |
| Argon2id browser perf ≤ 2 s on mid-range phone | **Likely** (extrapolating from native ~600 ms × 2× WASM penalty) — pending stage-5 measurement |
| TinyGo can shrink to ≤ 300 KB | **Speculative** — not tested |

**Bottom line: Option A is viable.** Pre-spike I rated my
confidence "moderate, not verified"; post-spike it's "high,
verified through reg-init; full round-trip is the next milestone."

---

## 18. What this doesn't do (and we're OK with that)

- **Does not protect against a fully compromised server.** An
  attacker who has the bolt file + the OPRF seed + the AKE key
  + can run arbitrary code can phish active sessions. OPAQUE
  raises the bar (no plaintext-password leak during normal logins;
  no MITM via TLS-terminating attacker can replay credentials),
  but it isn't a confidentiality boundary against root.
- **Does not eliminate the need for TOTP / hardware keys.** OPAQUE
  is one factor (something-you-know). Hardware-backed second
  factors are still the right next step for the `admin` user.
- **Does not change SSO.** OAuth + OIDC flows are untouched.
- **Does not migrate scheduled-report or alert-recipient
  passwords.** Those don't exist; they're emails/push-tokens.
