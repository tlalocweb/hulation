# Backlog

Tracked items that aren't tied to an in-flight phase plan. Newest at top.

---

## CSP violation in /v/hula_hello.html iframe (low priority — Cloudflare injection)

**Filed:** 2026-04-28 — surfaced in browser console on www.tlaloc.us.

**Symptom.** Browser console reports:
```
hula_hello.html?h=tlaloc&u=https%3A%2F%2Fwww.tlaloc.us%2F:6
Executing inline script violates Content Security Policy directive
'script-src 'self' tlaloc.us *.tlaloc.us'.
```

**Root cause — not a hula bug.** Hula's template
(`handler/visitor.go:194-200`) is `<head><script src=...></script></head>
<body></body>` — empty body, single external script, fully
CSP-compliant. The inline script being blocked is injected by
Cloudflare's edge: a Bot Management / JS-Detection beacon
(`/cdn-cgi/challenge-platform/scripts/jsd/main.js` wrapped in an
inline IIFE with random per-request tokens). The two random tokens
in the IIFE mean the SHA hash of the script changes every request,
so adding it to a CSP `script-src` allowlist cannot work.

**Impact.** None to users. The blocked script is CF telemetry only —
chat widget, tracking pixel, page navigation, all still function.
The console noise is the only symptom. Filed because it's the kind
of thing that masks real CSP violations later.

**Resolution path (operator side, no hula change).**

1. **Preferred** — Cloudflare → Rules → Configuration Rules: match
   `URI Path contains "/v/"` → set "Bot Fight Mode: Off" and
   "Browser Integrity Check: Off". The analytics iframe is loaded
   *by* the parent page that already passed CF's checks, so doubling
   up doesn't add security. Rest of www.tlaloc.us keeps its CF
   protections.
2. **Alternative** — turn off Bot Fight Mode zone-wide. Heavier hand,
   only if the surgical rule isn't an option.
3. **Avoid** — relaxing the CSP to `'unsafe-inline'`. Defeats the
   protection. Hula could emit nonces in a future change but they
   wouldn't help here because CF injects after hula's response is
   sent.

**Action item if/when hula does start emitting CSP nonces.** The
nonce machinery should still NOT cover CF-injected scripts; it'd
be wasted work. Resolution is at the CF layer.

---

## Associate visitor cookie with email on chat sign-up

**Filed:** 2026-04-27 — surfaced after Phase 4b chat went live on www.tlaloc.us.

**Symptom.** When a visitor opens the chat widget and submits their email
to `/chat/start`, that email currently lives only on the chat session row.
The visitor's tracking cookie (the `visitor_id` we already key analytics
events by) stays anonymous on the Visitors list — the operator can't see
"this anonymous visitor #abc123 is actually `jane@acme.com`".

**Desired behavior.**

1. When `/chat/start` accepts a session, write the (`server_id`,
   `visitor_id`, `email`) tuple to a visitor-identity store so it survives
   beyond the chat session.
2. The Visitors admin list (`/analytics/visitors`) should join against
   that store and display the email alongside the visitor row when known.
   Sort/filter by `has_email` would also be useful.
3. If the visitor returns later (same cookie, different chat session, or
   no chat at all) the email association persists. Subsequent chat
   sessions under the same `visitor_id` should auto-prefill the email
   form field client-side.
4. Multiple emails per visitor are possible (shared device, or visitor
   updates email later). Keep a history; surface the most-recent in the
   list view, expose the full set on the visitor detail page.

**Storage sketch.** Probably a new ClickHouse `visitor_identities`
ReplacingMergeTree keyed by `(server_id, visitor_id, email)` with
`first_seen` / `last_seen` columns — same shape as the existing chat
tables. Cheap to join into the Visitors query.

**Privacy / compliance.** Email is already PII we accept via the chat
form; promoting it to the visitor record doesn't change the legal
posture, but `ForgetVisitor` (the existing GDPR delete path) MUST also
delete from `visitor_identities`. Add coverage in the e2e suite that
already exercises ForgetVisitor.

**Where to make the change.**

- Write site: `pkg/api/v1/chat/chatimpl.go` — the `/chat/start` handler
  already has both `visitor_id` and the validated `email` in scope; emit
  the identity row alongside the existing session insert.
- Read site: the visitors list query (search for `events` /
  `visitor_id` aggregations under `pkg/api/v1/analytics/`) — LEFT JOIN
  `visitor_identities` and project the email.
- Forget path: `pkg/api/v1/analytics/forget.go` — add the new table to
  the per-visitor delete fan-out.

---

## Auto-rebuild git-autodeploy sites on hula startup when HEAD has moved

**Filed:** 2026-04-27 — surfaced while bringing up the chat widget on www.tlaloc.us.

**Symptom.** A fresh hula start serves the previously-cached Hugo build even
when the upstream `root_git_autodeploy` repo has new commits. Concretely we
saw `repo/` HEAD already at `359e78e` (chat-widget commit) but
`.last-build-commit` two days behind at `b54b0f1` — Hugo never re-ran on
boot, so the served HTML lacked the widget partial. Operator had to run
`hulactl build <id>` by hand to get the new build.

**Desired behavior.**

For every server with `root_git_autodeploy` configured:

1. **Serve immediately** from the existing build under
   `/var/hula/sitedeploy/<id>/site/` (zero downtime, no first-request stall).
2. **In a background goroutine**, fetch the configured ref (branch / tag).
   If `git rev-parse <ref>` differs from `.last-build-commit`, run a build
   the same way `hulactl build` does — into a staging dir.
3. **Atomically swap** the live `site/` symlink to the new build directory
   when the build finishes successfully, then update `.last-build-commit`.
4. On build failure, log loudly but keep serving the existing build —
   never blank the site because of a transient build failure.

**Opt-out.** Add a sibling field to `root_git_autodeploy`:

```yaml
root_git_autodeploy:
  repo: ...
  ref: { branch: main }
  hula_build: production
  no_rebuild_on_start: true   # default: false
```

When `true`, skip step 2/3 entirely and serve the existing build until the
operator triggers `hulactl build` manually. Useful for canary holds /
manual deploy gates.

**Where to make the change.**

- Boot path: `server/run_unified.go` around the `hasAutoDeploy` block
  (`run_unified.go:267-293`). Today it only kicks off `StartupStaging` for
  staging-mode containers; production-mode autodeploy gets nothing on
  startup. Add a `go buildMgr.StartupRebuild(conf.Servers)` call
  alongside the existing `go stagingMgr.StartupStaging(...)`.
- Config: `config.GitAutoDeployConfig` (in `config/config.go`) needs the
  new `NoRebuildOnStart bool \`yaml:"no_rebuild_on_start,omitempty"\``
  field.
- Logic: `pkg/sitedeploy/buildmanager.go` (or wherever `BuildManager` lives)
  for the new `StartupRebuild` method that diffs HEAD against
  `.last-build-commit` and dispatches a regular build through the existing
  pipeline so we don't duplicate the Docker-builder orchestration.

**Test coverage.** Add an e2e suite that:
1. Builds against commit A, snapshots `.last-build-commit`.
2. Pushes commit B to the test site repo.
3. Restarts the hula container.
4. Asserts the served HTML reflects commit B within ~30s without a manual
   `hulactl build`.
