# HA Phase 2 — Close-out Status

Branch: `ha/plan-2-raft-solo`
Plan: `HA_PLAN2.md`
Predecessor: Stage 1 (`HA_PLAN1.md`, branch `ha/plan-1-storage-seam`)

Status: **shipped**. RaftStorage replaces LocalStorage as the
production default; solo deployments (single-node Raft cluster)
remain the most common deployment shape and incur the same
write-cost as Stage 1's direct-bbolt path.

---

## What landed

### New package: `pkg/store/storage/raft`

| File | Purpose |
|---|---|
| `cmd.proto` / `cmd.pb.go` | Protobuf-encoded Raft log entries (Put / Delete / CAS / CAS-Create / Batch) |
| `cmd_codec.go` | Helper constructors + (de)serialise wrappers around the generated proto |
| `raftstorage.go` | `RaftStorage` — implements `storage.Storage`. Owns the raft node + transport + log/stable stores |
| `fsm.go` | `FSM` — `raft.FSM` impl backed by a bbolt file. Apply / Snapshot / Restore |
| `informer.go` | Per-prefix Watch fan-out (drop-on-slow-consumer; matches LocalStorage semantics) |
| `bootstrap.go` | `AutoConfig(*config.TeamConfig) → Config`; persists generated TeamID + NodeID under DataDir |
| `raftstorage_test.go` | Storage-contract suite + bootstrap idempotency + on-disk layout |
| `bootstrap_test.go` | Auto-solo + operator-pin overrides + multi-node-falls-back-to-solo |
| `watch_test.go` | Watch fires from FSM Apply (not just same-process publish) |
| `snapshot_test.go` | Snapshot → restart → log replay round-trip |

### Config surface

`config/team.go` introduces a `TeamConfig` block (under
`team:` in `config.yaml`). Solo deployments can omit the block
entirely; AutoConfig generates a UUID v7 TeamID and a
hostname-derived NodeID and persists them under DataDir
(`team-id`, `node-id` files). Multi-node fields (`peers:`,
`bootstrap: first-of-team|join`) are parsed but warn-and-fall-
back-to-solo until Stage 3.

### Boot wiring

`server/run_unified.go` swapped from
`localstorage.Open(...)` to
`raftbackend.New(raftbackend.AutoConfig(conf.Team))` followed by
`WaitLeader(ctx, 30s)` and `storage.SetGlobal(rs)`. The Stage-1
`HULA_BOLT_PATH` env-override + `storageDataPath` helper are
gone — operators now control the location via
`team.data_dir` (default `/var/hula/data/raft`).

LocalStorage is still alive in the codebase: tests in
`pkg/store/bolt/` use it via the `newTestStorage(t)` helper, and
the offline OPAQUE recovery tool (`hulactl forget-opaque-record`)
opens a bbolt file directly. RaftStorage is the production
default; LocalStorage is the developer / offline backend.

### On-disk layout

```
/var/hula/data/raft/
├── data.db          # FSM state (bbolt) — same bucket layout as Stage 1
├── raft-log.db      # raft log store (raft-boltdb)
├── raft-stable.db   # raft stable store
├── snapshots/       # raft snapshot store
├── team-id          # persisted TeamID (UUID v7)
└── node-id          # persisted NodeID (hostname by default)
```

### E2e suite

`test/e2e/suites/39-solo-raft.sh` — boot-log assertion + on-disk
layout check + identity persistence across container restart +
visitor consent round-trip (proves the FSM apply path is wired).

---

## Test results

```
ok  github.com/tlalocweb/hulation/pkg/store/bolt          0.057s
ok  github.com/tlalocweb/hulation/pkg/store/storage/local 0.623s
ok  github.com/tlalocweb/hulation/pkg/store/storage/raft  30.994s
ok  github.com/tlalocweb/hulation/pkg/store/clickhouse    0.009s
ok  github.com/tlalocweb/hulation/pkg/api/v1/chat         0.107s
ok  github.com/tlalocweb/hulation/handler                 (Stage 1)
```

Raft package suite: 17 contract cases + 4 bootstrap cases + 2
Watch cases + 1 snapshot/restore round-trip + 1 layout check.
Total raft package wallclock ~31s (single-node Raft has a
~1-2s leadership-acquisition warm-up per test).

E2e suites 32–37 (Phase 0/3/4/5 baselines), 38 (Stage 1
storage seam), and 39 (this stage) green pending CI run.

---

## What's NOT in this phase (per HA_PLAN2.md §1.3)

- Multi-node membership — peers parse but don't join. Stage 3.
- Network transport for remote peers — TCP transport binds to
  loopback. Stage 3 swaps to a routable address.
- Internal mTLS gRPC for analytics relay — Stage 4.
- Site build propagation across the team — Stage 5.
- Visitor cookie CRDT — Stage 6.

---

## Acceptance criteria (HA_PLAN2.md §10)

- [x] `pkg/store/storage/raft` package compiles + unit-tests green.
- [x] `hashicorp/raft` + `raft-boltdb` are in `go.mod`.
- [x] Stage-1 contract tests pass against `RaftStorage` via
      `storagetest.Run`.
- [x] Boot with no `team:` config auto-bootstraps a solo
      cluster within 30s on a fresh data dir.
- [x] Boot on an existing data dir picks up state without
      re-bootstrapping.
- [x] E2e suite 39 written and committed (CI run pending the
      stage-2 deploy of the harness).
- [x] Existing e2e suite 38 (Stage 1) updated for the new
      "Raft storage online" boot log + new on-disk layout.
- [x] `HA_PHASE2_STATUS.md` written.

---

## Lessons + carry-forwards into Stage 3

1. **Single-node bootstrap is ~50ms.** `WaitLeader(ctx, 30s)` is
   defensive — typical solo boots fall well under a second. We
   keep the 30s budget so multi-node bootstrap (which has to
   exchange initial heartbeats) isn't surprised.

2. **`Mutate` is just CAS-with-retry.** The `MaxMutateRetries=50`
   default is the same as izcr. Solo never hits the retry path
   (single proposer); Stage 3 will exercise it under cross-node
   contention. Leave the knob exposed.

3. **Snapshot interval 5m is plenty for solo.** The raft log
   grows ~1 entry per persistent write; a hula instance handling
   admin RPCs at typical rates won't accumulate enough to need
   compaction more often. Multi-node Stage 3 should drop this to
   2m so followers don't fall behind.

4. **`forwardToLeader` is a Stage-3 placeholder.** Solo never
   reaches it — the local node IS the leader. Stage 3 wires it
   to the internal-mTLS gRPC channel.

5. **The bucket router lives in `pkg/store/storage/local`.** The
   raft FSM imports `local.RouteKey` / `local.Buckets` /
   `local.IsKnownBucket`. That's the only cross-package coupling
   between the two backends; if a future backend wants different
   key partitioning it can ignore the helpers.

---

## Branches and merge plan

- `ha/plan-1-storage-seam` (Stage 1) → ready to merge to main
  once review lands.
- `ha/plan-2-raft-solo` (Stage 2, this phase) → branched off
  Stage 1. Will be rebased onto main after Stage 1 merges.
- `ha/plan-3-multi-node` (Stage 3) → not yet started; per
  HA_PLAN3.md preamble, revisit after Stages 1+2 are merged
  before committing to the planned design.
