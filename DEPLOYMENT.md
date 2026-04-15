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
      CP -r public/* site/
      FINALIZE /site
```

**Full example with multiple profiles:**

```yaml
# Optional: choose a builder image (default: "default" which is alpine-based)
builder_image: ubuntu22.04

# Optional: global Hugo version requirement
hugo:
  at_least: 0.147.0

configs:
  production:
    commands: |
      WORKDIR /builder
      HUGO --minify
      CP -r public/* site/
      CP -r extra_assets site/extra
      RM -rf site/extra/drafts
      FINALIZE /site

  staging:
    # Override Hugo version for this profile
    hugo:
      at_least: 0.160.0
    # Install extra tools before the build
    dockerfile_prebuild: |
      RUN apt-get update && apt-get install -y imagemagick
    commands: |
      WORKDIR /site
      HUGO
      CP -r public/* site/
      RUN convert site/images/hero.png -resize 1200x site/images/hero-opt.png
      FINALIZE /site
```

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
| `createform` | Create a new form |
| `modifyform` | Modify an existing form |
| `submitform` | Submit form data |
| `createlander` | Create a new lander |
| `initdb` | Initialize the ClickHouse database |
| `deletedb` | Delete the ClickHouse database |

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
