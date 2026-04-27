# Hulation Tests — Orientation

Two separate test harnesses live here. Both are bash scripts that orchestrate
Docker Compose stacks.

```
test/
├── integration/   # older, narrow harness — visitor APIs + forms + backends
└── e2e/           # broader harness — every hulactl command against real stack
```

## Phase-0 note (current state)

The Phase-0 gRPC migration is landing in stages. Current test status:

- **Both harnesses continue to work** against the unified server's
  legacy `/api/*` bridge. hulactl's existing HTTP paths are served by
  the unified `http.ServeMux` fallback — no path changes required.
- **No new suites added yet.** The plan calls for 8 new e2e suites
  (`13-grpc-smoke`, `14-rest-gateway`, `15-sso-google`, `16-rbac`,
  `17-analytics-foundation`, `18-events-migration`, `19-http2`,
  `20-single-listener`). They land in Stage 0.11 alongside the
  migration of existing suites onto `/api/v1/*`.
- **Expected results unchanged** — the legacy bridge is equivalent to
  the pre-Phase-0 behaviour for the 42 assertions the e2e harness
  makes. Any deviation is a bug to triage.

Once stage 0.8 closes (hulactl switches to `/api/v1/*`), the legacy
bridge can be deleted. The suites will need to be updated at that
point — this is Stage 0.11 work.

## test/e2e/ — End-to-End Harness

**What it proves**: every publicly documented `hulactl` command can successfully
drive a real hula instance (built from source, running in Docker) from
authentication through to the full staging mode lifecycle. In particular:

- `hulactl auth <url>` works non-interactively (via `HULACTL_PASSWORD` env var,
  a feature added specifically for this harness) and persists credentials under
  `servers.<fqdn>` in `hulactl.yaml`
- Git autodeploy production builds complete and serve rendered HTML
- Staging mode long-lived containers come up, respond to `staging-build`, and
  serve live-updated output
- WebDAV file upload via `staging-update` lands files in the source dir
- `staging-mount` does initial sync and detects filesystem changes
- CLI commands that are declared-but-unimplemented fall through to the default
  `Unknown command` case without crashing

**How to run**:

```bash
cd /home/ubuntu/work/hulation
# One-time: copy the GitHub token (or any valid one) into a local .env
cp test/e2e/.env.example test/e2e/.env
# Edit .env and set GITHUB_AUTH_TOKEN (not strictly required since we
# bind-mount the local test site repo — the token just satisfies the
# hula config env-var check)

./test/e2e/run.sh               # full run (builds images, brings stack up,
                                # runs 12 suites, tears down, prints totals)
./test/e2e/run.sh --suite 08    # just suite 08-staging-build
./test/e2e/run.sh --keep        # leave stack up for debugging
./test/e2e/run.sh --no-setup    # skip build+compose-up (needs stack already up)
```

Expected result: **42 passed, 0 failed**. Exit 0 on all-pass, nonzero on any
failure.

**First run is slow** (~5-10 minutes): hula:local, both builder images, and
hulactl all get built via `docker buildx`, Go modules download, etc. Subsequent
runs are faster.

**What the harness depends on**:

- Docker + Docker Compose v2
- Go 1.25 (picks up repo-local `.bin/go/bin/go` if present)
- `jq`, `openssl` on the host
- `mkcert` is **optional** — we fall back to a self-signed openssl cert
  trusted as its own root (no host CA modifications needed)
- A local checkout of `github.com/tlalocweb/tlaloc-hula-test-site` at
  `/home/ubuntu/work/tlaloc-hula-test-site` (or override via `TEST_SITE_SRC`
  in `.env`) — the test site repo is bind-mounted as a `file://` git URL so
  we don't depend on GitHub push access. The harness auto-commits a
  `.hula/sitebuild.yaml` locally if missing (never pushes).

**How it's organized**:

- `run.sh` is the entry point, sources `lib/harness.sh` + `lib/setup.sh` +
  `lib/teardown.sh`, then runs each file in `suites/*.sh` in sort order.
- Each suite is idempotent and self-contained — sets `PASSED`/`FAILED`
  counters via `pass`/`fail` helpers from `harness.sh`.
- The compose stack includes two ephemeral runner containers
  (`hulactl-runner` and `test-runner`) that are launched via
  `docker compose run --rm` per command invocation. Each gets the mkcert CA
  trusted and `/etc/hosts` entries pointing at the hula service.

## test/integration/ — Older, Narrower Harness

`test/integration/run.sh` exercises visitor APIs, forms, and backends with a
synthetic static site. It predates the e2e harness and remains useful for
narrow regression testing. Run with:

```bash
./test/integration/run.sh
```

It has overlapping coverage with e2e in some areas (forms, basic auth) but
doesn't touch site deployment, staging mode, or WebDAV. When in doubt, run
the e2e harness — it's the superset.

## When to reach for each

- **Developing a new hulactl command**: write a suite in `test/e2e/suites/`
  and run `./test/e2e/run.sh --suite NN-mycommand`
- **Debugging a flaky build or staging issue**: `./test/e2e/run.sh --keep`
  then `docker compose -p hula-e2e logs hula` for hula logs, or `docker exec`
  into the container
- **Changing the visitor API / form handling**: `test/integration/run.sh` is
  fine and faster to iterate on
- **CI smoke test**: `./test/e2e/run.sh` covers almost everything

## Gotchas encountered during development (preserved as lore)

1. **hulactl built on the host is glibc-linked, doesn't run in alpine
   containers.** Fix: extract hulactl from `hula:local` image at setup time.
2. **conftagz auto-populates empty `cloudflare_origin_ca` struct on
   `hula_ssl`** even when a static cert is configured, causing false
   validation errors. Fix was `hasStaticCert()` short-circuit in config.go.
3. **Bind-mounting a git repo into a container triggers git's "dubious
   ownership" check** when container-root doesn't own the host files. Fix:
   `git config --global safe.directory '*'` in the hula entrypoint (via
   docker-compose entrypoint override for the e2e stack).
4. **Bad actor detection can block the test-runner mid-run** because all
   containers share an IP on the docker network. The e2e config disables
   detection; suite 06 just smoke-tests the command.
5. **Don't use `timeout` to wrap shell functions** — `timeout` is a binary
   and only resolves real commands, not bash functions.
