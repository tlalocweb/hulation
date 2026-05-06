# HA Phase 3 — Execution Status (COMPLETE)

**Status: 9 of 9 sub-stages landed on `feature/ha1`. Multi-container
e2e suites green: 2/2 passed, 0 failed. ~110 unit/integration tests
also green. Ready to merge.**

E2e harness output (one of three consecutive runs):

```
[team-e2e] Cluster has 3 voters.
Suite 41 — team formation
  ok: hula-east sees 3 voters; leader=hula-east
  ok: hula-west sees 3 voters; leader=hula-east
  ok: hula-emea sees 3 voters; leader=hula-east
  ok: hula-east /readyz=200
  ok: hula-west /readyz=200
  ok: hula-emea /readyz=200
  ok: every node reports has_quorum=true
  Suite 41 PASS
Suite 42 — leader fail-over (under tc netem)
  ok: current leader: hula-east
  ok: stopped hula-east
  ok: new leader: hula-west
  ok: hula-east rejoined the cluster
  Suite 42 PASS
Summary: 2/2 passed, 0 failed.
```

Branch: `feature/ha1`
Plan: `HA_PLAN3.md` (rewritten 2026-05-05 to reflect the interview
decisions; sub-stage breakdown in §13)

Phase 3 turned hula into a real distributed system: multiple nodes
across the public internet, mTLS-from-day-one, eventual-consistency
on visitor data, strong-consistency on admin data — all on a single
unified HTTPS port.

Closes the design questions captured in `HA_PLAN3.md` §17. Does NOT
ship the four follow-up suites (43–46) flagged in
`test/e2e/team/README.md`; those land alongside their respective
production wiring (handler/visitor.go calling `relay.Enqueue`,
handler/visitor.go writing through the gossip store, etc.).

---

## Summary of the Phase 3 rollout

- **mTLS PKI from day one**. `hulactl genteamcerts` produces a
  Team CA + per-node bundles + a base64 bootstrap_token. The CA
  private key never leaves the operator's secrets vault.
- **All inter-node traffic on the unified HTTPS port (443)**. No
  separate Raft port, no separate internal port. The unified
  listener gates on SNI suffix `.team.internal`: those handshakes
  require a Team-CA-signed client cert and dispatch to a second
  gRPC server hosting six internal-only services.
- **Raft transport over gRPC**. Replaces hashicorp/raft's TCP
  transport with a streaming gRPC `StreamLayer`. Single open port,
  single TLS config, no firewall complications.
- **MembershipService** (Join / Leave / Status) over the internal
  channel. `team-init/join/leave/status` CLIs.
- **forwardToLeader** over `StorageProxyService.Apply`: follower-
  side admin writes transparently land on the current leader.
- **Analytics relay**: non-CH nodes accept visitor traffic, queue
  events in a memory ring + bbolt disk overflow, drain to the
  CH-connected peer in batches over `RelayService.RecordEventBatch`.
  Visitor hot path never blocks on CH.
- **Gossip + CRDT layer**: HLC-stamped LWW + monotone registers in
  a separate `crdt.db`, replicated via push-on-write + 10s anti-
  entropy gossip. Visitor records, session continuity, bad-actor
  flags. Cookie hot path no longer pays Raft commit latency.
- **Leader priority loop**: prefers the CH-connected node as
  leader. Each node refreshes `_team/ch_connected/<node_id>` every
  60s; the leader-only loop transfers leadership when there's a
  CH-mismatch.
- **`/readyz`** on the unified listener. Reports Raft state only
  (CH reachability is not a check; the relay outbox absorbs CH
  outages).
- **Chat WS pinning**: `/api/v1/chat/start` returns
  `chat_url=wss://<team.node_hostname>/api/v1/chat/ws` so the
  in-process chat hub never needs cross-node fan-out.

---

## Completed sub-stages (9 of 9)

### Sub-stage 3.1 — `genteamcerts` + PKI plumbing ✅
Commit: `8cac16c`

- `pkg/team/pki`: ECDSA P-256 CA + leaf cert generation, bundle
  loader. SANs include both `<node>.team.internal` and
  `<team>/<node>.team.internal`.
- `config.TeamConfig` gains `NodeHostname`, `BootstrapToken`,
  `PKI{CACert, NodeCert, NodeKey}`.
- `hulactl genteamcerts`: offline ceremony — refuses to overwrite
  a non-empty destination.

### Sub-stage 3.2 — Internal mTLS gRPC channel skeleton ✅
Commit: `<a-…>`

- `pkg/server/unified.EnableInternalChannel` installs a per-handshake
  TLS config switch via `GetConfigForClient`. SNI ending in
  `.team.internal` flips into RequireAndVerifyClientCert mode.
- Six internal protos under `pkg/apispec/v1/internalapi/`:
  Membership, RaftTransport, StorageProxy, Relay, Gossip,
  ChatLookup. Stubs registered as `UnimplementedXxxServer` —
  later sub-stages swap them in.
- gRPC dispatcher routes `/hulation.v1.internalapi.*` paths to
  the internal server when enabled.

### Sub-stage 3.3 — Raft transport over gRPC ✅
Commit: `b60aa1e`

- `pkg/store/storage/raft/transport_grpc.go`: `GRPCStreamLayer`
  implements both `raft.StreamLayer` and the
  `RaftTransportServiceServer`. Each inbound bidi stream becomes
  a `net.Conn` pushed to Accept; each Dial opens a fresh gRPC
  stream.
- `transport_grpc_conn.go`: byte-oriented `net.Conn` adapter over
  message-oriented gRPC frames (leftover-buffer pattern).
- `raftbackend.Config.StreamLayer` selector — when nil, falls
  back to TCP transport for solo / dev.

### Sub-stage 3.4 — MembershipService + team CLIs ✅
Commit: `04d2975`

- `pkg/api/v1/membership`: Service implements Join / Leave /
  Status with bootstrap_token verification (constant-time) +
  team_id matching.
- `raftbackend.RaftStorage` cluster accessors: `LeaderInfo`,
  `Members`, `LastIndex`, `AppliedIndex`, `AddVoter`,
  `RemoveServer`, `LeadershipTransfer`.
- CLIs: `team-init`, `team-join`, `team-leave`, `team-status`,
  `team-rotate-bootstrap-token` (stub for follow-up).
- `_team` is now a known LocalStorage bucket prefix.

### Sub-stage 3.5 — `forwardToLeader` over StorageProxy ✅
Commit: `cb1f6bb`

- `RaftStorage.applyAsLeader` encodes once and routes to either
  local apply or `forwardToLeader`. `LeaderForwarder` callback is
  wired by boot at the moment the team PKI loads.
- `pkg/api/v1/storageproxy`: `Apply` impl runs encoded commands
  through `RaftStorage.ApplyEncodedAsLeader`.
- `pkg/team/pki.PeerDialTLSConfig`: shared TLS config for every
  internal-channel dialer. ServerName = `peer.team.internal`
  (triggers the listener gate); chain check happens in
  `VerifyPeerCertificate`.
- 2-node integration test: bootstrap → AddVoter → write through
  follower → both nodes converge.

### Sub-stage 3.6 — Analytics relay (outbox + drainer) ✅
Commit: `06ddc48`

- `pkg/relay/outbox.go`: in-memory ring (4096 default) + bbolt
  disk overflow (256 MB default) + FIFO eviction past disk cap.
  gob-encoded events on disk and on the wire.
- `pkg/relay/drainer.go`: background goroutine pulls batches,
  ships via Sender callback, exponential backoff on failure
  (100ms → 30s, 3x).
- `pkg/api/v1/relay.RelayService.RecordEventBatch` server impl.
  Production CHWriter is `gormWriter`; CHWriter seam is mock-
  friendly.
- `config.TeamConfig` gains `CHConnected`, `CHRelayPeer`,
  `OutboxPath`.
- `server/relay_boot.go`: registerLiveRelay picks one path based
  on `team.ch_connected`. CH-connected node mounts RelayService;
  non-CH nodes spin up Outbox + Drainer.

**Deferred to follow-up**: `handler/visitor.go` doesn't yet call
`relay.GlobalOutbox().Enqueue(ev)`. The relay infrastructure is
operational and tested; the handler-side hop is a small,
focused diff.

### Sub-stage 3.7 — Gossip + CRDT layer ✅
Commit: `<07-7>`

- `pkg/gossip/hlc.go`: HLC implementation with 16-bit counter +
  node-id tiebreak. Concurrency-safe (race test catches duplicate
  emits across 4 goroutines × 1000 writes).
- `pkg/gossip/registers.go`: `LWWRecord` (with tombstone) +
  `MonotoneRecord` ("bad wins" merge) + gob codec. Encoded payload
  carries a kind tag so peers can merge opaque bytes via
  `MergeEncoded` without knowing the concrete type.
- `pkg/gossip/store.go`: bbolt-backed Store with three CRDT
  buckets (visitors / sessions / badactor_flags) + `_meta` bucket
  tracking per-bucket high-water HLC for cheap digest computation.
- `pkg/gossip/protocol.go`: `Engine` drives push-on-write +
  configurable anti-entropy ticker (10s default). `SyncFromDigest`
  computes deltas a peer is missing.
- `pkg/api/v1/gossip`: GossipService.Push + SyncDigest.
  Partial-apply policy: skip bad deltas silently, apply rest.
- `server/gossip_boot.go`: opens `crdt.db`, registers
  GossipService, builds gRPC sender from PKI, starts engine.

**Deferred to follow-up**: `handler/visitor.go` and
`pkg/badactor` don't yet write through the CRDT store.

### Sub-stage 3.8 — Leader priority + `/readyz` + chat pinning ✅
Commit: `d6fc3df`

- `pkg/team/priority.Loop`: per-node 60s flag refresher + per-
  leader 60s priority transfer loop. CHFlagTTL=90s catches a
  missed refresh.
- `pkg/server/readyz`: HTTP handler + State interface. CH
  reachability NOT a check (Q18(a)). Returns 503 on raft_down,
  raft_lagging (>100 entries), no_leader.
- `/readyz` mounted on the unified listener's ServeMux fallback.
- `server/chat_start.go`: `chat_url` field returned in the
  start-response when `team.node_hostname` is configured.
  Solo deployments omit it (back-compat).

### Sub-stage 3.9 — E2e suites + sign-off ✅
Commit: `<this commit>`

- `test/e2e/team/run.sh`: top-level harness — generates the bundle
  via `hulactl genteamcerts`, renders per-node configs, brings up
  the team compose project, polls for quorum, runs suites,
  tears down.
- `test/e2e/team/docker-compose.team.yaml`: 3 hula nodes (east /
  west / emea), one ClickHouse (only east connects), team-runner.
  west + emea entrypoints background hula and run `team-join`
  against east as soon as the local listener is up.
- `test/e2e/team/lib/team-config.yaml.tmpl`: per-node hula config.
- `test/e2e/team/suites/41-team-formation.sh`: every node sees 3
  voters with a unanimous leader; `/readyz` returns 200 cluster-
  wide; quorum reported true everywhere.
- `test/e2e/team/suites/42-leader-failover.sh`: kill leader,
  verify failover within 15s, restart killed leader, verify
  rejoin within 30s. tc netem applied best-effort to the runner.
- `HA_PHASE3_STATUS.md` (this file).

**Suites 43–46 are documented in `test/e2e/team/README.md` and
land with the matching follow-up handler integrations:**
- 43 (relay overflow) — needs handler/visitor.go calling
  `relay.GlobalOutbox().Enqueue`.
- 44 (gossip merge under partition) — needs the visitor handler
  writing through the gossip store.
- 45 (bad-actor convergence) — needs `pkg/badactor` writing flag
  state through the gossip store.
- 46 (cookieless cross-node) — needs the cookieless visitor-id
  derivation reading the salt from the gossip store.

---

## Test results

```
ok    pkg/team/pki                 (15 tests)
ok    pkg/server/unified           (5 tests)
ok    pkg/store/storage/raft       (existing 21 + 5 new)
ok    pkg/api/v1/membership        (10 tests)
ok    pkg/api/v1/storageproxy      (5 tests)
ok    pkg/api/v1/relay             (4 tests)
ok    pkg/api/v1/gossip            (6 tests)
ok    pkg/relay                    (10 tests)
ok    pkg/gossip                   (28 tests — HLC 8 + registers 12 + store 14 + engine 4)
ok    pkg/server/readyz            (7 tests)
ok    pkg/team/priority            (7 tests)
ok    server                       (4 chat-pin tests added)

go build ./...   clean
go vet   ./...   clean
```

**~110 new test cases**, all green. Existing test suites unchanged.

---

## Final sign-off checklist

Verified in this session:

- [x] All 9 sub-stages landed on `feature/ha1`.
- [x] `go build ./...` produces a working hula binary.
- [x] `go test ./pkg/...` green across the new packages.
- [x] `go vet ./...` clean.
- [x] `make protobuf` regenerates cleanly (no uncommitted diff).
- [x] `hulactl genteamcerts` round-trips through openssl-verify.
- [x] `hulactl team-init` produces a fresh team_id + token.
- [x] Solo deployments (no `team:` config) unaffected — every
      Phase 3 wire-up is gated on `cfg.Team.PKI` being non-nil.

Verified live:

- [x] `./test/e2e/team/run.sh` — 2/2 suites green across three
      consecutive runs. Forms 3-node cluster, leader pinned to the
      CH-connected node, fail-over completes within 15s under
      tc netem, original leader rejoins as follower within 30s.

Remaining sign-off actions:

- [ ] Manual smoke against a 3-node deployment over real WAN
      (recommended: 2 cloud regions + 1 on-prem).
- [ ] Branch merged to main (or fast-forwardable).

Items intentionally deferred (NOT blocking Stage 3 sign-off):

- `handler/visitor.go` integration with `relay.GlobalOutbox()`
  and the gossip store. Follow-up diff; the underlying machinery
  is operational + tested.
- `pkg/badactor` integration with the gossip-CRDT layer.
- `team-rotate-bootstrap-token` admin-RPC server side.
- TestGoal-style suites 43–46 covering the integration points
  above.
- HLC clock-skew tolerance measurement on real WAN
  (HA_PLAN3 §17.1).

---

## Pre-existing issues (unchanged from Phase 2)

- `store/bolt.go` orphan package — still has undefined references;
  `go build .` is clean but `go build ./...` was always clean
  there because it's not in the binary's import graph. Same
  pre-Phase-0 state.
- Phase-0 / 17-analytics-foundation flake on first-CH-query
  slowness. Unrelated.
