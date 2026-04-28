# Hulation HA — Outline (the "Team" architecture)

A multi-region high-availability fabric for hula. Modeled on the
`../izcr` Raft+Bolt pattern. Lands before Phase 5b (mobile). Goal:
let one Hula deployment span multiple servers across multiple cloud
providers / regions / countries with eventual consistency on
identity + tenancy data, no-cost single-node mode for existing
deployments, and a fast path that never blocks visitor requests on
WAN consensus.

Related docs: `PLAN_OUTLINE.md` (overall hula roadmap),
`../izcr/AGENTS.md` + `../izcr/pkg/store/raft/` (the prior art this
follows), `BACKLOG.md` (e2e bootstrap fix is a precondition for
some HA suites to run cleanly).

---

## 1. The "Team" concept

A **Team** is a set of Hula servers ("nodes") that share consensus
state via Raft. A Team can run anywhere — a single 3-node Kubernetes
ReplicaSet, two nodes in two different cloud providers' regions, or
a hybrid of both. Membership is intentional and operator-managed:
nodes don't auto-discover each other.

### 1.1 Reference configurations

| Shape | Nodes | Where | Why |
|---|---|---|---|
| Solo | 1 | anywhere | existing deployments — zero-config upgrade |
| Two-cloud HA | 2 | two cloud providers, two regions | survive a hyperscaler outage |
| K8s replica set | 3 | one cluster | rolling deploys + node-fail tolerance |
| Hybrid | 3 + 2 | K8s ReplicaSet + 2 external nodes | regulatory locality + DR |

### 1.2 What a Team has in common

- A **Team ID** (UUID) — generated at team-init, never changes.
- A **bootstrap token** — pre-shared secret used to authenticate new
  joins. Operator distributes via secrets manager / env var.
- A **shared per-Team state** under Raft: users, ACL grants, OPAQUE
  records, server (= virtual host) configs, goal definitions,
  scheduled reports, alerts, mobile-device registrations,
  notification prefs, chat ACL, cookieless salts, site-build
  version metadata.
- An **internal mTLS mesh** — every node has a cert issued by the
  Team's internal CA; nodes talk to each other via gRPC on a
  dedicated internal port (NOT the public 443).
- A **per-node hostname** — used to pin chat sessions (and any
  other long-lived per-node connection) directly to the node that
  owns the connection state, bypassing the LB. Required even in
  solo deployments (operator just sets it equal to the public host).

---

## 2. Storage architecture

The single biggest mechanical change. Today every Bolt-backed
subsystem (`pkg/store/bolt/{bolt,opaque,chat,consent,cookieless,...}.go`)
calls `bolt.Get()` directly. After this work all persistent state
flows through a `Storage` interface modeled on
`../izcr/pkg/store/common/types.go::Storage`.

```
+---------------------------------------------------------+
|  pkg/api/v1/* (admin RPCs, ACL, goals, reports, alerts) |
|  pkg/chat/*, pkg/auth/opaque/*, handler/visitor.go      |
+----------------------+----------------------------------+
                       |
                  Storage interface
                       |
        +--------------+----------------+
        |                               |
   LocalStorage (bbolt only)      RaftStorage (raft + bbolt)
   used by tests + simple CLI     default in production
   tools (hulactl --bolt path)    (single-node Raft = solo)
```

Two backends, one interface. Production always uses `RaftStorage`,
even in solo deployments — the single-node Raft cluster has
near-zero overhead (one disk write per operation, same as today).
Tests + offline CLI tools (`hulactl forget-opaque-record` etc.)
keep using `LocalStorage` for fast direct-bolt-edit semantics.

### 2.1 Read / write semantics

- **Reads** are always local. Every node serves reads from its
  bbolt copy of the FSM state. No round-trip, no leader election.
- **Writes** go through Raft Apply → leader → commit → all nodes'
  FSM apply. Default `BarrierOnApply: true` (read-after-write
  consistency on the writing node). Cross-region write latency is
  bounded by the slowest follower's ack — typically 100–200ms.
- **Visitor hot-path writes** (cookies, daily-counter rolls) bypass
  Raft and write to a separate **node-local** bucket in the same
  bbolt file. Replication is best-effort (Stage 6, CRDT).
- **CompareAndSwap** for distributed locks (e.g., scheduled-report
  dispatcher leader-of-the-hour, JWT key rotation lease).

### 2.2 Bucket layout decision matrix

| Bucket | Replicate via Raft? | Hot-path? | Notes |
|---|---|---|---|
| `server_access` (ACL) | yes | no | tenancy — must converge |
| `goals`, `reports`, `alerts`, `report_runs` | yes | no | admin actions |
| `mobile_devices`, `notification_prefs` | yes | no | identity |
| `opaque_records` | yes | no | auth — must converge |
| `chat_acl` | yes | no | per-server agent rosters |
| `consent_log` | yes | no | append-only audit |
| `cookieless_salts` | yes | no | per-Team value, set once |
| **`hello` cookies** (visitor identity) | **CRDT lazy** (Stage 6) | yes | written on every fresh visit |
| **`hello_ss` cookies** | **CRDT lazy** | yes | server-side cookie variant |
| `audit_forget` | yes | no | append-only audit |

---

## 3. The chat-pinning trick

Per the user's directive, chat WS sessions must always land on the
same node for the lifetime of the session. We do this **at the URL
layer**, not at the LB layer.

1. Visitor hits `https://www.example.com/` (LB / round-robin DNS) →
   resolves to whichever node.
2. Page loads `/scripts/hello.js` and `/v/hula_hello.html` from
   that node (still LB-routed).
3. Visitor opens chat → JS POSTs `/api/v1/chat/start` (LB-routed).
4. **The node serving `/chat/start` returns its own per-node
   hostname** in the response: `{"chat_url":
   "wss://node-east.www.example.com/api/v1/chat/ws", ...}`.
5. JS opens the WS to that direct hostname. Subsequent
   `/api/v1/chat/admin/agent-ws` connections from the admin SPA do
   the same — the SPA pins to whichever node it logged into.
6. Result: the WS lifetime is bound to the node, no cross-node WS
   handoff is ever needed.

Per-node hostnames are operator-provisioned (TLS cert valid for
both `www.example.com` and `node-east.www.example.com`). Solo
deployments set both equal so behaviour is identical to today.

---

## 4. The analytics-relay (no-CH nodes)

Visitor tracking endpoints (`/v/hello`, `/v/<iframe>`, `/scripts/*`)
live on **every** node. ClickHouse may live on **only some** nodes
(typically the tier-1 nodes; edge nodes in countries / providers
without CH access cannot reach it).

Approach:

1. Every node runs a small internal gRPC service —
   `pkg/internal/relay` — listening on a separate port (default
   `:9443`), mTLS only, identity = Team-CA-issued cert.
2. State `node.ch_connected: bool` is part of the Raft FSM (flipped
   by each node based on its own startup probe of CH).
3. On a no-CH node, `/v/hello` writes the event into a local Bolt
   buffer bucket `event_outbox`. A background drainer picks events
   off the outbox and ships them to a CH-connected peer via
   `relay.RecordEventBatch(...)`.
4. The receiving peer writes events into ClickHouse using its
   existing `model.Event.CommitTo`. Events flow back through normal
   ingest enrichment (UA parsing, channel classification, geo).
5. Outbox has a TTL (default 24h). Events older than the TTL are
   dropped with a metric tick. Operators alerting on the
   "outbox_drops" metric know they have a relay availability
   problem.
6. Admin RPCs that need CH (`/api/v1/analytics/*`) advertise
   themselves only on CH-connected nodes. Non-CH nodes return 503
   with `Hula-Has-Clickhouse: 0` so the admin SPA knows to retry
   against another node (LB will round-robin to a different one).

### 4.1 Why not Raft for the relay?

User asked. The reason is volume: page-views land at 100s/s on busy
sites. Raft's commit path is fine for tens-of-writes/s (admin RPCs)
but choking it with visitor events would drown the WAN gap. mTLS
gRPC point-to-point is the right tool — it's just a routed call
with the failure-handling we already do for Meta CAPI / GA4 MP
forwarders.

---

## 5. Leader priority for CH-connected nodes

User said: nodes with CH should preferentially become leader (they're
typically tier-1 / better-connected). Implementation:

- During raft `BootstrapCluster` and subsequent `AddVoter` calls,
  CH-connected nodes are added with `Suffrage: Voter`. Non-CH nodes
  start as `Voter` too (we still want their write-ack for quorum)
  but get a higher
  `LeadershipTransferPenalty`-style hint stored in the FSM.
- A small goroutine on each node checks every 60s: if leader is
  non-CH AND a CH-connected voter exists AND that voter is healthy,
  call `LeadershipTransfer(<ch_node_id>)`.
- This is best-effort. A non-CH leader does no harm; the gRPC
  relay still routes events. The hint is purely a "nudge to
  preferred topology" — never a hard constraint.

---

## 6. Site-build versioning + propagation

Per the user's directive: when a build succeeds on one node, the
others get the same update. Per the answer to interview Q2: replicate
metadata only — each node rebuilds locally from the same source.

- A successful local build commits a row into Raft:
  ```
  site_versions/<server_id>/<version_id> → {
    server_id, version_id, source_commit, hula_build,
    completed_at, completed_by_node, environment_hash
  }
  ```
  `version_id` is a monotonic per-server counter. `environment_hash`
  is the SHA256 of the build env (Go version, builder image
  digest, etc.) so we can detect "node B has different toolchain".
- Every node watches `site_versions/<server_id>/` via the Storage
  informer. On a new entry from a peer:
  1. Compare `version_id` to local `last_built_for_<server_id>`.
  2. If older locally → fetch the commit (`git fetch origin
     <source_commit>`), run the same `hula_build` pipeline, write
     a sibling Raft entry `site_versions/<server_id>/<version_id>/
     applied_by/<node_id>` recording the local apply.
- Admin SPA shows a Team-wide build status table: per server, which
  nodes are at which version + environment hash. Operators can see
  drift instantly.
- `hulactl build <server_id>` continues to work — it triggers on
  the **leader**; the leader runs the build locally + writes the
  Raft row + every other node sees the row and propagates.

---

## 7. Visitor identity (Stage 6 — the hard one)

User accepted Path (i) on cookie miss: a visitor who hits node B
without B knowing their cookie yet is treated as a new visitor on
B; merging happens in ClickHouse via a periodic dedup query that
unions visitor_id timelines.

**Cookieless salts** are different — they're a per-Team value
committed once at team-init (or via `hulactl rotate-cookieless-salt`
which goes through Raft like any other admin write). All nodes
have the same salt → derive the same id from the same (IP, UA)
within the same UTC day, no merging needed.

**Cookie visitors**: Node A sees a fresh visitor → mints
`<prefix>_hello` + `<prefix>_helloss` cookies → writes to a
node-local bucket → enqueues an async Raft replication task. The
replication propagates the (cookie value → visitor_id) tuple to
peers' node-local copies. A miss on node B before replication
catches up creates a fresh visitor row; both visitor_ids will have
events tagged in CH; a daily merge job in CH (or a manual merge
RPC) UNION ALLs by-cookie and rewrites the older visitor_id to the
newer one.

This is the CRDT-style design. Conflict resolution is "last-writer-
wins on the cookie value, additive on the visitor's event history".
Because the cookie value is a v4 UUID, collisions are vanishingly
rare; the merge work is bounded.

---

## 8. Fast-path guarantees

**No visitor request must block on Raft consensus.**

- `/v/hello`, `/v/<iframe>`, `/scripts/<hello>.js`, `/sub/*` all
  read from local bbolt only.
- Cookie writes bypass Raft (Stage 6).
- Event writes are buffered locally before any cross-node hop.
- The only cross-node sync on the visitor path is the analytics
  relay — and that's async (out-of-band drain).

**Admin requests can block on Raft.**

- `/api/v1/auth/login` writes a JWT issuance record → Raft.
- Goal/report/alert CRUD → Raft.
- Site build trigger → Raft (and follower nodes pull commit + build
  locally).

Latency budget: admin RPCs can take 100–500ms cross-region; this
is acceptable for operator workflows. Visitor RPCs stay sub-10ms.

---

## 9. Membership UX

Per interview Q6 — both YAML-driven and CLI-dynamic.

### 9.1 YAML (initial team setup)

```yaml
team:
  team_id: 4f1a3c2d-...
  node_id: node-east
  node_hostname: node-east.www.example.com   # for chat pinning
  bootstrap_token: "{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}"
  raft:
    bind_addr: 0.0.0.0:8300
    data_dir: /var/hula/raft
  internal_mtls:
    bind_addr: 0.0.0.0:9443
    ca_cert: /var/hula/team-ca/ca.pem
    node_cert: /var/hula/team-ca/node.pem
    node_key: /var/hula/team-ca/node.key
  peers:
    # First-run only; the running cluster updates itself.
    - { id: node-east,  raft_addr: east.internal:8300,  internal_addr: east.internal:9443 }
    - { id: node-west,  raft_addr: west.internal:8300,  internal_addr: west.internal:9443 }
```

If `team:` is missing entirely, hula runs in **single-node Raft
mode** — auto-bootstrap with a generated team_id (persisted on
first boot), node_id derived from hostname. Existing deployments
upgrade with no YAML change.

### 9.2 CLI (dynamic membership)

- `hulactl team-init` — first node, generates team_id +
  bootstrap_token, prints them.
- `hulactl team-join <leader-addr> --token <bootstrap-token>` —
  join an existing team. Run on the new node.
- `hulactl team-status` — show all members, leader, last-applied
  index, CH-connected flag.
- `hulactl team-leave <node-id>` — graceful removal (only the
  leader accepts; transfers leadership first if leaving leader).
- `hulactl team-rotate-bootstrap-token` — rotate the join secret.

---

## 10. Stage breakdown

Each stage lands on its own branch off main, gets a `HA_PLAN<N>.md`
detailed plan, and ends with at least one new e2e suite. Stages 1
and 2 are the foundation — everything else builds on the Storage
interface they put in place.

| Stage | What | New e2e suites | Estimated size |
|---|---|---|---|
| **1** | **Storage interface seam** — define a generic `Storage` interface; refactor every existing bbolt callsite onto it. Ship a tiny `LocalStorage` impl for tests / offline CLI tools. | suite 38 — Storage round-trip | 0.75 wk |
| **2** | **Single-node Raft default** — `RaftStorage` backend wrapping hashicorp/raft + raft-boltdb. Auto-bootstrap solo cluster on first run. Production default. | suite 39 — solo-Raft round-trip | 1 wk |
| **3** | **Multi-node Team membership** — YAML peers + CLI commands (`team-init/join/leave/status`). Bootstrap-token auth. Raft network transport over the internal-mTLS port. Leader priority for CH-connected nodes. | suite 41 — 3-node team formation; suite 42 — leader fail-over | 2 wk |
| **4** | **Internal gRPC + analytics relay** — internal-only mTLS gRPC service. `RelayService.RecordEventBatch` from no-CH nodes to CH-connected peers. `event_outbox` Bolt bucket + drainer. CH-connected flag in Raft. Admin RPCs gate on CH availability. | suite 43 — analytics relay round-trip; suite 44 — outbox drain after CH outage | 1.5 wk |
| **5** | **Site build versioning + propagation** — `site_versions/` keys in Raft. Informer-driven follower pulls + local rebuild. Admin SPA Team-wide build dashboard. Per-node hostname config + chat URL pinning ride along (small; same surface). | suite 45 — build propagates to peers; suite 46 — chat session pins to node | 2 wk |
| **6** | **CRDT visitor identity + WAN dedup** — node-local visitor cookies, async replication, daily ClickHouse dedup query. Cookieless salt as a single Raft value committed at team-init. End-to-end test of visitor moving between nodes. | suite 47 — same-cookie cross-node visit; suite 48 — daily dedup correctness | 1.5 wk |

**Total**: ~9.5 wk serial. Stages 4 + 5 + 6 can run in parallel
once Stage 3 lands; stage 1 + 2 are strictly serial.

---

## 11. Out of scope (worth flagging)

- **Load balancer / DNS configuration**: operator concern. We'll
  ship docs in a later phase showing Cloudflare Load Balancing,
  Route53 weighted records, and HAProxy Dataplane API examples.
  Not implemented inside hula.
- **Cross-region ClickHouse replication**: each Team that has CH
  has one (or more) CH instances; how those CH instances are
  replicated is the operator's call (CH cluster, ReplicatedMergeTree,
  Altinity, etc.). Hula treats CH as opaque.
- **Automatic certificate rotation for the internal CA**: we ship
  a 1-year cert at team-init and document the rotate path. A
  managed-rotation feature is a future phase.
- **Geographic-aware client routing**: operators do this at the LB
  level (Anycast, GeoDNS, Cloudflare Argo). Hula doesn't try to
  influence which node a visitor lands on.
- **Multi-Team federation**: two Teams sharing some state (e.g.,
  global SSO). Out of scope; would be a separate phase entirely.

---

## 12. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Raft minority partition causes admin-RPC stalls | Document: ops requires majority quorum. Reads continue everywhere. Health endpoint surfaces "raft.has_quorum" so LB can drain a partitioned node. |
| WAN partition between East ↔ West (true split-brain) | Raft prevents split-brain by quorum requirement. Minority side can't accept admin writes; visitor traffic continues to function locally; analytics relay buffers in `event_outbox` and drains when partition heals. |
| CH-connected node becomes leader, then loses CH | Leader priority is a *nudge*, not a constraint. Leader keeps role; events relay TO it from other CH-connected peers if needed. |
| Cookie replication lag double-counts visitors | Daily dedup job in CH; documented as expected behaviour for first ~5 minutes after a fresh visit. CRDT merge converges; counts are eventually accurate. |
| Bootstrap token leak | Token rotation CLI; tokens are short-lived (1h default) at the join handshake (long-lived for the FIRST join; re-rotated per team-rotate-bootstrap-token). |

---

## 13. Per-stage plan files

The full design + implementation detail for each stage lives in a
sibling file:

- `HA_PLAN1.md` — Storage interface seam
- `HA_PLAN2.md` — Single-node Raft + migration
- `HA_PLAN3.md` — Multi-node Team membership
- `HA_PLAN4.md` — Internal gRPC analytics relay
- `HA_PLAN5.md` — Site build propagation + chat URL pinning
- `HA_PLAN6.md` — CRDT visitor identity + WAN dedup

Stage 1 plan is written. Subsequent plans get filled in as each
stage starts (so we can incorporate learnings from the prior
stage without paper-baking the whole thing up front).
