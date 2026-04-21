# Hula End-to-End Test Harness

End-to-end tests for hula that exercise every `hulactl` command against a real
docker-compose stack (hula + clickhouse + builder images).

## Prerequisites

- Docker + Docker Compose v2
- Go 1.25+ (to build hula/hulactl from source)
- `jq`, `openssl`
- Optional: `mkcert` installed on the host. If not installed, the harness
  falls back to generating a self-signed cert with openssl (treated as its
  own root CA for curl trust — no host-side CA modification needed).
- A `.env` file at `test/e2e/.env` with a `GITHUB_AUTH_TOKEN` that can
  read `github.com/tlalocweb/tlaloc-hula-test-site`. See `.env.example`.

## Running

```bash
cd /path/to/hulation
cp test/e2e/.env.example test/e2e/.env
$EDITOR test/e2e/.env     # add GITHUB_AUTH_TOKEN
./test/e2e/run.sh
```

Pass `--suite NN` to run a specific suite only:

```bash
./test/e2e/run.sh --suite 01
./test/e2e/run.sh --suite 10
```

Pass `--no-setup` to skip the build/compose-up phase (useful when iterating
on a single suite while the stack is already up):

```bash
./test/e2e/run.sh --no-setup --suite 10
```

## What it does

1. **Setup**:
   - Builds `hula:local` via `make docker-local`
   - Builds the `hula-builder-alpine-default` image via `build-docker.sh --local`
   - Builds `.bin/hulactl` via `make hulactl`
   - Generates a random admin password and argon2 hash
   - Generates TLS certs for `hula.test.local`, `site.test.local`, `staging.test.local` via `mkcert`
   - Renders `hula-config.yaml` from the template
   - Brings up `docker-compose` and waits for `/hulastatus`

2. **Run all suites** in order:
   - `01-auth` — hulactl auth flow (multi-server config)
   - `02-admin` — generatehash, totp-key, reload
   - `03-users` — user CRUD
   - `04-forms` — form CRUD + submit
   - `05-landers` — lander CRUD
   - `06-badactors` — WP probe detection
   - `07-build` — production git-autodeploy build
   - `08-staging-build` — staging rebuild
   - `09-staging-update` — WebDAV PUT
   - `10-staging-mount` — live mount + autobuild
   - `11-webdav-patch` — PATCH X-Update-Range and X-Patch-Format: diff
   - `12-db-lifecycle` — initdb/deletedb (destructive, runs last)

3. **Teardown**: `docker compose down -v`, prune builder containers, clean workdir.

## Troubleshooting

- **"permission denied on docker.sock"**: add your user to the `docker` group
- **Build takes a long time on first run**: first-run only — Go module download
  happens inside `docker buildx build`. Subsequent runs are much faster.
- **Port 443 in use**: edit `fixtures/docker-compose.yaml` to change the host
  port binding.
- **Test site clone fails**: the harness bind-mounts
  `/home/ubuntu/work/tlaloc-hula-test-site` into hula as a `file://` repo.
  Override with `TEST_SITE_SRC=/path/to/repo` in `.env` if your copy lives
  elsewhere.
