#!/bin/bash
# Suite 09: staging-update + staging-get (single-file WebDAV PUT/GET).
#
# Commands exercised: staging-update, staging-get

# Create a local test file on the host workdir (shared with hulactl-runner via volume).
MOUNT_HOST="$WORKDIR/hulactl-mount"
mkdir -p "$MOUNT_HOST"
EXPECTED_BODY="<h1>E2E Test Content</h1>"
echo "$EXPECTED_BODY" > "$MOUNT_HOST/e2e-test-static.html"

# Upload it via hulactl staging-update. The file lives at /mnt/hulactl-mount
# inside the hulactl-runner container.
su_out=$(hulactl staging-update testsite-staging /mnt/hulactl-mount/e2e-test-static.html /static/e2e-test-static.html 2>&1 || true)
if echo "$su_out" | grep -q "Uploaded"; then
    pass "staging-update reports upload"
else
    fail "staging-update reports upload" "output: $(echo "$su_out" | tail -3 | tr '\n' '|')"
fi
assert_not_contains "$su_out" "Error uploading" "staging-update did not report error"

# --- staging-get round-trip: download what we just uploaded ---
rm -f "$MOUNT_HOST/e2e-test-static.html"  # ensure local file is fresh
sg_out=$(hulactl staging-get testsite-staging /static/e2e-test-static.html /mnt/hulactl-mount/e2e-test-static-get.html 2>&1 || true)
if echo "$sg_out" | grep -q "Downloaded"; then
    pass "staging-get reports download"
else
    fail "staging-get reports download" "output: $(echo "$sg_out" | tail -3 | tr '\n' '|')"
fi
if [ -f "$MOUNT_HOST/e2e-test-static-get.html" ]; then
    got=$(cat "$MOUNT_HOST/e2e-test-static-get.html")
    if [ "$got" = "$EXPECTED_BODY" ]; then
        pass "staging-get downloaded content matches the upload"
    else
        fail "staging-get downloaded content matches the upload" "got: $got"
    fi
else
    fail "staging-get downloaded content matches the upload" "file does not exist"
fi

# Clean up
rm -f "$MOUNT_HOST/e2e-test-static.html" "$MOUNT_HOST/e2e-test-static-get.html" 2>/dev/null || true
