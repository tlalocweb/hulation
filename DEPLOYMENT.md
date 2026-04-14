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
| `/var/lib/clickhouse` | ClickHouse data (persist) |
| `/var/log/clickhouse-server` | ClickHouse logs |
