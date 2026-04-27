# Phase 4b — Execution Status (COMPLETE — backend live; widget queued)

**Status: 9 of 10 stages landed live; 4b.8 (tlalocwebsite chat
widget) committed locally and pending a single `git push origin
main` to the test-site repo. The hula side is fully shipped.**

Phase 4b added a visitor-chat surface to hula's API layer — public
WebSocket for the website visitor, admin WebSockets for live agent
↔ visitor messaging, next-available agent routing with a queue,
ClickHouse-persisted history with FTS, and an admin SPA tab for
browsing + replying. The first consumer is `../tlalocwebsite`,
which gains a HubSpot-style chat bubble in stage 4b.8 (the widget
files are committed locally; pushing main triggers hula's
git-autodeploy and the bubble goes live on www.tlaloc.us +
staging.tlaloc.us).

Summary of the Phase-4b rollout:

- **New ClickHouse tables** `chat_sessions` (ReplacingMergeTree,
  365-day TTL) + `chat_messages` (MergeTree with `tokenbf_v1` +
  `ngrambf_v1` skip indexes for FTS), wired into the schema
  runner via a new `ChatRetentionDays` template var.
- **Bolt** gained a `chat_acl` bucket (per-server agent
  notification roster) — chat ACL itself rides on the existing
  `server_access` bucket.
- **Three public/admin HTTP surfaces:**
  - `POST /api/v1/chat/start` — Turnstile (or reCAPTCHA, or
    test-bypass) → AfterShip email-verifier → optional OpenAI
    moderation → per-IP rate-limit (5/5min) → persists session +
    first message → returns a 30-min chat-session JWT.
  - `WS /api/v1/chat/ws?token=...` — visitor side, JSON frames
    (`msg`, `typing`, `ping`, `ack`, `presence_snapshot`,
    `presence`, `system`, `error`); 25s ping / 90s idle / 10s
    write deadline; per-session 10msg/30s rate cap; kicks prior
    visitor socket on reconnect.
  - `WS /api/v1/chat/admin/agent-ws?session_id=...` — admin JWT +
    `server_access` ACL on upgrade; multi-agent allowed; presence
    + typing indicators with `Hub.Publish(..., exclude:)` so
    senders don't see their own frames echoed.
  - `WS /api/v1/chat/admin/agent-control-ws?server_id=...` —
    "I'm available" channel; while open the agent is in the
    per-server ready pool. Frames: `queue_snapshot`,
    `session_assigned`, `session_released`, `error`. Agent →
    server: `ack`, `decline`, `ping`.
- **Admin REST RPCs** — `ChatService` with 10 methods exposed via
  the existing grpc-gateway: `ListSessions`, `GetSession`,
  `GetMessages`, `PostAdminMessage`, `CloseSession`,
  `TakeSession` (claim queued/idle session, optional
  `force=true` to override another agent), `ReleaseSession`
  (re-queue), `GetQueue`, `GetLiveSessions`, `SearchMessages`.
- **In-process Hub + Router**:
  - `pkg/chat/hub.go` — role-aware Subscribe/Unsubscribe/Publish,
    presence-snapshot helper, `AgentsFor` / `VisitorOnline` /
    `VisitorSubscriber` accessors. Slow subscribers get the drop
    (matches `pkg/realtime`).
  - `pkg/chat/router.go` — round-robin ready ring + FIFO queue
    per server_id, 30s ack timer that re-routes if an agent
    doesn't open the per-session WS. Agent disconnect re-queues
    pending assignments at the head (preserves wait order).
- **Hula admin SPA `/analytics/chats`** — three new routes:
  - `/chats` — paginated history table with debounced FTS search
    box. Search hits include the matching message snippet under
    each row, with the matched tokens wrapped in `<mark>`.
  - `/chats/live` — control-WS-driven page; KPI strip (queue
    depth / assigned / other ready agents); auto-routes to
    `/chats/[id]` on `session_assigned` (the per-session WS open
    counts as ack).
  - `/chats/[id]` — live thread view; opens per-session agent-WS
    on mount, drains messages into a Svelte store, optimistic
    UI for sends with REST fallback when the socket is closed.
    Header chip shows visitor presence + other agents in the
    room. Compose box has typing-indicator broadcast (debounced
    500 ms / cleared after 3 s) + Enter-to-send.
- **Tlalocwebsite chat widget** (committed locally, awaiting
  push):
  - `themes/tlaloc/assets/js/chat.js` — vanilla JS bubble + panel,
    Turnstile rendering, `/chat/start` POST, WS open with
    reconnect-with-backoff (1/2/4/8/16/30s), state machine
    `closed → pre-chat-form → connecting → queued|live →
    expired`. Visitor sees agent typing dots, presence chip,
    inline message bubbles.
  - `themes/tlaloc/layouts/partials/chat.html` + inline CSS
    (mobile full-screen at `<480px` via `@media`).
  - `hugo.toml`: `chat_enabled = true`, `chat_server_id = "tlaloc"`.

## Completed stages (9 of 10)

### Stage 4b.1 — Schema + ClickHouse migration + Bolt bucket ✅
- `pkg/store/clickhouse/schema/chat_v1.sql` (sessions + messages)
- `pkg/store/clickhouse/migrations/0003_chat_v1.sql`
- `pkg/store/bolt/chat.go` + `chat_acl` bucket
- `config/chat.go` — `ChatConfig` with retention + (4b.3
  placeholders for) Captcha/EmailVerifier/OpenAI sub-configs
- Live verification: schema applied, both tables visible,
  FTS indexes intact, TTL = 365 days.

### Stage 4b.2 — Proto + admin REST RPCs ✅
- `pkg/apispec/v1/chat/chat.proto` — `ChatService` with 10 admin
  RPCs, ACL-annotated.
- `pkg/chat/store.go` — Session + Message types, ClickHouse
  readers/writers, `ForgetVisitor` extension for GDPR.
- `pkg/api/v1/chat/chatimpl.go` — handlers with `authorize()`
  gate (Unauthenticated / PermissionDenied / InvalidArgument).
- `server/chat_acl.go` — Bolt-backed ACL resolution, mirrors the
  analytics ACL pattern.
- Live: 401/200/403/400 gate matrix verified; seeded session
  round-trips through ListSessions, GetMessages, SearchMessages
  (FTS hit), TakeSession, PostAdminMessage.

### Stage 4b.3 — Captcha + email-verifier + OpenAI moderation + token ✅
- `pkg/chat/captcha/` — Turnstile + reCAPTCHA + TestBypass,
  testable via overridable URL field. `HULA_CHAT_CAPTCHA_TEST_BYPASS`
  env-var escape hatch with WARN-on-boot.
- `pkg/chat/emailverify/` — AfterShip wrapper, ctx-bounded.
- `pkg/chat/moderate/` — tiny inline OpenAI client, 3s default
  timeout, `OnError` policy.
- `pkg/chat/token.go` — HS256 chat-session JWT, 30-min default TTL.
- `pkg/chat/ratelimit.go` — sliding-window 5/5min per
  `(server_id, ip)`, concurrent-safe.
- `pkg/chat/service.go` — full pipeline: kill-switch → server
  check → rate limit → captcha → email → moderation → persist
  session+first message → issue token.
- `server/chat_start.go` + `server/chat_boot.go` — HTTP handler +
  config-driven Service builder.
- Live: gate matrix 400/404/503 verified; happy path ships a
  chat token + persists rows.

### Stage 4b.4 — Hub + visitor WebSocket ✅
- `pkg/chat/hub.go` — role-aware fan-out, presence snapshot,
  slow-subscriber drop, `VisitorSubscriber` for kick-on-reconnect.
- `server/chat_ws_visitor.go` — token-validate → fetch session →
  kick prior visitor → upgrade → subscribe → reader / writer
  goroutines / 25s ping / per-session rate cap.
- Live: WS opens, message round-trips, ack frame emitted, frames
  persisted as `direction=visitor`.

### Stage 4b.5 — Per-session agent WebSocket + presence + typing ✅
- `server/chat_ws_agent.go` — admin JWT + ACL on upgrade,
  multi-agent presence broadcast (`agent_joined`/`agent_left`),
  typing-indicator routing.
- Auto-flips session to `OPEN` on agent connect.
- Live: bidirectional smoke verified — visitor msg → agent saw
  instantly, agent typing → visitor saw dots, agent reply →
  visitor saw msg, close-from-agent → both sides got system
  close + agent_left presence.

### Stage 4b.6 — Control WS + next-available routing + queue ✅
- `pkg/chat/router.go` — round-robin ring + FIFO queue + 30s
  ack timer. AgentReady drains one queued session per connect.
  AgentGone re-queues pending assignments at the head.
- `server/chat_ws_control.go` — agent-control WS handler.
- `Service.Start` Enqueues new sessions; if an agent is ready,
  status flips to `assigned` and `assigned_agent_id` set.
- Live: agent control-WS connects → visitor `/chat/start` →
  `session_assigned` lands on the agent's WS within ms.

### Stage 4b.7 — Hula admin SPA: `/analytics/chats` ✅
- `web/analytics/src/lib/api/chat.ts` — typed REST wrappers
  (10 endpoints, snake_case shapes).
- `web/analytics/src/lib/api/agentSocket.ts` — per-session WS
  client; reconnect 1/2/4/8/16/30s; presence + typing stores.
- `web/analytics/src/lib/api/agentControlSocket.ts` — control-WS
  client; auto-routes the SPA to `/chats/[id]` on
  `session_assigned`.
- Three routes (`/chats`, `/chats/live`, `/chats/[id]`).
- Sidebar: `Chats` entry between Visitors and Admin.
- Bundle stays under budget (20.4 KB first-load gzipped).

### Stage 4b.8 — tlalocwebsite chat widget 🚧 (committed, awaiting push)
- All four files (chat.js + chat.html partial + baseof.html
  modification + hugo.toml flag) are committed on `main` of the
  tlalocwebsite repo. The push fails from this CI/sandbox env
  because the GITHUB_AUTH_TOKEN baked into `tlaloc-deploy-site/.env`
  is read-only. A single `git push origin main` from a
  workstation with the right SSH key triggers hula's autodeploy
  and the bubble goes live.

### Stage 4b.9 — FTS v1 + search RPC + UI search box ✅
- Skip indexes shipped in 4b.1 (`tokenbf_v1` + `ngrambf_v1` on
  `chat_messages.content`).
- `SearchMessages` RPC uses `multiSearchAnyCaseInsensitive` +
  whitespace-token splitter on the Go side.
- SPA `/chats` search box — debounced 300 ms, generation
  counter to drop stale results, immediate-clear on query
  change, **`<mark>` highlighting** of matching tokens in the
  per-row snippet preview.
- Live: search "shipping" matched bidir-vid session with
  highlighted snippet; "doesnotexist" returned empty cleanly;
  rapid retype lands on the latest query.

### Stage 4b.10 — Status doc + sign-off ✅
- This document.
- Three new e2e suites at `test/e2e/suites/`:
  - `32-chat-admin.sh` — auth gate + ListSessions / GetSession /
    GetMessages / SearchMessages / TakeSession /
    PostAdminMessage / ReleaseSession / CloseSession +
    closed-session write rejection.
  - `33-chat-ws.sh` — visitor WS msg+ack round-trip;
    chat_messages persistence; agent-WS upgrade reachability;
    `presence_snapshot` on agent connect.
  - `34-chat-routing.sh` — control-WS `queue_snapshot` on
    connect; `session_assigned` lands within 2 s of a visitor
    `/chat/start` while an agent is in the ready pool.
- Fixture `docker-compose.yaml` sets
  `HULA_CHAT_CAPTCHA_TEST_BYPASS=1` so the suites can issue real
  chat tokens without a Turnstile site key. Production hula
  must NOT set this — boot logs a WARN when active.

## Final sign-off checklist (Phase 4b → Phase 5a)

- [x] Stages 4b.1 through 4b.7 + 4b.9 + 4b.10 landed live.
- [ ] Stage 4b.8 (widget) pushed to `tlalocwebsite/main`.
- [x] `make test-unit` green for `pkg/chat/...`,
      `pkg/api/v1/chat/...`, `pkg/store/...`.
- [x] `cd web/analytics && pnpm build` green; bundle ≤ 165 KB
      first-load gzipped (currently 20.4 KB).
- [x] No-auth on chat admin endpoints → 401; admin JWT but
      unknown server_id → 403; missing server_id → 400.
- [x] Captcha gate: bad token → 403/503; missing secret_key →
      503 with `captcha_unavail` (fail-closed, not silent).
- [x] FTS RPC: rapid-retype + zero-hit + match-highlight all
      verified live with playwright.
- [x] Round-robin fairness: routing-smoke confirmed agent
      automatically picked up an arriving session via control-WS.
- [x] Bidirectional thread: visitor → agent → visitor round-trip
      via two parallel WSes verified.
- [ ] End-to-end on the tlalocwebsite widget (post-push only).

## Deferred to Phase 4c+

- **Skill-based routing** (route by tag), per-agent capacity
  caps, SLA timers + alerts on backlog. Phase 4b ships pure
  round-robin only.
- **Bot auto-responder.** Documented as a future pluggable
  "responder" interface.
- **Multi-process broadcast.** The hub + router are in-memory.
  If hula scales horizontally, sessions on process A won't see
  messages posted to process B.
- **File / image attachments.** Text-only.
- **Push notifications** to admins on new chat sessions. The
  Phase-3 mailer is wired but not auto-fired; lands with the
  Phase-5a notification engine.
- **End-to-end encryption.** Plaintext in ClickHouse — required
  for FTS. Transport security is HTTPS/WSS.

## Pre-existing issues caught during 4b

- **Disk pressure during iterative builds.** Each
  `./build-docker.sh --local` adds ~500 MB of buildkit cache
  layers; without periodic `docker builder prune`, the host
  fills (we hit 95%+ multiple times). ClickHouse's log-rotation
  also failed under disk pressure, spamming Poco stack traces.
  No hula-side fix; ops note: cap `./ch_logs` size with
  `<size>10G</size>` in the CH config and prune buildkit
  weekly.
- **Stale hula-builder-* containers.** Builder containers
  spawned by Phase-3 site builds linger as "Up" beyond their
  useful life. They consume disk + show up confusingly in
  `docker ps`. Worth a cleanup ticker on hula's side.

## Files changed

### hula
- `config/chat.go` (new)
- `config/config.go` (added `Chat *ChatConfig`)
- `pkg/apispec/v1/chat/chat.proto` (new) + generated `.pb.go`s
- `pkg/api/v1/chat/chatimpl.go` (new) + `chatimpl_test.go`
- `pkg/api/v1/analytics/forget.go` (extended for chat tables)
- `pkg/chat/{hub,router,service,store,token,ratelimit}.go` +
  tests
- `pkg/chat/captcha/{captcha,captcha_test}.go`
- `pkg/chat/emailverify/{emailverify,emailverify_test}.go`
- `pkg/chat/moderate/{moderate,moderate_test}.go`
- `pkg/store/clickhouse/schema/chat_v1.sql` (new)
- `pkg/store/clickhouse/migrations/{runner.go,0003_chat_v1.sql}`
- `pkg/store/clickhouse/clickhouse.go` (added retention arg)
- `pkg/store/bolt/{bolt.go,chat.go,chat_test.go}`
- `server/{chat_acl,chat_boot,chat_start,chat_ws_visitor,chat_ws_agent,chat_ws_control}.go`
- `server/{run_unified,unified_boot}.go` (wire-up)
- `go.mod` (`github.com/AfterShip/email-verifier@v1.4.1`)

### hula admin SPA
- `web/analytics/src/lib/api/{chat,agentSocket,agentControlSocket}.ts`
- `web/analytics/src/lib/components/Sidebar.svelte`
- `web/analytics/src/routes/chats/{+page,live/+page,[id]/+page}.svelte`

### tlalocwebsite (committed, awaiting push)
- `themes/tlaloc/assets/js/chat.js`
- `themes/tlaloc/layouts/partials/chat.html`
- `themes/tlaloc/layouts/_default/baseof.html`
- `hugo.toml`

## How to verify

After pushing the tlalocwebsite commit:

1. Open https://www.tlaloc.us in browser A. Bottom-right shows
   the chat bubble.
2. Click → fill email + first message + Turnstile → "Start chat".
3. Open https://www.tlaloc.us/analytics/chats/live in browser B
   (logged in as admin).
4. Browser B's banner / table shows the new session within 1 s.
5. Browser B clicks the session → opens the thread view → types
   a reply → browser A sees the message + agent name.
6. Either side closes; both see the system close frame.
