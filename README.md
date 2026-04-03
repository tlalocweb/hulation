# Hula

A modern web server for DM and CDP services. Hula is specifically designed to make deploying static web sites built with tools like [hugo](https://github.com/gohugoio/hugo) much easier, faster and requiring less tokens when driven by AI. 

Postman examples [here](https://www.getpostman.com/collections/0e83876e0f2a0c8ecd70).

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

**Example with explicit domains:**

```yaml
servers:
  - host: example.com
    port: 443
    aliases:
      - www.example.com
    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs
        domains:
          - example.com
          - www.example.com
```

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

**Docker example:**

```yaml
servers:
  - host: mysite.example.com
    port: 443
    ssl:
      acme:
        email: admin@example.com
        cache_dir: /var/hula/certs
```

When running in Docker, make sure to:
- Expose ports 80 and 443: `docker run -p 80:80 -p 443:443 ...`
- Mount a persistent volume for the cert cache: `-v hula-certs:/var/hula/certs`
