# Multi-node team e2e — design

HA Stage 3 ships multi-node Team membership (HA_PLAN3.md). The
existing single-hula compose stack can't exercise it. This dir
holds the overlay + helpers + suites that bring up a 3-node team
on the local Docker host and exercise the cross-node code paths.

This doc is the design + invariants the suites enforce. It is
authoritative as suites get written; if a suite needs to stretch
the design, update this doc first.

---

## Topology

Three hula nodes + one ClickHouse + one runner.

```
                 ┌──────────────────────────┐
                 │  hula-clickhouse          │
                 │  (single CH, only one     │
                 │   hula node connects)     │
                 └────────▲─────────────────┘
                          │ network
                          │
   ┌──────────────────────┼──────────────────────┐
   │                      │                      │
┌──┴──────────┐    ┌──────┴──────┐    ┌─────────┴───┐
│ hula-east   │    │ hula-west   │    │ hula-emea   │
│ CH-connected│    │ no CH       │    │ no CH       │
│ seed node   │    │ joins east  │    │ joins east  │
│ "primary"   │    │             │    │             │
└──────▲──────┘    └─────────────┘    └─────────────┘
       │
       └── e2e-runner (curl + grpcurl + hulactl)
```

- **`hula-east`** is the seed and the only CH-connected node. Per
  HA_PLAN3 §6, the leader-priority loop will pin leadership here.
- **`hula-west`** and **`hula-emea`** join via `team-join`. They
  accept visitor traffic at `/v/*`; analytics events queue locally
  and drain to `hula-east` over the internal mTLS gRPC channel.
- **`team-runner`** is an alpine container with `hulactl`,
  `grpcurl`, `curl`, `openssl`, and `tc`/`iproute2`. It runs the
  ceremony (genteamcerts), distributes per-node bundles via
  bind-mount, and drives the actual test assertions.

## Network / DNS

Compose uses a dedicated bridge network for the team overlay so
container names resolve via Docker's embedded DNS. Inside the
overlay:

- `hula-east.team.internal`  → hula-east container's IP
- `hula-west.team.internal`  → hula-west container's IP
- `hula-emea.team.internal`  → hula-emea container's IP

`*.team.internal` aliases come from `extra_hosts:` entries on each
container so the SNI gate on the unified listener (HA_PLAN3 §4.1)
fires correctly. The runner's `/etc/hosts` is patched at startup
the same way the existing compose does for `hula.test.local`.

Public-traffic SNIs (`www.example.com`, `site.test.local`) keep
their existing TLS cert path; only `*.team.internal` requires the
mTLS client cert.

## Ports

Each hula listens on `:443` inside its container. The host maps
`8443/8444/8445` → east/west/emea respectively so test scripts
running on the host can talk to specific nodes when needed. Inter-
container traffic uses the in-network port (443).

Visitor traffic in suites is directed to the host port. The
runner uses `/etc/hosts` entries to resolve the team-internal
names from inside the runner container.

## PKI bundle

`team-runner` generates the bundle once at suite-startup:

```
hulactl genteamcerts \
  --team-id $TEST_TEAM_ID \
  --nodes hula-east,hula-west,hula-emea \
  --validity 24h \
  --out /tmp/team-bundles
```

Per-node directories are bind-mounted into each hula's
`/var/hula/team-pki/`. The bundle's `team-id` and
`bootstrap-token` files are read by suite scripts.

The CA private key (`ca.key`) is intentionally NOT mounted into
any hula container — only the runner has it. Production matches
this: ca.key never deploys.

## Boot order

1. `hula-clickhouse` starts.
2. `team-runner` starts; runs `genteamcerts` and dumps bundles
   into a shared volume.
3. `hula-east` starts (`bootstrap: first-of-team`). `team-runner`
   polls `/readyz` until healthy.
4. `hula-west` and `hula-emea` start (`bootstrap: false`,
   `peers: [hula-east]`). Each runs `team-join` against
   `hula-east` from inside its own container at boot, supplying
   the bootstrap_token from an env var.
5. `team-runner` polls `team-status` against any node until all
   three voters appear with the expected leader.

`team-join` from inside the joining node is a chicken-and-egg case
— the joiner has hula running but isn't yet in the cluster. We
handle this with a small entrypoint wrapper that runs hula in the
background, waits for its local Raft to be in the "follower-without-
config" state, then runs `hulactl team-join` once before the
cluster operations should succeed. (The wrapper is an artifact of
the e2e harness; production operators run `team-join` manually.)

## Suites

| Suite | Topic | Asserts |
|---|---|---|
| `41-team-formation.sh` | bring-up + propagation | 3-node cluster forms within 30s; admin write on `hula-west` is visible from `hula-east` and `hula-emea` within 2s; visitor event POST against `hula-west` lands in CH (relay drain ≤ 5s); GDPR forget tombstone propagates via gossip within one anti-entropy tick. |
| `42-leader-failover.sh` | failover under packet loss | Same setup wrapped in `tc netem` (5% loss, 150ms RTT between containers); `docker stop hula-east` (the leader); within 15s a new leader is elected; admin write against the new leader succeeds; visitor traffic to `hula-west`/`hula-emea` keeps flowing (events queue to outbox); restart `hula-east`; priority loop transfers leadership back; outbox drains. |
| `43-relay-overflow.sh` | analytics relay backpressure | Stop `hula-east` (CH node); pump 5000 visitor events at `hula-west`; verify outbox FIFO eviction kicks in past the cap; warn log appears; restart `hula-east`; remaining events drain. |
| `44-gossip-merge.sh` | CRDT merge under partition | Use `tc` to partition `hula-emea` from the others for 30s; write the same visitor-id on both sides with different last_seen; on heal, verify HLC LWW resolves to the higher timestamp on every node. |
| `45-bad-actor-converge.sh` | bad-actor flag convergence | Trip the badactor scoring on `hula-west`; verify `hula-east` and `hula-emea` block the same IP within an anti-entropy tick. Then `hulactl unblock-actor` on the leader; verify all three nodes clear the block. |
| `46-cookieless-cross-node.sh` | cookieless visitor across nodes | Post the same (UA, IP) combination to two different nodes; verify the deterministic visitor_id matches (same Team CA salt, same day). |

41 + 42 are the sign-off gates for HA Stage 3. 43–46 are the
sub-stage-specific suites that each later sub-stage's PR adds.

## Why an overlay, not a profile

Compose profiles share the project's network. We need a separate
network with `team.internal` SNIs and tighter `extra_hosts` so the
internal-mTLS gate fires correctly. An overlay file
(`docker-compose.team.yaml`) lets us run:

```
docker compose -f docker-compose.yaml -f team/docker-compose.team.yaml up
```

…or, more typically, the suite runner brings up the team overlay
on its own dedicated project name so it doesn't fight with the
running single-hula stack:

```
docker compose -p hulateam -f team/docker-compose.team.yaml up
```

## Lifecycle expectations

- **Per-suite vs shared.** Suites 41+42 are heavyweight (3 hula
  containers + bring-up + tear-down). They run only in `--team`
  mode of the harness and share a single bring-up across both
  suites. Suites 43–46 reuse the same compose project.
- **Determinism.** Cluster bootstrap timing varies; suite scripts
  poll with bounded retries (`hulactl team-status` until 3 voters
  present, capped at 60s) instead of fixed sleeps.
- **Cleanup.** Even on suite failure, the harness must bring the
  team stack down. Use a trap in the suite runner.

## Open implementation choices for sub-stages

These get made as the corresponding sub-stages land:

- **3.5 forwardToLeader** — does the suite verify by writing to a
  follower and reading from the leader, or by running `team-status`
  from any node? Probably both.
- **3.6 outbox** — what's the cap for the 43 suite? Default
  4096-events / 256 MB is too big for a CI test; the suite should
  set a small cap (e.g. 32 events) via env var so the eviction
  fires quickly.
- **3.7 gossip** — anti-entropy tick is configurable. Suite 44
  sets it to 1s so the partition-heal merge is observable in test
  time.
- **3.8 leader-priority + /readyz** — suite 41 polls `/readyz`
  during catch-up to assert it returns 503 then 200.
