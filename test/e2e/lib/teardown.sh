#!/bin/bash
# Teardown: stop docker-compose, remove any stray builder containers, clean workdir.

set -u

e2e_teardown() {
    echo ""
    echo "--- Teardown ---"
    if [ -n "${COMPOSE_PROJECT:-}" ] && [ -n "${COMPOSE_FILE:-}" ]; then
        docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" \
            down -v --remove-orphans 2>/dev/null || true
    fi
    # Remove any stray builder containers that hula spawned.
    local stray
    stray=$(docker ps -a --filter "name=^hula-builder-" --format '{{.ID}}' 2>/dev/null || true)
    if [ -n "$stray" ]; then
        echo "  removing stray builder containers"
        echo "$stray" | xargs -r docker rm -f >/dev/null 2>&1 || true
    fi
    if [ -n "${WORKDIR:-}" ] && [ -d "$WORKDIR" ]; then
        # Keep admin_password.txt for debugging if tests failed
        if [ "${FAILED:-0}" -eq 0 ]; then
            rm -rf "$WORKDIR"
            echo "  removed $WORKDIR"
        else
            echo "  kept $WORKDIR (tests failed)"
        fi
    fi
}
