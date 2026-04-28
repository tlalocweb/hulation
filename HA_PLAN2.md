# HA Plan 2 — Single-node Raft Default

Stage 1 put the seam in. Stage 2 plugs `RaftStorage` into it.

By the end of this stage, every hula deployment runs a single-node
Raft cluster by default. The single-node "solo" mode is the most
common deployment shape and incurs near-zero overhead (one disk
write per operation, the same as a direct bbolt write). Multi-node
arrives in Stage 3; everything from `team_id` to the bootstrap
flow is forward-compatible from day one.

Closely modelled on `../izcr/pkg/store/raft/{driver,boltbackend,raftnode}.go`.

Related docs: `HA_OUTLINE.md`, `HA_PLAN1.md`,
`../izcr/pkg/store/raft/example_usage.go`.

---

## 1. Context and scope

### 1.1 What Stage 1 delivered that we reuse

- `pkg/store/storage/Storage` interface — the only API any
  caller uses.
- `pkg/store/storage/local/LocalStorage` — bbolt-direct
  implementation. Stays alive for tests + offline CLI tools.
- `storage.SetGlobal(s)` / `storage.Global()` — boot-time
  installation point.

### 1.2 What Stage 2 ships

- New package `pkg/store/storage/raft` with:
  - `RaftStorage` — implements `Storage`. Wraps a Raft node +
    a "FSM Bolt" (the application-state bbolt that the FSM
    mutates) + an "informer" (Watch fan-out).
  - FSM type with `Apply` / `Snapshot` / `Restore`.
  - Command serialisation: a tiny protobuf format covering
    Put / Delete / CompareAndSwap / CompareAndCreate / Batch /
    Mutate-as-CAS.
- Auto-bootstrap of a single-node cluster when no `team:` block
  is configured. Persistent `team_id` UUID and `node_id` derived
  from hostname.
- Boot wiring change in `server/run_unified.go`: replace the
  `LocalStorage` constructor (Stage 1) with `RaftStorage`.
- One e2e suite: `39-solo-raft.sh`.

### 1.3 What Stage 2 does NOT ship

- Multi-node anything. Bootstrap is single-node only. The
  `team:` block parser silently ignores `peers:` (Stage 3).
- Network transport for Raft (still TCP, but bound to localhost
  in solo mode — no remote peers can reach it).
- Internal mTLS gRPC (Stage 4).
- Site build propagation (Stage 5).
- Visitor cookie CRDT layer (Stage 6).

### 1.4 Why solo-as-Raft instead of LocalStorage in production?

Single-node Raft is **functionally equivalent** to LocalStorage
plus a small constant overhead (one write to a Raft log, one
write to the FSM Bolt; the Raft log can be aggressively
truncated by snapshot). The win: there's exactly one production
code path. Adding a second (or third) node in Stage 3 is purely
additive — no flag flips, no "promote LocalStorage to RaftStorage"
migration, no production code paths that only run in multi-node.
Solo deployments stay simple from the operator's perspective
(no `team:` block needed) while the implementation is the same
machinery that powers a 5-node cross-cloud cluster.

---

## 2. The RaftStorage implementation

### 2.1 Package layout

```
pkg/store/storage/raft/
  raftstorage.go    // RaftStorage struct + ctor + Storage methods
  fsm.go            // FSM (Apply / Snapshot / Restore)
  command.go        // Command type + (de)serialisation
  bootstrap.go      // single-node cluster bootstrap helpers
  informer.go       // Watch fan-out from FSM Apply
  config.go         // RaftConfig — wraps hashicorp/raft tunables
  raftstorage_test.go
  fsm_test.go
  command_test.go
```

### 2.2 Construction

```go
type RaftConfig struct {
    NodeID            string         // unique per node; default = hostname
    DataDir           string         // /var/hula/data/raft
    BindAddr          string         // 127.0.0.1:0 in solo; non-loopback in Stage 3
    SnapshotInterval  time.Duration  // default 5m in solo, 2m multi-node
    SnapshotThreshold uint64         // default 8192 log entries
    BarrierOnApply    bool           // default true
    Bootstrap         bool           // true on first run only
    Peers             []Peer         // empty in Stage 2; Stage 3 fills this in
    Logger            hclog.Logger
}

func NewRaftStorage(cfg RaftConfig) (*RaftStorage, error)
```

The constructor:

1. Opens (or creates) `<DataDir>/data.db` for the FSM's
   application state.
2. Opens (or creates) `<DataDir>/raft-log.db` and
   `<DataDir>/raft-stable.db` via `raft-boltdb`.
3. Creates a `raft.FileSnapshotStore` at `<DataDir>/snapshots/`.
4. Creates a TCP transport at `cfg.BindAddr`. In solo mode the
   address is `127.0.0.1:0` and we record the resolved address
   for `team-status` introspection but never advertise it.
5. Constructs the `*raft.Raft` instance with the FSM, log store,
   stable store, snapshot store, and transport.
6. If `cfg.Bootstrap == true` AND the cluster has no committed
   configuration yet, calls `r.BootstrapCluster(...)` with a
   1-server config (just this node).
7. Returns a `*RaftStorage` that holds (raft, fsmDB, informer,
   logger, mu).

### 2.3 The FSM

```go
type FSM struct {
    db       *bbolt.DB
    informer *Informer
    logger   *log.TaggedLogger
}

func (f *FSM) Apply(l *raft.Log) interface{} {
    cmd, err := decodeCommand(l.Data)
    if err != nil {
        return &ApplyResult{Err: fmt.Errorf("decode: %w", err)}
    }
    switch cmd.Op {
    case OpPut:           return f.applyPut(cmd)
    case OpDelete:        return f.applyDelete(cmd)
    case OpCAS:           return f.applyCAS(cmd)
    case OpCASCreate:     return f.applyCASCreate(cmd)
    case OpBatch:         return f.applyBatch(cmd)
    }
    return &ApplyResult{Err: fmt.Errorf("unknown op: %d", cmd.Op)}
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
    return newBoltSnapshot(f.db), nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
    // Replace the FSM's Bolt with the snapshot's contents atomically.
}
```

`applyPut` writes via `db.Update(...)` then publishes to
`f.informer` so any active `Watch` channels see the change.

`applyCAS` reads the current value, compares to expected, writes
fresh; returns `ErrCASFailed` (encoded into `ApplyResult.Err`)
if the precondition fails. Crucially this happens **after**
Raft has committed the log entry — every node's FSM evaluates
the CAS against its own copy and produces the same result
because the log is deterministic.

`applyBatch` runs the entire batch in one `db.Update` so it's
atomic locally too.

### 2.4 Command format

```go
type Op byte

const (
    OpPut       Op = 1
    OpDelete    Op = 2
    OpCAS       Op = 3
    OpCASCreate Op = 4
    OpBatch     Op = 5
)

type Command struct {
    Op         Op
    Key        string
    Value      []byte
    Expected   []byte         // for CAS
    Batch      []BatchEntry  // for OpBatch
    Origin     string         // node_id that proposed (debug)
    ProposedAt time.Time
}

type BatchEntry struct {
    Op    Op       // OpPut or OpDelete
    Key   string
    Value []byte
}

// Serialise via protobuf (so we can extend without breaking
// snapshot compatibility). We declare the message in
// pkg/apispec/v1/raftcmd/cmd.proto and generate cmd.pb.go
// alongside the rest of the protos.
```

We use protobuf rather than gob because:

1. Forward-compat: adding a field is non-breaking.
2. Snapshot interop: a snapshot from `v0.2.0` should restore on
   `v0.3.0`. Gob's reflection-based encoding makes that fragile.
3. We already have the protoc toolchain.

### 2.5 Mutate — read-modify-write through Raft

`Storage.Mutate(ctx, key, fn)` is the trickiest method. The user
function runs Go code; we can't ship Go code through the Raft log.
Resolution: the leader runs the function locally and submits a
CAS log entry. Followers re-run the CAS deterministically.

```go
func (s *RaftStorage) Mutate(ctx context.Context, key string, fn MutateFn) error {
    if !s.raft.IsLeader() {
        return s.forwardToLeader(ctx, "mutate", key)  // Stage 3 implements
    }
    for retry := 0; retry < s.cfg.MaxMutateRetries; retry++ {
        current, err := s.localGet(key)  // direct FSM read
        if err != nil && !errors.Is(err, ErrNotFound) { return err }
        var currentForFn []byte
        if !errors.Is(err, ErrNotFound) { currentForFn = current }
        next, err := fn(currentForFn)
        if errors.Is(err, ErrSkip) { return nil }
        if err != nil { return err }
        if next == nil {
            // Mutator wants to delete.
            if err := s.applyAsLeader(ctx, &Command{
                Op: OpCAS, Key: key, Expected: current, Value: nil,
            }); err == nil {
                return nil
            } else if !errors.Is(err, ErrCASFailed) {
                return err
            }
            continue  // retry
        }
        if err := s.applyAsLeader(ctx, &Command{
            Op: OpCAS, Key: key, Expected: current, Value: next,
        }); err == nil {
            return nil
        } else if !errors.Is(err, ErrCASFailed) {
            return err
        }
        // CAS failed → someone else wrote between our read and
        // our propose. Retry.
    }
    return fmt.Errorf("mutate %q: too many retries", key)
}
```

For Stage 2 (solo) `forwardToLeader` is unreachable — there's
only one node and it's always the leader. Stage 3 implements
it. We define the method signature now so the call site
doesn't change.

### 2.6 Watch — informer fan-out

The FSM's `Apply` function publishes every change to an
`Informer` that maintains per-prefix subscriber maps. When a
caller invokes `Watch(prefix)`, the informer adds a buffered
channel to the per-prefix map and returns it.

```go
type Informer struct {
    mu      sync.RWMutex
    subs    map[string][]chan<- Event
}

func (i *Informer) Publish(key string, value []byte, op OpKind) {
    i.mu.RLock()
    defer i.mu.RUnlock()
    for prefix, chans := range i.subs {
        if !strings.HasPrefix(key, prefix) { continue }
        ev := Event{Key: key, Value: value, Op: op, Timestamp: time.Now()}
        for _, ch := range chans {
            select {
            case ch <- ev:
            default:
                // Slow consumer — drop. Watch is for change
                // notification, not a durable log; consumers
                // that miss intermediate states should re-list.
            }
        }
    }
}
```

In solo mode this is essentially a same-process message bus.
In multi-node mode (Stage 3) every node's FSM publishes
independently, so a Watch on node B sees B's local Apply, not
A's. Callers that need globally-ordered events should subscribe
on the leader (Stage 3 surfaces a `WatchOnLeader` helper).

---

## 3. Bootstrap modes

### 3.1 Solo (Stage 2 default)

Triggered when `team:` is missing from `hula-config.yaml`. **This
is the most common deployment shape** — operators running a
single hula instance see no configuration burden.

```yaml
# Implicit:
# team:
#   bootstrap: solo
#   data_dir: /var/hula/data/raft
```

Boot flow:

1. `bootstrap.go::AutoSoloConfig()`:
   - Compute `team_id`: read `<DataDir>/team-id` if present,
     otherwise generate UUID v7 and persist.
   - Compute `node_id`: read `<DataDir>/node-id` if present,
     otherwise derive from `os.Hostname()` and persist.
   - Compute `bind_addr`: `127.0.0.1:0` (loopback only).
2. Construct `RaftStorage` with `Bootstrap: true`.
3. The constructor checks `r.HasExistingState()` (via
   `raft.HasExistingState`); if false, calls
   `BootstrapCluster` with a 1-server config; if true, skips
   bootstrap (we're rejoining our own state).
4. Wait for leadership: poll `r.State() == Leader` for up to
   30s. Solo bootstrap is fast (~50ms typical).
5. `storage.SetGlobal(rs)` and proceed.

### 3.2 Configured solo (explicit)

Same as auto-solo but the operator pinned values:

```yaml
team:
  team_id: 4f1a3c2d-7b6e-4f9a-9c12-7a3e1b0f2c89
  node_id: solo-node
  data_dir: /var/hula/data/raft
  bootstrap: solo
```

Useful when running multiple solo clusters on the same host
(testing) — different `data_dir`s mean different clusters.

### 3.3 Configured multi-node (Stage 3 — placeholder)

```yaml
team:
  team_id: 4f1a3c2d-...
  node_id: node-east
  data_dir: /var/hula/data/raft
  bootstrap: false   # set true on the SEED node only, first run only
  peers:
    - { id: node-east, raft_addr: east.internal:8300 }
    - { id: node-west, raft_addr: west.internal:8300 }
```

Stage 2 parses but ignores `peers:` (warns that multi-node is
not supported yet). Stage 3 turns it on.

---

## 4. The `team:` config block (Stage 2 surface)

```go
// config/team.go

type TeamConfig struct {
    // TeamID — UUID v7. Auto-generated on first boot of solo
    // cluster. Operators can pin via config to share state across
    // restarts on different hosts (rare).
    TeamID string `yaml:"team_id,omitempty"`

    // NodeID — unique per node within the Team. Default = hostname.
    NodeID string `yaml:"node_id,omitempty"`

    // DataDir — root for Raft data + FSM Bolt. Default
    // /var/hula/data/raft.
    DataDir string `yaml:"data_dir,omitempty" default:"/var/hula/data/raft"`

    // Bootstrap — explicit override. "solo" / "first-of-team" /
    // "join". Defaults inferred from peers presence.
    Bootstrap string `yaml:"bootstrap,omitempty"`

    // BindAddr — Stage 3 surface; ignored in Stage 2.
    BindAddr string `yaml:"bind_addr,omitempty"`

    // Peers — Stage 3 surface; ignored in Stage 2 with a warning.
    Peers []TeamPeer `yaml:"peers,omitempty"`

    // SnapshotInterval / SnapshotThreshold — hashicorp/raft
    // tunables. Sensible defaults for solo (longer interval since
    // single node = no log sync to other nodes).
    SnapshotInterval  string `yaml:"snapshot_interval,omitempty" default:"5m"`
    SnapshotThreshold uint64 `yaml:"snapshot_threshold,omitempty"`
}

type TeamPeer struct {
    ID       string `yaml:"id"`
    RaftAddr string `yaml:"raft_addr"`
    // InternalAddr is Stage 4 (gRPC mTLS); ignored in Stage 2/3.
    InternalAddr string `yaml:"internal_addr,omitempty"`
}
```

Default config — totally absent `team:` block:

```yaml
# Equivalent to:
team:
  team_id: <generated UUID v7, persisted>
  node_id: <hostname>
  data_dir: /var/hula/data/raft
  bootstrap: solo
  snapshot_interval: 5m
```

---

## 5. Boot wiring

`server/run_unified.go`:

```go
// Stage 1 (current):
db, err := bbolt.Open(filepath.Join(conf.DataDir, "data.db"), 0o600, ...)
if err != nil { return err }
s := localstorage.NewFromBolt(db)
storage.SetGlobal(s)

// Stage 2 (replacement):
teamCfg := conf.Team
if teamCfg == nil { teamCfg = config.DefaultSoloTeam(conf.DataDir) }

raftCfg, err := raftbackend.AutoConfig(*teamCfg)
if err != nil { return fmt.Errorf("team config: %w", err) }

rs, err := raftbackend.NewRaftStorage(raftCfg)
if err != nil { return fmt.Errorf("raft storage: %w", err) }

if err := raftbackend.WaitLeader(rs, 30*time.Second); err != nil {
    return fmt.Errorf("solo bootstrap stalled: %w", err)
}

storage.SetGlobal(rs)
log.Infof("Raft storage online (team=%s, node=%s, mode=solo)",
    raftCfg.TeamID, raftCfg.NodeID)
```

LocalStorage stays available for tests + offline CLI tools but
is no longer the production default.

---

## 6. Stage breakdown

### Sub-stage 2.1 — RaftStorage skeleton + FSM (3d)

- `pkg/store/storage/raft/raftstorage.go` — struct, ctor,
  Storage method shells.
- `pkg/store/storage/raft/fsm.go` — Apply / Snapshot / Restore.
- `pkg/store/storage/raft/command.go` — proto definition + (de)
  serialise.
- `pkg/apispec/v1/raftcmd/cmd.proto` — Command message.
- Unit tests: round-trip every Storage method against a single-
  node `RaftStorage` running in a `t.TempDir()`.

### Sub-stage 2.2 — Bootstrap + auto-solo (1d)

- `pkg/store/storage/raft/bootstrap.go`.
- `config/team.go` — TeamConfig struct.
- `server/run_unified.go` wiring.
- Tests: fresh data dir → bootstrap → leader within 30s.
- Tests: existing data dir → no rebootstrap, picks up state.

### Sub-stage 2.3 — Informer + Watch (1.5d)

- `pkg/store/storage/raft/informer.go`.
- Wire into FSM.Apply → publish.
- Tests: Watch sees Puts that match prefix; doesn't see
  outside-prefix; cancel via context unsubscribes.

### Sub-stage 2.4 — Snapshot + Restore round-trip (1d)

- Implement `BoltSnapshot.Persist` (streams the FSM Bolt out).
- Implement `FSM.Restore` (replaces the FSM Bolt with the
  snapshot). Must be atomic — write to temp then rename.
- Tests: take snapshot mid-traffic, restore on a fresh node,
  verify all keys present.

### Sub-stage 2.5 — E2e + sign-off (0.5d)

- `test/e2e/suites/39-solo-raft.sh` — solo Raft round-trip
  through admin RPCs.
- Update `HA_OUTLINE.md` and write `HA_PHASE2_STATUS.md`.

Total: ~7 working days = **~1.5 calendar weeks**.

---

## 7. Test plan

### 7.1 Unit tests — `pkg/store/storage/raft/`

- **Storage interface contract** (mirrors Stage 1's
  `LocalStorage` tests): Get-after-Put, Delete idempotent,
  CAS race, Mutate skip, Batch atomicity, Watch fires
  correctly. Must pass against `RaftStorage` with the same
  table-driven harness Stage 1 used.
- **FSM determinism**: encode a sequence of commands, apply on
  two FSMs in lockstep, assert byte-identical FSM Bolt
  contents.
- **Snapshot round-trip**: write 1000 keys → snapshot →
  restore on a fresh FSM → all 1000 keys present.
- **Bootstrap idempotency**: call `NewRaftStorage` twice on
  the same data dir, second call should skip
  `BootstrapCluster` and just rejoin.

### 7.2 Existing tests stay green

`RaftStorage` is a drop-in replacement for `LocalStorage`. Every
Stage-1 callsite must work against it without change. The
Stage-1 `Storage` contract tests reuse the same table-driven
harness, swapping out the implementation under test.

### 7.3 E2e

`39-solo-raft.sh`:
1. Start hula with no `team:` config.
2. Verify boot logs show
   `Raft storage online (team=<uuid>, node=<host>, mode=solo)`.
3. Drive admin RPCs end-to-end: create a goal, list it, delete
   it; create a chat ACL entry; issue OPAQUE registration.
4. Restart hula. Verify state persisted across restart (the
   chat ACL entry is still there; the deleted goal is still
   gone).
5. Verify Raft data dir contains `data.db`, `raft-log.db`,
   `raft-stable.db`, `snapshots/`, `team-id`, `node-id`.

---

## 8. New dependencies

```
go.mod additions:
  github.com/hashicorp/raft               v1.7.x  // consensus
  github.com/hashicorp/raft-boltdb/v2     v2.x    // log + stable store
  github.com/hashicorp/go-hclog           v1.x    // raft's logger interface
```

These are widely used, MPL-licensed, and the same set izcr
uses (re-checked against `../izcr/go.mod`). Non-trivial deps
but with strong track records.

---

## 9. What Stage 3 needs from Stage 2

- A `RaftStorage` that already supports a non-loopback
  `BindAddr` (we just don't use it in Stage 2). Stage 3 turns
  it on.
- The `peers:` config field is already parsed (and ignored
  with a warning) — Stage 3 wires it to `r.AddVoter`.
- The `forwardToLeader` method has a placeholder — Stage 3
  implements it.
- The Informer is already in place; Stage 3 lets followers
  subscribe to the leader's stream for cross-node Watch.

---

## 10. Acceptance criteria

Stage 2 is done when:

- [ ] `pkg/store/storage/raft` package compiles + unit-tests
      green.
- [ ] `hashicorp/raft` + `raft-boltdb/v2` are in `go.mod`.
- [ ] Existing Stage-1 unit tests pass against `RaftStorage`
      via the same parity harness.
- [ ] Boot with no `team:` config auto-bootstraps a solo
      cluster within 30s on a fresh data dir.
- [ ] Boot on an existing data dir picks up state without
      re-bootstrapping.
- [ ] E2e suite 39 is green.
- [ ] Existing e2e suites 32–37 + 38 (Stage 1) are green.
- [ ] `HA_PHASE2_STATUS.md` written and committed.

---

## 11. Open questions for Stage 2 review

These are real decisions worth a sanity check before
implementation starts:

1. **Snapshot file format** — `bolt-to-bolt` raw copy vs proto
   stream vs gob? Raw `bbolt.Tx.WriteTo()` is simplest and is
   what izcr uses. Cross-version compatibility comes from a
   snapshot version field that Stage 4 may extend.
2. **What if snapshot threshold is hit DURING bootstrap?** —
   solo node, lots of writes, snapshot fires. Should be safe
   (snapshots block briefly while the FSM is read-only) but
   worth a deliberate test in §7.1.
3. **Single-process bbolt locking** — `data.db`, `raft-log.db`,
   and `raft-stable.db` are all separate bbolt files held by
   the same process; no lock contention between them. But the
   FSM's `data.db` MUST NOT be opened by any other process
   while hula is running. We document this in operator notes
   and add a startup check that flock fails fast if another
   hula is up on the same data dir.
