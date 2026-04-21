#!/bin/bash
# Suite 08: staging rebuild via API.
#
# Commands exercised: staging-build

# The staging container should be running already (started at hula init).
# A staging-build triggers hugo inside it via EXEC_BUILD protocol.

sb_out=$(hulactl staging-build testsite-staging 2>&1 || true)
assert_contains "$sb_out" "Staging build" "staging-build returns status"

if echo "$sb_out" | grep -qE "error|failed"; then
    fail "staging-build completes without error" "output: $(echo "$sb_out" | tail -5)"
else
    pass "staging-build completes without error"
fi

# Verify the staging site serves Hugo output.
staging_body=$(curl_test "https://$STAGING_HOST:443/" 2>&1 || true)
if echo "$staging_body" | grep -qE "<html|<body|<!DOCTYPE"; then
    pass "staging site serves HTML content"
else
    fail "staging site serves HTML content" "first 200 chars: $(echo "$staging_body" | head -c 200)"
fi

# Trigger two staging-builds back to back. The second should either succeed
# after the first finishes (serialized) or return a 409 (busy) — both are
# valid outcomes. It must not queue indefinitely.
sb2=$(hulactl staging-build testsite-staging 2>&1 || true)
if echo "$sb2" | grep -qE "Staging build|busy|409|progress"; then
    pass "second staging-build handled sanely (complete or busy)"
else
    fail "second staging-build handled sanely" "got: $(echo "$sb2" | head -3)"
fi
