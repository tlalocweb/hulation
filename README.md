# Hula

Hula is a modern web server for static sites, marketing landers, and digital‑marketing / CDP workloads. It's purpose‑built to make Hugo (and similar) site deployments fast, low‑touch, and AI‑friendly: one container fronts your domains with automatic HTTPS, auto‑deploys from Git, and ships a full visitor analytics, lead‑capture, live‑chat, and consent stack out of the box.

## Features

**Site delivery**
- Static site serving with byte‑range, transparent compression, and immutable cache control
- **Git autodeploy** — pulls and (optionally) builds your repo on boot and on demand; production + staging modes per server
- **Staging WebDAV** — mount a remote staging site as a local folder, edit live, and trigger rebuilds (`hulactl staging-mount --autobuild`)
- **Backend containers** — Hula manages and reverse‑proxies Docker containers as per‑server backends, isolated on dedicated networks

**TLS & networking**
- Automatic Let's Encrypt certificates (ACME HTTP‑01) with shared listeners across servers
- Cloudflare Origin CA support and manual cert/key paths
- Reverse‑proxy aware HTTP/TLS protocol detection on a single listener

**Visitor analytics & marketing (Phase 1–4)**
- ClickHouse‑backed visitor + event analytics with first‑class hello/landing/form/conversion/goal events
- **Forms & landers** — versioned CRUD with hooks (Risor) for `on_new_form_submission`, `on_lander_visit`, `on_new_visitor`
- **Goals, reports & scheduled email digests** (Phase 4) with operator alerts
- **Live chat** (Phase 4b) — visitor → operator chat with WebSocket transport and badactor gating

**Privacy / GDPR (Phase 4c)**
- `consent_mode: opt_in | opt_out | off` per server, with `Sec‑GPC` honored as a binding marketing opt‑out
- **Server‑side forwarders** — Meta CAPI and GA4 Measurement Protocol adapters with per‑purpose consent gating
- **Cookieless mode** (`tracking_mode: cookieless`) — daily HMAC visitor IDs derived from a per‑server salt; no cookie banner needed
- `hulactl rotate-cookieless-salt` for emergency wipe

**Auth & accounts**
- **OPAQUE PAKE** for admin and operator passwords — passwords never travel the wire on login or rotation
- TOTP 2FA with at‑rest encryption
- OIDC SSO providers (Auth config) alongside internal password
- Per‑user, per‑server access roles (viewer / manager) for non‑admin operators

**Bot & abuse defense**
- Radix‑tree IP **badactor** scoring with TTL expiry, allowlist, and CIDR allowlist
- Scores HTTP probe paths (`/wp-login.php`, `/xmlrpc.php`, …), TCP protocol probes, and TLS handshake failures (no shared cipher, EOF, malformed handshake)
- ClickHouse audit row per incident; automatic block at threshold

**Mobile / notifications (Phase 5a)**
- Push via APNs (iOS) and FCM (Android) for operator alerts
- Email + push fan‑out with per‑recipient delivery accounting

**HA storage**
- Stage 1 — Storage interface seam separates ACL / goals / reports state from ClickHouse
- Stage 2 — **Single‑node Raft is the production storage default**; solo installs auto‑bootstrap with no `team:` block. Multi‑node clustering is configured via `team:` in `config.yaml` (see `HA_PLAN2.md`).

## Quick Start

Get a site running with automatic HTTPS on any Linux machine with Docker:

```bash
curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/install.sh | bash
```

This creates a `./hula` directory, pulls the Hula and ClickHouse Docker images, and starts both containers. You can customize with environment variables:

```bash
HULA_PORT=443 HULA_DIR=/opt/hula curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/install.sh | bash
```

### Prerequisites

- **Docker Engine 20.10+** — [Install Docker](https://docs.docker.com/engine/install/) or `curl -fsSL https://get.docker.com | sh`
- **Linux server** with a public IP (for ACME/Let's Encrypt)
- **Ports 80 and 443** reachable from the internet, either directly or through your proxy / firewall (port 80 is required for ACME HTTP‑01 certificate challenges)
- **DNS** A records pointing your domain to the server

### Getting Your Site Live with HTTPS

After running the installer, follow these steps to serve your site with automatic Let's Encrypt certificates:

**1. Point your DNS**

Create A records for your domain pointing to your server's public IP:

```
example.com      → 203.0.113.10
www.example.com  → 203.0.113.10
```

**2. Edit the config**

```bash
cd hula
nano config.yaml
```

First, set the admin password using OPAQUE PAKE registration (no plaintext password is ever sent to the server, even during setup):

```bash
# If installed via the quick-start installer:
HULACTL_NEW_PASSWORD='your-strong-password' ./hulactl set-password

# Or interactively (prompts for current + new password):
./hulactl set-password
```

`set-password` defaults to the `admin` user. The first run on a fresh install accepts an empty current password — pass `HULACTL_CURRENT_PASSWORD=''` to confirm.

> Legacy installs that still use `admin.hash` (argon2) can generate one with `./hulactl generatehash` and paste it into `config.yaml`. New installs should prefer OPAQUE.

Then a minimal `config.yaml` looks like:

```yaml
jwt_key: "change-me-to-something-random"

port: 443

ssl:
  acme:
    email: you@example.com
    cache_dir: /var/hula/certs

servers:
  - host: example.com
    aliases:
      - www.example.com
    id: mysite
    root: /var/hula/public

cors:
  allow_credentials: true

dbconfig:
  host: hula-clickhouse
  port: 9000
  user: hula
  pass: hula
  dbname: hula
```

**3. Add your site content**

Copy your static site (e.g., Hugo output) into the public directory:

```bash
cp -r /path/to/your/site/public/* ./public/
```

The `public/` directory maps to `/var/hula/public` inside the container.

**4. Open firewall ports**

Ports 80 and 443 must be reachable from the internet:

```bash
# Ubuntu/Debian with ufw
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp

# Or with firewalld
sudo firewall-cmd --permanent --add-service=http --add-service=https
sudo firewall-cmd --reload
```

**5. Restart with HTTPS ports exposed**

Stop the default instance and restart with ports 80 and 443:

```bash
./start-with-docker.sh --stop
HULA_PORT=443 ./start-with-docker.sh
```

Note: you also need port 80 exposed for ACME challenges. Edit `start-with-docker.sh` or run manually:

```bash
docker run -d \
  --name hula \
  --network hula-net \
  -p 443:443 -p 80:80 \
  -v "$(pwd)/config.yaml":/etc/hula/config.yaml:ro \
  -v "$(pwd)/hula_certs":/var/hula/certs \
  -v "$(pwd)/public":/var/hula/public:ro \
  --restart unless-stopped \
  ghcr.io/tlalocweb/hula:latest
```

**6. Visit your site**

Open `https://example.com` — Hula automatically obtains a Let's Encrypt certificate on the first request. The certificate is cached in `hula_certs/` and renews automatically.

### Management

```bash
cd hula

# View logs
./start-with-docker.sh --logs

# Stop everything
./start-with-docker.sh --stop

# Restart after config changes
./start-with-docker.sh --restart

# Pull and restart with latest image
./start-with-docker.sh --pull --restart

# Hot reload after editing config.yaml (no container restart)
./hulactl reload
```

### Docker Compose

For more control, see [DEPLOYMENT.md](DEPLOYMENT.md) which covers Docker Compose, backend containers, and Kubernetes deployments.

## Install CLI Tools

Install `hulactl` (the Hula management CLI) on Linux or macOS without building from source:

```bash
curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
```

This downloads the latest pre‑built `hulactl` binary for your platform and installs it to `~/.local/bin/`. You can customize the install location or pin a version:

```bash
# Install to /usr/local/bin
INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | sudo bash

# Pin a specific version
HULA_VERSION=v0.20.0-pre1 curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
```

### `hulactl` command reference

```text
Authentication & accounts
  auth [URL]              Authenticate against a hula server and store credentials
  authok                  Check if hulactl authentication is working
  logout                  Remove stored credentials from hulactl.yaml
  set-password            Set / rotate a password via OPAQUE PAKE registration
  generatehash            Generate an argon2 hash (legacy admin.hash flow)
  updateadminhash         Generate a hash and write it into the hula config file
  totp-key                Generate a TOTP encryption key for the config
  totp-setup              Set up TOTP for the admin user (interactive)
  opaque-seed             Generate base64url OPAQUE OPRF seed + AKE secret
  forget-opaque-record    EMERGENCY offline removal of an OPAQUE record from Bolt

Users & access
  createuser / modifyuser / deleteuser / listusers

Forms & landers
  createform / modifyform / submitform / deleteform / listforms
  createlander / modifylander / deletelander / listlanders

Site builds (production)
  build <server-id>            Trigger a site build and poll until complete
  build-status <build-id>      Show status of a build
  builds <server-id>           List recent builds

Staging
  staging-build <server-id>                  Rebuild in the long-lived staging container
  staging-update <server-id> <local> <path>  Upload one file via WebDAV
  staging-mount  <server-id> <folder> [--autobuild] [--dangerous]
                                              Live-sync a local folder to the staging site

Operations
  badactors                  List scored IPs with block status
  initdb / deletedb          Initialize / drop the analytics schema
  rotate-cookieless-salt     Replace the per-server cookieless visitor-id salt
  reload                     SIGHUP the running hula process to reload config
```

Run `hulactl` with no arguments for the full inline help.

## Building from Source

```bash
# Install development tools
./setup-dev.sh
source env.sh

# Build
make            # build hula server
make all        # build server + CLI tools (hulactl, setupdb)
make help       # show all targets
```

## Site Deployment from Git

Hula can pull and build a site directly from a Git repo on boot and on demand. The repo is cloned into a per‑server directory and (when `hula_build: production`) the deployed output is served from `deploy_dir`:

```yaml
servers:
  - host: example.com
    id: example
    root_git_autodeploy:
      repo: https://github.com/myorg/example-site.git
      creds:
        username: x-access-token
        password: ${GITHUB_TOKEN}
      ref:
        branch: main
      hula_build: production         # or 'staging'
      build_env:
        - HUGO_ENV=production
```

`hulabuild` (shipped with the builder image) understands Hugo by default. The `data_dir`, `deploy_dir`, `staging_dir`, and `staging_src_dir` keys default to `/var/hula/sitedeploy/{{serverid}}/...` so you only need to override them in unusual layouts.

Trigger an out‑of‑band rebuild any time:

```bash
hulactl build example
hulactl build-status <build-id>
hulactl builds example
```

### Live staging via WebDAV

When `hula_build: staging`, Hula keeps a long‑lived builder container around so you can edit the site as if it were a local folder:

```bash
# One-shot file upload
hulactl staging-update example /tmp/about.md content/about.md
hulactl staging-build example

# Live sync — runs until Ctrl-C; rebuilds automatically on every change
hulactl staging-mount example ./local-site --autobuild
```

`staging-mount` syncs both directions, debounces rapid edits, and refuses to upload executables or security‑sensitive files unless `--dangerous` is set.

## Forms & Landers

Forms (lead‑capture endpoints) and landers (campaign landing pages) are first‑class objects with versioned CRUD and Risor hooks. Create one with hulactl:

```bash
hulactl createform '{
  "name": "newsletter",
  "description": "Newsletter signup",
  "schema": "{\"fields\":[{\"name\":\"email\",\"required\":true}]}"
}'

hulactl listforms
hulactl submitform newsletter '{"email":"alice@example.com"}'
```

Landers follow the same shape (`createlander`, `listlanders`, `modifylander`, `deletelander`). Both fire optional Risor hooks declared in the server config — `on_new_form_submission`, `on_lander_visit`, `on_new_visitor` — so you can trigger custom enrichment, webhook fanout, or email without leaving the config.

## Privacy: Consent, Forwarders, and Cookieless

### Consent gating (Phase 4c.1)

```yaml
servers:
  - host: example.com
    consent_mode: opt_in    # off (default) | opt_in | opt_out
```

- `off` — analytics events always written; marketing‑tagged events still respect `Sec-GPC: 1`.
- `opt_in` — no event row is written until the client supplies affirmative consent. `/v/hello` returns `204` with `Hula-Consent-Required: 1` so an embedding CMP can react.
- `opt_out` — events always written, consent flags reflect state at write time.

### Server‑side forwarders (Phase 4c.2)

Forward completed visitor / conversion events server‑to‑server to ad platforms (no client‑side beacons, no third‑party cookies):

```yaml
servers:
  - host: example.com
    forwarders:
      - kind: meta_capi
        pixel_id: "1234567890"
        access_token: ${META_CAPI_TOKEN}
        purpose: marketing
      - kind: ga4_mp
        measurement_id: "G-XXXXXXXX"
        api_secret: ${GA4_API_SECRET}
        purpose: analytics
```

Each adapter is consent‑gated by its declared `purpose`; events whose consent flag is `false` for that purpose are silently dropped before they leave Hula.

### Cookieless mode (Phase 4c.3)

```yaml
servers:
  - host: example.com
    tracking_mode: cookieless
```

In cookieless mode, no cookies are set. The visitor ID is derived per request via `HMAC(per-server salt || YYYYMMDD, IP || UA)`. Same‑day visitors remain recognisable; cross‑day stitching is impossible by design — the documented answer to "I want analytics without a cookie banner."

Rotate the per‑server salt at any time (Hula must be stopped first; Bolt is single‑writer):

```bash
hulactl --bolt /var/hula/data/hula.db rotate-cookieless-salt example
```

## Live Chat (Phase 4b)

When `chat:` is omitted from `config.yaml`, visitor chat is still enabled with default retention, captcha, and email‑verifier knobs. Operators connect through the admin UI; visitors connect through the public `/chat/start` endpoint over WebSocket. Bad‑actor scoring applies at chat start (rate‑limit + abuse signals share the radix tree with HTTP probes).

```yaml
chat:
  retention_days: 30
  captcha_provider: turnstile     # or 'recaptcha' | 'none'
```

## Mobile Push & Operator Alerts (Phase 5a)

Operator alert and report dispatch fans out across email + APNs + FCM. Push is optional — when creds are missing, those channels degrade silently:

```yaml
mailer:
  smtp_host: smtp.example.com
  smtp_port: 587
  smtp_user: ${SMTP_USER}
  smtp_pass: ${SMTP_PASS}
  from: alerts@example.com

apns:
  team_id: ABCDE12345
  key_id:  KEY1234567
  bundle_id: us.tlaloc.hulaadmin
  key_pem: /etc/hula/apns.p8

fcm:
  service_account_json: /etc/hula/fcm.json
```

## Bot & Abuse Defense

Hula scores suspicious activity into a radix‑tree of IPs (TTL‑expired entries fall off automatically). The default scorer covers:

- Known WordPress / vuln‑probe paths (`/wp-login.php`, `/xmlrpc.php`, `.env`, `.git/`, …)
- TCP protocol probes (HTTPS port hit with non‑HTTP/non‑TLS bytes)
- TLS handshake failures (no shared cipher, EOF, malformed `ClientHello`)

```bash
hulactl badactors        # list scored IPs with block status

# Allowlist your office in config.yaml:
badactor:
  allow_cidrs:
    - 198.51.100.0/24
  block_threshold: 50
```

ClickHouse keeps the audit log; the in‑memory scorer is what gates traffic.

## High Availability

- **Stage 1** introduces a `Storage` interface seam — non‑analytics state (ACL, goals, reports, OPAQUE records, etc.) lives behind it.
- **Stage 2** makes single‑node Raft (`hashicorp/raft` + raft‑boltdb) the production default. Solo installs auto‑bootstrap a `TeamID` + `NodeID` under `data_dir` on first boot — no `team:` block is required.
- Multi‑node clustering is opt‑in via the `team:` block; see `HA_PLAN2.md` for membership and join workflows.

## TLS / SSL

Hula supports TLS three ways: manual certificates, Cloudflare Origin CA, or automatic Let's Encrypt via ACME.

### Manual Certificates

Provide cert and key as file paths or inline PEM data:

```yaml
servers:
  - host: example.com
    port: 443
    ssl:
      cert: /path/to/cert.pem
      key: /path/to/key.pem
```

### Automatic Certificates with Let's Encrypt (ACME)

Hula can automatically obtain and renew TLS certificates from Let's Encrypt using the ACME protocol.

```yaml
servers:
  - host: example.com
    port: 443
    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs
```

**Configuration options:**

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `email` | Recommended | — | Contact email for the ACME account. Let's Encrypt uses this for expiry notices. |
| `cache_dir` | No | `certs` | Directory to cache certificates. Supports `{{confdir}}` variable substitution. |
| `domains` | No | Host + Aliases | Explicit list of domains. If omitted, derived from the server's `host` and `aliases`. |
| `http_port` | No | `80` | Port for the HTTP‑01 challenge listener. Change this when port 80 is handled by a reverse proxy that forwards to an alternate port. |

**How it works:**

- Certificates are automatically obtained on first request and cached to disk in `cache_dir`.
- Renewal happens automatically before expiry.
- An HTTP listener starts on port 80 to handle ACME HTTP‑01 challenges. All other HTTP traffic on port 80 is redirected to HTTPS.
- Multiple servers sharing the same listener port can each use ACME — their domains are merged into a single ACME manager.
- ACME and manual certificates can coexist on different servers, even on the same port.

**Requirements:**

- Port 443 must be reachable from the internet (for serving the certificate).
- Port 80 (or the configured `http_port`) must be reachable from the internet (for HTTP‑01 challenge validation). When behind a reverse proxy like nginx, set `http_port` to the internal port and configure the proxy to forward `/.well-known/acme-challenge/` traffic to it.
- DNS for all configured domains must point to the server.
- The `cache_dir` must be writable and should be on persistent storage (so certs survive restarts).

## Backend Containers

Hula can manage Docker containers as backend services and reverse‑proxy requests to them. Each virtual server can have one or more backends:

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
```

Backends on different virtual servers are isolated on separate Docker networks. Backends on the same server can reach each other. When running Hula in Docker, mount the Docker socket: `-v /var/run/docker.sock:/var/run/docker.sock`.

## API

Postman examples [here](https://www.getpostman.com/collections/0e83876e0f2a0c8ecd70).


## License and Terms of Use

Copyright © 2026 Tlaloc LLC

This project is made available under a dual‑license model: **AGPLv3** and **SSPL‑1.0**.

The full text of the SSPL‑1.0 is provided in `SSPL1_0_LICENSE.md`.

The full text of the AGPLv3 is provided in `LICENSE.md`.

For ordinary self‑hosted use, including use by an individual, an employer, a contractor on behalf of a client, or a non‑profit organization, this project may be used under the terms of the **AGPLv3**. Running this software as a web server for your own organization, employer, client, or non‑profit may be done under the **AGPLv3**.

However, offering this software, or substantially similar functionality derived from it, as a web hosting, managed hosting, multi‑tenant hosting, cloud hosting, SaaS, or similar service for third parties is licensed under the **SSPL‑1.0**, and not the AGPLv3.

Additionally, if you use this software as part of a high‑availability, clustered, failover, replicated, load‑balanced, or multi‑node service environment, then your use is licensed under the **SSPL‑1.0**, unless you have obtained a separate commercial license.

You may not use this software, its source code, documentation, examples, architecture, APIs, configuration files, tests, or other project materials as training data, prompts, examples, templates, reference material, retrieval‑augmented context, or other input to an artificial intelligence or machine‑learning system for the purpose of generating, deriving, reproducing, or developing software that is intended to replace, compete with, or avoid the licensing obligations of this project. Any such use is outside the scope of the AGPLv3 license grant and the SSPL‑1.0 license grant and requires a separate commercial license.

**Commercial licenses are also available**, including for high‑availability deployments, managed‑service use, proprietary integrations, OEM distribution, AI‑assisted development rights, or other uses requiring different terms. Contact: `<licensing@tlaloc.us>`.
