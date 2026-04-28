# HA Plan 1 — Storage Interface Seam

The unglamorous foundation. We introduce a `Storage` interface and
move every persistence callsite onto it. **No Raft yet.** The whole
point of this stage is to land a clean seam so Stage 2 can drop a
Raft-backed implementation in behind it without touching another
file.

Greenfield codebase: no migration, no backward-compat shims, no
parity-against-old-impl tests. Refactor is direct — every Bolt
callsite gets rewritten to call `Storage` instead.

Related docs: `HA_OUTLINE.md`,
`../izcr/pkg/store/common/types.go` (the prior-art interface this
follows).

---

## 1. Context and scope

### 1.1 What stage 1 ships

- A new package `pkg/store/storage` with a generic
  key/value/bucket interface modeled on
  `../izcr/pkg/store/common/types.go::Storage`.
- A `LocalStorage` implementation in `pkg/store/storage/local`
  that wraps a `bbolt.DB` handle. This is the **test default**
  and the basis for offline CLI tools that need direct file
  access; production will swap to `RaftStorage` in Stage 2.
- Refactored accessors in `pkg/store/bolt/*.go` that take a
  `storage.Storage` argument. The old singleton-style
  functions (`hulabolt.PutConsent`, `hulabolt.GetCookielessSalt`,
  etc.) are rewritten to call into the new interface; the
  implementation moves but the callers don't.
- Boot wiring in `server/run_unified.go` that constructs a
  `LocalStorage` and installs it via `storage.SetGlobal(s)`.
  Stage 2 swaps this single line to construct `RaftStorage`
  instead.
- One e2e suite (`38-storage-roundtrip.sh`) that drives admin
  RPCs end-to-end through the new interface.

### 1.2 What stage 1 does NOT ship

- Raft consensus (Stage 2).
- Multi-node anything (Stage 3).
- New on-disk schema or new buckets.
- Behaviour changes for any caller (caller-visible API is
  unchanged; the internals shift to Storage).

---

## 2. The Storage interface

Designed to be a strict superset of what izcr exposes so Stage 2
can drop in `RaftStorage` from izcr's playbook with minimal
adaptation. Names align with izcr where reasonable.

```go
// pkg/store/storage/storage.go

package storage

import (
    "context"
    "errors"
    "time"
)

var (
    // ErrNotFound — Get / GetObject returns this for missing keys.
    ErrNotFound = errors.New("storage: key not found")
    // ErrCASFailed — CompareAndSwap / CompareAndCreate when the
    // precondition isn't met.
    ErrCASFailed = errors.New("storage: compare-and-swap failed")
    // ErrSkip — a MutateFn returns this to indicate "don't write".
    ErrSkip = errors.New("storage: mutate skipped")
)

// Storage is the unified persistence contract. Implementations:
//
//   - LocalStorage    — bbolt direct. Test default + offline CLI.
//   - RaftStorage     — Stage 2. hashicorp/raft + raft-boltdb.
//                       Production default.
//
// Keys are slash-delimited strings ("server_access/<userID>|<serverID>").
// Buckets are inferred from the first segment by the implementation;
// callers don't need to manage buckets directly.
type Storage interface {
    // Get returns the value for the key. Returns ErrNotFound if
    // the key is absent.
    Get(ctx context.Context, key string) ([]byte, error)

    // Put writes the key. Overwrites any existing value.
    Put(ctx context.Context, key string, value []byte) error

    // Delete removes the key. Idempotent — no error if absent.
    Delete(ctx context.Context, key string) error

    // List returns every (key, value) pair under the prefix.
    // Bounded — operators set a max via config; default 10000 entries.
    List(ctx context.Context, prefix string) (map[string][]byte, error)

    // Keys returns just the keys under the prefix. Cheaper than List
    // when callers only need the index.
    Keys(ctx context.Context, prefix string) ([]string, error)

    // CompareAndSwap atomically replaces value at key from
    // expected to fresh. Returns ErrCASFailed if current value
    // doesn't match expected.
    CompareAndSwap(ctx context.Context, key string, expected, fresh []byte) error

    // CompareAndCreate atomically creates the key. Returns
    // ErrCASFailed if the key already exists.
    CompareAndCreate(ctx context.Context, key string, value []byte) error

    // Mutate runs the user-supplied mutator inside a single
    // transaction. The mutator receives the current value (or nil
    // if absent) and returns either (newValue, nil) to write,
    // (nil, ErrSkip) to leave unchanged, or (nil, err) to abort.
    Mutate(ctx context.Context, key string, fn MutateFn) error

    // Batch runs multiple Put/Delete operations atomically in one
    // transaction. Stage 2's Raft impl coalesces these into a
    // single Raft Apply.
    Batch(ctx context.Context, ops []BatchOp) error

    // Watch streams every change matching the prefix. Buffered
    // channel of size 64; if a consumer is slow it'll miss
    // intermediate states (Watch is for change notification, not
    // a durable log). Cancel via the context.
    Watch(ctx context.Context, prefix string) (<-chan Event, error)

    // Close releases resources. Safe to call multiple times.
    Close() error
}

// MutateFn is the signature for Storage.Mutate.
type MutateFn func(current []byte) (next []byte, err error)

// BatchOp is one operation inside a Storage.Batch transaction.
type BatchOp struct {
    Op    OpKind  // OpPut or OpDelete
    Key   string
    Value []byte // ignored for OpDelete
}

type OpKind int
const (
    OpPut OpKind = iota
    OpDelete
)

// Event is the payload streamed via Watch.
type Event struct {
    Key       string
    Value     []byte
    Op        OpKind
    Timestamp time.Time
}
```

Notes:

- **No bucket parameter on the interface.** Buckets are an
  implementation detail. `LocalStorage` extracts the bucket from
  the key prefix (everything before the first `/`); `RaftStorage`
  has a single FSM map, no buckets at all.
- **Context everywhere.** Stage 2 uses this for Raft Apply
  timeouts. Stage 1 ignores it.
- **`Watch` returns a channel.** Stage 2's implementation hooks
  into raft FSM Apply. Stage 1's implementation polls (acceptable
  because solo-mode test fixtures are the only callers).
- **No `Reset()` / no separate `Begin()`/`Commit()`.** Batch is
  the only multi-op primitive.

### 2.1 Why not adopt izcr's interface verbatim?

izcr's `Storage` carries a lot of cruft from its full lifecycle —
`MutateObjectFromIndex`, `GetObjectsByOtherIndex`, `SerialAdapter`
plumbing. Hula doesn't need that. Our interface is the core
key/value subset (~10 methods) that maps cleanly onto a Raft
FSM in Stage 2.

We did keep izcr's naming where it overlaps (`CompareAndSwap`,
`CompareAndCreate`, `MutateFn`, `BatchOp`) so anyone reading both
codebases sees the family resemblance.

---

## 3. LocalStorage implementation

```
pkg/store/storage/local/
  local.go         // LocalStorage struct + ctor
  local_test.go    // Storage interface contract tests
  bucket_router.go // first-segment-of-key → bucket name mapping
```

### 3.1 Bucket routing

The `Storage` interface doesn't mention buckets. `LocalStorage`
maps the **first slash-delimited segment** of every key to a
bbolt bucket name:

| First segment | Bucket |
|---|---|
| `server_access` | `server_access` |
| `goals` | `goals` |
| `reports` | `reports` |
| `report_runs` | `report_runs` |
| `alerts` | `alerts` |
| `alert_events` | `alert_events` |
| `audit_forget` | `audit_forget` |
| `mobile_devices` | `mobile_devices` |
| `notification_sends` | `notification_sends` |
| `notification_prefs` | `notification_prefs` |
| `opaque_records` | `opaque_records` |
| `chat_acl` | `chat_acl` |
| `consent_log` | `consent_log` |
| `cookieless_salts` | `cookieless_salts` |

The router is a small map in `bucket_router.go`. Anything that
doesn't match the table goes to a fallback bucket
`storage_unrouted` and is logged at warn level — useful so a
typo doesn't silently scatter keys but doesn't fail-stop boot
either.

### 3.2 Key format

Keys are `<bucket>/<id>`. The `<id>` portion can contain `|`
separators (existing convention for compound keys, e.g.,
`audit_forget/<visitorID>|<RFC3339Nano>`). The Storage
implementation only splits on the first `/` — anything after it
is opaque.

---

## 4. Refactor strategy

Greenfield: rewrite every accessor. No deprecated shims, no
"keep old function signatures alongside new". The package-level
singletons (`hulabolt.PutConsent`, etc.) are rewritten to take a
`storage.Storage` parameter and every callsite is updated in the
same PR as the function it calls.

### 4.1 Two patterns

**Stateful accessors** (most of them) — the function takes
`storage.Storage` directly:

```go
// pkg/store/bolt/consent.go (new):
func PutConsent(ctx context.Context, s storage.Storage, c StoredConsent) error {
    if c.At.IsZero() {
        c.At = time.Now().UTC()
    }
    data, err := json.Marshal(&c)
    if err != nil {
        return err
    }
    key := fmt.Sprintf("consent_log/%s|%s|%s",
        c.ServerID, c.VisitorID, c.At.UTC().Format(time.RFC3339Nano))
    return s.Put(ctx, key, data)
}
```

Every callsite (`recordConsent` in `handler/consent.go` etc.)
adds the `ctx` + `storage.Global()` arguments.

**Stateful constructors** (a handful) — for the few subsystems
that hold a long-lived store handle (e.g., the JWT factory in
`pkg/server/authware`), they accept `storage.Storage` at
construction and stash it.

### 4.2 Storage handle wiring

`pkg/store/storage/global.go`:

```go
package storage

import "sync"

var (
    globalMu sync.RWMutex
    global   Storage
)

func SetGlobal(s Storage) {
    globalMu.Lock()
    global = s
    globalMu.Unlock()
}

func Global() Storage {
    globalMu.RLock()
    defer globalMu.RUnlock()
    return global
}
```

`server/run_unified.go` boot sequence (Stage 1 form):

```go
db, err := bbolt.Open(filepath.Join(conf.DataDir, "data.db"), 0o600, ...)
if err != nil { return err }
s := localstorage.NewFromBolt(db)
storage.SetGlobal(s)
```

Stage 2 will replace those three lines with the RaftStorage
constructor. Every callsite using `storage.Global()` keeps
working.

### 4.3 The `pkg/store/bolt` package after this stage

The package keeps existing it as a **schema + key layout** owner
— `StoredConsent` types, `BucketConsentLog` constants, the
key-formatting helpers. It no longer owns:

- The `bolt.DB` singleton (`Open` / `Get` / `Close` are removed
  or moved to `pkg/store/storage/local`).
- Direct `db.View` / `db.Update` calls (everything goes through
  `Storage`).

The package becomes thin: types, constants, key formatters, and
the accessor functions that each take a `Storage` argument.

---

## 5. Stage breakdown

### 5.1 Sub-stages

**Stage 1.1 — Interface + LocalStorage skeleton (1d)**
- `pkg/store/storage/storage.go` + `storage_test.go` (interface
  conformance unit tests against an in-memory mock).
- `pkg/store/storage/local/local.go` + `local_test.go`
  (round-trip + every method tested against a temp bbolt file).
- `pkg/store/storage/global.go`.
- `pkg/store/storage/local/bucket_router.go` with the 14-entry
  table.

**Stage 1.2 — Refactor accessors (2d)**
- One file per accessor type. Order:
  1. `consent.go` — newest, smallest surface area, easy first
     win.
  2. `cookieless.go` — 4 functions.
  3. `alerts.go` — biggest single file; covers alerts,
     alert_events, audit_forget.
  4. `mobile.go`, `chat.go`, `opaque.go` — each ~6 functions.
  5. `reports.go`, `goals.go`, `server_access.go` —
     straightforward.
- Each file: rewrite accessors to take `storage.Storage`,
  update every callsite in the same commit, run unit tests +
  e2e suites green.

**Stage 1.3 — Boot wiring + e2e (0.5d)**
- `server/run_unified.go` initialises `LocalStorage` at boot.
- `test/e2e/suites/38-storage-roundtrip.sh` — drives admin
  RPCs end-to-end and verifies the underlying bbolt has the
  expected keys.

Total: ~3.5 working days = **~0.75 calendar week**.

---

## 6. Test plan

### 6.1 Unit tests

- `pkg/store/storage/storage_test.go` — interface contract tests
  against an in-memory `Storage` mock: Get-after-Put, Delete
  idempotent, CAS race, Mutate skip, Batch atomicity, Watch
  fires on changes within prefix and doesn't fire outside,
  context cancel unsubscribes.
- `pkg/store/storage/local/local_test.go` — same contract tests
  against the bbolt-backed implementation.
- Each refactored accessor file in `pkg/store/bolt/` keeps its
  existing unit tests; tests are updated to construct a
  `storage.Storage` (use a temp `LocalStorage`) and pass it in.

### 6.2 Existing unit + e2e tests stay green

The behaviour is identical post-refactor. Every existing test
suite (32–37, plus all unit tests) must pass without change to
the test logic, only to test setup (passing `storage.Storage`
in).

### 6.3 New e2e suite

`test/e2e/suites/38-storage-roundtrip.sh` — drives:

1. Create a goal via admin RPC, list it, delete it.
2. Create a chat ACL entry via admin RPC, look it up.
3. Issue an OPAQUE registration, verify the record exists.
4. Verify the underlying `data.db` has keys under the expected
   buckets (using `bolt-cli` inside the test runner).

The suite is a smoke test of the seam — if it passes, every
admin-RPC path is correctly routing through `Storage`.

---

## 7. File change summary

### 7.1 New files

- `pkg/store/storage/storage.go`
- `pkg/store/storage/storage_test.go`
- `pkg/store/storage/global.go`
- `pkg/store/storage/local/local.go`
- `pkg/store/storage/local/local_test.go`
- `pkg/store/storage/local/bucket_router.go`
- `test/e2e/suites/38-storage-roundtrip.sh`

### 7.2 Edited files

- `pkg/store/bolt/consent.go` — rewrite accessors to take Storage
- `pkg/store/bolt/cookieless.go` — same
- `pkg/store/bolt/alerts.go` — same
- `pkg/store/bolt/mobile.go` — same
- `pkg/store/bolt/chat.go` — same
- `pkg/store/bolt/opaque.go` — same
- `pkg/store/bolt/{reports,goals,server_access}.go` — same
- `pkg/store/bolt/bolt.go` — slim down or remove `Open`/`Get`/`Close`
- `server/run_unified.go` — initialise LocalStorage
- Every callsite of the refactored accessors in `pkg/auth/`,
  `pkg/chat/`, `handler/`, `pkg/api/v1/*`, `server/`,
  `model/tools/hulactl/` — update to pass Storage.

### 7.3 No-touch (mostly)

- `model/`, `pkg/apispec/v1/`, `web/analytics/` — these don't
  reach into Bolt; no changes.
- `pkg/store/clickhouse/` — separate store, no changes.

---

## 8. Acceptance criteria

Stage 1 is done when:

- [ ] `pkg/store/storage` package exists with `Storage` interface
      + `LocalStorage` implementation.
- [ ] All 14 Bolt buckets have accessors in `pkg/store/bolt`
      that take `storage.Storage` arguments.
- [ ] Every callsite in the codebase passes a `Storage` instance
      explicitly (or uses `storage.Global()`).
- [ ] Every existing unit test passes.
- [ ] Every existing e2e suite passes.
- [ ] `38-storage-roundtrip.sh` is green.
- [ ] `server/run_unified.go` initialises `Storage` at boot.
- [ ] `git grep 'bbolt\\.Open\\|tx\\.Bucket'` outside
      `pkg/store/storage/local/` returns zero results
      (everything must go through the seam — except the
      LocalStorage impl itself + offline CLI tools that
      explicitly need direct file access).

---

## 9. What Stage 2 needs from Stage 1

Stage 2 plugs in `pkg/store/storage/raft/RaftStorage`. The seam
this stage provides means Stage 2 only has to:

1. Implement the `Storage` interface backed by hashicorp/raft +
   raft-boltdb (closely modelled on
   `../izcr/pkg/store/raft/{driver,boltbackend,raftnode}.go`).
2. Wire it in via `storage.SetGlobal(rs)` in
   `server/run_unified.go` — replacing the LocalStorage
   construction with RaftStorage construction.

If Stage 1 lands cleanly, Stage 2 is mechanical. If Stage 1 has
a leaky abstraction (some caller reaching past `Storage` into
the underlying bbolt), Stage 2 will surface it and we'll fix
the leak there.
