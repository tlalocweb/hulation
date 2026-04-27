#!/bin/bash
# Suite 06: badactors command — smoke test only.
#
# Bad actor detection is DISABLED in the e2e config to prevent test IPs from
# getting blocked mid-run. When detection is disabled, the /api/badactor/*
# routes are not registered, so `hulactl badactors` receives 404.
# We just verify the command doesn't crash and produces a meaningful error.

ba_out=$(hulactl badactors 2>&1 || true)
if echo "$ba_out" | grep -qE "Bad Actor Status|404|not found|error|Error|disabled"; then
    pass "badactors command produces meaningful output (enabled or disabled)"
else
    fail "badactors command produces meaningful output" "got: $(echo "$ba_out" | tail -3 | tr '\n' '|')"
fi
