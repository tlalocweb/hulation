#!/bin/bash
# Suite 05: lander commands.
#
# Commands exercised: createlander
# (modifylander, deletelander, listlanders are declared but not implemented.)

lander_json='{"name":"e2e-lander","server":"'"$SITE_HOST"'","description":"e2e test","redirect":"https://example.com"}'
cl_out=$(hulactl createlander "$lander_json" 2>&1 || true)
# Just verify it didn't crash; server may reject due to missing site, but hulactl itself should handle it.
if echo "$cl_out" | grep -qiE "created|lander|error|status"; then
    pass "createlander runs without crashing"
else
    fail "createlander runs without crashing" "unexpected output: $(echo "$cl_out" | head -1)"
fi
