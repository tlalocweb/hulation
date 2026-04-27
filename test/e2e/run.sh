#!/bin/bash
# Hula E2E test runner.
#
# Usage:
#   ./test/e2e/run.sh                 # full run (setup + all suites + teardown)
#   ./test/e2e/run.sh --suite 01      # run only suite 01 (still does setup/teardown)
#   ./test/e2e/run.sh --no-setup      # skip the build+compose-up phase
#   ./test/e2e/run.sh --keep          # skip teardown (leave stack running)
#
# Suites are in test/e2e/suites/NN-name.sh and run in lexicographic order.

set -uo pipefail

HULA_E2E_ROOT="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HULA_E2E_ROOT/../.." && pwd)"
export HULA_E2E_ROOT REPO_ROOT

# shellcheck source=lib/harness.sh
. "$HULA_E2E_ROOT/lib/harness.sh"
# shellcheck source=lib/setup.sh
. "$HULA_E2E_ROOT/lib/setup.sh"
# shellcheck source=lib/teardown.sh
. "$HULA_E2E_ROOT/lib/teardown.sh"

ONLY_SUITE=""
DO_SETUP=1
DO_TEARDOWN=1

while [ $# -gt 0 ]; do
    case "$1" in
        --suite) ONLY_SUITE="$2"; shift 2 ;;
        --no-setup) DO_SETUP=0; shift ;;
        --keep) DO_TEARDOWN=0; shift ;;
        -h|--help)
            head -12 "$0" | sed -n 's/^# *//p'
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            exit 2
            ;;
    esac
done

export PASSED=0
export FAILED=0

echo "=== Hula E2E Tests ==="

if [ $DO_TEARDOWN -eq 1 ]; then
    trap 'e2e_teardown; echo ""; echo "=== Results: $PASSED passed, $FAILED failed ==="; if [ "$FAILED" -gt 0 ]; then exit 1; fi' EXIT
else
    trap 'echo ""; echo "=== Results: $PASSED passed, $FAILED failed ==="; echo "(stack left running; use docker compose -p $COMPOSE_PROJECT -f $COMPOSE_FILE down -v to stop)"; if [ "$FAILED" -gt 0 ]; then exit 1; fi' EXIT
fi

if [ $DO_SETUP -eq 1 ]; then
    e2e_setup
else
    # Re-derive env vars without rebuilding
    WORKDIR="$HULA_E2E_ROOT/workdir"
    COMPOSE_PROJECT="hula-e2e"
    COMPOSE_FILE="$HULA_E2E_ROOT/fixtures/docker-compose.yaml"
    HULA_HOST="hula.test.local"
    SITE_HOST="site.test.local"
    STAGING_HOST="staging.test.local"
    HULA_HOST_PORT="${HULA_HOST_PORT:-4443}"
    export HULA_E2E_ROOT REPO_ROOT WORKDIR COMPOSE_PROJECT COMPOSE_FILE
    export HULA_HOST SITE_HOST STAGING_HOST HULA_HOST_PORT
    TEST_SITE_SRC="${TEST_SITE_SRC:-/home/ubuntu/work/tlaloc-hula-test-site}"
    export TEST_SITE_SRC
    if [ -f "$WORKDIR/admin_password.txt" ]; then
        ADMIN_PASS=$(cat "$WORKDIR/admin_password.txt")
        export ADMIN_PASS
    fi
    # Load .env for GITHUB_AUTH_TOKEN
    if [ -f "$HULA_E2E_ROOT/.env" ]; then
        set -a
        . "$HULA_E2E_ROOT/.env"
        set +a
    fi
fi

SUITE_DIR="$HULA_E2E_ROOT/suites"
suites=()
if [ -n "$ONLY_SUITE" ]; then
    for f in "$SUITE_DIR"/${ONLY_SUITE}-*.sh; do
        [ -e "$f" ] && suites+=("$f")
    done
    if [ ${#suites[@]} -eq 0 ]; then
        echo "No suite matching '$ONLY_SUITE' found in $SUITE_DIR" >&2
        exit 2
    fi
else
    for f in "$SUITE_DIR"/*.sh; do
        [ -e "$f" ] && suites+=("$f")
    done
fi

for suite in "${suites[@]}"; do
    name=$(basename "$suite" .sh)
    echo ""
    echo "--- Suite: $name ---"
    # shellcheck disable=SC1090
    . "$suite"
done
