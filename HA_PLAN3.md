# HA Plan 3 — Multi-node Team Membership

> **REVISIT NOTE**: this plan should be re-read and amended after
> Stages 1 and 2 are implemented. Several design choices below
> depend on shape decisions that will only firm up once
> `RaftStorage` (Stage 2) is running in solo mode against the
> real codebase. Specifically: (a) the exact `forwardToLeader`
> wire format, (b) how `WaitLeader` semantics behave under
> network jitter, (c) whether the bootstrap-token verification
> can ride on the raft TCP transport or needs its own pre-handshake
> bytes, and (d) the actual cost of the cross-node Watch fan-out
> at >50ms WAN latency. Treat this document as a faithful sketch
> of the intent, not the literal implementation contract.

Stage 2 shipped solo deployments. Stage 3 turns hula into a real
distributed system: multiple nodes, real consensus across regions,
operator-driven membership. This is where "Team" becomes a thing
operators interact with directly.

Closely modelled on `../izcr/pkg/store/raft/raftnode.go` for the
join/transport flow, plus a fresh CLI surface modeled on the
existing `hulactl` command pattern.

Related docs: `HA_OUTLINE.md`, `HA_PLAN1.md`, `HA_PLAN2.md`,
`../izcr/cmd/izcrd/commands/run.go` (multi-node boot pattern).

---

## 1. Context and scope

### 1.1 What Stages 1 + 2 delivered that we reuse

- `pkg/store/storage/Storage` interface — unchanged.
- `pkg/store/storage/raft/RaftStorage` — already supports
  arbitrary `BindAddr`, just defaults to loopback in solo. We
  flip it.
- `RaftConfig.Peers` — already parsed and stashed; we wire it to
  the Raft cluster configuration.
- `RaftStorage.forwardToLeader(...)` — already exists as a stub.
  Stage 3 implements the wire path.
- The Informer + Watch — already in place. We add per-node
  fan-out semantics and a `WatchOnLeader` helper.

### 1.2 What Stage 3 ships

- **Multi-node Raft cluster formation** — operators bring up
  N hula nodes (typically 3 or 5), one of which is bootstrapped
  as the initial leader; the rest join via a shared bootstrap
  token. After formation the cluster self-manages.
- **Two membership UX paths** (per the outline §9):
  - **YAML-driven peers** for initial team setup. The first
    node bootstraps; subsequent nodes have peer addresses in
    YAML and discover the leader on first boot.
  - **CLI-dynamic** for runtime membership: `hulactl team-init`,
    `team-join`, `team-leave`, `team-status`,
    `team-rotate-bootstrap-token`.
- **Bootstrap-token authentication** for joins — a per-Team
  shared secret that the joining node presents during the join
  handshake. Token is rotatable.
- **Leader priority for ClickHouse-connected nodes** — a small
  loop that nudges leadership toward CH-connected voters when
  the current leader is non-CH.
- **`forwardToLeader` implementation** for non-leader writes.
  All admin-RPC writes that hit a follower forward through this
  path.
- **Health / readiness endpoint** that reflects Raft state
  (leader id, last applied index, has quorum, CH connected).
  External LBs read this for membership.
- **Two e2e suites**: `41-team-formation.sh` (3-node
  bring-up + admin-RPC propagation), `42-leader-failover.sh`
  (kill leader, verify failover + read continuity).

### 1.3 What Stage 3 does NOT ship

- **mTLS for the Raft transport**. Stage 3 uses plain TCP for
  Raft (the operator is expected to put nodes on a private
  network or VPN). Stage 4 introduces a separate mTLS-only
  internal gRPC channel; Stage 5 may move the Raft transport
  onto that channel as well.
- **Internal gRPC service for analytics relay** (Stage 4).
- **Site build propagation** (Stage 5).
- **CRDT visitor identity** (Stage 6).

---

## 2. Boot flow — the three modes

A hula node boots into exactly one of three modes, picked from
config:

### 2.1 Solo (Stage 2 default — unchanged)

`team:` block missing or `bootstrap: solo` → single-node Raft
cluster, loopback transport, no peers.

### 2.2 First-of-team (the seed node)

`bootstrap: first-of-team` → bootstrap a multi-node-capable
Raft cluster with this single node as the only initial voter;
listen on a non-loopback `bind_addr`; expect peers to join via
the CLI flow OR via a `peers:` list that the operator pre-popu-
lated. After the first run, the operator should change
`bootstrap: first-of-team` → `bootstrap: false` (or remove the
key) so a future restart doesn't re-bootstrap.

### 2.3 Join (every other node)

`bootstrap: false` (default in YAML when peers are present) →
contact each address in `peers:` (or the address provided to
`hulactl team-join`), present the bootstrap token, request
to be added as a voter via the leader's join RPC. The leader
calls `r.AddVoter(...)`; the joining node's local Raft picks up
the cluster configuration and starts following.

```yaml
# Seed node:
team:
  team_id: 4f1a3c2d-...
  node_id: node-east
  data_dir: /var/hula/data/raft
  bind_addr: 0.0.0.0:8300
  bootstrap: first-of-team
  bootstrap_token: "{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}"
  # Optional — operators can pre-populate so the CLI isn't
  # required for initial bring-up.
  peers:
    - { id: node-west, raft_addr: west.internal:8300 }
    - { id: node-emea, raft_addr: emea.internal:8300 }

# Joining node:
team:
  team_id: 4f1a3c2d-...   # MUST match the seed
  node_id: node-west
  data_dir: /var/hula/data/raft
  bind_addr: 0.0.0.0:8300
  bootstrap: false
  bootstrap_token: "{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}"
  peers:
    - { id: node-east, raft_addr: east.internal:8300 }
```

`team_id` mismatch between two nodes → join is refused with a
clear error. This protects against accidentally joining the
wrong cluster.

---

## 3. The join handshake

The join is a single RPC from the joining node to the leader.
Stage 3 ships it as a small gRPC service on the same port as the
existing admin gRPC (separate `MembershipService`). Stage 4 will
move it onto the mTLS-only internal port; for Stage 3 we
authenticate with the bootstrap token over plain TCP.

```proto
// pkg/apispec/v1/membership/membership.proto

service MembershipService {
  // Join is called by a new node asking to be added to the team.
  // The receiving node forwards to the leader if not leader.
  rpc Join(JoinRequest) returns (JoinResponse);

  // Leave is called by a node asking to be removed from the team.
  // Graceful — the leader transfers leadership first if needed.
  rpc Leave(LeaveRequest) returns (LeaveResponse);

  // Status returns membership + leader + index info. Public-ish
  // (admin auth required, no bootstrap token).
  rpc Status(StatusRequest) returns (StatusResponse);
}

message JoinRequest {
  string team_id          = 1;
  string node_id          = 2;
  string raft_addr        = 3;  // where the joining node is listening
  string bootstrap_token  = 4;  // pre-shared secret
  bool   ch_connected     = 5;  // hint for leader-priority loop
}

message JoinResponse {
  string leader_id        = 1;
  string leader_raft_addr = 2;
  uint64 last_index       = 3;
  // The joining node uses these to validate its own state matches.
}
```

### 3.1 Bootstrap token verification

The bootstrap token is a 32-byte random secret stored in the
Team's Raft FSM under the key `_team/bootstrap_token` and
shared with operators via env var. The leader verifies the
joining node's token using a constant-time compare; a mismatch
returns gRPC `Unauthenticated` and is logged loudly.

`hulactl team-rotate-bootstrap-token` regenerates the secret
and writes it to the FSM via `Storage.Put`. Existing nodes are
unaffected (rotation only matters for FUTURE joins). The
operator must propagate the new token to any node that hasn't
yet joined.

### 3.2 Add-voter on the leader

```go
func (m *MembershipService) Join(ctx context.Context, req *JoinRequest) (*JoinResponse, error) {
    if !m.raft.IsLeader() {
        // Forward to leader.
        return m.forwardToLeader(ctx, req)
    }
    // Verify team_id + bootstrap_token.
    expected, _ := m.storage.Get(ctx, "_team/bootstrap_token")
    if subtle.ConstantTimeCompare([]byte(req.BootstrapToken), expected) != 1 {
        return nil, status.Error(codes.Unauthenticated, "bad bootstrap token")
    }
    if req.TeamId != m.cfg.TeamID {
        return nil, status.Error(codes.FailedPrecondition, "team_id mismatch")
    }

    // Add as voter.
    f := m.raft.AddVoter(
        raft.ServerID(req.NodeId),
        raft.ServerAddress(req.RaftAddr),
        0,  // prevIndex (let Raft figure it out)
        10*time.Second,  // timeout
    )
    if err := f.Error(); err != nil {
        return nil, status.Errorf(codes.Internal, "AddVoter: %s", err)
    }

    // Record the CH-connected hint in the FSM so the priority
    // loop can use it.
    if req.ChConnected {
        if err := m.storage.Put(ctx, "_team/ch_connected/"+req.NodeId, []byte("1")); err != nil {
            log.Warnf("ch_connected hint write failed: %s", err)
        }
    }

    return &JoinResponse{
        LeaderId:       string(m.raft.Leader()),
        LeaderRaftAddr: m.cfg.BindAddr,
        LastIndex:      m.raft.LastIndex(),
    }, nil
}
```

---

## 4. The CLI commands

```
hulactl team-init                     - Generate team_id + bootstrap_token, print them.
                                        Operator runs this once before bringing up the
                                        seed node. Doesn't talk to any running hula —
                                        it just generates the bytes.
hulactl team-join <leader-addr>       - Run on a node that ALREADY HAS hula running but
   --token <bootstrap-token>            isn't yet in any team. Causes the local hula
                                        to dial leader-addr's MembershipService.Join.
hulactl team-leave [<node-id>]        - Default: leave the team using the local node's
                                        identity. Optional <node-id> for graceful
                                        removal of another node (operator must run on
                                        a node that's still in the team; goes through
                                        the leader).
hulactl team-status                   - Print the team's membership table: nodes,
                                        roles (leader/voter/follower), last applied
                                        index, CH-connected, raft-version drift.
hulactl team-rotate-bootstrap-token   - Generate a new bootstrap token, write to FSM.
                                        Print the new token. Must be a Team
                                        admin (existing JWT auth).
```

`team-init` is operator-only ceremony (no hula needed). Every
other command goes through the local hula's existing admin
auth (JWT bearer token) and either calls
`MembershipService.Join` against a remote node or writes to the
local Storage.

### 4.1 Where the CLI lives

Same place as existing commands: `model/tools/hulactl/main.go`
+ `cmddef.go`. Add five new `CMD_TEAM_*` constants and case
handlers.

---

## 5. Leader priority for ClickHouse-connected nodes

Per the outline §5: nodes with CH should preferentially become
leader because they're typically the better-connected, tier-1
nodes. Implementation:

1. Each node, on startup, probes its configured ClickHouse host
   (existing `pkg/store/clickhouse` ping). If reachable, write
   `_team/ch_connected/<node_id>` = `"1"` into the FSM (via the
   join hint above OR a periodic re-write — every 60s a node
   refreshes its own flag). If unreachable, delete the key.
2. A small goroutine on each node runs every 60s:
   - Read the current leader id.
   - Read the leader's `_team/ch_connected/<leader_id>` value.
   - If leader is non-CH AND there exists a CH-connected voter
     that's healthy (last-contact within 5s), call
     `r.LeadershipTransfer(<ch-connected-id>)`.
3. Transfer is **best-effort** — if it fails for any reason
   (target unhealthy, network glitch), just log + retry next
   cycle. Never block on the transfer.

This is a "nudge" not a "constraint". A non-CH leader works
fine; the gRPC analytics relay (Stage 4) routes events to a
CH-connected peer regardless of leadership.

### 5.1 Why not use hashicorp/raft's native priority?

hashicorp/raft has voter / non-voter / staging suffrage but no
native "leader priority" concept. The transfer-on-mismatch loop
above is the standard pattern. izcr does the same.

---

## 6. `forwardToLeader` implementation

Stage 2 left this as a stub. Stage 3 fills it in. Two callsites
need it:

1. `RaftStorage.Mutate` on a follower → forward the mutate
   request to the leader.
2. Any admin RPC that performs a Raft write on a follower →
   forward the entire RPC.

For (1), we add a small internal RPC `StorageProxy.Apply(cmd
*Command)` on the same gRPC port. Followers serialise the
Command via the same proto, dial the leader, and call
`StorageProxy.Apply`. The leader runs `r.Apply(cmd)` and
returns the result.

For (2), the existing admin RPCs are gRPC services — we add a
gRPC client interceptor that detects "not leader" (a sentinel
status code we add) and transparently retries against the
current leader address discovered via `RaftStorage.Leader()`.
This means callers (the admin SPA, hulactl) never have to think
about which node they hit.

```go
// pkg/store/storage/raft/proxy.go

func (s *RaftStorage) forwardToLeader(ctx context.Context, cmd *Command) error {
    leaderAddr := s.raft.LeaderWithID()
    if leaderAddr == "" {
        return ErrNoLeader
    }
    conn, err := s.dialPeer(string(leaderAddr))
    if err != nil { return err }
    defer conn.Close()
    client := pb.NewStorageProxyClient(conn)
    _, err = client.Apply(ctx, &pb.ApplyRequest{Command: encodeCommand(cmd)})
    return err
}
```

`dialPeer` uses plain gRPC over the Raft port for Stage 3.
Stage 4 will route this through the mTLS internal channel.

---

## 7. Health / readiness endpoint

The LB / DNS layer needs to know whether a node is fit to
serve traffic. We add `/healthz` (liveness) and `/readyz`
(readiness):

- `/healthz` — process is up. Always 200 unless we're shutting
  down. Existing `/hulastatus` keeps working.
- `/readyz` — node is fit to serve traffic. 200 only if:
  - Raft has joined the cluster (state is Leader, Follower, or
    Candidate — not Shutdown).
  - Raft last-applied index is within 100 of the last-known
    leader index (i.e., we're caught up).
  - If config claims CH connectivity, CH is reachable.
  Returns 503 with a JSON body explaining which check failed.

External LBs poll `/readyz` every ~5s. A node that's catching
up after a restart returns 503 until it's caught up; the LB
drains traffic from it during that window.

---

## 8. Stage breakdown

### Sub-stage 3.1 — MembershipService proto + skeleton (1d)

- `pkg/apispec/v1/membership/membership.proto` + generated code.
- `pkg/api/v1/membership/impl.go` with stub Join / Leave /
  Status methods.
- Wire into the existing gRPC server registration.

### Sub-stage 3.2 — Bootstrap-token storage + verification (1d)

- `_team/bootstrap_token` key in the FSM, generated at first
  team-init.
- `MembershipService.Join` validates the token + team_id.
- `team-rotate-bootstrap-token` CLI.
- Tests: bad token → Unauthenticated; team_id mismatch →
  FailedPrecondition; valid → AddVoter called.

### Sub-stage 3.3 — Multi-node bring-up + AddVoter flow (3d)

- Boot flow that handles `bootstrap: first-of-team` vs `false`.
- `team-init`, `team-join`, `team-leave` CLI commands.
- Three-node integration test in `pkg/store/storage/raft/`
  using temp ports + temp dirs.
- Tests: 3-node cluster forms within 30s; admin write on any
  node propagates to all three.

### Sub-stage 3.4 — Leader priority loop + ch_connected (1d)

- Periodic ch_connected refresh (60s ticker).
- Periodic priority-transfer loop (60s ticker) on each node.
- Tests: simulate non-CH leader + CH-connected follower →
  leadership transfers within 2 ticks.

### Sub-stage 3.5 — forwardToLeader for Mutate + admin RPCs (1.5d)

- `StorageProxy.Apply` RPC.
- Client interceptor that retries against the discovered
  leader on `NotLeader` status.
- Tests: write on a follower → forwarded → applied on leader →
  visible on all nodes.

### Sub-stage 3.6 — /readyz + team-status (0.5d)

- New `/readyz` HTTP endpoint.
- `team-status` CLI command (calls Status RPC + formats).

### Sub-stage 3.7 — E2e + sign-off (1d)

- `test/e2e/suites/41-team-formation.sh` — 3-node bring-up.
- `test/e2e/suites/42-leader-failover.sh` — kill leader,
  verify failover + read continuity.
- Update e2e fixture compose to support a 3-node hula stack
  (new compose profile `team-3`).
- Update `HA_OUTLINE.md`, write `HA_PHASE3_STATUS.md`.

Total: ~9 working days = **~2 calendar weeks**.

---

## 9. Test plan

### 9.1 Unit tests

- `pkg/api/v1/membership/impl_test.go` — Join verification
  cases (token, team_id, role).
- `pkg/store/storage/raft/multi_test.go` — 3-node cluster
  formation + propagation, leader priority transfer,
  forwardToLeader retry.
- `pkg/store/storage/raft/proxy_test.go` — StorageProxy.Apply
  round-trip.

### 9.2 E2e

`41-team-formation.sh`:
1. `hulactl team-init` (CLI ceremony — generates bytes).
2. Bring up node-A as `bootstrap: first-of-team`.
3. Bring up node-B and node-C as `bootstrap: false` with
   peers pointing at node-A.
4. Wait 30s for cluster formation.
5. Verify `team-status` on all three reports the same leader,
   3 voters, last_index converged.
6. Issue an admin write (create a goal) against node-B.
7. Verify node-A and node-C both see the goal within 2s.
8. Repeat with admin write against node-C → all three see.

`42-leader-failover.sh`:
1. Same 3-node setup as above.
2. Identify the current leader.
3. `docker stop` the leader's container.
4. Wait up to 10s for new leader election.
5. Verify `team-status` on remaining two reports a new leader.
6. Issue an admin write against the new leader → succeeds.
7. Restart the original leader.
8. Verify it rejoins as a follower within 30s and catches up.

### 9.3 Existing tests stay green

Stages 1 + 2 introduced the Storage interface and the
single-node default. Stage 3 must not regress either:

- All existing unit tests pass against the multi-node-capable
  `RaftStorage` running in solo mode.
- All existing e2e suites (32–37, 38, 39) pass.

---

## 10. Operator notes

### 10.1 Recommended Team sizes

- **1 node** — solo. Default. No `team:` config needed.
- **3 nodes** — minimum for HA. Tolerates 1 failure.
- **5 nodes** — recommended for cross-region. Tolerates 2
  failures.
- **2 or 4 nodes** — discouraged. Even number of voters has
  no fault-tolerance benefit over (N-1) odd.

### 10.2 Cross-region latency budget

- Raft Apply: each write blocks until committed on a quorum.
  With 3 nodes spread 50–200ms apart, expect 100–250ms p99
  for admin writes.
- Reads: always local, sub-millisecond.
- Visitor traffic: never goes through Raft (Stage 6's CRDT layer).

### 10.3 Network requirements

- Each node must reach every other node's `raft_addr` (default
  port 8300). Bidirectional.
- Stage 3 uses plain TCP. Stage 4 introduces mTLS for the
  internal channel. Operators are expected to put nodes on a
  private network or VPN until Stage 4 lands.
- Outbound DNS must resolve peer hostnames.

### 10.4 What happens during a partition

- Majority side: continues serving admin writes + reads.
- Minority side: serves reads from local FSM (stale-but-bounded).
  Admin writes return `503 Quorum Lost` until partition heals.
  Visitor traffic continues normally (no Raft hot path).

---

## 11. Acceptance criteria

Stage 3 is done when:

- [ ] `pkg/apispec/v1/membership/` proto + generated code in
      place.
- [ ] `pkg/api/v1/membership/impl.go` implementing
      Join/Leave/Status.
- [ ] `team-init/join/leave/status/rotate-bootstrap-token` CLI
      commands work.
- [ ] 3-node cluster forms cleanly; admin writes propagate;
      leader fail-over works within 10s.
- [ ] Leader priority loop transfers leadership to a
      CH-connected voter within 2 ticks of mismatch detection.
- [ ] `forwardToLeader` works for Mutate + admin RPCs; followers
      transparently forward.
- [ ] `/readyz` endpoint reflects Raft + CH state.
- [ ] e2e suites 41 + 42 are green.
- [ ] Existing unit + e2e tests stay green.
- [ ] `HA_PHASE3_STATUS.md` written and committed.

---

## 12. Open questions to resolve during implementation

(These are the items called out in the revisit-note at the
top — they need real implementation experience before they're
locked.)

1. **Should Raft transport require TLS in Stage 3?** — The
   plan says "plain TCP, mTLS in Stage 4". Reasonable, but
   operators on the public internet won't be happy. Maybe
   ship Stage 3 with optional TLS via a `--insecure-raft`
   flag?
2. **`forwardToLeader` retry budget** — fixed N retries vs.
   exponential backoff with deadline? Initial inclination:
   3 retries with 100/300/900ms backoff before surfacing
   `NoLeader` to the caller.
3. **bootstrap_token in plain config vs. always env-only** —
   the plan says `{{env:HULA_TEAM_BOOTSTRAP_TOKEN}}`. Should
   we hard-fail if a literal value appears in YAML?
4. **Health endpoint port** — same as the admin port, or
   separate? Cloud LBs sometimes need a distinct port for
   health checks. Lean: same port, separate path.
5. **Leader stickiness for admin SPA** — the SPA caches the
   leader address discovered on first call. If the leader
   changes, the SPA hits a follower → gets `NotLeader` →
   client interceptor retries. UX-wise this is fine. Worth
   verifying the existing SPA fetch wrapper handles this
   transparently.
6. **`team_id` mismatch surfacing in CLI** — should
   `team-status` against a node with a different `team_id`
   error loudly, or display "different team" plainly?
7. **Does hashicorp/raft handle cross-region leadership
   transfer cleanly under packet loss?** — needs real testing.
   Worth a dedicated integration test that injects 5% loss.
