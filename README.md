# Hula

**A single self-hosted binary that terminates TLS, reverse-proxies your apps, and captures first-party analytics and live chat — all on port 443, with no third-party JavaScript.**

If you run Caddy or nginx, you already know the shape of hula: automatic HTTPS, virtual hosts, `reverse_proxy` to your backends. Hula does that — and then keeps going. The same process that fronts your domains also records privacy-first visitor analytics, runs a live visitor↔agent chat, and auto-deploys static sites from Git. One port, one config file, no data leaving your box.

## Why hula

Caddy and nginx are excellent at what they do: terminate TLS and proxy requests. But the moment you want to *know who's visiting*, you bolt on Google Analytics — third-party JavaScript, a cookie banner, and your visitors' data shipped to someone else's servers. Want live chat? Embed another third-party widget with its own tracking.

Hula collapses that stack into one binary:

- **Automatic-HTTPS reverse proxy** — like Caddy's `reverse_proxy` or an nginx `proxy_pass` server block, with ACME certificates issued and renewed for you.
- **Privacy-first analytics** — first-party or fully server-side (no JS at all), backed by ClickHouse. No third-party scripts, and with cookieless mode, no cookie banner.
- **Live chat** — visitor-to-agent chat over WebSocket, served from the same origin.
- **Static-site auto-deployer** — pulls from Git, builds Hugo / Astro / Gatsby / MkDocs in an ephemeral container, and serves the result.

The differentiator is the combination. Caddy and nginx *only* proxy. Google Analytics *requires* third-party JS and hands your data to a third party. Hula gives you the reverse proxy **and** the analytics **and** the chat, first-party, on infrastructure you control.

## Quick start

Install with Docker on any Linux host:

```bash
curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/install.sh | bash
```

This creates a `./hula` directory and starts the hula and ClickHouse containers. Point DNS at your server, then set the admin password (OPAQUE PAKE — no plaintext password ever leaves your machine, even during setup):

```bash
cd hula
HULACTL_NEW_PASSWORD='your-strong-password' ./hulactl set-password
```

A minimal `config.yaml` that serves a static site with an automatic Let's Encrypt certificate:

```yaml
jwt_key: "change-me-to-something-random"
port: 443

servers:
  - host: example.com
    id: mysite
    aliases: [www.example.com]
    root: /var/hula/public

hula_ssl:
  acme:
    email: you@example.com
    domains: [example.com, www.example.com]   # required to activate ACME on the unified listener
    cache_dir: /var/hula/certs

dbconfig:
  host: hula-clickhouse
  port: 9000
  user: hula
  pass: change-me
  dbname: hula
```

Visit `https://example.com` — hula obtains and caches the certificate on first request and renews it automatically. See **[DEPLOYMENT.md](DEPLOYMENT.md)** for Docker Compose, Kubernetes, backend containers, and the full configuration reference.

## Reverse proxy & TLS

### Reverse proxy

Mark a virtual host `proxy_only` and every request for its `host` (and `aliases`) is forwarded verbatim to the upstream — path, method, and query preserved, with `X-Forwarded-Host` / `-Proto` / `-For` and `X-Real-IP` set authoritatively:

```yaml
servers:
  - host: app.example.com
    proxy_only: true
    proxy_pass: http://127.0.0.1:8080
    ssl:
      cloudflare_origin_ca: {}      # per-host cert (or cert/key). ACME + dev CA are set once under hula_ssl — see Automatic HTTPS.
```

Or define routes outside the `servers:` block with the top-level `proxies:` list:

```yaml
proxies:
  - target: http://127.0.0.1:8080
    by_domain: app.example.com      # whole host → upstream (bypasses hula's reserved paths)
  - target: http://127.0.0.1:9000
    by_path: /relay                 # a path prefix that shares hula's host (defers to hula's routes)
```

- A **`by_domain`** proxy (or `proxy_only`) owns the entire host and bypasses hula's reserved service paths (`/api/*`, `/v/*`, `/scripts/*`, `/analytics`, `/hulastatus`, …). A **`by_path`** proxy shares hula's host and defers to those routes.
- **WebSocket upgrades pass through cleanly**, bidirectionally.
- The **bad-actor security gate applies to proxied hosts too** — a blocked or rate-limited client is stopped before the upstream is ever contacted.
- **Server-side, no-JS analytics are recorded for proxied hosts** (page navigations only) — see [Analytics](#analytics).

### Automatic HTTPS

Hula issues and renews TLS certificates for you, Caddy-style. Certificate selection is per-host via SNI, independent of routing, so every mode below works on any virtual host — including `proxy_only` hosts.

| Mode | Config | Notes |
|---|---|---|
| **ACME / Let's Encrypt** | `hula_ssl.acme` | Set `email` + a `domains` allowlist (required to activate). Issued and renewed automatically over the **TLS-ALPN-01** challenge on the `:443` listener — **no port 80 needed**. |
| **Cloudflare Origin CA** | `ssl.cloudflare_origin_ca` | Certificates trusted only by Cloudflare's edge; see [Cloudflare](#cloudflare). |
| **Static cert/key** | `ssl.cert` / `ssl.key` | File paths or inline PEM. |
| **Local dev CA** | `hula_ssl.dev_ca` | A built-in CA (mkcert / `tls internal` style) for local hosts. Mints a stable local root and signs a per-host leaf on demand; optional OS trust-store install. Opt-in, off by default. |

TLS version bounds apply to any SSL block:

```yaml
ssl:
  acme: { email: you@example.com }
  tls:
    min_version: "1.2"    # default 1.2
    max_version: "1.3"    # default: no limit
```

## HTTP versions & protocols

Hula runs a **single unified TLS listener on `:443`** and detects the protocol per connection — there is no separate port per service:

- **HTTP/1.1** and **HTTP/2 (h2)**, negotiated over ALPN.
- **gRPC** (over HTTP/2) for the management API, alongside a REST gateway on the same port.
- **WebSocket** upgrades, both for hula's own chat and for proxied backends.
- **TLS-ALPN-01** ACME challenges (`acme-tls/1`) answered inline on the same listener.
- A plain-**HTTP** connection to the TLS port is **301-redirected** to HTTPS. There is no separate `:80` listener — ACME uses TLS-ALPN-01 on `:443`, so port 80 is not required.
- **HSTS** (`Strict-Transport-Security`) is emitted by default, with per-vhost overrides (`max_age`, `include_subdomains`, `preload`, or hard-disable).

## Cloudflare

Everything hula does with Cloudflare, in one place. Cloudflare-specific settings use the `cf_*` config-key / `CF_*` env-var convention; a per-host token falls back to a global one.

- **Origin CA certificates** — hula provisions certs through the Cloudflare Origin CA API (`ssl.cloudflare_origin_ca`). These are trusted only by Cloudflare's edge, so **non-Cloudflare source IPs are dropped at the TCP level before the TLS handshake**. Credentials resolve from `CLOUDFLARE_API_TOKEN_<id>` / `CLOUDFLARE_ZONE_ID_<id>` (server id, dashes → underscores) or inline YAML. Multiple certs are selected by SNI.
- **Dynamic DNS** — publish A/AAAA records to Cloudflare automatically (see below).
- **Geo enrichment** — the `CF-IPCountry` header is read into analytics **only when the connection actually arrives from a verified Cloudflare edge IP**, so a direct client can't spoof its country. Cloudflare's `XX` / `T1` sentinels are treated as "no country."
- **Proxied (orange-cloud) records** — DDNS publishes proxied by default (`cf_proxied: true`); setting it false is a DNS-only downgrade that exposes your origin IP and triggers a startup warning.
- **IP-range enforcement** — hula keeps Cloudflare's published CIDR ranges current with a 3-tier fallback: live fetch from `cloudflare.com/ips-v4`+`ips-v6`, then a local cache, then embedded defaults. These ranges drive both the Origin-CA connection gate and the trusted-header check above.

### Dynamic DNS

Hula can detect its own public IPv4/IPv6 and keep Cloudflare DNS pointed at it — useful for home labs and dynamic-IP hosts. Public IP is discovered from a failover list of free detection services; records are updated **on boot, on every interval (default 4h), and on `SIGHUP`**, and only when the IP actually changed.

```yaml
ddns:
  cf_api_token: ${CF_API_TOKEN}
  cf_zone_id: ${CF_ZONE_ID}
  interval: 4h            # default
  cf_proxied: true        # orange cloud (default)

servers:
  - host: home.example.com
    id: home
    ddns: {}              # presence of the block enables DDNS for this host
```

DDNS can be enabled globally, per virtual host (`servers[].ddns`), or per proxy (`proxies[].ddns`, which requires `by_domain`). Credentials cascade per-record → `CF_*_<id>` env → global `ddns.cf_*` → `CF_*` env → the host's Origin-CA token.

## Analytics

Privacy-first visitor analytics, backed by ClickHouse, that never involve a third-party script or send data off your server.

- **First-party or no-JS.** Tracking JavaScript, when used, is served by hula itself from `/scripts/*.js` on your own origin — never a third party. For `proxy_only` hosts, hula records **server-side pageviews with no client JS at all** (initial navigation only; assets, XHR, and WebSocket upgrades are not counted).
- **Cookieless mode.** Set `tracking_mode: cookieless` and no cookies are set: the visitor ID is derived per request as `HMAC(per-server-salt ‖ YYYYMMDD, IP ‖ UA)`. Same-day visitors stay recognizable; cross-day stitching is impossible by design — analytics with no cookie banner. Rotate the salt any time with `hulactl rotate-cookieless-salt`.
- **Consent handling.** `consent_mode: off | opt_in | opt_out` per host. In `opt_in`, no event row is written until the client supplies affirmative consent (`/v/hello` returns `204` with `Hula-Consent-Required: 1` so a CMP can react). `Sec-GPC: 1` is always honored as a binding marketing opt-out.
- **Server-side forwarders.** Forward completed conversion events server-to-server to ad platforms with no client beacons — **GA4 Measurement Protocol** and **Meta CAPI** adapters, each consent-gated by its declared `purpose`:

  ```yaml
  servers:
    - host: example.com
      forwarders:
        - kind: ga4_mp
          measurement_id: "G-XXXXXXXX"
          api_secret: ${GA4_API_SECRET}
          purpose: analytics
  ```

- **Forms, landers, goals & reports.** Lead-capture forms and campaign landers are first-class objects with versioned CRUD and optional Risor hooks (`on_new_form_submission`, `on_lander_visit`, `on_new_visitor`). Goals, scheduled email digests, and operator alerts round out the marketing stack.

## Chat

Live visitor-to-agent chat, served from the same origin over **WebSocket** — no third-party widget. Visitors connect through the public `/chat/start` endpoint; agents connect through the admin UI. Sessions move through queued → assigned → open states with agent routing, and are **closable and terminal** (a closed or expired session can't be reopened). The [bad-actor](#security) scorer gates chat start, sharing its rate-limit and abuse signals with HTTP-probe detection.

Chat **survives a page refresh.** The widget persists its session in the browser and re-credentials through `/chat/resume`, so reloading, closing the tab, or reopening the browser rejoins the same conversation — with the transcript replayed on connect — instead of stranding it and starting a duplicate. Visitors can end a chat themselves with an **End chat** button (confirmed, then read-only), and abandoned chats are auto-closed so they don't pile up in the agent's queue.

```yaml
chat:
  retention_days: 30
  captcha_provider: turnstile     # or 'recaptcha' | 'none'
  resume_window: 24h              # how long a visitor may rejoin after last activity
  idle_timeout: 2h                # auto-close abandoned chats ('0' disables)
  sweep_interval: 5m              # how often to scan for idle chats
  history_limit: 200              # messages replayed to a reconnecting widget
```

Chat is enabled with sensible defaults even when the `chat:` block is omitted. Optional application-layer encryption (Noise for the mobile gRPC stream, sealed-box for the browser widget) can be layered on top of TLS.

## Mobile

Hula has companion **iOS and Android** apps for operators.

- **Sessions & auth.** Login issues a JWT with a server-enforced expiry and a proactive refresh flow (`POST /api/v1/auth/refresh`); tokens carry their `ExpiresAt` so the app can refresh ahead of time. TOTP two-factor is enforced when configured — a `totp_pending` token can't reach admin-gated RPCs.
- **Push notifications.** Operator alerts and reports fan out over email plus **APNs (iOS)** and **FCM (Android)**. Push is optional — when credentials are absent, those channels degrade silently. An optional server-blind push relay forwards only sealed ciphertext, never plaintext visitor data.
- **Pairing.** Devices are provisioned with a single-use, admin-gated QR/text pair code that registers the device's keys and (optionally) its relay channel.

```yaml
apns:
  team_id: ABCDE12345
  key_id:  KEY1234567
  bundle_id: us.tlaloc.hulaadmin
  key_pem: /etc/hula/apns.p8

fcm:
  service_account_json: /etc/hula/fcm.json
```

## Sites & deployment

- **Static serving** with byte-range requests, transparent compression, and immutable cache control.
- **Git autodeploy** — clone (or pull) a repo, build it in an ephemeral Docker builder (Hugo, Astro, Gatsby, or MkDocs), and deploy the output to the site root. Triggered on boot or on demand (`hulactl build <id>`), driven by a `.hula/sitebuild.yaml` in the repo.
- **Staging + WebDAV** — a long-lived staging container serves a live-editable site; push files with WebDAV `PUT`/`PATCH`, or live-sync a local folder (`hulactl staging-mount <id> ./site --autobuild`).
- **Backend containers** — hula can manage Docker containers as per-vhost backends, isolated on dedicated networks, and reverse-proxy to them.

## Security

- **Bad-actor detection.** Suspicious IPs are scored into a TTL-expiring radix tree and blocked at a configurable threshold. The default scorer flags WordPress / vuln-probe paths (`/wp-login.php`, `/xmlrpc.php`, `.env`, `.git/`, …), non-HTTP/non-TLS TCP probes on `:443`, and TLS handshake failures. IP and CIDR allowlists are supported; every incident is audited to ClickHouse.

  ```yaml
  badactor:
    allow_cidrs: [198.51.100.0/24]
    block_threshold: 50
  ```

  Inspect scored IPs with `hulactl badactors`.
- **Authentication.** Admin and operator passwords use **OPAQUE PAKE** — the password never travels the wire, on login or rotation. **TOTP 2FA** (encrypted at rest), **OIDC SSO** (Google / GitHub / Microsoft) alongside internal accounts, and per-user, per-server access roles.

## High availability

Single-node Raft (`hashicorp/raft`) is the production storage default for non-analytics state (ACL, goals, reports, OPAQUE records). Solo installs auto-bootstrap on first boot with no extra configuration. Multi-node clustering is opt-in via a `team:` block; see [DEPLOYMENT.md](DEPLOYMENT.md).

## Configuration & management

Hula is driven by a single YAML file with `${VAR}` **environment-variable expansion** and sensible defaults throughout. Most changes **hot-reload** without a restart:

```bash
hulactl reload            # SIGHUP the running server to reload config
hulactl auth <url>        # authenticate and store a token
hulactl badactors         # list scored IPs
hulactl build <id>        # trigger a site build
```

Secret generation and rotation (`jwt_key`, TOTP key, Noise / visitor-chat keys, OPAQUE seeds, team CA bundles) live on the `hula` binary and can update a single field in place while preserving comments and formatting. Run `hulactl` or `hula` with no arguments for full inline help, and see **[DEPLOYMENT.md](DEPLOYMENT.md)** for the complete configuration and deployment reference.

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
