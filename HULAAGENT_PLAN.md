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
+----------------+     unix-socket (HLAP)        +----------+   mTLS    +-------+
|  local app(s)  | ---------------------------> | hulaagent | -------> | hula  |
+----------------+    (text protocol)            +----------+           +-------+
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

Plain text, line-oriented, on a unix-socket. The local app and the
agent send single-line commands and receive one or more response
lines terminated by a blank line.

```
> BUILD gravhl
< OK build_id=abc123
< STATUS=complete
< (blank line)
```

Verbs in v1:

| HLAP verb       | Maps to hula API                                    |
| --------------- | --------------------------------------------------- |
| `BUILD`         | `POST /api/site/trigger-build`                      |
| `STAGING-BUILD` | `POST /api/staging/build`                           |
| `PULL`          | `POST /api/staging/{id}/git/pull`                   |
| `PUSH`          | `POST /api/staging/{id}/git/push`                   |
| `SYNC`          | `POST /api/staging/{id}/git/sync`                   |
| `COMMIT`        | `POST /api/staging/{id}/git/commit` (msg via body)  |
| `STAGE`         | `POST /api/staging/{id}/git/stage` (paths via body) |
| `PUSH-FILE`     | `PUT /api/staging/{id}/dav/<remote-path>` (stdin)   |
| `GET-FILE`      | `GET /api/staging/{id}/dav/<remote-path>` (stdout)  |

Response shape:

```
< OK key=value [key=value ...]
```

or:

```
< ERR <reason>
```

For verbs that return file content (GET-FILE) or large logs (BUILD),
the agent streams the body verbatim after the OK line, terminated by
a sentinel:

```
< OK content_length=12345
< <... 12345 bytes ...>
< (blank line)
```

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

| Phase | Scope                                                                      | Status |
| ----- | -------------------------------------------------------------------------- | ------ |
| 1     | This design doc + skeleton dirs + `hulactl create-agent` (offline mode)    | DONE   |
| 2     | Server-side `/api/agent/create`, agent CA bootstrap, registry storage      | DONE   |
| 3     | mTLS verification middleware + `agent_perms` request context               |        |
| 4     | Rust agent: config load, unix-socket server, HLAP parser, BUILD verb       |        |
| 5     | Remaining HLAP verbs                                                       |        |
| 6     | `hulactl list-agents` / `revoke-agent` + e2e suite                         |        |

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

Each phase lands as its own PR.
