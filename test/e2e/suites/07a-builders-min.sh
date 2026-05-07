#!/bin/bash
# Suite 07a: end-to-end production build for each generator we
# support, against a self-contained fixture committed under
# test/e2e/fixtures/sites/. No external repo / GitHub auth.
#
# Each fixture covers a different code path:
#
#   - hugo-min/ — explicit .hula/sitebuild.yaml. Exercises the
#     "operator hand-wrote the config" branch.
#   - mkdocs-min/ — no sitebuild.yaml. Exercises P1's auto-detection
#     fallback and the synthesised default profile.
#
# Per fixture, the suite asserts:
#   1. `hulactl build` reports completion (no generator-side errors).
#   2. The served URL responds with the expected generator output
#      AND contains the fixture's marker string (proving the build
#      actually used our source tree, not a stale leftover).
#
# Together with suite 07 (which exercises the Hugo-specific external
# tlaloc-hula-test-site repo via GitHub auth), this gives us:
#
#   suite 07  → real-world repo, GitHub auth path, tagged builds
#   suite 07a → minimal in-repo fixtures, no external deps,
#               full mkdocs path AND hugo auto-detection + explicit
#               sitebuild.yaml paths

build_one() {
    local server_id="$1" host="$2" marker="$3" generator="$4"
    echo "  --- $generator: $server_id ---"

    echo "    triggering build (may take ~1 min on first run)..."
    local build_out
    build_out=$(hulactl build "$server_id" 2>&1 || true)

    if echo "$build_out" | grep -q "Build complete"; then
        pass "$generator build completes ($server_id)"
    else
        fail "$generator build completes ($server_id)" \
            "tail: $(echo "$build_out" | tail -5 | tr '\n' '|')"
        return
    fi

    # Served HTML check. We expect:
    #   - HTML doctype / tag
    #   - the fixture's unique marker (proves the build used our source)
    local body
    body=$(curl_test "https://$host:443/" 2>&1 || true)
    if echo "$body" | grep -qE "<html|<body|<!DOCTYPE"; then
        pass "$generator site serves HTML ($host)"
    else
        fail "$generator site serves HTML ($host)" \
            "first 200: $(echo "$body" | head -c 200)"
        return
    fi
    if echo "$body" | grep -qF "$marker"; then
        pass "$generator output contains fixture marker '$marker'"
    else
        fail "$generator output contains fixture marker '$marker'" \
            "marker missing — build may have served a stale or wrong tree"
    fi
}

build_one "hugo-min"   "hugo-min.test.local"   "hugo-min-marker-9876"   "hugo"
build_one "mkdocs-min" "mkdocs-min.test.local" "mkdocs-min-marker-1234" "mkdocs"
