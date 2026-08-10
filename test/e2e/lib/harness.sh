#!/bin/bash
# Shared helpers for the e2e test harness.
#
# Variables set by setup.sh (must be in the environment when a suite runs):
#   HULA_E2E_ROOT   — absolute path to test/e2e/
#   REPO_ROOT       — absolute path to the hulation repo root
#   COMPOSE_PROJECT — docker compose project name
#   COMPOSE_FILE    — path to fixtures/docker-compose.yaml
#   WORKDIR         — e2e work dir (certs, config, temp files)
#   HULA_HOST       — hula.test.local
#   SITE_HOST       — site.test.local
#   STAGING_HOST    — staging.test.local
#   HULA_HOST_PORT  — host port mapped to container :443
#   ADMIN_PASS      — generated admin password
#
# And exports these counters (incremented by pass/fail):
#   PASSED, FAILED

set -u

PASSED=${PASSED:-0}
FAILED=${FAILED:-0}

pass() {
    PASSED=$((PASSED + 1))
    echo "  PASS: $1"
}

fail() {
    FAILED=$((FAILED + 1))
    echo "  FAIL: $1"
    if [ -n "${2:-}" ]; then
        echo "        $2"
    fi
}

assert_eq() {
    local got="$1" want="$2" msg="$3"
    if [ "$got" = "$want" ]; then
        pass "$msg"
    else
        fail "$msg" "got '$got' want '$want'"
    fi
}

assert_contains() {
    local body="$1" expected="$2" msg="$3"
    # -F: fixed string (no regex). Matters for patterns containing $, *, etc.
    if echo "$body" | grep -qF "$expected"; then
        pass "$msg"
    else
        fail "$msg" "expected '$expected' in output"
    fi
}

assert_not_contains() {
    local body="$1" notexpected="$2" msg="$3"
    if echo "$body" | grep -qF "$notexpected"; then
        fail "$msg" "did NOT expect '$notexpected' in output"
    else
        pass "$msg"
    fi
}

# --- Docker compose wrappers ---

dc() {
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"
}

# dcq: same as dc but silences the docker compose noise (warnings like
# "No services to build"). Errors from the command itself still surface.
dcq() {
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@" 2> >(grep -vE "level=warning|No services to build" >&2)
}

# Run hulactl inside a short-lived container.
hulactl() {
    dcq run --rm -T hulactl-runner "$@"
}

# Run an arbitrary shell command inside the hulactl-runner container.
# Overrides the entrypoint so the hulactl.yaml volume and other runner volumes
# are available to the shell.
#
# The compose entrypoint installs curl/jq/ca-certificates + sets /etc/hosts
# aliases; since --entrypoint skips that script, we have to re-run its
# important bits (ca-certificates install + hostname mapping) before the
# user's command or every test that needs hula.test.local resolution fails.
runner_shell() {
    dcq run --rm -T --entrypoint /bin/sh hulactl-runner -c "$(runner_preamble)"'
        '"$*"
}

# runner_preamble is the setup every --entrypoint-overriding runner invocation
# needs: CA trust, the tools the suites shell out to, and the test-hostname
# mappings. Factored out so runner_shell and runner_shell_bg can't drift —
# they previously carried separate copies of this, and only one of them knew
# about each tool.
#
# websocat is here (not just in the compose entrypoint) because every WebSocket
# suite reaches the runner through an --entrypoint override, which skips that
# entrypoint script entirely.
runner_preamble() {
    cat <<'PREAMBLE'
        if ! command -v curl >/dev/null 2>&1 || ! command -v websocat >/dev/null 2>&1; then
          apk add --no-cache ca-certificates curl jq websocat >/dev/null 2>&1
        fi
        update-ca-certificates >/dev/null 2>&1
        HULA_IP=$(getent hosts hula | awk "{print \$1}")
        echo "$HULA_IP hula.test.local site.test.local staging.test.local seed.test.local seed-cookieless.test.local hugo-min.test.local mkdocs-min.test.local proxy.test.local" >> /etc/hosts
PREAMBLE
}

# Run a shell command inside a DETACHED, named runner container — for
# long-running clients (websocat holding a WebSocket open) whose output a suite
# collects later with `docker logs <name>`.
#
# This exists because the obvious form is silently wrong:
#
#   dc run --rm -T -d --name X hulactl-runner sh -c "websocat ..."
#
# The compose entrypoint is ["/bin/sh","-c",<script>,"--"] ending in
# `exec hulactl "$@"`, so a trailing `sh -c "..."` becomes ARGUMENTS TO
# hulactl and the script never runs. Overriding the entrypoint is the only way
# to actually execute a shell in this container — and doing so means
# re-applying the preamble, since the override skips it.
#
# Deliberately NOT --rm: callers collect output with `docker logs <name>` after
# the client exits, and --rm deletes the container the instant it does — so the
# log read races the teardown and usually loses ("No such container"). The
# suites remove these explicitly with `docker rm -f` once they've read them.
#
# Output goes to stdout, not a file inside the container: a redirect to
# /tmp/foo.out is unreadable from the host and vanishes with the container.
#
# Usage: runner_shell_bg <container-name> '<shell command>'
runner_shell_bg() {
    local name="$1" cmd="$2"
    dcq run -T -d --name "$name" --entrypoint /bin/sh hulactl-runner -c "$(runner_preamble)"'
        '"$cmd" >/dev/null 2>&1 || true
}

# Run the `auth` command non-interactively using HULACTL_IDENTITY and
# HULACTL_PASSWORD env vars (hulactl skips the readline prompts when they're set).
hulactl_auth() {
    local host="$1" identity="${2:-admin}" password="$3"
    dcq run --rm -T \
        -e HULACTL_IDENTITY="$identity" \
        -e HULACTL_PASSWORD="$password" \
        hulactl-runner auth "$host"
}

# Run curl inside the test-runner container.
curl_test() {
    dcq run --rm -T test-runner curl -sS "$@"
}

# Get the HTTP status code of a URL (inside the test network).
curl_status() {
    dcq run --rm -T test-runner curl -sS -o /dev/null -w '%{http_code}' "$@"
}

# Wait for a condition to hold. Retries every 2s up to $2 seconds (default 60).
wait_for() {
    local desc="$1" timeout="${2:-60}" check="$3"
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        if eval "$check" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    echo "    timeout waiting for: $desc" >&2
    return 1
}

# Poll a build's status until complete or failed.
# Usage: poll_build <build-id> [timeout-seconds]
# Returns 0 if complete, 1 if failed or timed out.
poll_build() {
    local build_id="$1" timeout="${2:-300}" elapsed=0
    while [ $elapsed -lt $timeout ]; do
        local status
        status=$(hulactl build-status "$build_id" 2>/dev/null | awk '/^Status:/ {print $2}') || true
        case "$status" in
            complete) return 0 ;;
            failed) return 1 ;;
        esac
        sleep 3
        elapsed=$((elapsed + 3))
    done
    return 1
}
