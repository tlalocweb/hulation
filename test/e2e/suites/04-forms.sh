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
# Must go through runner_shell: `dc run … hulactl-runner sh -c "…"` does NOT
# run a shell — the compose entrypoint ends in `exec hulactl "$@"`, so the
# script arrives as arguments to hulactl. Its error output then survived the
# pipeline as a non-empty string, so the emptiness check below passed while
# `token` held junk.
token=$(runner_shell 'cat /root/.hula/hulactl.yaml' 2>/dev/null \
    | grep -oE 'token: [^ ]+' | head -1 | awk '{print $2}' || true)
if [ -z "$token" ]; then
    fail "could not read admin token for form ID lookup"
    return 0 2>/dev/null || exit 0
fi
pass "read admin token for form ID lookup"

# Actually USE the token. It was previously read and then never referenced, so
# nothing verified it was a working credential rather than a junk string —
# which is precisely how the old broken read went unnoticed. A 200 from an
# authenticated, permission-gated endpoint proves both.
forms_status=$(curl_test -s -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer ${token}" \
    "https://${HULA_HOST}/api/v1/forms/testsite-seed" || true)
if [ "$forms_status" = "200" ]; then
    pass "admin token authenticates against the forms API (HTTP 200)"
else
    fail "forms API rejected the admin token (HTTP ${forms_status})"
fi

# Try modifyform on the just-created form. We don't have the ID, so as a smoke
# test we pass a fake ID and expect a server error (not a crash).
#
# Match on hulactl's actual failure output — it prints "Error: <msg>" — and
# require the success line to be ABSENT. The old pattern (error|status|400|404,
# case-insensitive) matched almost any output including a success response, so
# it could not meaningfully fail.
mf_out=$(hulactl modifyform "nonexistent-form-id" '{"name":"x","schema":"{}"}' 2>&1 || true)
if echo "$mf_out" | grep -q 'Error:' && ! echo "$mf_out" | grep -q 'Form created'; then
    pass "modifyform handles unknown form id gracefully"
else
    fail "modifyform handles unknown form id gracefully" "got: $(echo "$mf_out" | head -2 | tr '\n' ' ')"
fi

# submitform with a fake id should return a server error
sf_out=$(hulactl submitform "nonexistent-form-id" '{"url":"https://site.test.local","fields":{},"sscookie":""}' 2>&1 || true)
if echo "$sf_out" | grep -q 'Error:'; then
    pass "submitform handles unknown form id gracefully"
else
    fail "submitform handles unknown form id gracefully" "got: $(echo "$sf_out" | head -2 | tr '\n' ' ')"
fi
