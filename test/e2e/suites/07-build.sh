#!/bin/bash
# Suite 07: production site build via git autodeploy.
#
# Commands exercised: build, build-status, builds
# Uses the tlaloc-hula-test-site repo via GITHUB_AUTH_TOKEN.

# --- build ---
# hulactl's `build` command polls internally until complete/failed. It may
# take several minutes on first run (git clone + hugo). `hulactl` is a shell
# function; `timeout` can't call a function directly, so we export it or wrap
# via subshell. Simplest: the polling itself has a reasonable timeout inside
# hulactl; we just let it run and fail-fast if stuck.
echo "  triggering production build (may take a few minutes)..."
build_out=$(hulactl build testsite 2>&1 || true)

# build_out should end with "Build complete!" on success.
if echo "$build_out" | grep -q "Build complete"; then
    pass "build testsite completes"
else
    fail "build testsite completes" "output: $(echo "$build_out" | tail -5 | tr '\n' '|')"
    # Don't continue — the rest of the suite depends on this succeeding.
    return 0 2>/dev/null || exit 0
fi

# Extract the build ID from the output.
build_id=$(echo "$build_out" | grep -oE '[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}' | head -1)
if [ -n "$build_id" ]; then
    pass "build output contains a build ID"
else
    fail "build output contains a build ID" "could not extract UUID from output"
    return 0 2>/dev/null || exit 0
fi

# --- build-status ---
bs_out=$(hulactl build-status "$build_id" 2>&1 || true)
assert_contains "$bs_out" "Status:" "build-status returns a Status field"
assert_contains "$bs_out" "complete" "build-status shows complete"

# --- builds ---
b_out=$(hulactl builds testsite 2>&1 || true)
assert_contains "$b_out" "$build_id" "builds list includes the new build"

# --- verify served site ---
# After a successful build, curl should return the Hugo-rendered HTML.
site_body=$(curl_test "https://$SITE_HOST:443/" 2>&1 || true)
if echo "$site_body" | grep -qE "<html|<body|<!DOCTYPE"; then
    pass "production site serves HTML content"
else
    fail "production site serves HTML content" "first 200 chars: $(echo "$site_body" | head -c 200)"
fi
