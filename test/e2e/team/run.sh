#!/bin/bash
# HA Stage 3 multi-node team e2e harness.
#
# Brings up a 3-node hula team (east/west/emea) on its own
# docker-compose project, runs the team suites, tears down.
#
# Usage:
#   ./test/e2e/team/run.sh                  # full run
#   ./test/e2e/team/run.sh --suite 41       # single suite
#   ./test/e2e/team/run.sh --keep           # leave the stack running
#
# Suites live under test/e2e/team/suites/. They run in lex order
# and share the same compose project so a single bring-up amortises
# across all of them.

set -uo pipefail

TEAM_E2E_ROOT="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$TEAM_E2E_ROOT/../../.." && pwd)"
WORKDIR="$TEAM_E2E_ROOT/workdir"
COMPOSE_FILE="$TEAM_E2E_ROOT/docker-compose.team.yaml"
COMPOSE_PROJECT="hulateam"

ONLY_SUITE=""
DO_TEARDOWN=1

while [ $# -gt 0 ]; do
    case "$1" in
        --suite) ONLY_SUITE="$2"; shift 2 ;;
        --keep)  DO_TEARDOWN=0; shift ;;
        -h|--help)
            head -14 "$0" | sed -n 's/^# *//p'
            exit 0
            ;;
        *) echo "Unknown argument: $1" >&2; exit 2 ;;
    esac
done

export TEAM_E2E_ROOT REPO_ROOT WORKDIR COMPOSE_FILE COMPOSE_PROJECT

log() { printf '[team-e2e] %s\n' "$*"; }
err() { printf '[team-e2e] ERROR: %s\n' "$*" >&2; }

cleanup() {
    if [ "$DO_TEARDOWN" = "1" ]; then
        log "Tearing down team compose project..."
        docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans 2>&1 | tail -5
    else
        log "--keep set: leaving stack running."
        log "  docker compose -p $COMPOSE_PROJECT -f $COMPOSE_FILE down -v"
    fi
}
trap cleanup EXIT

mkdir -p "$WORKDIR"

# ---- Build hulactl (we need it on team-runner via bind mount) ----
log "Building hulactl for team-runner..."
(
    cd "$REPO_ROOT"
    PATH="$REPO_ROOT/.bin/go/bin:$PATH" go build -o "$WORKDIR/hulactl" ./model/tools/hulactl/
) || { err "hulactl build failed"; exit 1; }

# ---- Generate the team bundle (offline) so we know the team_id ----
TEAM_ID="${TEST_TEAM_ID:-$(uuidgen)}"
log "Team ID: $TEAM_ID"
rm -rf "$WORKDIR/team-bundles"
"$WORKDIR/hulactl" \
    --team-id "$TEAM_ID" \
    --nodes hula-east,hula-west,hula-emea \
    --validity 24h \
    --out "$WORKDIR/team-bundles" \
    genteamcerts >/dev/null 2>&1 || { err "genteamcerts failed"; exit 1; }
TEAM_TOKEN="$(cat "$WORKDIR/team-bundles/bootstrap-token")"

# ---- Render per-node configs ----
ADMIN_PASS="${ADMIN_PASS:-changeme123!}"
ADMIN_ARGON_HASH="${ADMIN_ARGON_HASH:-$("$WORKDIR/hulactl" generatehash <<<"$ADMIN_PASS" 2>/dev/null || echo "stub")}"
JWT_KEY="${JWT_KEY:-$(head -c 32 /dev/urandom | base64)}"
export TEAM_ID ADMIN_ARGON_HASH JWT_KEY

mkdir -p "$WORKDIR/configs"
for node in hula-east:true:first-of-team hula-west:false:join hula-emea:false:join; do
    NODE_ID="${node%%:*}"
    rest="${node#*:}"
    IS_CH_CONNECTED="${rest%%:*}"
    BOOTSTRAP_MODE="${rest##*:}"
    export NODE_ID IS_CH_CONNECTED BOOTSTRAP_MODE
    envsubst < "$TEAM_E2E_ROOT/lib/team-config.yaml.tmpl" \
        > "$WORKDIR/configs/${NODE_ID}.yaml"
done

# ---- Bring up the compose project ----
export HULA_TEAM_BOOTSTRAP_TOKEN="$TEAM_TOKEN"
export TEST_TEAM_ID="$TEAM_ID"

log "Bringing up the team compose project..."
docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" up -d 2>&1 | tail -10
if [ "${PIPESTATUS[0]}" != "0" ]; then
    err "compose up failed"
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" logs --tail 50
    exit 1
fi

# ---- Wait for the cluster to form ----
log "Waiting for cluster formation (up to 60s)..."
deadline=$(( $(date +%s) + 60 ))
while [ "$(date +%s)" -lt "$deadline" ]; do
    if docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" exec -T team-runner \
        sh -c "hulactl --pki-dir /team-bundles/hula-east \
            --team-id $TEAM_ID \
            team-status hula-east.team.internal:443 2>/dev/null | grep -q 'has_quorum:    true'"
    then
        log "Cluster has quorum."
        break
    fi
    sleep 2
done
if [ "$(date +%s)" -ge "$deadline" ]; then
    err "Cluster did not reach quorum within 60s"
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" logs --tail 100
    exit 1
fi

# ---- Run suites ----
TOTAL=0
PASSED=0
FAILED=0

for suite in "$TEAM_E2E_ROOT"/suites/*.sh; do
    name="$(basename "$suite" .sh)"
    if [ -n "$ONLY_SUITE" ] && [[ "$name" != *"$ONLY_SUITE"* ]]; then
        continue
    fi
    TOTAL=$((TOTAL+1))
    log "Running $name..."
    if bash "$suite"; then
        log "$name PASSED"
        PASSED=$((PASSED+1))
    else
        err "$name FAILED"
        FAILED=$((FAILED+1))
    fi
done

log "Summary: $PASSED/$TOTAL passed, $FAILED failed."
[ "$FAILED" -eq 0 ]
