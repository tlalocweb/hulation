# hulaagent — Rust mTLS sidecar for hula

A small, fast-starting Rust binary that holds an mTLS identity and lets
local apps drive a hula server through a unix-socket text protocol.

The motivating scenario: a CI runner, a developer's laptop, or a
deploy-bot needs to issue `build`, `staging-build`, `pull`, `push`,
`sync`, `commit`, `push-file`, or `get-file` against a hula server
without holding an admin password or JWT token. The local apps speak
to hulaagent over a unix-socket; hulaagent presents the operator-issued
agent cert to hula and forwards permitted commands.

This file is the source-of-truth design doc — same role HA_PLAN3.md
plays for HA Stage 3.

## Architecture at a glance

```
+----------------+   unix-socket (HLAP)            +----------+  mTLS  +-------+
|  local app(s)  | -----------------------------> | hulaagent | -----> | hula  |
+----------------+   JSON-Lines, multiplexed      +----------+         +-------+
                                                                         |
                                                                  agent registry
                                                                  (cert ID → perms)
```

- **hulaagent** is a single Rust binary. It reads its config (a yaml
  file produced by `hulactl create-agent`), opens a unix-socket,
  speaks HLAP, and translates each HLAP command into a single
  authenticated REST call against hula.
- **hula** verifies the agent's mTLS cert against its registry, looks
  up the registered permissions, and runs the requested operation iff
  the cert is allowed for that op against that server.

## Authentication: cert-only, no tokens

The agent's mTLS cert is the only credential. No JWT, no bearer
tokens, no OPAQUE. The certificate carries the agent's identity in
two places:

- **Subject CN**: `agent:<id>` where `<id>` is a base64url-encoded
  random 16-byte value generated server-side at create-agent time.
- **NotAfter**: the cert's expiry. Operators pick a duration via
  `--expires-in 1yr` (or whatever); hula refuses any cert past
  NotAfter even if the registry still has it active.

Hula maintains an **agent registry** (raft-FSM-backed) keyed by `<id>`,
storing:

- `permissions`: the per-site `allow` map from create-agent.
- `created_at`, `expires_at`: timestamps.
- `revoked`: bool. `hulactl revoke-agent <id>` flips this.

Every agent-mTLS connection: hula extracts `<id>` from the SAN, looks
it up, refuses if missing/revoked/expired. Every API call from that
connection: hula intersects the requested op against the agent's
permissions.

## Cert authority

A new **Agent CA**, separate from the existing Team CA. Reasons:

- Team CA is for hula-to-hula internal traffic and lives only on hula
  servers. Agents run on third-party machines; they shouldn't be
  members of the team trust domain.
- Distinct CAs make it easy to revoke "all agents" (rotate Agent CA)
  without disrupting team traffic, and vice versa.

The Agent CA is created on first hula boot if absent (lives at
`<data_dir>/agent-ca.{pem,key}`). The `ca.pem` (cert only) ships
inline in every generated agent yaml so the agent can verify hula's
serving cert. Hula's existing TLS cert (Let's Encrypt or operator-
provided) is used to authenticate the **server** side of the mTLS
handshake — agents pin to the configured `hula_host`.

## `hulactl create-agent`

Two invocation forms:

**Flag form** (one-shot for simple cases):
```
hulactl create-agent \
    --allow-build=gravhl \
    --allow-staging-build=gravhl,<OPTS> \
    --allow-build=tlaloc \
    --expires-in=1yr \
    > my-agent-config.yaml
```

**Template form** (for repeatable agent configs in version control):
```
hulactl create-agent -c agent-template.yaml > agent-config.yaml
```

Where `agent-template.yaml` is:
```yaml
config:
  expires-in: 1yr
sites:
  gravhl:
    allow:
      build: ""
      staging-build: ""
  tlaloc:
    allow:
      build: ""
```

In both cases the output yaml has the same shape:
```yaml
agent:
  id: <secure-rand-base64>
  mTLS:
    ca: |
      -----BEGIN CERTIFICATE-----
      ...
    cert: |
      -----BEGIN CERTIFICATE-----
      ...
    key: |
      -----BEGIN EC PRIVATE KEY-----
      ...
  hula_host: hula.example.com:443
sites:
  gravhl:
    allow:
      build: ""
      staging-build: "<OPTS>"
  tlaloc:
    allow:
      build: ""
```

The `key` is generated client-side in `hulactl` (CSR submitted to hula
for signing) so the private key never touches hula's disk. Or — for
simplicity in v1 — generated server-side and shipped back inline; we
mark this as a v2 hardening item.

## Permission model

`sites.<site_id>.allow.<verb>` carries the option string for that
verb (empty string = no options, just allow). Verbs match the HLAP
verbs (see below). The permission string is opaque to hula — agents
can't pass arbitrary options at HLAP-call time; whatever the
registered `allow` string says is what they get. This means:

- `allow.build: ""` lets the agent do a plain `build` against that
  site, no flags.
- `allow.staging-build: "OPT1,OPT2"` lets the agent do
  `staging-build OPT1,OPT2` and nothing else.
- `allow.staging-build` absent means no staging-build for that site.

Wildcards (`"*"` to allow any options) are explicitly NOT supported in
v1. Operators who need flexibility issue multiple agents.

## HLAP — Hula Local Agent Protocol

Newline-delimited JSON ("JSON Lines") over a unix-socket. One JSON
object per line, separated by single `\n` (no CRLF), UTF-8. Verbs
multiplex on a single connection via client-chosen stream IDs;
cancellation runs on a separate control connection.

### Why JSON Lines

A small local LLM is a first-class HLAP client: the caller may be a
1–3B-parameter on-device model translating natural-language goals
("ship the May 10 fix to gravhl") into HLAP envelopes. JSON is the
only format with the pretraining mass, the uniform escape rules, and
the constrained-decoding tooling (llama.cpp grammars, vLLM guided
generation, outlines, etc.) to let a small model emit valid HLAP
without a post-validator. Length-prefixed framing and shell-quoted
arg lists both lost to JSON for that reason — LLMs cannot reliably
count bytes, and shell-quote escape rules are a known failure mode
for sub-7B models.

### Connection handshake

The agent writes exactly ONE banner envelope before reading anything:

```
< {"hulaagent":1,"hlap":1,"streams":true,"max_inflight":16}
```

| Field          | Meaning                                                     |
| -------------- | ----------------------------------------------------------- |
| `hulaagent`    | Binary major version (informational).                       |
| `hlap`         | HLAP protocol version. v1 in this spec.                     |
| `streams`      | True if multiplex is supported (always true in v1).         |
| `max_inflight` | Per-connection cap on concurrent in-flight verbs.           |

Forward-compatibility: the agent may add fields; clients ignore
unknown ones. Clients that don't recognise the banner's required
keys (`hlap` in particular) MUST abort.

### Stream multiplexing

Every client→agent envelope carries `"stream": <uint>` — a small,
client-chosen integer scoped to this connection. Stream IDs MUST be
fresh per verb on a given connection (no reuse while in flight); a
verb's stream becomes reusable only after its terminating envelope.
Every agent→client envelope echoes the `stream` of the verb it
belongs to. The agent may interleave responses across streams
freely; clients demultiplex.

A stream terminates with one of:
- `{"stream":N,"ok":true,"done":true, ...result fields}` — success
- `{"stream":N,"err":"<reason>", ...}` — failure

After either, the agent emits no further envelopes for that stream.

### Sessions and cancellation

The agent's first response envelope for any long-running verb
carries `"session": "<opaque-id>"` in addition to `stream`. The
session ID is stable for the lifetime of the verb's server-side
execution and unique across the running hulaagent process.

To cancel an in-flight verb, the client opens a **separate**
connection and sends:

```
> {"verb":"cancel","stream":1,"session":"sess_abc123"}
< {"stream":1,"ok":true,"done":true,"cancelled":true}
```

The agent context-cancels the underlying hula REST call. The
originating connection's stream terminates with
`{"stream":N,"err":"cancelled"}`. Cancellation is best-effort: side
effects already committed upstream (a git push that landed, a build
that finalised) don't roll back. Hula's existing build/sync
handlers honour `ctx.Done`.

Cancel runs on a separate connection so a head-of-line block on the
original socket (a slow `BUILD` whose log envelopes are draining
slowly into a wedged reader) doesn't prevent cancel from being
heard.

### Verbs (v1)

Each verb is a JSON envelope of the form:

```
{"verb":"<name>","stream":<uint>, ...verb-specific fields}
```

Verbs are lowercase. Permission gating happens server-side at hula
against the agent's registry record — hulaagent does NOT pre-
validate. (The agent's registered `allow.<verb>` permission string
is opaque to the agent; only hula can intersect it with the
incoming call.)

| Verb              | Required fields                            | Server-side hula endpoint                           |
| ----------------- | ------------------------------------------ | --------------------------------------------------- |
| `build`           | `site`                                     | `POST /api/site/trigger-build`                      |
| `staging-build`   | `site`                                     | `POST /api/staging/build`                           |
| `pull`            | `site`                                     | `POST /api/staging/{id}/git/pull`                   |
| `push`            | `site`                                     | `POST /api/staging/{id}/git/push`                   |
| `sync`            | `site`                                     | `POST /api/staging/{id}/git/sync`                   |
| `commit`          | `site`, `message`                          | `POST /api/staging/{id}/git/commit`                 |
| `stage`           | `site`, `paths` (array of strings)         | `POST /api/staging/{id}/git/stage`                  |
| `push-file`       | `site`, `path`, `content` (base64 bytes)   | `PUT /api/staging/{id}/dav/<path>`                  |
| `get-file`        | `site`, `path`                             | `GET /api/staging/{id}/dav/<path>`                  |
| `cancel`          | `session`                                  | (in-process; cancels the targeted session)          |

Binary file payloads (`push-file`, `get-file`) are carried as base64
in v1. The simplicity of one-JSON-per-line outweighs the ~33%
overhead. v2 may add a raw-bytes framing if large-file traffic
becomes a measured bottleneck.

### Streaming responses (build, sync, get-file)

After the initial OK envelope, long-running verbs emit one envelope
per logical chunk of output, in order, on the verb's stream. The
chunk envelopes carry `log` (text) or `chunk` (base64 bytes) keys.
The terminating envelope carries `ok:true,done:true` and any
verb-specific result fields.

Example `build` flow:

```
> {"verb":"build","stream":1,"site":"gravhl"}
< {"stream":1,"ok":true,"session":"sess_abc123","streaming":true}
< {"stream":1,"log":"resolving deps..."}
< {"stream":1,"log":"compiling 12 files..."}
< {"stream":1,"log":"WARN: unused var x"}
< {"stream":1,"log":"linking..."}
< {"stream":1,"ok":true,"done":true,"build_id":"b_123","status":"complete"}
```

Example `get-file` flow:

```
> {"verb":"get-file","stream":2,"site":"gravhl","path":"content/index.md"}
< {"stream":2,"ok":true,"session":"sess_def456","streaming":true,"content_type":"text/markdown"}
< {"stream":2,"chunk":"IyBJbmRleAo..."}
< {"stream":2,"ok":true,"done":true,"bytes":4096}
```

For short-output verbs (`commit`, `stage`, `push-file`, the failure
arms of any verb) the agent may emit a single combined
`ok:true,done:true` envelope with no intermediate `log`/`chunk`
envelopes.

### Error envelope

Errors share a common shape:

```
< {"stream":N,"err":"<short-reason>","detail":"<optional human text>","code":"<optional machine tag>"}
```

`err` is always present. `detail` is intended for log output;
`code` is intended for client branching. After an `err` envelope
the stream is terminated; no `done:true` follows.

### Authentication / authorisation

Implicit local trust: any process able to `connect(2)` to the
socket is treated as authorised. Authorisation is enforced
server-side at hula via the agent's mTLS cert and registry
permissions. Filesystem permissions on the socket path are the
only local gate — hulaagent binds at 0600 to a path under
`$XDG_RUNTIME_DIR` (or `/tmp` fallback) so only the running
user's processes can connect.

### Cert expiry mid-session — graceful drain

When the agent's mTLS cert crosses its `NotAfter` while sessions
are in flight, the agent drains gracefully rather than cutting off
immediately:

- **In-flight verbs run to completion.** The cert was valid when
  each verb started; the agent does not terminate ongoing builds,
  syncs, or file transfers just because the cert expired during
  execution. Hula's server-side handlers continue to honour the
  request (the registry's `IsActive(now)` check fires at mTLS
  handshake time, not on every API call within a session).
- **New verbs reject** with
  `{"err":"agent expired","code":"cert_expired"}`. This includes
  new verbs issued on existing connections that already had other
  verbs running successfully.
- **New connections fail at handshake** — the Phase-3 mTLS
  middleware refuses the TLS connection outright. The client sees
  a TLS error, not an HLAP envelope.
- **The agent does not auto-restart** on expiry; the operator
  re-runs `hulactl create-agent` and updates the agent config.

Operators tuning `--expires-in` should pick a window comfortably
longer than the longest verb expected to run (full mkdocs/hugo
build for the slowest site). One-week minimum is a reasonable
starting point; the v1 install template uses one year.

## Server-side changes in hula

1. **Agent CA bootstrap** at hula boot: if `<data_dir>/agent-ca.{pem,key}`
   absent, generate.
2. **Agent registry** in the FSM:
   ```
   _agents/by-id/<id>           → JSON{ permissions, created, expires, revoked }
   _agents/by-cert-fingerprint  → maps SHA256(cert) → id (fast lookup)
   ```
3. **Agent CA-signed cert verification middleware** on the unified
   listener: intercepts mTLS handshakes whose client cert was signed
   by the Agent CA, looks up the registry, attaches an `agent_perms`
   value to the request context.
4. **Per-route permission check** for the verbs above: when
   `agent_perms` is set, the route handler validates the verb+site
   against the registry.
5. **`hulactl create-agent`**: hits `POST /api/v1/agent/create` (admin
   JWT required), receives the yaml.
6. **`hulactl list-agents` / `hulactl revoke-agent <id>`**: parallel
   admin-only verbs for ongoing management.

## Rust crate layout

```
hulaagent/
├── Cargo.toml
├── src/
│   ├── main.rs           # arg parse, config load, run loop
│   ├── config.rs         # yaml parse for the agent config
│   ├── hlap.rs           # unix-socket server, HLAP parser, verb dispatch
│   ├── client.rs         # mTLS client to hula, REST mapping
│   └── error.rs          # error types
└── tests/
    └── e2e.rs            # spawned-hula + agent + HLAP-client roundtrip
```

Dependencies, kept tight for fast startup and small binary:

- `tokio` (single-threaded scheduler, `rt` + `net` features only)
- `rustls` + `tokio-rustls` for mTLS (no openssl dep)
- `serde` + `serde_yaml` for config
- `reqwest` configured with `rustls-tls` for the hula HTTP calls
- `clap` for arg parsing (or `argh` if we want even smaller)
- No async runtime sugar beyond what tokio provides; no actor frameworks.

Target: <2MB stripped binary, <50ms cold startup.

## Phasing

| Phase | Scope                                                                          | Gating e2e suite | Status |
| ----- | ------------------------------------------------------------------------------ | ---------------- | ------ |
| 1     | This design doc + skeleton dirs + `hulactl create-agent` (offline mode)        | —                | DONE   |
| 2     | Server-side `/api/agent/create`, agent CA bootstrap, registry storage          | 12a              | DONE   |
| 3     | mTLS verification middleware + `agent_perms` request context                   | — (unit tests)   | DONE   |
| 4     | Rust agent: config load, unix-socket server, HLAP parser, BUILD verb           | **12b**          | DONE   |
| 5     | Remaining HLAP verbs + multiplex / cancel / streaming protocol features        | **12c**          |        |
| 6     | `hulactl list-agents` / `revoke-agent` + cert-expiry drain                     | **12d**          |        |

Each unstarted phase's PR must land its gating suite green; see
§e2e test plan for what each suite covers.

## e2e test plan

Each unstarted phase ships its own gating bash suite under
`test/e2e/suites/`, following the convention of suite 12a (Phase
2). Bash is chosen over Rust integration tests to stay consistent
with the existing harness — discoverable by anyone scanning the
e2e tree, gated by the same GitHub Actions workflow that already
runs the team suites. JSON assertions use `jq`; suites that need
the hula-agent binary build it once at suite-time (`cargo build
--release`) and skip the binary-dependent steps if `cargo` isn't
on PATH (mirrors 12a's pattern).

Rust unit-level coverage of the HLAP parser, banner edge cases,
and JSON envelope decoding lives separately in
`hulaagent/src/**/*.rs` `#[test]` blocks — out of scope for this
section.

### Suite 12b — Phase 4 gate (HLAP basics, BUILD verb)

Covers the protocol-foundations slice that Phase 4 ships:

1. **Banner handshake.** Opening a socket to a running hula-agent
   produces exactly one banner envelope before any client write.
   Banner has `hlap:1`, `streams:true`, integer `max_inflight`.
   Negative path: a mock socket missing the banner causes a
   client to abort cleanly after a 1s read timeout.
2. **mTLS handshake at hula.** hula-agent's outbound mTLS
   connection to a running hula completes; a leaf signed by a
   different (non-agent) CA is rejected at TLS handshake time.
   This is the integration regression test for Phase 3's
   middleware against real Phase 4 dial code (Phase 3's existing
   middleware tests are mocks).
3. **BUILD verb roundtrip — success.** Client sends
   `{"verb":"build","stream":1,"site":"gravhl"}`, expects an
   initial OK envelope with `session` + `streaming:true`, ≥1
   `log` envelope, terminating
   `{"ok":true,"done":true,"build_id":...,"status":"complete"}`.
   Hula's build is verified to have produced site output on
   disk.
4. **BUILD permission denial.** Agent registered without
   `allow.build` for the target site; the verb returns
   `{"err":"forbidden"}` and the stream terminates with no
   `done` envelope.

### Suite 12c — Phase 5 gate (protocol features, remaining verbs)

Covers the protocol features that span verbs (multiplex, cancel,
streaming) plus a happy-path + permission-denial case per verb:

5. **Multiplexed streams.** Two `build` verbs issued on one
   connection with `stream:1` and `stream:2`. Log envelopes are
   correctly demultiplexed; both streams terminate independently
   regardless of completion order. A third concurrent verb
   attempted while at `max_inflight` returns
   `{"err":"max_inflight"}`.
6. **Cancellation roundtrip.** `build` on conn-A; on conn-B send
   `{"verb":"cancel","stream":1,"session":"<id from A>"}` and
   expect `{"ok":true,"done":true,"cancelled":true}`. Conn-A's
   stream terminates with `{"err":"cancelled"}`. Hula's build is
   verified to have stopped (no further log lines reach the
   client; no finalized output on disk).
7. **Streaming logs are incremental, not buffered.** `build`
   against a fixture site whose builder emits log output over 3+
   seconds; the suite records envelope arrival timestamps and
   asserts at least 3 distinct deltas — i.e. the agent is not
   accumulating logs and flushing once at the end.
8. **Each new verb's roundtrip.** `staging-build`, `pull`,
   `push`, `sync`, `commit`, `stage`, `push-file`, `get-file`
   each get a targeted happy-path case driven by the suite, and
   a permission-denied case where the agent's registry record
   omits the relevant `allow.<verb>`.
9. **get-file streams chunks.** Large file (>64 KB) returns via
   multiple `chunk` envelopes; client concatenates the base64
   bodies and verifies the SHA256 matches the served file.

### Suite 12d — Phase 6 gate (lifecycle: list / revoke / expiry)

Covers the operator-facing lifecycle surfaces and the cert-expiry
drain semantics:

10. **list-agents.** `hulactl list-agents` returns the agent
    written by suite 12a plus the agents created during this
    suite. Revoked agents appear with `revoked:true`.
11. **revoke-agent.** After revoke, a fresh agent connection
    fails at the mTLS middleware ("agent revoked", 401). New
    verbs on existing open connections also return
    `{"err":"agent revoked"}` and terminate the stream.
    In-flight verbs at the moment of revocation terminate at the
    next server-side ctx check (best-effort, like cancel).
12. **Cert expiry — graceful drain.** Agent created with
    `--expires-in=20s`. Suite issues a slow `build` 15s into the
    cert's lifetime; cert expires mid-build. The in-flight build
    runs to completion and emits its terminator. A second verb
    attempted post-expiry returns
    `{"err":"agent expired","code":"cert_expired"}`.

### Naming + harness conventions

- Suite scripts: `test/e2e/suites/12<letter>-<short-slug>.sh`.
  The 12-prefix keeps agent-related suites adjacent to the
  existing 12a-create-agent.
- Each script sources the standard suite helpers (compose-driven
  hula bring-up, `dc()`/`fail()`/`pass()` patterns shared with
  suites 41/42).
- Suite-required env: `WORKDIR`, `COMPOSE_PROJECT`,
  `COMPOSE_FILE` (the existing e2e harness wiring), plus
  agent-specific `HULA_AGENT_BIN` (path to the built Rust
  binary; empty triggers an inline `cargo build --release`).

### Out of scope for v1 e2e

- Performance / load (many agents fanning out; max-inflight
  saturation under realistic verb mixes).
- Fuzz testing of the JSON parser (Rust unit-level concern).
- Chaos cases (agent killed mid-stream; hula killed mid-stream;
  unix-socket corrupted). These land if and when an incident
  surfaces a gap, not preemptively.

## Phase 4 — what landed

Phase 4 turned the Phase-1 Rust placeholder into a working sidecar.
Shipped in four incremental commits gated by suite 12b:

- **Step 1 — wire plumbing.** Tokio current-thread runtime, unix-
  socket bind at mode 0600 under an operator-chosen path, banner
  emission on accept, JSON-Lines envelope decoder, per-connection
  handler that rejects malformed envelopes with `code:bad_envelope`
  / `code:missing_field` and tags responses with the originating
  stream id. SIGTERM/SIGINT unlink the socket on shutdown.

- **Step 2a — synchronous BUILD.** Rust mTLS client
  (`hulaagent/src/client.rs`) built on reqwest + rustls with the
  PEM blobs from the agent yaml as the Identity. Construction
  fails-fast on malformed PEMs. New Go endpoint
  `POST /api/agent/build` parallel to `/api/site/trigger-build`:
  same handler internals, but auth comes from the Phase 3
  middleware's attached `*registry.Record`, with
  `record.IsAllowed(site, "build")` gating each call.
  The registered allow-string is parsed as comma-separated build
  args; agents cannot override them at HLAP-call time.

- **Step 2b — log streaming.** New Go endpoint
  `GET /api/agent/build/{buildid}/stream` emits NDJSON
  (`{"type":"log","line":"..."}` per new log line, terminating
  `{"type":"end","status":"complete",...}`). Polls
  `BuildState.Snapshot()` every 250ms; flushes after each line
  for reverse-proxy-friendly streaming. The Rust BUILD dispatcher
  drains `reqwest::Response::bytes_stream()`, parses
  `\n`-delimited NDJSON, and re-emits each line as an HLAP
  envelope on the verb's stream. Single combined-OK form gave
  way to the spec's full streaming shape: initial OK with
  `streaming:true` and `build_id`, then log envelopes, then
  terminal `ok:true,done:true` envelope with final status.

- **Step 2c — host-side HLAP roundtrip in suite 12b.** Added two
  CLI flags to hula-agent — `--resolve HOST=IP:PORT` (mirrors
  curl's --resolve; overrides DNS at the socket layer without
  changing SNI) and `--extra-ca PATH` (extra trust roots on top
  of system + yaml CA). Lets suite 12b run hula-agent on the
  host against the e2e fixture's hula (private CA, non-default
  port) without a musl-target build. Cases 3 and 4 of suite 12b
  exercise the full BUILD HLAP roundtrip — banner + initial OK
  + ≥1 log envelope + terminal OK with status — and the
  HLAP-side permission denial path returning `err:"forbidden"`.

What's deliberately NOT in Phase 4:

- Multiplexed in-flight verbs per connection. Today verbs serialise
  within a connection; multiplex bookkeeping (in-flight map keyed
  by stream id, max_inflight enforcement) lands in Phase 5.
- Cancel verb on a separate control connection. Phase 5.
- Verbs beyond BUILD (staging-build, pull, push, sync, commit,
  stage, push-file, get-file). Phase 5.
- Cert expiry mid-session graceful drain. The behaviour rule is
  documented in §Cert expiry mid-session; implementation is
  Phase 6 (it tangles with the registry's IsActive checks that
  the revoke flow needs anyway).
- list-agents / revoke-agent CLI surfaces. Phase 6.

## Phase 2 — what landed

Concrete shape of the Phase-2 work that's already in main:

- **`pkg/agent/pki/persist.go`**: `LoadOrCreateCA(dataDir)` reads
  `<dataDir>/agent-ca.{pem,key}` if present, else generates a fresh
  10-year Agent CA and writes both files (cert 0644, key 0600).
- **`pkg/agent/registry/`**: bbolt-backed registry under
  `_agents/by-id/<id>` (canonical) + `_agents/by-fingerprint/<sha256>`
  (lookup index). `Put` writes both atomically via `storage.Batch`.
  `Record.IsActive` and `Record.IsAllowed` are the consult-at-RPC-time
  primitives Phase 3 / 5 will use.
- **`POST /api/agent/create`** in `handler/agent.go`: admin-auth'd.
  Mints an ID, signs a leaf under the persistent CA, writes the
  registry record (canonical + fingerprint), renders the agent yaml
  in stable-key order, returns it as a JSON-encapsulated string.
- **`server/agent_boot.go::BootAgentCA`** runs in `run_unified.go`
  right after `storage.SetGlobal` so the handler is usable as soon
  as the HTTP listener accepts traffic.
- **`hulactl create-agent`** now defaults to server mode (the new
  `--offline` flag drops back to Phase-1 behaviour for dev loops
  without a server). Output yaml is identical between modes.
- **e2e suite 12a**: server-mode round-trip — yaml shape, PEM
  presence, sites reflect `--allow-*`, direct curl 200, optional
  `hula-agent --dump` of the produced yaml when the rust binary is
  buildable on the host.

What's deliberately NOT in Phase 2:

- mTLS handshake-time validation against the registry. That's
  Phase 3.
- Hula serving the cert it's expected to authenticate against on
  agent connections — Phase 3 wires the Agent CA into the unified
  listener's ClientCAs alongside the existing trust roots.
- `revoke-agent` / `list-agents` CLI surfaces. Phase 6.

## Phase 3 — what landed

- **`pkg/agent/mtls/middleware.go`**: HTTP middleware that, after
  the TLS handshake, examines `r.TLS.PeerCertificates`. When the
  leaf chains to the Agent CA (via `cert.CheckSignatureFrom(ca)` —
  not a DN-string compare; two CAs can share a DN) it fingerprints
  the leaf, calls `registry.GetByFingerprint`, and either:
  - 401 "agent not registered" if the fingerprint isn't in the
    registry,
  - 401 "agent revoked" / "agent expired" when `IsActive(now)` is
    false,
  - or attaches the live `*registry.Record` to the request context
    via `RecordFromContext` for Phase-5 route handlers.
  Non-agent traffic flows through untouched.
- **`server/agent_boot.go::agentClientCAPool`**: assembles a
  `*x509.CertPool` from the loaded Agent CA, passed through to
  `unified.Config.ClientCAs` so Agent-CA-signed leaves complete
  the TLS handshake on the public listener.
- **`server/agent_boot.go::attachAgentMTLSMiddleware`**: wires the
  middleware into `srv.AttachHTTPMiddleware` after `unified.NewServer`
  returns. Lookup is bound to `storage.Global()`; degraded boot
  (storage unavailable) returns 503 to agent traffic — refusing
  agent calls during a degraded boot is the safe default.
- **`pkg/agent/mtls/middleware_test.go`**: 7 cases covering the
  five branches (no CA, no client cert, non-agent cert, missing
  registry record, revoked, expired, active) plus a registry-
  error path that asserts 503.

What's deliberately NOT in Phase 3:

- Per-route enforcement of `record.IsAllowed(site, verb)`. The
  middleware attaches the record but doesn't gate any route — that
  lands in Phase 5 alongside the HLAP-verb endpoints.
- Agent-CA bundle reload. Phase 3 reads the CA once at boot; cert
  rotation requires a hula restart. Hot-reload can land alongside
  Phase 6's revoke/list flows when there's a need.

Each phase lands as its own PR.
