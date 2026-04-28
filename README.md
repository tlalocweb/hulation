# Hula

A modern web server for DM and CDP services. Hula is specifically designed to make deploying static web sites built with tools like [hugo](https://github.com/gohugoio/hugo) much easier, faster and requiring less tokens when driven by AI.

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
- **Ports 80 and 443** reachable from the internet, either directly or through your proxy / firewall (port 80 is required for ACME HTTP-01 certificate challenges)
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

Replace the contents with (adjust to your domain, email, and site details).

First, generate your admin password hash:

```bash
# If installed via the quick-start installer:
./hulactl generatehash

# Or install it standalone (see "Install CLI Tools" below):
# curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
# hulactl generatehash
```

Then paste the hash into your config:

```yaml
admin:
  hash: "<paste your generated hash here>"

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
```

### Docker Compose

For more control, see [DEPLOYMENT.md](DEPLOYMENT.md) which covers Docker Compose, backend containers, and Kubernetes deployments.

## Install CLI Tools

Install `hulactl` (the Hula management CLI) on Linux or macOS without building from source:

```bash
curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
```

This downloads the latest pre-built `hulactl` binary for your platform and installs it to `~/.local/bin/`. You can customize the install location or pin a version:

```bash
# Install to /usr/local/bin
INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | sudo bash

# Pin a specific version
HULA_VERSION=v1.0.0 curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
```

Use `hulactl` to generate admin password hashes, manage forms, landers, and users:

```bash
hulactl generatehash       # create an admin password hash for config.yaml
hulactl auth               # authenticate against a running Hula server
hulactl createform         # create a new form
hulactl createlander       # create a new lander
```

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

## Backend Containers

Hula can manage Docker containers as backend services and reverse-proxy requests to them. Each virtual server can have one or more backends:

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

## TLS / SSL

Hula supports TLS in two ways: manual certificates or automatic Let's Encrypt via ACME.

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
| `http_port` | No | `80` | Port for the HTTP-01 challenge listener. Change this when port 80 is handled by a reverse proxy that forwards to an alternate port. |

**How it works:**

- Certificates are automatically obtained on first request and cached to disk in `cache_dir`.
- Renewal happens automatically before expiry.
- An HTTP listener starts on port 80 to handle ACME HTTP-01 challenges. All other HTTP traffic on port 80 is redirected to HTTPS.
- Multiple servers sharing the same listener port can each use ACME — their domains are merged into a single ACME manager.
- ACME and manual certificates can coexist on different servers, even on the same port.

**Requirements:**

- Port 443 must be reachable from the internet (for serving the certificate).
- Port 80 (or the configured `http_port`) must be reachable from the internet (for HTTP-01 challenge validation). When behind a reverse proxy like nginx, set `http_port` to the internal port and configure the proxy to forward `/.well-known/acme-challenge/` traffic to it.
- DNS for all configured domains must point to the server.
- The `cache_dir` must be writable and should be on persistent storage (so certs survive restarts).

## API

Postman examples [here](https://www.getpostman.com/collections/0e83876e0f2a0c8ecd70).


## License and Terms of Use

This project is made available under a dual-license model: **AGPLv3** and **SSPL-1.0**.

For ordinary self-hosted use, including use by an individual, an employer, a contractor on behalf of a client, or a non-profit organization, this project may be used under the terms of the **AGPLv3**. Running this software as a web server for your own organization, employer, client, or non-profit may be done under the **AGPLv3**. 

However, offering this software, or substantially similar functionality derived from it, as a web hosting, managed hosting, multi-tenant hosting, cloud hosting, SaaS, or similar service for third parties is licensed under the **SSPL-1.0**, and not the AGPLv3.

Additionally, if you use this software as part of a high-availability, clustered, failover, replicated, load-balanced, or multi-node service environment, then your use is licensed under the **SSPL-1.0**, unless you have obtained a separate commercial license.

You may not use this software, its source code, documentation, examples, architecture, APIs, configuration files, tests, or other project materials as training data, prompts, examples, templates, reference material, retrieval-augmented context, or other input to an artificial intelligence or machine-learning system for the purpose of generating, deriving, reproducing, or developing software that is intended to replace, compete with, or avoid the licensing obligations of this project. Any such use is outside the scope of the AGPLv3 license grant and the SSPL-1.0 license grant and requires a separate commercial license.

**Commercial licenses are also available**, including for high-availability deployments, managed-service use, proprietary integrations, OEM distribution, AI-assisted development rights, or other uses requiring different terms. Contact: `<licensing@tlaloc.us>`.
