# hulaagent end-to-end test

Self-contained e2e for the Rust hula-agent + HLAP wire spec. Brings
up the smallest possible hula+clickhouse stack via docker compose,
provisions an admin and an agent, runs `hula-agent` on the host
against the running hula, and exercises the BUILD verb through to
its terminal envelope.

Distinct from `test/e2e/run.sh` — the main e2e fixture is heavyweight
(multiple sites, full OPAQUE flow, mkdocs / hugo / sitedeploy
fixtures, sometimes 10+ minutes). This script targets only what
hulaagent needs: roughly 30-60 seconds end-to-end on a warm box.

## Run

```
./test/hulaagent-e2e/run.sh
```

Options:
- `--keep` — leave the stack running after the suite passes (useful
  for poking at the agent socket directly).
- `HULA_PORT=<n>` — host-port mapping for hula (default 14443; pick
  a different value if 14443 collides with the main e2e fixture).

The first run builds `hula:local` via `./build-docker.sh --local`;
subsequent runs reuse the image. Cargo builds `hula-agent` in release
mode every invocation (incremental compile, ~3-5 s when nothing
changed).

## What it asserts

Against the BUILD verb wired through HLAP:

1. **Banner** is emitted on connect with `hlap:1`.
2. **Initial OK envelope** carries `streaming:true` and a real
   `build_id`.
3. **Terminal envelope** has `done:true`.
4. **Terminal status** is `failed` (the fixture configures the
   testsite with a bogus repo URL so git clone fails fast — gives
   the streaming pipeline real log envelopes without needing a
   working builder image). A `complete` status here means someone
   plugged a real repo into the fixture; the suite flags that
   loudly rather than silently passing.
5. **At least one log envelope** appears between the initial OK
   and the terminal envelope.
6. **Permission denial** — BUILD against a site the agent isn't
   allowed for (`othersite`, configured but not in the agent's
   `allow.build`) returns `err:"forbidden"` with the stream id
   echoed.
7. **Missing required field** — BUILD without `site` returns
   `err:"missing_field"`.

## What it does NOT cover

- Real builder image (mkdocs / hugo / etc.) — that needs the main
  e2e fixture's full site setup.
- Multiplexed in-flight verbs — Phase 5 protocol feature; not in
  the agent yet.
- Cancel verb on a second control connection — Phase 5.
- Verbs beyond BUILD — Phase 5.
- list-agents / revoke-agent / cert-expiry — Phase 6.

When Phase 5 or 6 lands, this suite gains new assertion blocks; the
overall harness shape (compose-up, agent provision, host hula-agent,
probe) shouldn't need changes.

## Generated state

`run.sh` writes to `test/hulaagent-e2e/workdir/`:
- `certs/{cert,key,rootCA}.pem` — self-signed cert covering all
  vhosts in the config
- `hulactl` — extracted from `hula:local` (alpine-musl binary for
  the runner container; not for host use)
- `hula-config.yaml` — rendered from the .tmpl
- `agent.yaml` — what `hulactl create-agent` produced
- `hlap.sock` — the agent's HLAP socket (deleted on cleanup)
- `case-*.out` — captured probe output for each assertion block
- `hula-agent.log` — agent stdout+stderr during the suite

Everything under `workdir/` is regenerated on each run; the dir is
fine to delete between runs.
