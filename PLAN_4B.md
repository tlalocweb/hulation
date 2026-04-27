# Phase 4b — Visitor Chat APIs (detailed plan)

A late addition between Phase 4 (UI + alerts) and Phase 5a (mobile +
push). Phase 4b adds a visitor-facing chat surface to hula's API
layer. Hula provides protected APIs only; the website embedding the
chat owns the widget UI. The hula admin SPA gains a Chats tab to
read history and (in a later phase) join sessions live.

The first consumer is `../tlalocwebsite` — we'll ship a HubSpot-style
popup widget there as the reference integration and the e2e test
target.

Related docs: `PLAN_OUTLINE.md`, `PHASE_4_STATUS.md`, `UI_PRD.md`
(extended in stage 4b.6 with §8 Chat).

---

## 1. Context and scope

### 1.1 What Phase 0–4 already delivered that this phase reuses

- **Visitor identity** — `hello.js` fingerprints visitors (cookie +
  bounce id) and writes per-page `events` rows tagged with
  `belongs_to=<visitor_id>`. Phase 4b's chat sessions key on the
  same `visitor_id`, so the existing visitor detail page
  (`/analytics/visitors/[id]`) gains a "Conversations" section for
  free.
- **WebSocket plumbing** — `gorilla/websocket v1.5.3` is already a
  direct dep. `server/mobile_ws.go` is the working pattern to imitate
  (upgrade → bearer-validate → register subscriber → 25 s ping/60 s
  idle close → drop slow writers). `pkg/realtime/hub.go` shows the
  in-process fan-out style.
- **Admin auth + ACL** — OPAQUE login (Phase 3) issues admin JWTs
  used by `authware.AdminBearerInterceptor`. `pkg/store/bolt`'s
  `server_access` bucket scopes admins to a server-id list. The
  Chats RPCs reuse this without changes.
- **Per-server config** — `config.Server{Host, Aliases, ID}` is what
  the chat token-issue handler needs to recognise which server a
  request came from.
- **Mailer + alerts** — Phase 4's mailer sends notification emails on
  alert fire. Phase 4b reuses it for "you have a new chat message"
  notifications to the configured `agent_email` (out of scope to
  ship in this phase; doc and stub).
- **ClickHouse migrations** — The runner at
  `pkg/store/clickhouse/migrations` applies numbered `.sql` files
  once. Chat-related schema lands as `0003_chat_v1.sql`.
- **AfterShip email-verifier** — not in deps yet; this phase adds
  `github.com/AfterShip/email-verifier` and uses it server-side at
  token-issue time so we don't have to send a verification email.
- **OpenAI classifier (tlalocwebsite)** — the existing
  `tlalocwebsite/backend` already calls OpenAI via a small
  `internal/classifier` wrapper for contact-form spam detection.
  Phase 4b's optional model-based Turing test (server-side, in hula)
  uses the same vendor + similar prompt pattern. The hula side
  doesn't import the tlaloc code; it has its own thin OpenAI client
  configured by the operator.

### 1.2 What Phase 4b must deliver

**Hula-side (this is where the work lives):**

- Two new ClickHouse tables — `chat_sessions`, `chat_messages` —
  with FTS indexes on `chat_messages.content` (token+ngram bloom
  filters; doc the upgrade path to ClickHouse `text` index).
- `chat.proto` defining `ChatService` (Public + Admin halves) and
  the Bolt-stored chat metadata.
- Token issuance endpoint
  (`POST /api/v1/chat/start`) — gates on Turnstile/reCAPTCHA →
  optional OpenAI moderation → AfterShip email-verifier →
  per-IP/per-visitor rate limit → returns a short-lived chat-session
  JWT bound to `(server_id, visitor_id, session_id, email)`.
- Visitor WebSocket endpoint (`WS /api/v1/chat/ws`) — token-gated;
  visitor↔server bidirectional message frames; every accepted
  frame is persisted to `chat_messages` synchronously before the
  hub fans it out. Slow-client policy mirrors `mobile_ws.go`.
- Admin RPCs — `ListSessions`, `GetMessages`, `SearchMessages`,
  `CloseSession`, plus `PostAdminMessage` (REST async write path
  used by the SPA for optimistic UI and as a fallback when the
  admin's WebSocket isn't open).
- **Admin live-agent WebSocket** —
  `WS /api/v1/chat/admin/agent-ws?session_id=...`. Upgrades an
  admin JWT into a real-time bidirectional connection joined to a
  specific session. Multiple admins may share a session (handy for
  senior-observing-junior, or live handoff). Visitor and agent
  see each other's typing indicators + presence frames ("agent
  connected", "agent left"). Auth = admin JWT + the same
  `server_access` ACL the admin REST RPCs use; an admin without
  ACL for the session's `server_id` is rejected at upgrade.
- **Agent control WebSocket + next-available routing** —
  `WS /api/v1/chat/admin/agent-control-ws?server_id=...`. Each
  admin who is "ready to take chats" holds one of these open
  (the SPA's `/chats/live` page opens it on mount). Holding a
  control-WS = "available". When a new visitor session starts,
  hula picks the next-available agent by per-server round-robin,
  sets `chat_sessions.assigned_agent_id`, and pushes a
  `session_assigned` frame down that agent's control-WS so the
  SPA can highlight + auto-open the session. If no agent is
  available, the session is queued (`assigned_agent_id` empty,
  visitor sees "Connecting you with an agent…"); the queue is
  drained as agents connect. Round-robin only — no skill-based
  routing or SLA timers in 4b.
- Hula admin SPA — new top-level `Chats` tab with two sub-routes:
  - `/analytics/chats` — All chats (paginated table; filters: from/to,
    server_id, email contains, q=FTS).
  - `/analytics/chats/live` — Live chats (sessions with `status=open`
    and a connected visitor; auto-refreshes every 5 s like the
    Realtime page).
  - Click-into a session opens a thread view with the full message
    timeline + the admin-message compose box.
- Visitor-detail enrichment — the `/visitors/[id]` page gains a
  "Conversations" section listing chat sessions and counts.
- E2e suite **29 — chat** that drives the full flow against a
  freshly-seeded hula (Turnstile mocked, OpenAI mocked, real
  ClickHouse).
- `PHASE_4B_STATUS.md` and a one-paragraph mention in
  `PLAN_OUTLINE.md` so the phase shows up in the roadmap.

**Test website (`../tlalocwebsite`):**

- A drop-in chat widget (HubSpot-style bubble bottom-right; full
  screen on narrow viewports). Implemented as a Hugo partial +
  small TypeScript module that calls the new hula APIs. Uses
  Cloudflare Turnstile (matches what tlalocwebsite already issues
  site keys for; see `TURNSTILE_PRODUCTION_SITE_KEY` in the deploy
  config).
- Wired into `themes/tlaloc/layouts/_default/baseof.html` next to
  the existing `<iframe>` analytics embed, behind a
  `params.chat_enabled` toggle so non-chat pages can opt out.
- Server-side rendered email + first-message form before the WS
  opens; once the user passes Turnstile + email validation, the
  client opens a WS and the bubble switches to live mode.

**Out of scope (deferred):**

- **Skill-based routing / SLA timers / per-agent capacity caps.**
  4b ships round-robin next-available only. No "route accounting
  questions to alice", no "fire alarm if visitor waits > 5 min",
  no "limit bob to 3 concurrent chats". Future phase.
- **Bot auto-responder.** Optional; documented as a future
  pluggable "responder" interface. Out of 4b.
- **Multi-process broadcast.** The hub is in-process (single hula
  binary). Multi-tenant horizontal scaling needs Redis/NATS pub/sub
  — deferred to whenever we run more than one hula container.
- **File / image attachments.** Text-only in 4b.
- **End-to-end encryption.** Chat messages live in ClickHouse in
  plaintext; FTS depends on it. The transport is HTTPS/WSS — that
  is the security boundary.
- **Push notifications to admins for new chats.** Mailer email is
  noted but not wired this phase.

---

## 2. Architecture overview

```
                   ┌──────────────────────────────────┐
                   │ Visitor browser (tlalocwebsite)  │
                   │   chat.ts + Turnstile widget     │
                   └─────┬─────────────────────┬──────┘
        POST /chat/start │                     │ WS /chat/ws?token=...
        (turnstile,email,│                     │
         visitor_id)     │                     │
                         ▼                     ▼
   ┌──────────────────────────────────────────────────────────────────┐
   │                       hula (Go)                                  │
   │  ┌──────────────┐   ┌──────────────┐    ┌────────────────────┐  │
   │  │ /chat/start  │──▶│   Router     │◀──▶│   ChatHub          │  │
   │  │   guards     │   │  ready ring  │    │   per-session      │  │
   │  │ (Turnstile,  │   │  + FIFO queue│    │   1 visitor +      │  │
   │  │  email ver,  │   │  per server_id│    │   N agents         │  │
   │  │  OpenAI mod, │   └──────┬───────┘    └─────────┬──────────┘  │
   │  │  rate limit) │          │ session_assigned     │              │
   │  └──────┬───────┘          ▼                      │              │
   │         │ persist  ┌─────────────────┐            │              │
   │         ▼          │ /admin/         │            │ Admin REST   │
   │  ┌─────────────────┤  agent-control- │  /admin/   │ RPCs         │
   │  │   ClickHouse    │  ws             │  agent-ws  │              │
   │  │   chat_sessions │  (one per logged-in agent)   │              │
   │  │   chat_messages │                 │            │              │
   │  └─────────────────┴────┬────────────┘            │              │
   │                         │                          │              │
   │  ┌────────────────────────────────────────────────────────┐     │
   │  │   Bolt: chat_acl (per-server agent emails)             │     │
   │  └────────────────────────────────────────────────────────┘     │
   └──────────────────────────────────────────────────────────────────┘
                         ▲                  ▲              ▲
                admin JWT│        control-WS│              │ per-session WS
                         │      (queue feed) │              │ (live thread)
                ┌────────┴───────────────────┴──────────────┴───┐
                │            Hula admin SPA                     │
                │   /analytics/chats          (history)         │
                │   /analytics/chats/live     (queue, ready)    │
                │   /analytics/chats/[id]     (live thread)     │
                └───────────────────────────────────────────────┘
```

Two RPC surfaces, one transport split:

- **Public (token-gated)** — exactly two endpoints:
  `POST /api/v1/chat/start`, `WS /api/v1/chat/ws`. No admin JWT
  here. The token returned by `start` carries the only auth the WS
  cares about.
- **Admin (admin JWT)** — gRPC + JSON via the existing gateway for
  the REST shape (`ListSessions`, `GetMessages`, `SearchMessages`,
  `CloseSession`, `PostAdminMessage`) **plus a real-time agent
  WebSocket** at `WS /api/v1/chat/admin/agent-ws`. The agent-WS
  upgrade validates the admin JWT then runs the same
  `server_access` ACL check the REST RPCs use, scoped to the
  session's `server_id`. Same hub, different subscriber role.

Why one ChatHub, not extend the existing `pkg/realtime/hub`: the
visitor-event hub fans events to *every* subscriber that matches a
server-id filter. Chat fan-out is the opposite shape — a message to
session X goes to exactly the sockets bound to session X. A small
new hub keyed by `session_id` is cleaner than overloading the
existing one. (Both ride the same gorilla/websocket primitives.)

---

## 3. Data model

### 3.1 ClickHouse — `pkg/store/clickhouse/schema/chat_v1.sql`

```sql
CREATE TABLE IF NOT EXISTS chat_sessions
(
    id              UUID,
    server_id       LowCardinality(String),
    visitor_id      String,
    visitor_email   String,
    -- denormalised for fast list-page rendering without a join
    visitor_country LowCardinality(String) DEFAULT '',
    visitor_device  LowCardinality(String) DEFAULT '',
    visitor_ip      IPv6 DEFAULT toIPv6('::'),
    user_agent      String DEFAULT '',
    started_at      DateTime64(3),
    closed_at       Nullable(DateTime64(3)),
    last_message_at DateTime64(3),
    message_count   UInt32 DEFAULT 0,
    -- enum mirrors model/chat.go SessionStatus
    -- (queued|assigned|open|closed|expired)
    status          LowCardinality(String) DEFAULT 'queued',
    -- routing: empty = unassigned/queued; set when next-available
    -- routing matches the session to an admin or when an admin
    -- explicitly takes a session from the queue. Resets to '' on
    -- agent_left if reassignment is allowed (cf. §6.5).
    assigned_agent_id LowCardinality(String) DEFAULT '',
    assigned_at     Nullable(DateTime64(3)),
    -- arbitrary JSON the client can attach (page URL, referrer, …)
    meta            String DEFAULT ''
)
ENGINE = ReplacingMergeTree(last_message_at)
ORDER BY (server_id, started_at, id)
TTL toDateTime(started_at) + INTERVAL 365 DAY DELETE;
```

```sql
CREATE TABLE IF NOT EXISTS chat_messages
(
    id            UUID,
    session_id    UUID,
    server_id     LowCardinality(String),
    visitor_id    String,
    -- direction = visitor | agent | system | bot
    direction     LowCardinality(String),
    -- For direction='agent': admin username from the JWT claims.
    -- For direction='visitor': empty (visitor_id + email on session).
    sender_id     String DEFAULT '',
    content       String,
    when          DateTime64(3),
    INDEX idx_content_token (content) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 4,
    INDEX idx_content_ngram (content) TYPE ngrambf_v1(3, 32768, 3, 0) GRANULARITY 4
)
ENGINE = MergeTree
ORDER BY (server_id, session_id, when)
TTL toDateTime(when) + INTERVAL 365 DAY DELETE;
```

Notes:

- `chat_sessions` is `ReplacingMergeTree` keyed by `last_message_at`
  so updating `message_count` / `closed_at` is just an INSERT and
  the merger collapses to the newest row. Avoids ALTER UPDATE
  mutations on what becomes a hot table.
- The two skip indexes on `content` give us "find sessions
  mentioning *pricing*" both at word and substring granularity.
  When we move to ClickHouse 25.x we replace both with a single
  `INDEX idx_content TYPE text` — code path stays the same because
  the search SQL uses `multiSearchAny()` / `hasToken()` either way.
- `TTL ... 365 DAY DELETE` keeps the table bounded by default. The
  retention is a config knob (`config.chat.retention_days`,
  default 365). Hot operators can raise it; cold ones can drop it.

### 3.2 Bolt — `pkg/store/bolt/chat.go`

A single bucket: `chat_acl`. Maps `server_id → []agent_email`. Used
by the admin RPCs to filter who can see chats for which server. We
already have a more general `server_access` bucket; this is the
chat-specific *notification target* list ("who gets emailed when a
new chat opens"), not an authorization list. Authz still flows
through `server_access`.

No session state lives in Bolt. Live socket presence is in-process
(see §6); persisted state is in ClickHouse.

---

## 4. API surface

### 4.1 Public (no admin JWT)

#### `POST /api/v1/chat/start`

Request:

```json
{
  "server_id": "tlaloc",
  "visitor_id": "<bounce-id from hello.js cookie>",
  "email": "alice@example.com",
  "turnstile_token": "0.…",
  "first_message": "Hi, do you ship to Mexico?"
}
```

Response (200):

```json
{
  "session_id": "01J9Z…",
  "chat_token": "eyJ…",                  // signed JWT, exp = 30 min
  "expires_at": "2026-04-26T08:00:00Z",
  "message_id": "01J9Z…"                 // id of the first message,
                                         // already persisted
}
```

Server-side validation order (fail fast, return 400/403 with a
machine-readable `code` so the widget can render the right copy):

1. Rate limit per (`server_id`, peer IP). Default: 5 starts / 5 min.
2. `server_id` must be a configured server; otherwise 404.
3. `turnstile_token` validated against Cloudflare's siteverify
   endpoint (or, if `config.chat.captcha.provider = "recaptcha"`,
   Google's). 30 s timeout; fail = 403.
4. AfterShip email-verifier syntactic + DNS + disposable + role +
   misspell. SMTP probe **disabled** by default (slow, often
   greylisted) — we want offline-only checks. Fail = 400 with the
   verifier's reason code.
5. Optional model-based moderation. If `config.chat.openai.enabled`,
   send `first_message` to OpenAI with a small classifier prompt
   ("is this spam / abuse / a real customer?"). Fail = 403 with
   reason. The same `OpenAIConfig` shape the tlaloc backend uses
   (model + api_key) is the lift; we vendor a thin client.
6. Issue session row in ClickHouse + first message row.
7. Sign and return chat token.

Token shape (JWT, HS256 with the existing `jwt_key`):

```
{
  "sub":  "chat:visitor",
  "sid":  "<session_id>",
  "vid":  "<visitor_id>",
  "srv":  "<server_id>",
  "iat":  <issued>,
  "exp":  <issued + 30m>
}
```

Bound to a single session — refreshing requires a new `start`.

#### `WS /api/v1/chat/ws?token=<chat_token>`

Frames are JSON text. Visitor → server:

```json
{ "type": "msg", "content": "..." }
{ "type": "typing", "active": true }
{ "type": "ping" }                       // keepalive (server also pings)
```

Server → visitor:

```json
{ "type": "msg", "id": "...", "direction": "agent",
  "content": "...", "ts": "..." }
{ "type": "msg", "id": "...", "direction": "system",
  "content": "Session closed by agent.", "ts": "..." }
{ "type": "ack",  "id": "..." }          // ack for visitor msg
{ "type": "error", "code": "rate_limited", "message": "..." }
```

Connection lifecycle:

- 1 visitor session = 1 active socket. Re-opening with the same
  token kicks the older socket (sends a `system` close frame).
- Per-message rate cap: 10 messages / 30 s. Above → `error`
  + drop the offending frame, but keep the socket open.
- Idle: 60 s without any frame → server sends `ping`. 90 s
  without any client activity (incl. pong) → close.
- Token expiry mid-connection → server sends `error` with
  `code=token_expired` and closes after 1 s. Widget shows "Session
  expired, please refresh".

### 4.2 Admin (admin JWT, scoped via `server_access`)

All under `/api/v1/chat/admin/...`, auto-injected with the standard
`authware` middleware chain so the `Claims` are available.

```
GET    /admin/sessions?server_id=...&status=...&from=...&to=...&q=...&limit=&offset=
GET    /admin/sessions/{id}
GET    /admin/sessions/{id}/messages?limit=&offset=
POST   /admin/sessions/{id}/messages   { content }       # async post
POST   /admin/sessions/{id}/close      { reason }
POST   /admin/sessions/{id}/take                          # claim a queued session
POST   /admin/sessions/{id}/release                       # give it back to the queue
GET    /admin/queue?server_id=...                         # queued + assigned-but-unclaimed
GET    /admin/messages/search?server_id=...&q=...&from=&to=...&limit=&offset=
```

`POST /admin/sessions/{id}/take`: sets `assigned_agent_id` to the
caller, broadcasts `agent_assigned` on the session hub. Used both
to claim from the queue and to grab an idle session from another
agent (handoff flow). Returns 409 if the session is already
assigned to a different agent unless `?force=1` is set (admin-only
override).

`POST /admin/sessions/{id}/release`: clears `assigned_agent_id`,
puts the session back at the head of the queue (oldest-first), and
broadcasts a `session_released` frame on every available agent's
control-WS so the next-available logic can re-route it.

`PostAdminMessage` writes a `direction=agent` row, fans it out to
all live sockets (visitor + any agents), and increments
`message_count`. The SPA uses it for two cases: optimistic-UI sends
when the agent-WS is in-flight, and as a fallback when the
agent-WS is closed (e.g. the admin has the page open in a
background tab Firefox parked).

#### Agent WebSocket — `WS /api/v1/chat/admin/agent-ws?session_id=<uuid>`

Live, bidirectional. Auth: admin JWT in `Authorization: Bearer`
header (or `?token=` query for browsers that can't set headers on
WS). On upgrade the handler:

1. Validates the JWT (same `authware` chain the REST RPCs use).
2. Reads `chat_sessions` for `session_id` → confirms it exists and
   isn't closed.
3. Runs the `server_access` ACL check for `(claims.username,
   session.server_id)`. No access → `403` and the upgrade is
   refused.
4. Registers an agent subscriber on the hub for that session.
5. Pushes a `system` frame to all other subscribers:
   `{"type":"presence","event":"agent_joined","agent":"<username>"}`.

Frame shapes (agent → server, server → agent are mirror):

```json
{ "type": "msg",      "content": "..." }
{ "type": "typing",   "active": true }
{ "type": "ping" }
{ "type": "close",    "reason": "resolved" }       // ends session
```

Server → agent (in addition to the visitor-side frames):

```json
{ "type": "msg",       "id": "...", "direction": "visitor",
  "content": "...",   "ts": "..." }
{ "type": "presence",  "event": "visitor_connected" | "visitor_disconnected"
  | "agent_joined" | "agent_left",
  "visitor_id": "...", "agent": "..." }
{ "type": "typing",    "from": "visitor" | "agent",
  "agent": "<username if from=agent>", "active": true }
```

Multiple agents on a session is allowed; every agent sees every
other agent's frames. The visitor sees only one merged "agent"
identity in their UI even if N admins are observing — the SPA
labels each frame with the agent's username for the admin view.

#### Closing a session

Two ways:

- Agent sends `{"type":"close", "reason":"..."}` over their WS.
- Visitor closes the widget (or token expires; same effect).

Either path: server writes `closed_at` on `chat_sessions`,
broadcasts a `system` close frame, drops every subscriber, marks
`status=closed`. A new `/chat/start` from the same visitor is a
new session.

#### Agent control WebSocket — `WS /api/v1/chat/admin/agent-control-ws?server_id=<id>`

The "I'm available to take chats" channel. One per logged-in agent
per server. The SPA opens it on `/chats/live` mount and closes it
on unmount; while open the agent is in the per-server ready pool.

Auth: admin JWT + `server_access` ACL for `server_id` (same as
agent-ws). Frames flowing **server → agent**:

```json
{ "type": "session_assigned",   "session_id": "...", "visitor_email": "...",
  "first_message": "...", "queued_for_seconds": 12 }
{ "type": "session_released",   "session_id": "..." }   // session re-queued
{ "type": "queue_snapshot",     "queued":   [ { "session_id": "...", "queued_for": 8 }, ... ],
                                "assigned": [ { "session_id": "...", "agent": "alice" }, ... ] }
{ "type": "ping" }
```

Frames flowing **agent → server**:

```json
{ "type": "ack",      "session_id": "..." }       // I've opened the per-session WS
{ "type": "decline",  "session_id": "..." }       // re-route to next agent
{ "type": "ping" }
```

Lifecycle:

- On connect, the server sends a `queue_snapshot` so the SPA
  shows current state without a separate REST call.
- New visitor session arrives → next-available agent picked →
  `session_assigned` pushed → SPA pops a banner / row highlight.
- Agent has 30 s to open the per-session agent-WS (= the `ack`
  frame) or send `decline`. If neither: the assignment is dropped
  (`assigned_agent_id` cleared, agent is moved to the back of the
  round-robin), and the session is reassigned to the next
  available agent.
- Disconnect of the control-WS removes the agent from the ready
  pool. Sessions they had assigned but hadn't yet `ack`-ed get
  reassigned. Sessions they were actively chatting on stay theirs
  (the per-session agent-WS is the source of truth for "actively
  chatting") — until that WS also closes, at which point the
  session's `agent_left` presence frame fires and the session goes
  back to `status=open` with no assignment, available for any
  agent to `take`.

### 4.3 Wire-up in unified server

`registerChatHandlers(srv *unified.Server, cfg *config.Config)`
called from `server/unified_boot.go`, mirrors the
`registerAnalyticsUI` pattern. Two custom handlers (the public
endpoints, since gRPC doesn't speak WebSocket cleanly) plus the
gRPC-defined admin RPCs surface through the existing gateway.

---

## 5. Token + Turing-test design (detail)

### 5.1 Why a per-session token, not the admin JWT

The visitor never sees an admin JWT — they aren't an admin. The
chat token is a narrowly-scoped capability:

- One session id; expires in 30 minutes.
- Useless for any endpoint other than `/chat/ws`.
- Lost-token replay → an attacker can talk to that one visitor's
  session, see what was typed before expiry. They can't read other
  sessions or post as an agent.

### 5.2 Captcha provider abstraction

`pkg/chat/captcha`:

```go
type Verifier interface {
    Verify(ctx context.Context, token, remoteIP string) error
}
```

Two impls, picked by `config.chat.captcha.provider`:

- `turnstile` — POST to
  `https://challenges.cloudflare.com/turnstile/v0/siteverify` with
  `secret` + `response` + `remoteip`.
- `recaptcha` — POST to
  `https://www.google.com/recaptcha/api/siteverify` (v2 or v3,
  config flag).

Test mode: a third impl `noopVerifier` keyed off
`HULA_CHAT_CAPTCHA_TEST_BYPASS=1` so e2e and `pnpm dev` against
local hula don't need real provider keys.

### 5.3 Email verifier knobs

Config:

```yaml
chat:
  email_verifier:
    smtp_check: false        # don't make outbound SMTP probes
    disposable_check: true   # block 10minutemail etc.
    role_check: true         # block postmaster@, info@
    misspell_check: true     # surface "did you mean gmail.com?"
```

Wrapped in `pkg/chat/emailverify`. The library is fast offline; we
cache the `EmailVerifier` instance globally (it loads disposable
domain lists on construction).

### 5.4 Optional OpenAI moderation

Off by default. When on:

```yaml
chat:
  openai:
    enabled: true
    api_key:  "{{env:OPENAI_KEY}}"
    model:    "gpt-5.4-nano"        # match tlaloc backend default
    timeout:  3s
    prompt_kind: classifier         # the only kind in 4b
```

Same vendor + similar shape to the tlaloc classifier. Prompt:
"You are a moderation classifier. Reply with one token: REAL,
ABUSE, or SPAM." If the call times out we **allow** the message —
unavailable moderation should not block real customers. This is a
configurable trade-off (`on_error: allow|deny`).

---

## 6. Live messaging (websocket internals)

### 6.1 Hub structure

`pkg/chat/hub`:

```go
type Role int

const (
    RoleVisitor Role = iota
    RoleAgent
)

type Subscriber struct {
    Out      chan []byte   // bounded (32); slow → drop
    Role     Role
    AgentID  string        // username; empty for visitors
}

type Hub struct {
    mu       sync.RWMutex
    sessions map[uuid.UUID]map[*Subscriber]struct{}
}

// Subscribe registers s and returns a "presence" snapshot the
// caller writes back to the new subscriber so it sees who else is
// in the room. Unsubscribe broadcasts an "X_left" presence frame.
func (h *Hub) Subscribe(id uuid.UUID, s *Subscriber) PresenceSnapshot
func (h *Hub) Unsubscribe(id uuid.UUID, s *Subscriber)

// Publish fans frame to every subscriber on the session. Optional
// `exclude` skips the originator (so a sender doesn't see their
// own message echoed).
func (h *Hub) Publish(id uuid.UUID, frame []byte, exclude *Subscriber)

// AgentsFor / VisitorOnline are read-only helpers used by the
// admin "live sessions" view.
func (h *Hub) AgentsFor(id uuid.UUID) []string
func (h *Hub) VisitorOnline(id uuid.UUID) bool
```

Publish is non-blocking — slow buffers get the message dropped, the
socket reader handles the rest. Same shape as `pkg/realtime/hub`,
plus the role-aware presence + the `exclude` parameter so
visitor↔agent typing indicators don't loop back at the sender.

### 6.2 WS handler

```go
func chatVisitorWSHandler(hub *Hub, ch *chat.Service) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        token, err := chat.ValidateToken(r.URL.Query().Get("token"))
        if err != nil {
            http.Error(w, "invalid token", 401); return
        }
        conn, err := upgrader.Upgrade(w, r, nil)
        if err != nil { return }
        defer conn.Close()

        sub := &subscriber{out: make(chan []byte, 32), role: "visitor"}
        hub.Subscribe(token.SessionID, sub)
        defer hub.Unsubscribe(token.SessionID, sub)

        // Reader: parse frames, persist, fan out.
        go func() {
            for {
                _, raw, err := conn.ReadMessage()
                if err != nil { return }
                if err := ch.AppendVisitorMessage(...); err != nil {
                    // surface as `error` frame, keep socket
                }
                hub.Publish(token.SessionID, agentVisibleFrame)
            }
        }()

        // Writer: ping ticker + drain sub.out.
        ticker := time.NewTicker(25 * time.Second); defer ticker.Stop()
        for {
            select {
            case msg, ok := <-sub.out:
                if !ok { return }
                conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
                if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil { return }
            case <-ticker.C:
                if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil { return }
            }
        }
    }
}
```

Persistence is **synchronous** before fan-out: if ClickHouse is
down, the visitor sees an error, no ghost messages.

`PostAdminMessage` (from the admin REST RPC) reuses the same
`AppendXxxMessage` writers + `hub.Publish` so an admin reply lands
instantly on the visitor's open socket — even when the admin is
posting from the SPA without an open agent-WS (e.g. the SPA fell
back to REST after a transient socket drop).

### 6.3 Agent WS handler

Mirror of the visitor handler with admin auth + ACL up front and a
slightly bigger frame vocabulary (presence, typing, close).

```go
func chatAgentWSHandler(hub *Hub, ch *chat.Service) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        claims, err := authware.RequireAdmin(r)
        if err != nil { http.Error(w, "unauthorized", 401); return }

        sessionID, err := uuid.Parse(r.URL.Query().Get("session_id"))
        if err != nil { http.Error(w, "bad session_id", 400); return }

        sess, err := ch.GetSession(r.Context(), sessionID)
        if err != nil { http.Error(w, "session not found", 404); return }
        if sess.Status == "closed" {
            http.Error(w, "session closed", 410); return
        }
        if !ch.AdminCanAccess(claims, sess.ServerID) {
            http.Error(w, "forbidden", 403); return
        }

        conn, err := upgrader.Upgrade(w, r, nil)
        if err != nil { return }
        defer conn.Close()

        sub := &Subscriber{
            Out:     make(chan []byte, 32),
            Role:    RoleAgent,
            AgentID: claims.Username,
        }
        snap := hub.Subscribe(sessionID, sub)
        defer hub.Unsubscribe(sessionID, sub)
        // 1) Send the new agent the current presence snapshot so
        //    they see who else is in the room.
        // 2) Broadcast `agent_joined` to the visitor + other agents.
        writePresenceSnapshot(conn, snap)
        hub.Publish(sessionID, presenceFrame("agent_joined",
            claims.Username), sub)

        // Reader: parse, persist, fan out.
        // Writer: drain sub.Out + 25 s ping ticker.
        // (Same shape as the visitor handler.)
    }
}
```

Notes:

- The ACL check (`AdminCanAccess`) reads `server_access` from Bolt
  — same authority that gates the existing analytics RPCs. No new
  permission scope.
- N admins on one session is fine. The data model already records
  `sender_id` per message, so the SPA thread view shows
  *which* agent said what.
- Agent disconnect (clean or otherwise) → `Unsubscribe` →
  `agent_left` presence broadcast. If that was the *last* agent,
  the visitor sees a `system` "agent left the chat" message; the
  session stays open (an agent can rejoin until the visitor closes
  or the session expires).

### 6.4 Next-available routing + queue

`pkg/chat/router`:

```go
// Router holds per-server-id ready pools and a FIFO queue of
// sessions waiting for an agent. Single Goroutine owns mutation
// (single mutex); reads via small RLock helpers.
type Router struct {
    mu     sync.Mutex
    ready  map[string][]*AgentSlot  // server_id → ring; head = next pick
    queued map[string][]*QueuedSession
}

type AgentSlot struct {
    Username string
    Out      chan []byte             // control-WS write side
    LastPick time.Time                // for debug only; not used by RR
}

type QueuedSession struct {
    SessionID uuid.UUID
    QueuedAt  time.Time
}

// AgentReady adds the agent to the ready pool. Returns a snapshot
// of (queued, assigned) the SPA renders on first paint.
func (r *Router) AgentReady(serverID string, slot *AgentSlot) Snapshot

// AgentGone removes the agent. Any sessions that were assigned-
// but-not-yet-acked get re-queued at the head; actively-chatted
// sessions are unaffected (their per-session agent-WS continues).
func (r *Router) AgentGone(serverID, username string)

// Enqueue appends a new session to the per-server queue and tries
// to pick an agent immediately; if successful, returns the picked
// agent's username so the start handler can persist it.
func (r *Router) Enqueue(serverID string, sessionID uuid.UUID) (assignedTo string, queuedDepth int)

// Ack drops the assignment-pending mark; called when the agent's
// per-session WS opens.
func (r *Router) Ack(serverID, username string, sessionID uuid.UUID)

// Decline re-routes the session to the next ready agent.
func (r *Router) Decline(serverID, username string, sessionID uuid.UUID) (newAssignee string)
```

Algorithm summary:

- **Round-robin**: `ready[server_id]` is a ring; `Enqueue` pops the
  head, sends `session_assigned` to that slot, pushes them to the
  tail. Even distribution without bookkeeping.
- **No-agent fallback**: if `ready[server_id]` is empty, the session
  lands on `queued[server_id]`. Visitor's WS gets a `system`
  message ("Connecting you with an agent…"). When an agent opens a
  control-WS and `AgentReady` is called, the queue is drained
  oldest-first up to that agent's currently-empty slot count
  (default: 1 — assign one at a time so the agent isn't flooded).
- **30-second ack timer**: `Enqueue` schedules a single `time.AfterFunc(30*time.Second, …)` that re-routes if `Ack` hasn't fired by then. The timer is cancelled on `Ack` / `Decline`.
- **Re-routing on agent disconnect**: in `AgentGone`, the router
  walks the assigned-pending list, re-enqueues each session at
  the *head* (so they're served first when the next agent appears,
  preserving customer wait order).
- **Persistence**: routing state lives only in the Router. The DB
  reflects the *outcome* (`assigned_agent_id`, `assigned_at`); a
  hula restart drops in-flight assignments and re-queues every
  `status='queued'|'assigned'` session whose visitor WS reconnects.

### 6.5 Reassignment policy when an agent's per-session WS drops

If an agent's per-session WS closes (browser tab killed, network
flap), the session's `assigned_agent_id` does **not** auto-clear —
they may simply be reconnecting. The visitor sees an `agent_left`
presence frame; the session stays in `status='open'` with the
existing `assigned_agent_id`. If the agent doesn't reconnect within
60 s, the SPA's control-WS-side loses them from the ready pool
(control-WS heartbeat fails first), and the router clears the
assignment + re-enqueues at the head. New agent picks it up.

The same agent reconnecting within 60 s just re-opens the per-session WS and the visitor sees `agent_joined` again — no
re-route happens.

### 6.6 Single-process limitation

The hub is in-memory. If hula scales horizontally, sessions on
process A won't see admin posts to process B. Phase 4b is
single-process; we add a panic-loud check at boot if
`HULA_CHAT_MULTIPROC=1` is set without a backend configured, so
operators can't accidentally split sessions. The Redis/NATS hop
lands when (if) we go multi-process.

---

## 7. Hula admin SPA — `/analytics/chats`

### 7.1 Routes

```
/analytics/chats              → All chats (paginated table)
/analytics/chats/live         → Live chats (auto-refresh, 5 s)
/analytics/chats/[id]         → Thread view + admin compose
```

All routes use the existing layout (sidebar + filter bar). The
filter bar's date range applies to the All-chats list (filters
`started_at`). Live-chats ignores the date range (always "now").

### 7.2 New API client wrappers

Add to `web/analytics/src/lib/api/chat.ts`:

```ts
export const chat = {
  listSessions: (opts) => get('/admin/sessions', opts),
  getSession:   (opts) => get(`/admin/sessions/${opts.id}`, opts),
  getMessages:  (opts) => get(`/admin/sessions/${opts.id}/messages`, opts),
  postMessage:  (opts) => post(`/admin/sessions/${opts.id}/messages`, opts),
  closeSession: (opts) => post(`/admin/sessions/${opts.id}/close`, opts),
  search:       (opts) => get('/admin/messages/search', opts),
};
```

### 7.3 Pages

- **All chats** — `ReportTable` (existing component) with columns:
  Visitor (email + visitor_id link to `/visitors/[id]`), Started,
  Last message, Messages (count), Status. Search box at the top
  (debounced 300 ms) → calls `chat.search()` and merges the row
  set.
- **Live chats** — same table, filtered to `status=open AND has_live_socket=true`.
  The "has_live_socket" flag is a derived read from the hub
  (exposed as a small admin endpoint `GET /admin/live-sessions`
  that returns `[{session_id, server_id, visitor_id,
  visitor_email, visitor_online, agent_count, last_message_at}]`).
  The list polls every 5 s; a WS-driven push for *new sessions
  appearing* would be nicer but per-session WS already covers the
  thread view, so the list-page poll is acceptable for 4b.
- **Thread view** — message timeline (visitor right, agent left,
  system centered, monospace timestamp). Sticky compose box at
  bottom. The page opens a WebSocket to
  `/api/v1/chat/admin/agent-ws?session_id=...` on mount and closes
  it on unmount. Frames flowing through the WS:
  - inbound: visitor `msg`, presence (`visitor_connected` /
    `visitor_disconnected` / `agent_joined` / `agent_left`),
    visitor typing indicator
  - outbound: agent `msg`, agent typing indicator, `close`
  Compose-box "Send" first tries the WS; on a failed write or
  closed socket it transparently falls back to `PostAdminMessage`
  (REST). Either path appends the agent's message optimistically
  and reconciles when the server's `ack` (or REST 200) lands.
  Header chip shows current presence: "Visitor online" /
  "Visitor offline" / "Other agents: alice, bob".

### 7.4 Sidebar entry

Append to `web/analytics/src/lib/components/Sidebar.svelte`'s
`reportItems`:

```ts
{ href: 'chats', label: 'Chats' },
```

(Right after Visitors, before the Admin section.)

---

## 8. Test website integration — `../tlalocwebsite`

### 8.1 Scope

A drop-in chat bubble. Functional checklist:

- Floating bubble bottom-right, 56×56 dp, brand-coloured.
- Opens a 360×500 panel on desktop; full-screen on viewports < 480 px.
- States: `closed`, `pre-chat-form`, `connecting`, `queued`,
  `live`, `expired`, `error`. All transitions reversible except
  `expired`. `queued` shows "All agents are with another customer.
  You're #N in line — feel free to start typing." while the
  visitor's WS waits for the first `agent_joined` presence frame.
- Pre-chat-form fields: email, first message, Turnstile widget
  (managed mode, invisible). Submit → POST `/api/v1/chat/start` →
  on success transition to `connecting` → open WS → `live`.
- Live: shows a scrolling thread; visitor types in a textarea;
  Enter sends; Shift-Enter newline. Visitor sends `typing` frames
  on input (debounced 500 ms; auto-cleared after 3 s of no
  keystrokes). When an agent's `typing` frame arrives, the widget
  shows three animated dots labelled with the agent's username
  ("Alice is typing…"); presence frames toggle a header chip
  between "Connecting…", "Connected — wait for an agent",
  "Connected with Alice", and "Agent left, you can keep typing".
- Reconnect-on-disconnect with backoff (1, 2, 4, 8 s; cap at 30 s).
  After `chat_token` expiry → render "Session expired, refresh to
  start a new chat".

### 8.2 Implementation shape

- New Hugo partial `themes/tlaloc/layouts/partials/chat.html`
  inserts a `<div id="hula-chat" data-server-id="tlaloc"
  data-turnstile-sitekey="…" data-api-base="/api/v1/chat" hidden>`
  + a small `<script src="/scripts/chat.js" async>` tag.
- Source TS at `assets/js/chat.ts` (Hugo asset pipeline), gets
  bundled with esbuild on build. ~10 KB gzipped target.
- Visitor id is read from the existing hello cookie (same name
  hello.js sets); falls back to a fresh UUIDv7 if absent.
- Cloudflare Turnstile via the `<script
  src="https://challenges.cloudflare.com/turnstile/v0/api.js"
  async defer>` global — only loaded once the user clicks the
  bubble.

### 8.3 Hugo config

`hugo.toml` gains:

```toml
[params]
  chat_enabled = true
  chat_server_id = "tlaloc"
  # turnstile_sitekey is already on params for the contact form.
```

`baseof.html` includes the partial behind that flag, next to the
existing visitor-tracking iframe.

### 8.4 Mobile behaviour

CSS `@media (max-width: 480px)` makes the panel full-screen with a
back-arrow on the header. Address-bar autohide on iOS Safari is
handled by a `100dvh` height (with a `100vh` fallback for older
browsers). Tested in playwright Mobile-emulation viewport.

---

## 9. FTS strategy

### 9.1 v1: dual bloom-filter skip indexes (this phase)

The `chat_messages.content` column gets two indexes:

| Index | Type | Use |
|---|---|---|
| `idx_content_token` | `tokenbf_v1(32768, 3, 0)` | word-level: `hasToken(content, 'pricing')` |
| `idx_content_ngram` | `ngrambf_v1(3, 32768, 3, 0)` | 3-gram substring: `like '%foo-bar%'` |

Search RPC SQL:

```sql
SELECT id, session_id, visitor_id, sender_id, direction, content, when
FROM chat_messages
WHERE server_id = {server_id:String}
  AND when BETWEEN {from:DateTime64} AND {to:DateTime64}
  AND multiSearchAny(lower(content), {tokens:Array(String)})
ORDER BY when DESC
LIMIT {limit:UInt32} OFFSET {offset:UInt32}
```

Tokens come from a lowercase-and-split-on-whitespace pre-pass in
the Go layer. Phrase-search ("two words near each other") falls
back to a `like '% % %'` clause when the query contains spaces.

### 9.2 v2 (future, after CH upgrade): `text` index

ClickHouse 25.x stabilises a Tantivy-backed inverted index. Once
we upgrade we replace the two skip indexes with one
`INDEX i_text content TYPE text(...)` and switch the search to
`searchAny(content, ...)` for true relevance ranking. The RPC
contract doesn't change; the query builder swaps the predicate.

### 9.3 Dedicated FTS service

We considered Meilisearch / Tantivy / Bleve as a sidecar. Rejected
for 4b because:

- Adds a new piece of infra to operate.
- Cross-store consistency on message TTL is non-trivial.
- ClickHouse skip indexes already handle the volume we expect for
  chat (≪ 1M messages / month at any reasonable site size).

We will revisit if chat volume crosses ~10M messages or query
latency p95 > 500 ms — neither plausible in 4b.

---

## 10. Stage breakdown

Each stage is a standalone PR-size slice, lands behind its own
commit. Stages 4b.6 + 4b.7 + 4b.8 are independent and can run in
parallel once 4b.5 has landed.

### Stage 4b.1 — Schema + ClickHouse migration + Bolt bucket

**Goal**: tables exist, migration applies cleanly idempotently, no
behaviour changes.

- New file `pkg/store/clickhouse/schema/chat_v1.sql` (sessions +
  messages tables + skip indexes).
- New `pkg/store/clickhouse/migrations/0003_chat_v1.sql` —
  effectively a no-op when run on a fresh DB (the schema file
  creates everything); placeholder for future migrations.
- New `pkg/store/bolt/chat.go` — `chat_acl` bucket with
  `Get/Put/Delete` helpers and JSON marshalling.
- Test: `0003` migration runs on a fresh DB, schema queries work.

**Sign-off**: `make test-unit` green, `0003` shows up in
`schema_migrations`, both tables visible via `SHOW TABLES`.

### Stage 4b.2 — Proto + admin REST RPCs (no sockets yet)

- `pkg/apispec/v1/chat/chat.proto` — `ChatService` (Public +
  Admin), wire-up in the gateway, `protoc` regen.
- `pkg/api/v1/chat/chatimpl.go` — `ListSessions`, `GetSession`,
  `GetMessages`, `PostAdminMessage`, `CloseSession`,
  `SearchMessages`. ACL via `server_access`.
- `pkg/chat/store.go` — ClickHouse readers + writers.
- Extend the existing `ForgetVisitor` RPC to delete from
  `chat_messages` and `chat_sessions` for the target visitor.
- E2e suite stub `29-chat.sh` covers the admin REST paths against
  hand-seeded rows.

**Sign-off**: admin REST endpoints round-trip via curl; suite 29
(admin slice) green; `ForgetVisitor` removes chat rows.

### Stage 4b.3 — Captcha + email-verifier + OpenAI moderation + token

- New deps: `github.com/AfterShip/email-verifier` (vendored).
  Already-present: gorilla/websocket, the OpenAI client we'll
  inline (~80 LOC, REST POST).
- `pkg/chat/captcha` (Turnstile + reCAPTCHA + Test bypass).
- `pkg/chat/emailverify` (AfterShip wrapper, cached singleton).
- `pkg/chat/openai_moderate` (3 s timeout, on_error policy).
- `pkg/chat/token.go` (sign + verify chat JWT with the existing
  `jwt_key`).
- `POST /api/v1/chat/start` handler in `server/chat_start.go`,
  registered alongside the visitor tracking handlers.
- Config additions to `config/config.go` (validated on boot).
- Test: handler returns the expected 4xx codes for each failure
  mode, 200 on the happy path; tests use the `noop` captcha
  verifier and a mock email verifier.

**Sign-off**: `curl POST /chat/start` works end-to-end against a
local hula with `HULA_CHAT_CAPTCHA_TEST_BYPASS=1`. Session row +
first message land in ClickHouse.

### Stage 4b.4 — Hub + visitor WebSocket endpoint

- `pkg/chat/hub` (Subscribe / Unsubscribe / Publish, role-aware,
  presence snapshot helper).
- `WS /api/v1/chat/ws` handler in `server/chat_ws_visitor.go`.
- 25 s ping / 90 s idle / 10 s write deadline / per-session
  rate-cap (10 msg / 30 s).
- Suite 29 grows a visitor-WS sub-suite: open WS, send msg,
  observe DB row, observe `ack` frame, observe admin's
  `PostAdminMessage` fan out to the open socket.

**Sign-off**: e2e drives visitor → server → admin REST →
visitor round trip.

### Stage 4b.5 — Per-session agent WebSocket + presence + typing

- `WS /api/v1/chat/admin/agent-ws?session_id=...` handler in
  `server/chat_ws_agent.go`.
- Admin JWT validation + `server_access` ACL check up front (403
  on upgrade if missing).
- Multi-agent support: N agents per session, presence broadcasts
  on join/leave, all agents see each other's frames.
- Typing-indicator routing (visitor → all agents, agent → visitor
  + other agents). Same `Hub.Publish(..., exclude:)` machinery.
- Suite 29 gains an agent-WS sub-suite: admin opens agent-WS,
  visitor sends msg, admin sees it without polling, admin types →
  visitor sees typing → admin sends → visitor sees msg → either
  side closes → presence frame propagates → second admin can join
  the same session.

**Sign-off**: end-to-end live chat (visitor + agent both via WS,
no REST in the hot path). p99 visitor→agent latency < 200 ms on
local, < 500 ms over Cloudflare.

### Stage 4b.6 — Agent control WS + next-available routing + queue

- `pkg/chat/router` (round-robin ring, queue, 30 s ack timer).
- `WS /api/v1/chat/admin/agent-control-ws?server_id=...` handler.
- `chat_sessions.assigned_agent_id` write paths:
  - on `Enqueue` when an agent is available
  - on `take` / `release` REST endpoints
  - on `decline` / `Ack` / `AgentGone` events
- REST: `POST /admin/sessions/{id}/take`,
  `POST /admin/sessions/{id}/release`,
  `GET /admin/queue?server_id=...`,
  `GET /admin/live-sessions` (reads `Hub.AgentsFor` +
  `Hub.VisitorOnline` plus the router's queue + ready pool).
- Visitor WS sees a `system` "Connecting you with an agent…"
  frame on `/chat/start` if no agent is available, then
  `agent_joined` once the assignment lands.
- Suite 29 gains a routing sub-suite: agent A control-WS connects
  → visitor session opens → A receives `session_assigned` → A
  acks (opens per-session WS) → routing complete; second visitor
  session arrives → A is at tail → goes to ready agent B; B
  declines → routes back to A; agent A disconnects mid-session →
  re-queue → C connects → C gets it within 1 s.

**Sign-off**: routing fairness verified (round-robin distributes
evenly across N agents); no-agent queue + drain works; 30 s ack
timer reroutes; release returns to head of queue.

### Stage 4b.7 — Hula admin SPA: `/analytics/chats`

- `web/analytics/src/lib/api/chat.ts` (typed REST wrappers).
- `web/analytics/src/lib/api/agentSocket.ts` — per-session WS
  helper: opens/heartbeats/reconnects, Svelte store of
  `{ messages, presence, typing }`. Reconnect with exponential
  backoff (1, 2, 4, 8 s; cap 30 s).
- `web/analytics/src/lib/api/agentControlSocket.ts` —
  control-WS helper for `/chats/live`: opens on mount, exposes
  `{ queue, assigned, readyAgents, incomingAssignment }` store.
  Auto-acks an incoming `session_assigned` by routing the SPA to
  `/chats/[id]` (which opens the per-session WS = the implicit
  ack). The agent can `decline` from the assignment banner.
- Three new routes (`/chats`, `/chats/live`, `/chats/[id]`).
- `/chats/live` shows two stacked tables: "Queued" (waiting,
  oldest-first) with a "Take" button per row, and "Active"
  (assigned + connected) with the agent's username. Holds the
  control-WS while the page is mounted; the SPA tab title shows a
  badge of queue depth.
- Sidebar entry.
- Visitor-detail page gains a "Conversations" section.
- `pnpm check` clean, `pnpm build` green, bundle size budget held
  (currently 150 KB gzipped first-load; chat adds ~14 KB with
  both WS clients).

**Sign-off**: playwright e2e drives admin login → /chats/live →
visitor session arrives → assignment banner appears → SPA opens
the thread automatically → admin types → visitor sees within 1 s;
second admin's `/chats/live` shows the queue empty (already
assigned) and updates in real time as sessions open/close.

### Stage 4b.8 — tlalocwebsite chat widget

- `themes/tlaloc/layouts/partials/chat.html`.
- `assets/js/chat.ts` + Hugo asset pipeline wiring.
- Mobile / desktop layouts.
- Reconnect-on-disconnect with backoff.
- Visitor sees "Agent connected", typing dots, agent name as the
  bubble label.
- `params.chat_enabled` toggle in `hugo.toml`.
- Smoke test: `hugo server` locally, hit the bubble, send a
  message, agent (logged into the local hula admin SPA) replies,
  visitor sees the reply in real time.

**Sign-off**: a real human (or playwright on the deploy pipeline)
can open the page, send a message, see an agent type, see the
agent's reply, all live.

### Stage 4b.9 — FTS v1 + search RPC + UI search box

- Skip indexes already shipped in 4b.1; this stage wires the search
  RPC + the SPA search input.
- Token splitter on the Go side, `multiSearchAny` predicate.
- Empty-state, debounce, highlight-matches in the table.

**Sign-off**: search returns hits in < 200 ms p95 against a
seeded 100k-message dataset (e2e perf check).

### Stage 4b.10 — Status doc + sign-off

- `PHASE_4B_STATUS.md` written, mirroring the
  `PHASE_4_STATUS.md` shape (per-stage ✅ list, deferred items,
  pre-existing issues).
- `PLAN_OUTLINE.md` gains a §Phase 4b paragraph.
- `UI_PRD.md` gains §8 Chat (matches what shipped, no aspirational
  language).
- `roadmap/phase4b` branch fast-forwarded; merge PR opened against
  `main`.

---

## 11. Timeline

Best-guess for one engineer + one frontend-leaning helper:

| Stage | Days |
|---|---|
| 4b.1 schema | 1 |
| 4b.2 proto + admin REST | 2 |
| 4b.3 token + guards | 2 |
| 4b.4 visitor WS + hub | 2 |
| 4b.5 per-session agent WS + presence | 2 |
| 4b.6 control WS + routing + queue | 2 |
| 4b.7 admin SPA | 2.5 |
| 4b.8 tlaloc widget | 2 |
| 4b.9 FTS | 1 |
| 4b.10 docs + sign-off | 0.5 |
| **Total** | **~17 working days** |

Stages 4b.7 / 4b.8 / 4b.9 parallelisable once 4b.6 is in. Critical
path is 4b.1 → 4b.2 → 4b.3 → 4b.4 → 4b.5 → 4b.6.

---

## 12. Risks + open items

- **Captcha provider keys.** Tlaloc already has Turnstile site keys
  for the contact form; we'll piggyback on those for the test site.
  For a fresh deployment the operator must provision keys.
- **OpenAI optionality.** With `enabled: false` (default), token
  issuance is fast and quotaless. Operators turning it on need to
  understand the cost (~$0.0001/message on gpt-5.4-nano, but it
  adds up at scale). Document in `UI_PRD.md`.
- **AfterShip dep license.** MIT — fine to vendor.
- **Single-process hub.** Doc the foot-gun (see §6.3); fail loud if
  someone scales out.
- **GDPR / right-to-be-forgotten** — chat_messages is in the same
  ClickHouse instance as `events`; the existing `ForgetVisitor` RPC
  must extend to also delete from `chat_messages` and
  `chat_sessions`. Tracked as part of stage 4b.2 (small ALTER
  DELETE add to that RPC).
- **Spam waves.** Turnstile + email-verifier + per-IP rate limits +
  optional OpenAI is a strong stack; if a campaign still gets
  through, the per-message rate cap on the WS limits damage. We
  add a kill-switch config (`chat.disable_new_sessions: true`) so
  an operator can shut down new chats while triaging.
- **Multi-agent races.** Two admins typing into the same session
  concurrently is supported (every agent's frames are tagged with
  their `sender_id`). UX-wise the SPA renders agent messages with
  the username so the visitor sees one merged "agent" identity but
  agents see who said what. We deliberately do **not** lock a
  session to one agent — handoff and observation are common. The
  router only *prefers* the originally-assigned agent (round-robin
  picks them); the per-session WS allows additional joiners
  without re-routing.
- **Restart semantics.** The router's queue + ready pool are
  in-memory only. A hula restart drops every in-flight assignment
  and the queued list; visitors with an open WS get a `system`
  reconnect-prompt frame on close, the SPA's control-WS
  reconnects, and the queue rebuilds itself from
  `chat_sessions WHERE status IN ('queued','assigned')` (a
  one-shot recovery query at router boot). Documented in
  `PHASE_4B_STATUS.md` as expected behaviour.
- **Restart drift on assigned-but-idle sessions.** A session in
  `status='open'` with `assigned_agent_id` set whose visitor isn't
  connected and whose agent isn't connected either is recovered
  by a 5-minute stale-cleanup ticker that flips it to
  `status='expired'`. Avoids a session lingering as "yours" if both
  sides walked away.
- **Queue-depth observability.** The control-WS `queue_snapshot`
  doubles as a metric source — the router exposes
  `chat.queue.depth{server_id}` and `chat.queue.oldest_seconds`
  via the existing prom-style metrics endpoint so an alert
  (Phase 4 `bad_actor_rate`-style) can fire on backlog.
- **Storage growth.** ClickHouse compresses chat text aggressively
  (LZ4 on the column); rough estimate is < 200 bytes/message
  on disk. 1M messages ≈ 200 MB, well within a single-node CH.
  TTL keeps it bounded.

---

## 13. Sign-off checklist (Phase 4b → Phase 5a)

- [ ] All 10 stages landed on `roadmap/phase4b`.
- [ ] `make test-unit` green; `make test-verbose` green.
- [ ] `cd web/analytics && pnpm check` → 0 errors, 0 warnings.
- [ ] `cd web/analytics && pnpm build` green; bundle size budget
      held (≤ 165 KB first-load gzipped).
- [ ] `./test/e2e/run.sh` passes; suite 29-chat green
      end-to-end (admin REST, visitor WS, agent WS, presence,
      typing indicators, **routing + queue**, FTS).
- [ ] Routing fairness: 6 simulated visitor sessions across 3
      agents distribute 2-2-2 (round-robin verified).
- [ ] Queue: with 0 agents available, 3 sessions queue; agent
      connects, drains in arrival order, each with a < 1 s
      `session_assigned` push.
- [ ] Tlaloc widget driving the full live path: visitor opens
      bubble → Turnstile → email + first message → WS connects →
      "Connecting you with an agent…" if queued → admin's
      /chats/live banner pops → admin opens session → visitor sees
      typing dots → admin sends → visitor sees reply within 1 s →
      either side closes → presence `agent_left` / session
      `closed` propagates.
- [ ] `PHASE_4B_STATUS.md` written.
- [ ] `UI_PRD.md` §8 Chat describes the shipped shapes (not
      aspirations).

---

## 14. What happens after Phase 4b

- **Phase 4c (small follow-on, optional before 5a):**
  skill-based routing (tag agents, route by tag), per-agent
  capacity caps, SLA timers + alerts when queue depth or wait
  time exceeds thresholds, optional bot auto-responder using the
  existing OpenAI client, canned replies / saved snippets in the
  admin compose box.
- **Phase 5a:** Mobile APIs + push notification engine — including
  push to admins when a new chat opens or when the visitor sends a
  message and no agent is connected.
- **Far future:** dedicated FTS sidecar if ClickHouse text index
  isn't enough; multi-process hub via Redis/NATS if we run more
  than one hula container per cluster (see §6.4); end-to-end
  encryption (would force FTS off, so currently a non-goal).
