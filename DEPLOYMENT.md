# Deploying Hulation

This document covers deploying the Hulation server with ClickHouse using Docker Compose and Kubernetes.

## Prerequisites

- Docker Engine 20.10+ with Compose V2
- A `config.yaml` tailored to your deployment (see [Configuration](#configuration) below)
- For Kubernetes: `kubectl` configured against your cluster

## Configuration

Hulation requires a YAML config file. Start from the included example:

```bash
cp docker-example-config.yaml config.yaml
```

Key settings to change for production:

```yaml
admin:
  # Generate with: hulactl generatehash
  # Or update directly: hulactl -hulaconf config.yaml updateadminhash
  hash: "$argon2id$v=19$m=16384,t=12,p=4$..."

# Generate a strong random string
jwt_key: "change-me-to-a-random-string"

port: 443

ssl:
  acme:
    email: admin@example.com
    cache_dir: /var/hula/certs
    domains:
      - example.com
      - www.example.com

servers:
  - host: example.com
    aliases:
      - www.example.com
    id: your-site-id

dbconfig:
  host: hula-clickhouse    # must match the clickhouse service/container name
  port: 9000
  user: hula
  pass: change-me
  dbname: hula
```

Database connection can also be set via environment variables: `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD`, `DB_NAME`.

## TLS / SSL Configuration

Hulation supports three TLS modes for both per-server SSL and `hula_ssl` (the admin/API identity). Only one mode can be active per SSL block.

### ACME (Let's Encrypt)

Automatically provisions and renews certificates via Let's Encrypt.

**Per-server:**

```yaml
servers:
  - host: example.com
    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs   # default: /var/hula/certs
        http_port: 80                # default: 80
        domains:                     # default: derived from host + aliases
          - example.com
          - www.example.com
```

**Hula admin identity:**

```yaml
hula_host: hula.example.com
hula_ssl:
    acme:
        email: admin@example.com
```

Requires `hula_host` to be set to an actual hostname (not `localhost`). Port 80 must be reachable for HTTP-01 challenges.

### Cloudflare Origin CA

Provisions certificates via the Cloudflare Origin CA API. These certificates are only trusted by Cloudflare's edge servers -- all traffic must pass through Cloudflare's proxy. Non-Cloudflare IPs are dropped at the TCP level before TLS handshake.

**Per-server:**

```yaml
servers:
  - host: www.example.com
    id: mysite
    ssl:
      cloudflare_origin_ca:
        cache_dir: /var/hula/certs   # default: /var/hula/certs
        key_type: ecdsa              # default: ecdsa (or rsa)
        validity_days: 5475          # default: 5475 (15 years)
```

API token and zone ID are resolved from environment variables keyed by the server ID (dashes replaced with underscores):

```bash
CLOUDFLARE_API_TOKEN_mysite=cfat_...
CLOUDFLARE_ZONE_ID_mysite=73453a...
```

Or set explicitly in YAML:

```yaml
ssl:
  cloudflare_origin_ca:
    api_token: "cfat_..."
    zone_id: "73453a..."
```

**Hula admin identity:**

```yaml
hula_host: hula.example.com
hula_ssl:
    cloudflare_origin_ca: {}
```

Uses env vars `CLOUDFLARE_API_TOKEN_hula` and `CLOUDFLARE_ZONE_ID_hula`. All defaults (`cache_dir`, `key_type`, `validity_days`) apply automatically. Requires `hula_host` to be set to an actual hostname.

**Cloudflare DNS setup:**

- Each hostname must have an **A record** (not CNAME) pointing to your origin server's IP, with the orange cloud (proxy) enabled
- CNAME records between two proxied hostnames in the same zone will fail with Cloudflare error 1016 -- Cloudflare cannot resolve the origin through a proxied CNAME
- All three fields (`cache_dir`, `key_type`, `validity_days`) have defaults and can be omitted

**Multiple virtual hosts:**

When multiple servers share the same Cloudflare zone, each needs its own env vars:

```bash
# Server id: mysite
CLOUDFLARE_API_TOKEN_mysite=cfat_...
CLOUDFLARE_ZONE_ID_mysite=73453a...

# Server id: staging-site (dashes become underscores)
CLOUDFLARE_API_TOKEN_staging_site=cfat_...
CLOUDFLARE_ZONE_ID_staging_site=73453a...

# Hula admin identity
CLOUDFLARE_API_TOKEN_hula=cfat_...
CLOUDFLARE_ZONE_ID_hula=73453a...
```

The API token and zone ID can be the same across all entries if they share the same Cloudflare zone and token.

**SNI-based certificate selection:**

When multiple Origin CA certificates are loaded (e.g., `www.example.com` and `staging.example.com`), hula matches the TLS SNI (Server Name Indication) against each certificate's DNS SANs to select the correct one.

### Static Certificate

Use your own certificate and key files:

```yaml
ssl:
  cert: /path/to/cert.pem
  key: /path/to/key.pem
```

Or inline:

```yaml
ssl:
  cert: |
    -----BEGIN CERTIFICATE-----
    ...
  key: |
    -----BEGIN PRIVATE KEY-----
    ...
```

### TLS Version Controls

Any SSL block can include TLS version constraints:

```yaml
ssl:
  cloudflare_origin_ca: {}
  tls:
    min_version: "1.2"   # default: 1.2
    max_version: "1.3"   # default: no limit
```

### Phase-0 TLS note

Starting with the Phase-0 gRPC migration, hula uses a single unified HTTPS
listener for **every** web service (gRPC, REST gateway, WebDAV, visitor
tracking, static scripts, `/hulastatus`, and per-host site routing).
The unified server requires a static `hula_ssl.cert` + `hula_ssl.key`
on disk; the legacy ACME / Cloudflare-origin-CA / per-host SNI flows are
a planned follow-up on the unified listener. For the immediate Phase-0
cutover, configure a static cert pair or continue using `mkcert` for
dev / self-signed deployments.

## Authentication (Phase 0)

### Built-in admin

```yaml
admin:
  username: admin
  hash:     "<argon2id-hash>"    # use `hulactl generatehash`
  totp_required: false            # optional
```

The `admin` user is the break-glass account: always present, authenticates
via username + argon2id password, and receives admin privileges. TOTP is
enforceable via `admin.totp_required: true` and enrolled via the
`AuthService.TotpSetup` RPC (or the legacy `/api/auth/totp/setup`).

### SSO via OIDC (Google / GitHub / Microsoft)

```yaml
auth:
  providers:
    - name: google
      provider: oidc
      config:
        display_name: Google
        discovery_url: https://accounts.google.com/.well-known/openid-configuration
        client_id:     ${HULA_GOOGLE_CLIENT_ID}
        client_secret: ${HULA_GOOGLE_CLIENT_SECRET}
        redirect_url:  https://${HULA_HOST}/api/v1/auth/callback/google
        scopes:        [openid, email, profile]
        icon_url:      /analytics/icons/google.svg

    - name: github
      provider: oidc
      config:
        display_name: GitHub
        # GitHub isn't strictly OIDC-compliant at the discovery-doc level,
        # but the hula OIDC provider handles it via a type=github subclass.
        discovery_url: https://token.actions.githubusercontent.com/.well-known/openid-configuration
        client_id:     ${HULA_GITHUB_CLIENT_ID}
        client_secret: ${HULA_GITHUB_CLIENT_SECRET}
        redirect_url:  https://${HULA_HOST}/api/v1/auth/callback/github
        scopes:        [read:user, user:email]
        icon_url:      /analytics/icons/github.svg

    - name: microsoft
      provider: oidc
      config:
        display_name: Microsoft
        discovery_url: https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration
        client_id:     ${HULA_MICROSOFT_CLIENT_ID}
        client_secret: ${HULA_MICROSOFT_CLIENT_SECRET}
        redirect_url:  https://${HULA_HOST}/api/v1/auth/callback/microsoft
        scopes:        [openid, email, profile]
        icon_url:      /analytics/icons/microsoft.svg
```

Environment variables for the client IDs and secrets are the recommended
approach — hula's config loader expands `${…}` references against the
process environment.

**User provisioning**: admin pre-creates users by email via
`AuthService.CreateUser` (or the legacy `/api/auth/user` POST).
First SSO login by a user whose email matches an existing row completes
the identity link. Unknown emails are rejected; hula does not
self-provision users through SSO by default.

## Analytics (Phase 0)

```yaml
analytics:
  events_ttl_days: 395   # ~13 months; default if unset
```

All fields optional. The `events_ttl_days` value is consumed by the
`pkg/store/clickhouse` migration runner when it applies the explicit
`events_v1` DDL. In Phase 0, GORM AutoMigrate still manages the events
table for backward compatibility; the migration runner is infrastructure
ready for the next phase to flip over.

## Docker Compose

### Full Stack (Hulation + ClickHouse)

Create a `docker-compose.yaml`:

```yaml
services:
  hula:
    image: ghcr.io/tlalocweb/hula:latest
    container_name: hula
    restart: unless-stopped
    ports:
      - "443:443"
      - "80:80"       # needed for ACME HTTP-01 challenges
    volumes:
      - ./config.yaml:/etc/hula/config.yaml:ro
      - hula-certs:/var/hula/certs
      - ./public:/var/hula/public       # optional: static site content
      # Required if using the backends feature (Docker-managed backend containers):
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      hula-clickhouse:
        condition: service_healthy
    environment:
      - DB_HOST=hula-clickhouse
      - DB_PASSWORD=change-me

  hula-clickhouse:
    image: clickhouse/clickhouse-server:latest
    container_name: hula-clickhouse
    restart: unless-stopped
    cap_add:
      - SYS_NICE
      - NET_ADMIN
      - IPC_LOCK
    ulimits:
      nofile:
        soft: 262144
        hard: 262144
    volumes:
      - ch-data:/var/lib/clickhouse
      - ch-logs:/var/log/clickhouse-server
    environment:
      - CLICKHOUSE_DB=hula
      - CLICKHOUSE_USER=hula
      - CLICKHOUSE_PASSWORD=change-me
      - CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1
    healthcheck:
      test: ["CMD", "clickhouse-client", "--query", "SELECT 1"]
      interval: 5s
      timeout: 3s
      retries: 10

volumes:
  hula-certs:
  ch-data:
  ch-logs:
```

Deploy:

```bash
docker compose up -d
```

View logs:

```bash
docker compose logs -f hula
```

### With Backend Containers

When a server has `backends:` configured, Hulation manages Docker containers as backend services and reverse-proxies to them. This requires mounting the Docker socket.

Example `config.yaml` with a backend:

```yaml
servers:
  - host: example.com
    port: 443
    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs
    backends:
      - container_name: myapi
        image: registry.example.com/myapi:latest
        virtual_path: "/api"
        container_path: "/api/v2"
        expose:
          - "8002"
        restart: always
        environment:
          - API_KEY=secret
        command: /app/server --port 8002
        health_check: /healthz
        health_timeout: 60
```

Hulation creates an isolated Docker bridge network per virtual server (`hula_example_com`), starts the containers, waits for health checks, and proxies matching requests. Backends on different virtual servers cannot reach each other.

### Building the Image Locally

The Dockerfile expects the build context to be the **parent directory** of the hulation repo (because `go.mod` has a `replace` directive pointing to `../clickhouse`):

```bash
# From the hulation directory:
make docker-local

# Or manually:
docker build -f Dockerfile \
  --build-arg hulaversion=$(git describe --tags) \
  --build-arg hulabuilddate=$(date -u +'%Y-%m-%dT%H:%M:%SZ') \
  -t ghcr.io/tlalocweb/hula:latest \
  ..
```

Multi-platform build and push:

```bash
make docker-push
```

## Site Deployment from Git

Hulation can automatically build and deploy static websites from a git repository. When triggered via API, hula clones the repository, reads a build configuration, spins up an ephemeral Docker builder container, runs the site generator (Hugo, Astro, Gatsby, or MkDocs), and deploys the result to the server's static file root.

### How It Works

1. An admin calls `POST /api/site/trigger-build` with the server ID
2. Hula clones (or pulls) the configured git repository
3. Hula reads `.hula/sitebuild.yaml` from the repository for build instructions
4. Hula starts a builder container (e.g., `hula-builder-alpine-default`) with the `hulabuild` binary as its entrypoint
5. The site source is transferred into the builder container via the Docker API
6. `hulabuild` runs the build commands (Hugo, Astro, etc.) inside the container
7. The built site is transferred out and deployed to the server's root directory
8. The builder container is removed

Builder containers are ephemeral -- they are created for each build and destroyed when done. Hula communicates with the builder via stdin/stdout, and transfers files using the Docker API (`CopyToContainer` / `CopyFromContainer`), so no shared filesystem is required. This means site deployment works whether hula runs directly on the host or inside a Docker container.

### Configuration

Add `root_git_autodeploy` to a server in `config.yaml`:

```yaml
servers:
  - host: www.example.com
    id: mysite
    aliases:
      - example.com

    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs
        http_port: 80

    # Static site serving
    root: /var/hula/mysite/site
    root_index: index.html
    root_compress: true
    root_max_age: 3600

    # Git-based site deployment
    root_git_autodeploy:
      repo: https://github.com/yourorg/yoursite
      creds:
        username: deploy-user
        password: {{env:GITHUB_AUTH_TOKEN}}
      ref:
        # Use one of:
        tag: semver       # deploy the highest semver tag
        # tag: any        # deploy the most recent tag
        # tag: production # deploy the exact tag named "production"
        # branch: main    # deploy from a branch
      hula_build: production   # which build profile from sitebuild.yaml to use
      # Optional: override where repos are cloned (default: /var/hula/sitedeploy/<id>/repo)
      # data_dir: /custom/path/to/repo
```

**Credentials:** The `creds` section is optional for public repositories. For private repos, use `{{env:GITHUB_AUTH_TOKEN}}` to read the token from an environment variable. For GitHub, set `username` to any value and `password` to a Personal Access Token or fine-grained token with read access to the repo.

**Ref modes:**

| `ref` setting | Behavior |
|---------------|----------|
| `tag: semver` | Checks out the highest valid semver tag (e.g., `v2.1.0` over `v2.0.3`) |
| `tag: any` | Checks out the most recent tag regardless of format |
| `tag: <name>` | Checks out the exact tag (e.g., `production`, `latest`) |
| `branch: <name>` | Checks out the specified branch |

### The `.hula/sitebuild.yaml` File

Every site repository must contain a `.hula/sitebuild.yaml` file at its root. This tells hula how to build the site.

**Minimal example (Hugo site):**

```yaml
configs:
  production:
    commands: |
      WORKDIR /builder
      HUGO --minify
      FINALIZE /builder/site/public
```

**Example with defs (variable substitution) and staging:**

```yaml
defs:
  WORKDIR: /builder

configs:
  production:
    commands: |
      WORKDIR {{WORKDIR}}
      HUGO --minify
      FINALIZE {{WORKDIR}}/site/public

  staging:
    servedir: "{{WORKDIR}}/site/public"
    build_command: |
      HUGO
    commands: |
      WORKDIR {{WORKDIR}}
```

The `defs` section defines variables that are substituted (using mustache syntax `{{VAR}}`) into all profile fields (`commands`, `servedir`, `build_command`, `dockerfile_prebuild`) before use.

**Full example with multiple profiles:**

```yaml
# Optional: choose a builder image (default: "default" which is alpine-based)
builder_image: ubuntu22.04

# Optional: global Hugo version requirement
hugo:
  at_least: 0.147.0

defs:
  WORKDIR: /builder

configs:
  production:
    commands: |
      WORKDIR {{WORKDIR}}
      HUGO --minify
      FINALIZE {{WORKDIR}}/site/public

  staging:
    servedir: "{{WORKDIR}}/site/public"
    build_command: |
      HUGO
    # Install extra tools before the build
    dockerfile_prebuild: |
      RUN apt-get update && apt-get install -y imagemagick
    commands: |
      WORKDIR {{WORKDIR}}
```

**Staging profiles** are identified by the presence of `servedir`. See [Staging Mode](#staging-mode) below for details.

**Builder images:**

| Image name | Base | Tag |
|------------|------|-----|
| `default` or `alpine-default` | Alpine 3.19 | `hula-builder-alpine-default`, `hula-builder-default` |
| `ubuntu22.04` | Ubuntu 22.04 | `hula-builder-ubuntu22.04` |

Both images include Hugo (extended), Astro, Gatsby CLI, MkDocs, Node.js, Python, and git.

**`dockerfile_prebuild`:** When a build profile includes `dockerfile_prebuild`, hula builds a derived Docker image by appending the prebuild commands to the base builder image. Derived images are cached by content hash -- if the prebuild commands haven't changed, the cached image is reused.

### Build Commands Reference

The `commands` field contains a list of commands executed in order inside the builder container. Each command must be on its own line. Lines starting with `#` are comments. Command names are case-insensitive but conventionally written in uppercase.

| Command | Description |
|---------|-------------|
| `WORKDIR <path>` | **Required.** Set the working directory (must be absolute). Triggers transfer of the site source into the container at `<path>/site/`. |
| `HUGO [flags]` | Run Hugo with optional flags (e.g., `--minify`, `-e production`). Runs in the `site/` subdirectory. |
| `ASTRO [flags]` | Run Astro build with optional flags. |
| `GATSBY [flags]` | Run Gatsby build with optional flags. |
| `MKDOCS [flags]` | Run MkDocs build with optional flags. |
| `CP <args>` | Copy files (same syntax as Linux `cp`). Paths are relative to `site/`. |
| `RM <args>` | Remove files (same syntax as Linux `rm`). Paths are sandboxed to the WORKDIR. |
| `RUN <command>` | Run an arbitrary shell command in the `site/` directory. |
| `FINALIZE <path>` | **Required, must be last.** Tarballs the given directory and transfers it out of the container to become the deployed site. |

**Example flow:**

```
WORKDIR /builder        # Creates /builder, transfers repo source to /builder/site/
HUGO --minify           # Runs hugo in /builder/site/, output goes to /builder/site/public/
CP -r public/* site/    # Copies built files (relative to /builder/site/)
FINALIZE /site          # Tarballs /site, sends it back to hula for deployment
```

### Staging Mode

Staging mode provides a live development workflow where the built site is served directly from a Docker volume mount. Unlike production builds (which are ephemeral -- build, extract, destroy), staging containers are long-lived. The builder container stays running after the initial build, and changes are visible immediately without extracting tarballs.

**How it works:**

1. Hula starts a long-lived builder container with `hulabuild` as entrypoint
2. The `servedir` path inside the container is volume-mounted to a host directory
3. After the initial `commands` execute (WORKDIR transfers source, etc.), `hulabuild` enters a staging loop
4. Hugo (or another generator) runs and outputs to the `servedir` -- visible immediately since it's a volume mount
5. Rebuilds are triggered via `POST /api/staging/build` or `hulactl staging-build`, which sends an `EXEC_BUILD` command through the stdin/stdout protocol
6. A WebDAV server at `/api/staging/{server-id}/dav/` provides file management for the staging site

**Configuration:**

In `config.yaml`, set `hula_build: staging` to select the staging profile:

```yaml
servers:
  - host: staging.example.com
    id: staging-site
    ssl:
      cloudflare_origin_ca: {}
    root_git_autodeploy:
      repo: https://github.com/yourorg/yoursite
      creds:
        username: x-access-token
        password: "{{env:GITHUB_AUTH_TOKEN}}"
      ref:
        branch: main
      hula_build: staging
```

In `.hula/sitebuild.yaml`, the staging profile uses `servedir` and `build_command`:

```yaml
defs:
  WORKDIR: /builder

configs:
  production:
    commands: |
      WORKDIR {{WORKDIR}}
      HUGO --minify
      FINALIZE {{WORKDIR}}/site/public

  staging:
    servedir: "{{WORKDIR}}/site/public"
    build_command: |
      HUGO
    commands: |
      WORKDIR {{WORKDIR}}
```

| Field | Description |
|-------|-------------|
| `servedir` | Absolute path inside the container to volume-mount for serving. Identifies the profile as staging. |
| `build_command` | The command to re-run on rebuild triggers. Must be a known generator (`HUGO`, `ASTRO`, `GATSBY`, `MKDOCS`). Arbitrary commands are not allowed for security. |
| `commands` | Initial commands to run when the container starts. Does NOT include `FINALIZE` (staging profiles serve directly from the volume). |

**Staging API endpoints:**

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/staging/build` | Trigger a rebuild (`{"id": "server-id"}`) |
| ALL | `/api/staging/{server-id}/dav/*` | WebDAV file management for the staging site |

**hulactl staging commands:**

```bash
# Trigger a rebuild
hulactl staging-build <server-id>

# Upload a single file via WebDAV
hulactl staging-update <server-id> <local-file> <remote-path>

# Mount a local folder synced to the staging site (watches for changes)
hulactl staging-mount <server-id> <local-folder>
```

The `staging-mount` command syncs a local directory with the remote staging folder via WebDAV. It watches the local filesystem for changes and pushes them to the server in real-time. Security-sensitive files (executables, `.ssh`, `.env`, `*.key`, etc.) are skipped by default -- use `--dangerous` to override.

### API Endpoints

All site deployment endpoints require admin authentication (JWT + OPA).

**Trigger a build:**

```bash
curl -X POST https://your-server/api/site/trigger-build \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id": "mysite"}'
```

Response:

```json
{"status": "build_triggered", "build_id": "a1b2c3d4-..."}
```

Returns `409 Conflict` if a build is already running for that server.

**Check build status:**

```bash
curl https://your-server/api/site/build-status/a1b2c3d4-... \
  -H "Authorization: Bearer $TOKEN"
```

Response:

```json
{
  "build_id": "a1b2c3d4-...",
  "server_id": "mysite",
  "status": 8,
  "status_text": "complete",
  "started_at": "2026-04-14T15:30:00Z",
  "ended_at": "2026-04-14T15:30:45Z",
  "logs": [
    "Cloning/pulling repository...",
    "Repository ready at /var/hula/sitedeploy/mysite/repo",
    "Using build profile: production",
    ">>> WORKDIR /builder",
    ">>> HUGO --minify",
    "[hugo] Start building sites...",
    "[hugo] Total in 1234 ms",
    ">>> FINALIZE /site",
    "Site deployed successfully"
  ],
  "error": ""
}
```

**List builds for a server:**

```bash
curl https://your-server/api/site/builds/mysite \
  -H "Authorization: Bearer $TOKEN"
```

Returns an array of build states, newest first.

**Build status values:**

| Status | Meaning |
|--------|---------|
| `pending` | Build queued |
| `cloning` | Cloning or pulling the git repository |
| `preparing_image` | Building a derived image (if `dockerfile_prebuild` is used) |
| `starting_container` | Starting the builder container |
| `transferring_source` | Copying site source into the builder container |
| `running` | Build commands executing |
| `extracting_result` | Copying built site out of the builder container |
| `deploying` | Writing the built site to the server's root directory |
| `complete` | Build finished successfully |
| `failed` | Build failed (see `error` field) |

### Builder Images Setup

Builder images must be loaded into Docker before triggering builds. They ship as tarballs with the hula distribution.

**Build the images from source:**

```bash
cd builder-images
./build-images.sh
```

This compiles `hulabuild`, builds both Docker images, and exports them as tarballs in `builder-images/output/`.

**Load images on the target machine:**

```bash
docker load < hula-builder-ubuntu22.04.tar.gz
docker load < hula-builder-alpine-default.tar.gz
```

Verify:

```bash
docker images | grep hula-builder
```

### Docker Compose with Site Deployment

When running hula in Docker with site deployment enabled, you need:

1. The Docker socket mounted (so hula can manage builder containers)
2. A persistent volume for cloned repositories
3. A persistent volume for the site root (so deployed sites survive container restarts)
4. The `GITHUB_AUTH_TOKEN` environment variable (or however you pass git credentials)

```yaml
services:
  hula:
    image: ghcr.io/tlalocweb/hula:latest
    container_name: hula
    restart: unless-stopped
    ports:
      - "443:443"
      - "80:80"
    volumes:
      - ./config.yaml:/etc/hula/config.yaml:ro
      - hula-certs:/var/hula/certs
      - hula-sites:/var/hula/mysite          # site root
      - hula-sitedeploy:/var/hula/sitedeploy # git repos + build artifacts
      - /var/run/docker.sock:/var/run/docker.sock
    environment:
      - GITHUB_AUTH_TOKEN=${GITHUB_AUTH_TOKEN}
      - DB_HOST=hula-clickhouse
      - DB_PASSWORD=change-me
    depends_on:
      hula-clickhouse:
        condition: service_healthy

  hula-clickhouse:
    image: clickhouse/clickhouse-server:latest
    container_name: hula-clickhouse
    restart: unless-stopped
    volumes:
      - ch-data:/var/lib/clickhouse
    environment:
      - CLICKHOUSE_DB=hula
      - CLICKHOUSE_USER=hula
      - CLICKHOUSE_PASSWORD=change-me
      - CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1
    healthcheck:
      test: ["CMD", "clickhouse-client", "--query", "SELECT 1"]
      interval: 5s
      timeout: 3s
      retries: 10

volumes:
  hula-certs:
  hula-sites:
  hula-sitedeploy:
  ch-data:
```

### Triggering Builds

Builds are triggered via the API. Common integration patterns:

**GitHub webhook (via a lightweight relay):**

Set up a GitHub webhook that calls your server's trigger-build endpoint on push/tag events. You can use a small relay service, a GitHub Action, or a serverless function to transform the webhook payload into the hula API call.

**GitHub Actions:**

```yaml
# .github/workflows/deploy.yml
name: Deploy Site
on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Trigger hula build
        run: |
          TOKEN=$(curl -s -X POST https://your-server/api/auth/login \
            -H "Content-Type: application/json" \
            -d '{"username":"admin","password":"${{ secrets.HULA_ADMIN_PASSWORD }}"}' \
            | jq -r '.token')
          curl -X POST https://your-server/api/site/trigger-build \
            -H "Authorization: Bearer $TOKEN" \
            -H "Content-Type: application/json" \
            -d '{"id": "mysite"}'
```

**Manual trigger (via curl or hulactl):**

```bash
# Authenticate first
TOKEN=$(curl -s -X POST https://your-server/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"yourpassword"}' | jq -r '.token')

# Trigger build
curl -X POST https://your-server/api/site/trigger-build \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id": "mysite"}'

# Poll status
curl -s https://your-server/api/site/build-status/<build-id> \
  -H "Authorization: Bearer $TOKEN" | jq .status_text
```

### Architecture: Hula in Docker

When hula runs inside Docker, builder containers are **sibling containers**, not nested. Both talk to the same Docker daemon via the mounted socket. File transfers use the Docker API, not the filesystem:

```
Host
├── Docker daemon
│   ├── hula container
│   │   ├── /var/run/docker.sock ──(mounted from host)
│   │   ├── /var/hula/sitedeploy/  ──(persistent volume: git clones)
│   │   └── /var/hula/mysite/site/ ──(persistent volume: served files)
│   │
│   └── hula-builder-xxxx container  ──(ephemeral)
│       └── /builder/site/  ──(build workspace)
│
│   ← Docker API: CopyToContainer (source tarball)
│   → Docker API: CopyFromContainer (built site tarball)
```

Requirements when hula runs in Docker:

- Mount `/var/run/docker.sock` so hula can manage builder containers
- Mount a persistent volume for `/var/hula/sitedeploy` so cloned repos survive restarts
- Mount a persistent volume for the site root directory
- Install `git` in the hula image (included by default in the official image)
- Builder images must be loaded on the **host's** Docker daemon (not inside the hula container)

### Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| "Docker daemon not reachable" | Docker socket not mounted | Add `-v /var/run/docker.sock:/var/run/docker.sock` |
| "git not found in PATH" | git not installed in hula's container | Use the official hula image (includes git) or add `apk add git` |
| "builder image not found" | Builder images not loaded | Run `docker load < hula-builder-alpine-default.tar.gz` on the host |
| Build stuck at "transferring_source" | Large repository, slow I/O | Check Docker daemon resources; consider shallow clones (default) |
| "build already in progress" (409) | Previous build still running | Wait for it to complete or check `/api/site/build-status` |
| Site not updating after build | Volume not mounted for site root | Ensure the `root:` path in config maps to a persistent volume |

## Kubernetes

### Namespace and Secrets

```bash
kubectl create namespace hula

# Registry credentials (if using a private registry)
kubectl create secret docker-registry regcred \
  --namespace hula \
  --docker-server=ghcr.io \
  --docker-username=YOUR_USER \
  --docker-password=YOUR_TOKEN

# Config file
kubectl create configmap hula-config \
  --namespace hula \
  --from-file=config.yaml=config.yaml

# Database password
kubectl create secret generic hula-db-secret \
  --namespace hula \
  --from-literal=password=change-me
```

### ClickHouse StatefulSet

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: hula-clickhouse
  namespace: hula
spec:
  serviceName: hula-clickhouse
  replicas: 1
  selector:
    matchLabels:
      app: hula-clickhouse
  template:
    metadata:
      labels:
        app: hula-clickhouse
    spec:
      containers:
        - name: clickhouse
          image: clickhouse/clickhouse-server:latest
          ports:
            - containerPort: 9000
              name: native
            - containerPort: 8123
              name: http
          env:
            - name: CLICKHOUSE_DB
              value: hula
            - name: CLICKHOUSE_USER
              value: hula
            - name: CLICKHOUSE_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: hula-db-secret
                  key: password
            - name: CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT
              value: "1"
          volumeMounts:
            - name: ch-data
              mountPath: /var/lib/clickhouse
            - name: ch-logs
              mountPath: /var/log/clickhouse-server
          readinessProbe:
            exec:
              command: ["clickhouse-client", "--query", "SELECT 1"]
            initialDelaySeconds: 5
            periodSeconds: 5
          resources:
            requests:
              memory: "512Mi"
              cpu: "250m"
            limits:
              memory: "2Gi"
              cpu: "1"
  volumeClaimTemplates:
    - metadata:
        name: ch-data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 20Gi
    - metadata:
        name: ch-logs
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 2Gi
---
apiVersion: v1
kind: Service
metadata:
  name: hula-clickhouse
  namespace: hula
spec:
  clusterIP: None
  selector:
    app: hula-clickhouse
  ports:
    - name: native
      port: 9000
    - name: http
      port: 8123
```

### Hulation Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hula
  namespace: hula
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hula
  template:
    metadata:
      labels:
        app: hula
    spec:
      imagePullSecrets:
        - name: regcred
      containers:
        - name: hula
          image: ghcr.io/tlalocweb/hula:latest
          imagePullPolicy: Always
          ports:
            - containerPort: 443
              name: https
            - containerPort: 80
              name: http
          env:
            - name: DB_HOST
              value: hula-clickhouse
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: hula-db-secret
                  key: password
          volumeMounts:
            - name: config
              mountPath: /etc/hula/config.yaml
              subPath: config.yaml
              readOnly: true
            - name: certs
              mountPath: /var/hula/certs
          livenessProbe:
            httpGet:
              path: /hulastatus
              port: 443
              scheme: HTTPS
            initialDelaySeconds: 10
            periodSeconds: 15
          readinessProbe:
            httpGet:
              path: /hulastatus
              port: 443
              scheme: HTTPS
            initialDelaySeconds: 5
            periodSeconds: 5
          resources:
            requests:
              memory: "128Mi"
              cpu: "100m"
            limits:
              memory: "512Mi"
              cpu: "500m"
      volumes:
        - name: config
          configMap:
            name: hula-config
        - name: certs
          persistentVolumeClaim:
            claimName: hula-certs
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: hula-certs
  namespace: hula
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 100Mi
---
apiVersion: v1
kind: Service
metadata:
  name: hula
  namespace: hula
spec:
  type: LoadBalancer
  selector:
    app: hula
  ports:
    - name: https
      port: 443
      targetPort: 443
    - name: http
      port: 80
      targetPort: 80
```

Deploy:

```bash
kubectl apply -f clickhouse.yaml
kubectl apply -f hula.yaml
```

### Notes on Kubernetes

- **ACME certificates**: The cert PVC must be `ReadWriteOnce` and the deployment should run a single replica (or use a shared volume) so that the ACME cache is consistent.
- **Backend containers feature**: The Docker-managed backends feature is not available on Kubernetes. Use sidecar containers or separate Deployments/Services instead, and configure `proxies:` in `config.yaml` to point at those services.
- **Site deployment feature**: Like backends, the `root_git_autodeploy` feature requires access to a Docker daemon to run builder containers. On Kubernetes this requires Docker-in-Docker or a Docker socket mount, which is not recommended in production. Consider building your static site in CI/CD and deploying the output to a PVC or object storage instead.
- **Health endpoint**: `/hulastatus` is unauthenticated and suitable for liveness and readiness probes. If running on a non-standard port or without TLS, adjust the probe `port` and `scheme` accordingly.
- **Ingress**: The example uses a `LoadBalancer` Service. Replace with an Ingress resource or NodePort as appropriate for your cluster. If using an ingress controller for TLS termination, configure `external_scheme: https` and disable the `ssl:` block in `config.yaml`.

## hulactl CLI

`hulactl` is a command-line tool for managing a running Hulation instance. It is built separately from the server and runs on the host (not inside the container).

### Building

```bash
make hulactl
```

The binary is placed at `.bin/hulactl`.

### Initial Setup

**Set the admin password** (writes the hash directly into the config file):

```bash
.bin/hulactl -hulaconf /path/to/config.yaml updateadminhash
```

This prompts for a password, generates an Argon2 hash, and updates the `admin.hash` field in the config file. Restart the hula container afterward to pick up the change.

You can also generate a hash without modifying a file:

```bash
.bin/hulactl generatehash
```

**Authenticate** (get and save a JWT token):

```bash
.bin/hulactl -hulaapi https://your-server auth
```

The token is saved to `hulactl.yaml` (default: `/etc/hulation/hulactl.yaml`) and reused by subsequent commands.

### Using hulactl Inside Docker

hulactl is included in the hula Docker image at `/hula/hulactl`. When running inside the container, pass `-hulaapi` pointing to the local server:

```bash
# Set the admin password
docker exec -it hula /hula/hulactl -hulaapi https://localhost:443 -hulaconf /etc/hula/config.yaml updateadminhash

# Authenticate
docker exec -it hula /hula/hulactl -hulaapi https://localhost:443 auth

# List bad actors
docker exec -it hula /hula/hulactl -hulaapi https://localhost:443 badactors

# Reload config (sends SIGHUP to hula)
docker exec hula /hula/hulactl reload
# Or without hulactl:
docker kill --signal=HUP hula
```

### Commands

| Command | Description |
|---------|-------------|
| `generatehash` | Generate a password hash (prints to stdout) |
| `updateadminhash` | Generate a hash and write it to a hulation config file |
| `auth` | Authenticate and save JWT token |
| `authok` | Verify authentication is working |
| `badactors` | List bad actors with scores and blocked/flagged status |
| `build` | Trigger a site build for a server |
| `build-status` | Get the status of a site build |
| `builds` | List recent builds for a server |
| `staging-build` | Trigger a rebuild in a staging container |
| `staging-update` | Upload a file to the staging site via WebDAV |
| `staging-mount` | Mount a local folder synced to a staging site via WebDAV |
| `createform` | Create a new form |
| `modifyform` | Modify an existing form |
| `submitform` | Submit form data |
| `createlander` | Create a new lander |
| `initdb` | Initialize the ClickHouse database |
| `deletedb` | Delete the ClickHouse database |
| `reload` | Send SIGHUP to reload config |

### Configuration

`hulactl` reads from `/etc/hulation/hulactl.yaml` by default (override with `-config`):

```yaml
loglevel: warn
hulaurl: https://your-server:443
token: <saved by auth command>
```

Flags can also be passed directly:

```bash
.bin/hulactl -hulaapi https://localhost:443 -hulaconf /path/to/config.yaml <command>
```

### Example: Viewing Bad Actors

```bash
.bin/hulactl -hulaapi https://your-server badactors
```

Output:

```
Bad Actor Status: enabled=true  dry_run=false  threshold=50  ttl=24h
Blocked IPs: 3  Allowlisted: 1  Signatures: 40

IP                 SCORE   STATUS    DETECTED               EXPIRES                REASON
--                 -----   ------    --------               -------                ------
192.168.1.100      100     BLOCKED   2026-04-10 14:23:01    2026-04-11 14:23:01    web shell probe
10.0.0.55          30      flagged   2026-04-10 15:01:22    2026-04-11 15:01:22    admin panel probe

3 entries total
```

## Ports Reference

| Port | Protocol | Purpose |
|------|----------|---------|
| 443 | TCP | HTTPS (hulation) |
| 80 | TCP | HTTP / ACME HTTP-01 challenges |
| 9000 | TCP | ClickHouse native protocol |
| 8123 | TCP | ClickHouse HTTP interface |

## Volumes Reference

| Path | Purpose |
|------|---------|
| `/etc/hula/config.yaml` | Hulation configuration (required) |
| `/var/hula/certs` | ACME certificate cache (persist across restarts) |
| `/var/hula/public` | Static content root |
| `/var/hula/scripts` | Custom scripts |
| `/var/hula/sitedeploy` | Git clones and build artifacts for site deployment (persist) |
| `/var/run/docker.sock` | Docker socket (required for backends and site deployment) |
| `/var/lib/clickhouse` | ClickHouse data (persist) |
| `/var/log/clickhouse-server` | ClickHouse logs |
