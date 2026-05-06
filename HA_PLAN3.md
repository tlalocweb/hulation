# HA Plan 3 — Multi-node Team Membership

> **Amended 2026-05-05** after Round-5 design review. The original
> sketch was written before Stage 2 shipped and assumed a benign
> private-network deployment shape. The amended scope reflects three
> facts learned since:
> (a) real Teams run on **disparate VPS / cloud providers across
>     the public internet** with no VPN — this forces mTLS on every
>     hop from day one,
> (b) only **one node per Team has ClickHouse access** for the
>     foreseeable future — non-CH nodes must relay analytics events
>     over an internal mTLS gRPC channel, so the channel that was
>     planned for Stage 4 is now a Stage-3 precondition,
> (c) Raft commit latency over WAN (100–300ms p99) is **too slow
>     for the visitor cookie hot path** — visitor identity moves to
>     a CRDT layer gossiped over the same internal mTLS channel
>     (was originally Stage 6).
>
> Net effect: Stage 3 collapses what `HA_OUTLINE.md` called Stages
> 3 + 4 + (most of) 6 into one phase. Sizing moves from ~2 weeks
> to **~4–6 calendar weeks**. Stages 5 (site-build propagation) and
> the residual of 6 (CH-side dedup query) remain as follow-ups.
>
> Closely modelled on `../izcr/pkg/store/raft/` for the Raft path
> and on standard gossip/CRDT primitives (HLC, LWW-Register,
> push-pull anti-entropy) for the visitor layer.

Related docs: `HA_OUTLINE.md`, `HA_PLAN1.md`, `HA_PLAN2.md`,
`PHASE_*_STATUS.md`. Prior art: `../izcr/pkg/store/raft/raftnode.go`,
`../izcr/cmd/izcrd/commands/run.go`.

---

## 1. Context and scope

### 1.1 What Stages 1 + 2 delivered that we reuse

- `pkg/store/storage/Storage` interface — unchanged.
- `pkg/store/storage/raft/RaftStorage` — already supports
  arbitrary `BindAddr`, defaulted to loopback in solo. We flip it
  AND wrap it onto the unified HTTPS listener (see §6).
- `RaftConfig.Peers` — already parsed and stashed; we wire it to
  the Raft cluster configuration.
- `RaftStorage.forwardToLeader(...)` — already exists as a stub.
  Stage 3 implements it over the internal gRPC channel.
- The Informer + Watch — already in place. Used unchanged by
  Raft-replicated data; gossip-CRDT data uses its own informers.
- The Stage-2 `team:` config block — extended with PKI + peer
  fields below. The existing `data_dir: /var/hula/data/raft`
  becomes the parent for both Raft state and CRDT state.

### 1.2 What Stage 3 ships

1. **Multi-node Raft cluster formation** — operators bring up
   N hula nodes (typically 3 or 5), one bootstrapped as the
   initial leader; the rest join via a shared bootstrap token
   PLUS an mTLS handshake against operator-distributed certs.
2. **Two membership UX paths** (per outline §9):
   - **YAML-driven peers** for initial team setup. The first
     node bootstraps; subsequent nodes have peer addresses in
     YAML and discover the leader on first boot.
   - **CLI-dynamic** for runtime membership: `hulactl
     team-init`, `team-join`, `team-leave`, `team-status`,
     `team-rotate-bootstrap-token`.
3. **Bootstrap-token authentication** for joins, layered on top
   of mTLS. Constant-time compare; rotatable.
4. **`hulactl genteamcerts`** — generates a Team CA + per-node
   certs offline; operator distributes the bundles out-of-band.
5. **Internal mTLS gRPC channel** (was Stage 4) — riding on the
   unified HTTPS listener (no separate port). Carries:
   - Raft transport (gRPC streaming wrapper around hashicorp/raft).
   - `forwardToLeader` for `RaftStorage.Mutate` + admin RPCs.
   - `RelayService.RecordEventBatch` for analytics ingest from
     non-CH nodes to the CH-connected peer.
   - `GossipService.Push` + `GossipService.SyncDigest` for
     visitor / session / bad-actor CRDT state.
   - `ChatLookupService` for the CH-side chat lookaside cache.
6. **Analytics relay** (was Stage 4) — non-CH nodes accept all
   visitor traffic at the public edge, never block on CH; enqueue
   pre-enriched `model.Event` rows in a per-node FIFO with
   in-memory hot ring + disk-backed overflow; drain to the
   CH-connected peer over `RelayService.RecordEventBatch`.
7. **Gossip + CRDT layer** (was Stage 6) — visitor cookie state,
   session-continuity timestamps, and bad-actor flags replicated
   via push-on-write + periodic anti-entropy gossip. Backed by a
   second bbolt file (`crdt.db`) separate from the Raft FSM.
   HLC-based LWW-Register for visitor data; monotone "bad wins"
   merge for bad-actor flags; Raft for bad-actor *clearing*.
8. **Leader pinning to CH-connected** — design generalised so a
   multi-CH future works without changes to the predicate.
9. **`/readyz` health endpoint** on the unified listener —
   reports Raft state regardless of CH reachability (CH is a
   hint, not a gate; the analytics queue absorbs CH outages).
10. **Chat WS pinning** via per-node hostnames in chat-start
    response payload (HA_OUTLINE §3 — no architectural change,
    just operator config + a one-line response field).
11. **Two e2e suites**: `41-team-formation.sh` (3-node bring-up,
    admin write propagation, analytics relay end-to-end),
    `42-leader-failover.sh` (kill leader, verify failover under
    `tc netem`-injected 5% loss + 150ms latency).

### 1.3 What Stage 3 does NOT ship

- **Site-build propagation across the team** — Stage 5.
- **CH-side daily dedup query** for cross-node visitor merging —
  the residual of HA_OUTLINE Stage 6, lives in CH not hula.
- **Per-Team federation** (multiple Teams sharing some state) —
  out of scope per HA_OUTLINE §11.
- **Geographic-aware client routing** — operator concern at the
  LB layer (Anycast, GeoDNS, etc.).

---

## 2. Boot flow — the three modes

A hula node boots into exactly one of three modes from config:

### 2.1 Solo (Stage 2 default — unchanged)

`team:` block missing or `bootstrap: solo` → single-node Raft
cluster, loopback transport, no peers. mTLS material is generated
on first boot if absent (self-signed CA + node cert) so the same
code path serves solo and team. Existing solo deployments upgrade
with no YAML change.

### 2.2 First-of-team (the seed node)

`bootstrap: first-of-team` → bootstrap a multi-node-capable Raft
cluster with this single node as the only initial voter; listen
on the unified HTTPS port for inter-node traffic; expect peers to
join via the CLI flow OR via a `peers:` list the operator
pre-populated. After first run the operator changes
`bootstrap: first-of-team` → `bootstrap: false` so a future
restart doesn't re-bootstrap. The seed node is the source of the
Team CA; its cert bundle was produced by `hulactl genteamcerts`.

### 2.3 Join (every other node)

`bootstrap: false` (default in YAML when peers are present) →
contact each peer in `peers:` (or the address provided to
`hulactl team-join`). Open an mTLS connection using the
operator-distributed Team CA + this node's cert. Present the
bootstrap token in the `Join` RPC. The leader verifies the token,
calls `r.AddVoter(...)`; the joining node's local Raft picks up
the cluster configuration and starts following. Concurrently, the
node joins the gossip mesh — peer set is the same as Raft
membership.

```yaml
# Seed node:
team:
  team_id: 4f1a3c2d-...
  node_id: node-east
  node_hostname: node-east.www.example.com   # for chat WS pinning
  data_dir: /var/hula/data
  bootstrap: first-of-team
  bootstrap_token: "{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}"
  pki:
    ca_cert:   /var/hula/team-ca/ca.pem
    node_cert: /var/hula/team-ca/node-east.pem
    node_key:  /var/hula/team-ca/node-east.key
  peers:
    - { id: node-west, addr: west.example.com:443 }
    - { id: node-emea, addr: emea.example.com:443 }

# Joining node:
team:
  team_id: 4f1a3c2d-...   # MUST match the seed
  node_id: node-west
  node_hostname: node-west.www.example.com
  data_dir: /var/hula/data
  bootstrap: false
  bootstrap_token: "{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}"
  pki:
    ca_cert:   /var/hula/team-ca/ca.pem
    node_cert: /var/hula/team-ca/node-west.pem
    node_key:  /var/hula/team-ca/node-west.key
  peers:
    - { id: node-east, addr: east.example.com:443 }
```

`team_id` mismatch between two nodes → join is refused with a
clear error. The CLI exits non-zero on mismatch (Q24).

`data_dir` layout:

```
/var/hula/data/
├── raft/
│   ├── data.db          # FSM (bbolt) — Raft-replicated state
│   ├── raft-log.db
│   ├── raft-stable.db
│   ├── snapshots/
│   ├── team-id
│   └── node-id
├── crdt.db              # gossip-CRDT state (visitors, sessions, bad-actor)
├── outbox/
│   └── events.queue     # disk-overflow ring for the analytics relay
└── tombstones/          # GC'd lazily on the 30-day retention boundary
```

---

## 3. PKI — `hulactl genteamcerts` and the join handshake

mTLS is required from day one. The operator generates the Team
CA and per-node certs offline using a new CLI command, then
distributes them out-of-band (secrets manager, ansible-vault,
1Password, etc.).

### 3.1 `hulactl genteamcerts`

```
hulactl genteamcerts \
  --team-id 4f1a3c2d-... \
  --nodes node-east,node-west,node-emea \
  --validity 365d \
  --out ./team-bundles/

# Produces:
#   ./team-bundles/ca.pem
#   ./team-bundles/ca.key                 (operator-secured)
#   ./team-bundles/node-east/{cert.pem,key.pem,ca.pem}
#   ./team-bundles/node-west/{cert.pem,key.pem,ca.pem}
#   ./team-bundles/node-emea/{cert.pem,key.pem,ca.pem}
#   ./team-bundles/bootstrap-token        (32-byte b64)
#   ./team-bundles/team-id                (echoed for convenience)
```

- CA: ECDSA P-256, self-signed, valid for the chosen period.
- Per-node certs: ECDSA P-256, signed by the CA, SAN includes
  `node_id` + `team_id` (so wrong-team or wrong-node certs fail
  early at the TLS layer — defence in depth).
- `bootstrap-token`: 32 random bytes, base64-encoded.

The CA private key is the most sensitive output — operator
**must not deploy it to any node**; it lives in their secrets
vault for future cert generation. Per-node bundles deploy to the
matching node. The `ca.pem` is identical in every bundle; the
`cert.pem` + `key.pem` are unique.

### 3.2 The Join RPC — over mTLS, plus bootstrap token

```proto
// pkg/apispec/v1/membership/membership.proto

service MembershipService {
  rpc Join(JoinRequest) returns (JoinResponse);
  rpc Leave(LeaveRequest) returns (LeaveResponse);
  rpc Status(StatusRequest) returns (StatusResponse);
}

message JoinRequest {
  string team_id          = 1;
  string node_id          = 2;
  string raft_addr        = 3;  // host:443 — same unified port
  string bootstrap_token  = 4;  // pre-shared, layered on top of mTLS
  bool   ch_connected     = 5;  // hint for leader-priority loop
  string node_hostname    = 6;  // for chat WS pinning
}

message JoinResponse {
  string leader_id        = 1;
  string leader_addr      = 2;
  uint64 last_index       = 3;
}
```

The receiving node MUST be reached over mTLS — the gRPC server
rejects clients that don't present a Team-CA-signed cert. Once
the TLS handshake passes, the leader additionally validates the
`bootstrap_token` via `subtle.ConstantTimeCompare`. Both must
pass; mTLS alone or token alone is not enough.

### 3.3 Bootstrap-token storage and rotation

Token lives in the Raft FSM under `_team/bootstrap_token` (Raft-
replicated, so every voter agrees on the current value).
`hulactl team-rotate-bootstrap-token` regenerates the secret, writes
to the FSM, prints the new token. Existing nodes are unaffected;
rotation only matters for FUTURE joins.

Token surfacing in YAML: env-template recommended
(`{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}`). Literal value in YAML is
accepted but a `WARN` is logged on every boot referencing the
secret-leakage risk (Q22 — either acceptable, this is the
operator-friendly midpoint).

---

## 4. The unified listener — one port for everything

**Q23 — all inter-node traffic on the unified HTTPS port (typically 443).**
No separate Raft port, no separate internal-mTLS port. This
constraint shapes several mechanical decisions:

### 4.1 Public vs internal mux on the same port

The unified listener already demuxes:
- HTTP/2 gRPC (admin RPCs)
- HTTP/1.1 + HTTP/2 REST (gateway, static, WebDAV)
- ALPN-selected by the Go HTTP server.

Stage 3 adds a new gRPC service registration on the same port:
the **internal services**. We distinguish public from internal
purely by **client cert chain**:

- Public requests: TLS terminated against the public (ACME /
  static / Cloudflare-Origin) certs configured on the unified
  server. Client certs are not required.
- Internal requests: client presents a Team-CA-signed cert. The
  server's `tls.Config.GetConfigForClient` callback inspects the
  ClientHello SNI; if SNI matches a reserved internal-name
  pattern (e.g., `*.team.internal` or any of the configured peer
  `node_hostname` values), the server requires + verifies a
  Team-CA-signed client cert. Otherwise no client cert is
  required.

Internal gRPC services (`MembershipService`, `RelayService`,
`GossipService`, `ChatLookupService`, `RaftTransportService`,
`StorageProxyService`) are registered on a separate gRPC handler
that's only mounted when the request arrived with a verified
client cert. Mismatched cert → standard TLS handshake failure
before any HTTP layer runs.

### 4.2 Raft transport over gRPC

`hashicorp/raft` ships with a TCP transport. We replace it with
a gRPC streaming wrapper (`pkg/store/storage/raft/transport_grpc.go`)
that implements `raft.Transport` by tunnelling each Raft RPC
(AppendEntries, RequestVote, InstallSnapshot, TimeoutNow) over a
bidirectional gRPC stream. Port: 443 (unified listener), service
path: `/internal.RaftTransportService/Stream`. This is the same
pattern izcr uses post their internal-mTLS migration.

This costs a small amount of latency vs. raw TCP (gRPC framing +
HPACK), well-amortised against the 50–300ms WAN RTT we're
already paying. Benefits: no second port to plumb, no second
TLS config to manage, no firewall complications for operators.

---

## 5. Internal gRPC channel — full surface

The internal channel hosts six services. All are registered only
on requests that present a valid Team-CA-signed client cert.

| Service | Purpose |
|---|---|
| `MembershipService` | Join / Leave / Status (§3.2). |
| `RaftTransportService` | Streaming wrapper for hashicorp/raft RPCs (§4.2). |
| `StorageProxyService` | `Apply(cmd)` for `forwardToLeader` writes (§9). |
| `RelayService` | `RecordEventBatch(events)` from non-CH to CH-connected peer (§6). |
| `GossipService` | `Push(deltas)` + `SyncDigest(digest)` for CRDT visitor / session / bad-actor state (§7). |
| `ChatLookupService` | Cache-miss lookups for the per-node chat lookaside (§8). |

The internal services share a single gRPC server registered on
the unified listener, behind the client-cert gate. They never
appear on the public REST gateway. `grpcurl` against the public
port without a client cert won't see them at all — TLS rejects
the handshake before any HTTP layer.

---

## 6. Analytics relay — non-CH nodes ship events to CH

### 6.1 Hot path on a non-CH node

```
visitor request → /v/hello
  → handler/visitor.go: enrich + build model.Event
  → relay.Enqueue(evt)            ← never blocks
  → reply 204 to visitor          ← total time: same as today
```

`relay.Enqueue` is a non-blocking call. Internally:
1. Try to push to the in-memory hot ring (capacity: configurable,
   default 4096 events ≈ ~4 MB at ~1 KB/event).
2. If the ring is full, push to the disk-backed overflow log (a
   bbolt-backed FIFO at `data_dir/outbox/events.queue`,
   configurable cap default 256 MB).
3. If both are full → FIFO eviction: drop the oldest event from
   the disk overflow, log a `WARN` with a 1-per-second rate-
   limited message, increment `relay_outbox_evictions` counter.
4. Visitor request never observes any of this.

### 6.2 Drainer goroutine

A background drainer pulls from the hot ring (and overflow log
when the ring is empty), batches up to N events (default 64),
and calls `RelayService.RecordEventBatch(batch)` against the
CH-connected peer. On success, ack and remove from the queue.
On failure (peer unreachable, error response), exponential
backoff (100ms / 500ms / 2s / 10s / 30s, cap 30s) and retry.

The drainer never gives up — events stay in the queue until
delivered or evicted by FIFO when capacity is exhausted. An
hour-long CH outage drains naturally when CH returns, up to the
overflow cap.

### 6.3 CH-connected node receives

```go
func (r *RelayService) RecordEventBatch(ctx, req) (*Empty, error) {
    if !r.weAreCHConnected {
        return nil, status.Error(codes.FailedPrecondition,
            "this node is not CH-connected — relay misrouted")
    }
    for _, evt := range req.Events {
        if err := evt.CommitTo(r.ch); err != nil {
            return nil, status.Error(codes.Internal, err.Error())
        }
    }
    return &Empty{}, nil
}
```

Events arrive **pre-enriched** as `model.Event` (proto-encoded).
The CH-connected node writes them straight into ClickHouse via
the existing `model.Event.CommitTo` path. No re-enrichment, no
schema fan-out needed (Q7).

### 6.4 Wire format

`pkg/apispec/v1/internal/relay.proto`:

```proto
service RelayService {
  rpc RecordEventBatch(RecordEventBatchRequest) returns (Empty);
}

message RecordEventBatchRequest {
  repeated bytes events = 1;  // proto-encoded model.Event
  string source_node_id = 2;
}
```

Each `bytes` is the proto-encoded form of `model.Event` from the
existing apispec — zero schema duplication. The proto already
carries every enrichment column (since Phase 0).

### 6.5 Disk-overflow queue

`pkg/relay/outbox.go` implements an append-only ring buffer in a
dedicated bbolt file (`outbox/events.queue`):

- Two buckets: `events` (key = monotonic sequence, value = proto bytes)
  and `meta` (key = `head` / `tail` cursors).
- `Push(evt)`: append at `tail`, increment `tail`. If
  `tail - head > cap_bytes / avg_event_size`, advance `head` by
  one (FIFO eviction).
- `Drain(n)`: read up to `n` events starting at `head`, return
  them. Caller re-calls `Ack(seq)` after successful delivery to
  advance `head`.

A `// TODO` notes that scaling beyond a single hula process per
node could replace this with a real on-disk queue (NATS JetStream,
boltdb-WAL, etc.) — for v1 the simple ring is sufficient.

---

## 7. Gossip + CRDT layer (replaces Stage-6 visitor identity)

### 7.1 What's replicated via gossip vs. Raft

**Gossip-CRDT** (high-frequency, write-anywhere, eventual
consistency, sub-second visibility on healthy network):

| Data | CRDT type | Merge predicate |
|---|---|---|
| `visitors` records (cookie ↔ visitor_id, first-seen, last-seen, fingerprint) | LWW-Register with HLC | `max(hlc)` wins; tombstone on GDPR forget |
| Session-continuity state (`(server_id, visitor_id) → (session_id, last_event_ts)`) | LWW-Register with HLC | `max(hlc)` wins |
| Bad-actor flag set | Custom: monotone "bad wins" | If any replica says BAD → result is BAD (regardless of HLC) |

**Raft** (admin-grade, strongly-consistent, audit trail required):

| Data | Why Raft |
|---|---|
| Users, ACL grants, OPAQUE records | identity must converge atomically |
| Goals, scheduled reports, alerts, report runs | admin actions, low-frequency |
| Mobile devices, notification prefs | identity-tier |
| Chat ACL, server configs | tenancy |
| Cookieless salts | per-Team values, written rarely (`hulactl rotate-cookieless-salt`) |
| Bootstrap token, team_id, peer membership | foundational |
| **Bad-actor *clearing*** (TTL expiry + admin unblock) | strong consistency required to override the gossip "bad wins" |
| `_team/ch_connected/<node_id>` flag | observed by leader-priority loop |
| Audit logs (`audit_forget`, `consent_log`) | append-only ledger |

### 7.2 Storage layout

Gossip-CRDT state lives in **a separate bbolt file**
(`data_dir/crdt.db`) — Q16 — to keep it out of the Raft FSM
snapshot/restore lifecycle. The bucket layout:

- `visitors/<server_id>/<visitor_id>` → `{hlc, payload, tombstone?, tombstone_hlc?}`
- `sessions/<server_id>/<visitor_id>` → `{hlc, session_id, last_event_ts}`
- `badactor_flags/<ip>` → `{flagged_at_hlc, by_node_id}`
- `gossip_meta/digest` → tracks per-bucket last-merged HLC for
  efficient anti-entropy.

### 7.3 Hybrid Logical Clock (HLC)

`pkg/gossip/hlc.go`:

```go
type HLC struct {
    wallMs   int64
    counter  uint16
    nodeID   string  // tiebreak on equal (wallMs, counter)
}

func (c *Clock) Now() HLC { ... }      // local-monotonic-or-counter advance
func (c *Clock) Update(remote HLC) { ... }  // pulled-forward by max(remote, local)+1
```

- Wall-clock millisecond resolution, advanced by Update on
  every received message.
- 16-bit counter handles same-ms write bursts.
- Node ID for deterministic tiebreak.
- Standard HLC, ~50 lines.

### 7.4 Gossip protocol

**Push-on-write** (immediate, optimistic):
- Every CRDT write fans out a `GossipService.Push(deltas)` call
  to all known peers. Best-effort — if a peer is unreachable,
  the anti-entropy ticker will heal the gap.
- Push is fire-and-forget from the writer's perspective;
  acceptors call back with their HLC for clock-synch.

**Anti-entropy ticker** (periodic, self-healing):
- Every 10 seconds (configurable), each node picks one random
  peer and exchanges per-bucket HLC digests via
  `GossipService.SyncDigest(digest)`.
- Digest format: `{bucket_name → max_hlc_seen}`. The peer
  responds with any keys it has whose HLC > the requester's
  last-seen. Requester applies the deltas locally.
- 10s ticker is the default; a partition heals within 1–2 ticks
  in normal operation.

Both paths converge on the same merge predicates — duplicate
deltas are idempotent (HLC comparison rejects them).

### 7.5 Bad-actor: flag-via-gossip / clear-via-Raft

The asymmetry exists because:
- Flagging is high-frequency (real-time scoring on visitor
  traffic) — must be cheap.
- Clearing is rare (admin unblock, TTL expiry) — must be
  authoritative.

```
flag flow:
  badactor scoring on node-east hits threshold for IP X
    → gossip Push(badactor_flags/X = {flagged_at_hlc, by:east})
    → all nodes block X within ~push-RTT (or anti-entropy tick)

clear flow (admin or TTL):
  hulactl unblock-actor X
    → admin RPC against leader → Raft Apply: badactor_clears/X = {cleared_at_hlc, ttl_until}
    → followers see the FSM update via Watch
    → on every IP check: if Raft cleared_at_hlc > gossip flagged_at_hlc → not blocked
```

The merge predicate at read time:

```go
func IsBlocked(ip string) bool {
    flag, _   := crdt.Get("badactor_flags/" + ip)        // gossip
    clear, _  := storage.Get("badactor_clears/" + ip)    // raft
    if clear != nil && clear.HLC.After(flag.HLC) {
        // Raft clear wins. Optionally enforce a "do not re-flag for cooldown_secs" window (Q19c).
        return false
    }
    return flag != nil
}
```

A new bad-actor signal arriving AFTER the Raft clear can
re-flag the IP — that's intentional. The clear records a
`ttl_until` for the optional cooldown (Q19c) — during the
cooldown window, scoring signals are recorded but don't trip
the block flag.

### 7.6 GDPR forget propagation

`ForgetVisitor` (Phase-4 RPC) extends to:
1. Write tombstone to gossip CRDT: `visitors/<server_id>/<id> = {tombstone, hlc}`.
2. Push tombstone to all peers immediately.
3. Tombstone retains for `tombstone_retention_days` (default 30,
   configurable per Q17), then GC'd by a background sweeper on
   each node.
4. The existing CH `ALTER DELETE` + Bolt `audit_forget` writes
   stay on their existing paths.

Tombstones are first-class CRDT values: a record with
`tombstone=true` always wins LWW against a non-tombstone with an
equal-or-lower HLC.

### 7.7 Membership = Raft membership (Q15)

The gossip mesh's peer set is read directly from
`r.GetConfiguration().Configuration().Servers`. `team-join`
produces a single membership change; the gossip layer subscribes
to Raft configuration-change events and updates its peer list.
No separate gossip-membership concept.

### 7.8 Visitor merge in practice

Browser sticky LB means most writes are single-writer (Q12 — the
user is right that merges are the exception, not the norm). The
HLC + LWW machinery is overkill *most* of the time but matters
in three real cases:

1. **LB failover** — visitor's previous node dies; LB routes the
   next request to a different node. Gossip catches that node up
   within ~push-RTT or one anti-entropy tick.
2. **DNS / Anycast change** — same as LB failover.
3. **Network partition heal** — both halves had concurrent
   writes; LWW resolves to whichever HLC is higher.

Cross-node visitor double-counting in CH (the original Stage-6
worry) is now addressable by a CH-side daily dedup query —
deferred to Stage 5 follow-up since it's a CH-side artifact, not
a hula-side correctness issue.

---

## 8. Chat — pinning + lookaside cache

### 8.1 Pinning (per HA_OUTLINE §3, no architectural change)

Today (Phase 4b) the chat hub is in-process — visitor and agent
WS must terminate on the same node. For multi-node Teams:

1. `/api/v1/chat/start` accepts the visitor request on whatever
   node the LB picked.
2. The serving node returns its **per-node hostname** in the
   start response: `{"chat_url": "wss://node-east.www.example.com/api/v1/chat/ws", ...}`.
3. The visitor JS opens the WS to that direct hostname; subsequent
   admin agent-WS connections hit the same node via the same
   per-node hostname.
4. Result: WS lifetime is bound to the node, no cross-node hub
   fan-out needed.

`team.node_hostname` config field carries this per-node value.
Solo deployments set it equal to the public hostname → behaviour
identical to today.

### 8.2 Lookaside chat cache (Q11.v)

Chat session metadata + recent messages live in ClickHouse
(Phase 4b). Reads from a non-CH node go through the relay
channel: `ChatLookupService.Get(session_id)` returns rows from
the CH-connected peer. To avoid round-tripping every read, each
non-CH node maintains a TTL cache (default 5 min) keyed by
`session_id` + query shape. Cache invalidation: the agent-WS
hub publishes invalidation events on session updates, and the
gossip channel piggybacks them.

This is a lookaside cache, not a CRDT — eventual consistency is
fine (chat history is append-only; stale cache returns
sub-second-old data, never wrong data).

---

## 9. `forwardToLeader` over the internal channel

Stage 2 left this as a stub. Stage 3 implements it via
`StorageProxyService.Apply(cmd)` on the internal gRPC channel.

```go
func (s *RaftStorage) forwardToLeader(ctx context.Context, cmd *Command) error {
    leaderID, leaderAddr := s.raft.LeaderWithID()
    if leaderAddr == "" {
        return ErrNoLeader
    }
    conn, err := s.dialPeer(string(leaderAddr))  // mTLS, internal channel
    if err != nil { return err }
    defer conn.Close()
    client := pb.NewStorageProxyClient(conn)
    _, err = client.Apply(ctx, &pb.ApplyRequest{Command: encodeCommand(cmd)})
    return err
}
```

### 9.1 Retry budget (Q20 — confirmed)

5 retries with backoff `100ms / 200ms / 500ms / 1000ms / 2000ms`
(total ~3.8s wallclock). On retry #2, force a leader re-discovery
(re-read `s.raft.LeaderWithID()`) in case the cached address
points at a stepped-down node. After all retries exhausted,
surface `codes.FailedPrecondition` with `NotLeader` detail to
the caller; the admin SPA / hulactl gRPC interceptor then
re-resolves the leader and retries one more time.

### 9.2 Admin-RPC interceptor

A gRPC client interceptor (`pkg/server/grpc/leader_retry.go`)
detects `NotLeader` status from any unary admin RPC call and
transparently retries against the discovered leader address.
Callers (admin SPA, hulactl) never have to think about which
node they hit. The same interceptor wraps the SPA's fetch helper
so server-side leader changes are invisible to the UI.

---

## 10. Leader pinning — primary by default, multi-CH-ready

Per Q6, the CH-connected node is effectively the primary today,
but the design generalises to a multi-CH future without code
changes.

### 10.1 Predicate

```go
// Eligibility: this node is preferred-leader-class iff it is CH-connected.
func (n *Node) IsPreferredLeader() bool {
    return n.chProbe.LastSucceeded(within: 60*time.Second)
}
```

Each node refreshes `_team/ch_connected/<node_id>` in the Raft
FSM every 60s (TTL 90s — a missed refresh marks the node as
non-CH within 1.5 cycles).

### 10.2 Priority loop

```go
func (n *Node) leadershipPriorityLoop(ctx context.Context) {
    t := time.NewTicker(60 * time.Second)
    for {
        select {
        case <-ctx.Done(): return
        case <-t.C:
            if !n.raft.IsLeader() { continue }
            if n.IsPreferredLeader() { continue }  // I am CH; stay leader.
            if peer := n.findPreferredVoter(); peer != "" {
                if err := n.raft.LeadershipTransfer(peer).Error(); err != nil {
                    log.Warnf("priority transfer failed: %v", err)
                }
            }
        }
    }
}
```

Today there's exactly one CH node, so the predicate is "is this
me". Tomorrow with multiple CH nodes, the loop just picks any
healthy CH-connected voter — no rewrite.

If the CH node fails entirely:
- A non-CH node takes over leadership (Raft elects normally).
- Admin writes continue to work.
- Analytics ingest pauses — queued events accumulate in non-CH
  outboxes. When the CH node recovers, queues drain.
- The priority loop transfers leadership back to the CH node
  within one tick of recovery.

`/readyz` (§11) returns 200 throughout — the cluster as a whole
keeps serving traffic.

---

## 11. `/readyz` contract (Q18 — confirmed (a))

`/readyz` is exposed on the unified listener (Q23 — same port,
distinct path). Returns 200 if and only if:

- Raft state ∈ {Leader, Follower, Candidate} (i.e., not Shutdown).
- Local last-applied index is within 100 of the last-known
  leader index (catching-up nodes return 503 until caught up).

CH reachability is **not** a check on `/readyz` — non-CH nodes
return 200 even when they can't reach the CH peer. The relay
queue absorbs CH outages; the visitor hot path is unaffected.
Operators who want CH-aware routing add a custom probe; the
default `/readyz` answers "is this node fit to serve traffic"
truthfully without entangling CH availability.

`/healthz` continues to mean liveness — always 200 unless
shutting down.

Response body on 503 lists which check failed:
```json
{"ok":false,"reason":"raft_lagging","detail":"applied=12345 leader=12500"}
```

---

## 12. CLI commands

Same hulactl surface as the original plan, with one new command:

```
hulactl genteamcerts                  - Offline. Generate Team CA + per-node bundles + bootstrap_token.
                                        Output dir contains everything operator needs to deploy.
                                        Operator distributes per-node bundles + token out-of-band.

hulactl team-init                     - First node ceremony — generate team_id + bootstrap_token, print.
                                        Doesn't talk to any running hula. (Subset of genteamcerts;
                                        use genteamcerts for new deployments, team-init for solo→team
                                        upgrades where the operator already has certs.)

hulactl team-join <leader-addr>       - Run on a node already running hula but not yet in any team.
   --token <bootstrap-token>            Causes local hula to dial leader-addr's MembershipService.Join
                                        over mTLS using the configured node cert.

hulactl team-leave [<node-id>]        - Default: leave using local node identity. Optional <node-id>
                                        for graceful removal of another node (operator must run on a
                                        node still in the team; goes through the leader).

hulactl team-status                   - Print membership table: nodes, roles (leader/voter/follower),
                                        last applied index, CH-connected, raft-version drift.
                                        Hard exit (Q24) if local team_id mismatches the polled node.

hulactl team-rotate-bootstrap-token   - Generate a new token, write to FSM. Print the new token.
                                        Existing nodes unaffected. Operator updates HULA_TEAM_BOOTSTRAP_TOKEN
                                        on any node planning to join.

hulactl unblock-actor <ip> [--cooldown 1h]   - Admin override: clear a bad-actor flag via Raft.
                                        Optional cooldown window suppresses re-flagging (Q19c).
```

Lives in `model/tools/hulactl/main.go` + `cmddef.go`. New
`CMD_TEAM_*` + `CMD_GENTEAMCERTS` + `CMD_UNBLOCK_ACTOR` constants.

---

## 13. Stage breakdown

Bigger than the original ~2-week sketch. Total ~22 working days
≈ **~4–6 calendar weeks**, sized for one engineer working
full-time with ~30% calendar overhead.

### Sub-stage 3.1 — `genteamcerts` + PKI plumbing (2d)

- `hulactl genteamcerts` command produces CA + per-node bundles.
- `team.pki.{ca_cert,node_cert,node_key}` config wired through.
- Unified listener accepts client certs + dispatches internal
  services behind a Team-CA-verified mTLS gate.
- Tests: cert validation cases (good/bad/expired/wrong-team).

### Sub-stage 3.2 — Internal gRPC channel skeleton (2d)

- Six service stubs registered: Membership, RaftTransport,
  StorageProxy, Relay, Gossip, ChatLookup.
- ClientCert verification middleware.
- Live wiring on the unified listener.
- Tests: cert-required gate; unauthenticated clients see TLS
  rejection; valid clients see Unimplemented for unfilled methods.

### Sub-stage 3.3 — Raft transport over gRPC (3d)

- `pkg/store/storage/raft/transport_grpc.go` implementing
  `raft.Transport` over a streaming gRPC RPC.
- Replace the Stage-2 TCP transport.
- Tests: 3-node cluster forms cleanly with the new transport;
  AppendEntries / RequestVote round-trip.

### Sub-stage 3.4 — Membership + bootstrap_token + AddVoter (2d)

- `_team/bootstrap_token` in FSM.
- `MembershipService.Join` validates token + team_id + cert;
  calls `r.AddVoter`.
- `team-init`, `team-join`, `team-leave`,
  `team-rotate-bootstrap-token` CLIs.
- Tests: bad token → Unauthenticated; team_id mismatch →
  FailedPrecondition; valid → AddVoter called.

### Sub-stage 3.5 — `forwardToLeader` over StorageProxy (2d)

- `StorageProxyService.Apply(cmd)` wired.
- Client interceptor on admin RPCs that retries `NotLeader` (5
  retries, backoff per §9.1).
- Tests: write on follower → forwarded → applied on leader →
  visible on all nodes.

### Sub-stage 3.6 — Analytics relay (3d)

- `pkg/relay/{outbox,drainer}.go` — in-memory ring + bbolt
  disk overflow + drainer goroutine + FIFO eviction.
- `RelayService.RecordEventBatch` on the CH-connected node side.
- Configurable caps (`relay.ring_size`, `relay.outbox_bytes`).
- Tests: queue overflow → eviction + warn; CH down for 60s →
  events buffered and drain on recovery; non-CH-side rejection
  if relay misrouted.

### Sub-stage 3.7 — Gossip + CRDT layer (4d)

- `pkg/gossip/{hlc,clock}.go` — HLC implementation.
- `pkg/crdt/{lww,monotone,tombstone}.go` — register types.
- `pkg/gossip/{push,sync}.go` — push-on-write + anti-entropy
  ticker.
- `GossipService.Push` + `GossipService.SyncDigest` impls.
- `crdt.db` separate bbolt; visitor / session / bad-actor
  buckets wired through.
- Migration of existing `visitors` Bolt bucket from Raft FSM →
  gossip CRDT. Backfill from the Raft snapshot at first boot
  on the upgrade path.
- Tests: LWW merge under HLC; bad-actor monotone "bad wins";
  Raft-clear wins over stale gossip-flag; partition heal merge.

### Sub-stage 3.8 — Leader priority + `/readyz` + chat pinning (2d)

- `_team/ch_connected/<id>` FSM refresh + sweep.
- Priority transfer loop (60s ticker).
- `/readyz` endpoint with the §11 contract.
- Chat-start response includes the per-node hostname; admin SPA
  + visitor JS use it as the WS endpoint.
- Tests: leader transfers within 2 ticks of CH-mismatch detection;
  /readyz returns appropriate codes; chat WS pins to node.

### Sub-stage 3.9 — E2e + sign-off (4d)

- `test/e2e/suites/41-team-formation.sh` — 3-node bring-up via
  CLI; admin write on node-B propagates to node-A and node-C;
  visitor event on a non-CH node lands in CH on the CH-node.
- `test/e2e/suites/42-leader-failover.sh` — kill leader, verify
  failover within 10s, admin write succeeds against new leader,
  original leader rejoins as follower. Wraps the entire test in
  `tc netem` injection (5% loss + 150ms latency between
  containers) per Q21.
- Compose `team-3` profile with three hula nodes + one shared
  ClickHouse + a forwarder-recorder sidecar.
- Update `HA_OUTLINE.md`, write `HA_PHASE3_STATUS.md`.

Total: ~22 working days = **~4.5 calendar weeks**.

---

## 14. Test plan

### 14.1 Unit tests

- `pkg/api/v1/membership/impl_test.go` — Join verification cases
  (token, team_id, cert chain, role).
- `pkg/store/storage/raft/multi_test.go` — 3-node formation,
  propagation, leader-priority transfer, forwardToLeader retry.
- `pkg/store/storage/raft/transport_grpc_test.go` — gRPC
  transport round-trip; TimeoutNow handling.
- `pkg/relay/outbox_test.go` — push / drain / overflow eviction;
  disk persistence across restart.
- `pkg/gossip/hlc_test.go` — clock correctness under wall jumps.
- `pkg/crdt/lww_test.go` + `monotone_test.go` + `tombstone_test.go` —
  merge semantics, idempotency, partition-heal convergence.
- `pkg/crdt/badactor_test.go` — Raft-clear wins over stale flag;
  cooldown window suppresses re-flag.

### 14.2 E2e

`41-team-formation.sh`:
1. `hulactl genteamcerts` produces a 3-node bundle.
2. Bring up node-A as `bootstrap: first-of-team` (the CH-connected one).
3. Bring up node-B and node-C as `bootstrap: false` with peers
   pointing at node-A.
4. Wait 30s for cluster formation.
5. `team-status` on all three reports the same leader (node-A
   per priority), 3 voters, last_index converged.
6. Admin write (create a goal) on node-B → node-A and node-C see
   it within 2s.
7. Visitor event POST against node-B → row appears in CH within
   the relay drain window (≤ 5s on a healthy network).
8. GDPR forget on node-A → tombstone propagates via gossip;
   `visitor/:id` returns 404 from B and C within one anti-
   entropy tick (≤ 12s).

`42-leader-failover.sh`:
1. Same 3-node setup wrapped in `tc netem` (5% loss, 150ms latency).
2. Identify leader (node-A).
3. `docker stop` node-A.
4. Wait up to 15s for failover (extra budget for the lossy network).
5. `team-status` on node-B + node-C reports a new leader.
6. Admin write against new leader → succeeds.
7. Visitor traffic to node-B + node-C continues uninterrupted
   (events queue to outbox since the CH-connected node is down).
8. Restart node-A → priority loop transfers leadership back
   within 2 ticks; outbox drains.

### 14.3 Existing tests stay green

Stages 1 + 2 contract tests pass against the multi-node-capable
`RaftStorage` running in solo mode. All existing e2e suites
(32–37, 38, 39) pass.

---

## 15. Operator notes

### 15.1 Recommended Team sizes

- **1 node** — solo. Default. No `team:` config needed.
- **3 nodes** — minimum for HA. Tolerates 1 failure.
- **5 nodes** — recommended for cross-region. Tolerates 2 failures.
- **2 or 4 nodes** — discouraged. Even-voter has no fault-
  tolerance benefit over (N-1) odd.

### 15.2 Cross-region latency budget (revised)

- Raft Apply over WAN with 5% loss + 150ms RTT: 250–800ms p99.
- Visitor hot path: unchanged from solo (sub-10ms reply, async
  relay drain in the background).
- Reads: always local, sub-millisecond.
- Gossip convergence: ≤ 1 anti-entropy tick (10s default) for
  visitor / session state.

### 15.3 Network requirements

- Each node must reach every other node's `addr` (typically
  port 443). Bidirectional.
- mTLS handshake terminates on each node; intermediate proxies
  (Cloudflare etc.) must allow direct passthrough or the
  internal SNI pattern won't reach hula. Operators serving
  internal traffic through a CDN must configure a separate
  unproxied internal hostname.
- DNS must resolve peer hostnames.

### 15.4 What happens during a partition

- **Majority side**: continues serving admin writes + reads.
  Visitor traffic continues. Gossip pushes to majority peers
  succeed; pushes to minority peers buffer / fail-and-heal.
- **Minority side**: serves reads from local FSM (stale-but-
  bounded). Admin writes return `503 Quorum Lost` until partition
  heals. Visitor traffic continues normally (not on Raft hot
  path); gossip writes accumulate locally.
- **Heal**: gossip anti-entropy reconciles divergent visitor /
  session state via LWW + HLC. Bad-actor flags merge monotonically.
  Raft re-syncs missing log entries to the minority side.

### 15.5 Solo → Team upgrade path

A running solo deployment becomes the seed of a Team:
1. Operator runs `hulactl genteamcerts` for the planned node
   set (including the existing node's `node_id`).
2. Drop the matching cert bundle on the existing node; flip
   config: `bootstrap: solo` → `bootstrap: first-of-team`,
   add `peers:` and `pki:` blocks.
3. Restart hula. The existing FSM is preserved (no data
   migration); the node is now the seed.
4. Stand up new nodes with `bootstrap: false` + their cert
   bundles. They join via the bootstrap token.
5. Visitor data in the existing `visitors` Bolt bucket migrates
   from Raft FSM → gossip CRDT on first boot of the upgraded
   binary (one-shot backfill, idempotent).

### 15.6 Cert rotation

Year 1: certs expire 365d after `hulactl genteamcerts`. Operator
re-runs the command (with the same `--team-id`) to issue new
bundles, distributes them, restarts each node sequentially. The
Team CA itself can be rotated by issuing a new CA, cross-signing,
and rolling — runbook deferred to a follow-up doc.

---

## 16. Acceptance criteria

Stage 3 is done when:

- [ ] `hulactl genteamcerts` produces a working bundle.
- [ ] `pkg/apispec/v1/membership/` proto + generated code in place.
- [ ] `pkg/api/v1/membership/impl.go` implements Join/Leave/Status.
- [ ] Internal gRPC channel mounted on the unified listener
      behind Team-CA cert verification.
- [ ] Raft transport runs over the internal gRPC channel; 3-node
      cluster forms cleanly via mTLS.
- [ ] `team-init/join/leave/status/rotate-bootstrap-token`
      `genteamcerts/unblock-actor` CLI commands work.
- [ ] Analytics relay: non-CH nodes accept events, queue locally
      (memory + disk overflow), drain to CH peer; FIFO eviction
      on overflow with logged warning.
- [ ] Gossip + CRDT layer replicates visitor / session / bad-
      actor state with HLC LWW + monotone "bad wins" + Raft-
      clear-overrides; tombstones for GDPR forget propagate.
- [ ] `forwardToLeader` works for Mutate + admin RPCs; followers
      transparently forward.
- [ ] Leader-priority loop transfers leadership to CH-connected
      voter within 2 ticks of mismatch detection.
- [ ] `/readyz` endpoint reflects Raft state (CH not gating).
- [ ] Chat WS pins to per-node hostname returned by `chat-start`.
- [ ] e2e suites 41 + 42 are green under `tc netem` packet loss.
- [ ] Existing unit + e2e tests stay green.
- [ ] `HA_PHASE3_STATUS.md` written and committed.

---

## 17. Resolved questions (was §12 open questions)

Captured here for reviewer context — every prior open question
now has a decision.

| # | Question | Decision |
|---|---|---|
| §0(a) | `forwardToLeader` wire format | gRPC streaming over the internal mTLS channel, on the unified port. Proto: `StorageProxyService.Apply(Command)`. |
| §0(b) | `WaitLeader` semantics under jitter | Stage-2 default of 30s budget is fine for cross-region; first leader election under `tc netem` 5%+150ms completes well inside. |
| §0(c) | Bootstrap-token over Raft TCP vs separate handshake | Separate from raft transport — token is verified inside the `Join` RPC after mTLS terminates. Raft transport never sees the token. |
| §0(d) | Cross-node Watch fan-out cost at >50ms WAN | Watch only fires for Raft-replicated keys (admin state, low frequency) — cost is bounded. Visitor data uses gossip, doesn't go through Watch. |
| §12.1 | TLS for Raft transport | mTLS from day one (Round 1 Q3). |
| §12.2 | `forwardToLeader` retry budget | 5 retries: 100/200/500/1000/2000ms (Q20). |
| §12.3 | bootstrap_token in YAML | Either accepted; literal in YAML logs WARN on boot (Q22). |
| §12.4 | Health endpoint port | Same as unified port; path `/readyz` (Q23 forces this). |
| §12.5 | Leader stickiness for admin SPA | Verified — existing fetch wrapper handles `NotLeader` retries via the leader-retry interceptor. |
| §12.6 | `team_id` mismatch in CLI | Hard exit 1 with both team_ids printed (Q24). |
| §12.7 | hashicorp/raft under packet loss | Covered by suite 42 wrapping `tc netem` 5%+150ms (Q21). |

### 17.1 Still requiring implementation-time verification

- **HLC clock-skew tolerance** — wall-clock-bound HLC tolerates
  up to a few seconds of NTP skew. Worth measuring on real WAN
  with deliberately drifted clocks before declaring done.
- **Gossip push fan-out cost** at >50 nodes — N² push pattern.
  Below 5 nodes (typical Team size), trivially fine. If we ever
  scale past 10, swap to gossipsub-style mesh.
- **Disk overflow contention** — bbolt single-writer-lock
  semantics under heavy outbox traffic. Profile before sign-off;
  switch to a dedicated WAL if the outbox is hot.
- **Internal-cert SNI gate** — relies on the operator naming
  `node_hostname` consistently. Document the SNI pattern
  explicitly; provide a helper script that validates it.
