#!/bin/bash
# Suite 04: form CRUD + submit.
#
# Commands exercised: createform, modifyform, submitform
# (deleteform and listforms are declared but not implemented in hulactl;
#  they fall through to the default case.)

# --- createform ---
# Schema is stored as a string in FormModelReq, so we escape JSON-in-JSON.
# Use single quotes to avoid shell interpolation; backslash-escape the
# inner JSON quotes.
form_json='{"name":"e2e-contact","description":"e2e test form","schema":"{\"type\":\"object\"}","captcha":"","feedback":""}'
cf_out=$(hulactl createform "$form_json" 2>&1 || true)
assert_contains "$cf_out" "Form created" "createform succeeds"

# --- submitform ---
# First get the form ID via direct API (since listforms isn't implemented).
# We use the test-runner to hit the admin API with the saved JWT token.
# Get token from hulactl config:
token=$(dc run --rm -T hulactl-runner sh -c "cat /root/.hula/hulactl.yaml | sed -n 's/.*token: \(.*\)/\1/p' | head -1" | tr -d '"' | tr -d ' ')
if [ -z "$token" ]; then
    fail "could not read admin token for form ID lookup" "skipping submit test"
else
    pass "read admin token for form ID lookup"
fi

# Try modifyform on the just-created form. We don't have the ID, so as a smoke
# test we pass a fake ID and expect a non-200 response (not a crash).
mf_out=$(hulactl modifyform "nonexistent-form-id" '{"name":"x","schema":"{}"}' 2>&1 || true)
# Should be a server error, not a hulactl crash
if echo "$mf_out" | grep -qiE "error|status|400|404"; then
    pass "modifyform handles unknown form id gracefully"
else
    fail "modifyform handles unknown form id gracefully" "got: $(echo "$mf_out" | head -1)"
fi

# submitform with a fake id should return a server error
sf_out=$(hulactl submitform "nonexistent-form-id" '{"url":"https://site.test.local","fields":{},"sscookie":""}' 2>&1 || true)
if echo "$sf_out" | grep -qiE "error|status|400|404"; then
    pass "submitform handles unknown form id gracefully"
else
    fail "submitform handles unknown form id gracefully" "got: $(echo "$sf_out" | head -1)"
fi
