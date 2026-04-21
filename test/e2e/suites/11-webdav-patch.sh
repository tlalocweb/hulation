#!/bin/bash
# Suite 11: Direct WebDAV PATCH tests.
#
# Exercises the two PATCH modes on the staging WebDAV endpoint:
#   - X-Update-Range (SabreDAV byte-range convention)
#   - X-Patch-Format: diff (unified-diff apply)
#
# These tests bypass hulactl and hit the WebDAV endpoint directly with curl,
# using the admin JWT for auth.

# Get the admin token from the hulactl config inside the runner container.
TOKEN=$(dc run --rm -T hulactl-runner sh -c '
    awk "/token:/ {gsub(/\"/, \"\"); print \$2; exit}" /root/.hula/hulactl.yaml
' | tr -d ' \r\n')

if [ -z "$TOKEN" ]; then
    fail "WebDAV tests need an admin token" "could not extract token from hulactl config"
    return 0 2>/dev/null || exit 0
fi
pass "extracted admin token for WebDAV tests"

DAV_BASE="https://$HULA_HOST:443/api/staging/testsite-staging/dav"
TEST_FILE="e2e-patch-test.txt"

# --- Step 1: PUT an initial file ---
initial_content="Line 1
Line 2
Line 3
Line 4
Line 5"

put_status=$(dc run --rm -T test-runner sh -c "
    printf '%s' '$initial_content' | curl -sS -o /dev/null -w '%{http_code}' \
        -X PUT \
        -H 'Authorization: Bearer $TOKEN' \
        --data-binary @- \
        '$DAV_BASE/$TEST_FILE'
")
# Accept 200/201/204
if [ "$put_status" = "201" ] || [ "$put_status" = "204" ] || [ "$put_status" = "200" ]; then
    pass "PUT creates the test file (status $put_status)"
else
    fail "PUT creates the test file" "got status $put_status"
    return 0 2>/dev/null || exit 0
fi

# --- Step 2: PATCH X-Update-Range (bytes) ---
# Replace the first 6 bytes ("Line 1") with "NEW  1".
patch_range_status=$(dc run --rm -T test-runner sh -c "
    printf 'NEW  1' | curl -sS -o /dev/null -w '%{http_code}' \
        -X PATCH \
        -H 'Authorization: Bearer $TOKEN' \
        -H 'X-Update-Range: bytes=0-5' \
        --data-binary @- \
        '$DAV_BASE/$TEST_FILE'
")
if [ "$patch_range_status" = "204" ]; then
    pass "PATCH X-Update-Range returns 204"
else
    fail "PATCH X-Update-Range returns 204" "got status $patch_range_status"
fi

# Verify content
after_range=$(dc run --rm -T test-runner sh -c "
    curl -sS -H 'Authorization: Bearer $TOKEN' '$DAV_BASE/$TEST_FILE'
")
if echo "$after_range" | head -1 | grep -q "^NEW  1$"; then
    pass "byte range was applied correctly"
else
    fail "byte range was applied correctly" "first line: $(echo "$after_range" | head -1)"
fi

# --- Step 3: PATCH X-Patch-Format: diff ---
# Build a unified diff that changes "Line 3" to "DIFF 3" and leaves others intact.
# Since we already modified line 1 above, the current content is:
#   NEW  1\nLine 2\nLine 3\nLine 4\nLine 5
diff_body='--- a/f
+++ b/f
@@ -1,5 +1,5 @@
 NEW  1
 Line 2
-Line 3
+DIFF 3
 Line 4
 Line 5'

# Important: -d strips newlines, so use --data-binary
patch_diff_status=$(dc run --rm -T test-runner sh -c "
    cat <<'__EOF__' | curl -sS -o /dev/null -w '%{http_code}' \
        -X PATCH \
        -H 'Authorization: Bearer $TOKEN' \
        -H 'X-Patch-Format: diff' \
        -H 'Content-Type: text/x-diff' \
        --data-binary @- \
        '$DAV_BASE/$TEST_FILE'
$diff_body
__EOF__
")
if [ "$patch_diff_status" = "204" ]; then
    pass "PATCH X-Patch-Format: diff returns 204"
else
    fail "PATCH X-Patch-Format: diff returns 204" "got status $patch_diff_status"
fi

# Verify content
after_diff=$(dc run --rm -T test-runner sh -c "
    curl -sS -H 'Authorization: Bearer $TOKEN' '$DAV_BASE/$TEST_FILE'
")
if echo "$after_diff" | sed -n '3p' | grep -q "^DIFF 3$"; then
    pass "unified diff was applied correctly"
else
    fail "unified diff was applied correctly" "line 3: $(echo "$after_diff" | sed -n '3p')"
fi

# --- Step 4: PATCH diff with context mismatch should return 409 ---
bad_diff='--- a/f
+++ b/f
@@ -1,3 +1,3 @@
 NEW  1
-WRONG_CONTEXT_LINE
+Replacement
 Line 3'

bad_status=$(dc run --rm -T test-runner sh -c "
    cat <<'__EOF__' | curl -sS -o /dev/null -w '%{http_code}' \
        -X PATCH \
        -H 'Authorization: Bearer $TOKEN' \
        -H 'X-Patch-Format: diff' \
        --data-binary @- \
        '$DAV_BASE/$TEST_FILE'
$bad_diff
__EOF__
")
if [ "$bad_status" = "409" ]; then
    pass "PATCH diff with mismatched context returns 409 Conflict"
else
    fail "PATCH diff with mismatched context returns 409 Conflict" "got status $bad_status"
fi

# --- Cleanup: DELETE the test file ---
dc run --rm -T test-runner sh -c "
    curl -sS -o /dev/null -X DELETE \
        -H 'Authorization: Bearer $TOKEN' \
        '$DAV_BASE/$TEST_FILE'
" || true
