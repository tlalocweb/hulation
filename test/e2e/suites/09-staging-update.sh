#!/bin/bash
# Suite 09: staging-update (single-file WebDAV PUT).
#
# Commands exercised: staging-update

# Create a local test file on the host workdir (shared with hulactl-runner via volume).
MOUNT_HOST="$WORKDIR/hulactl-mount"
mkdir -p "$MOUNT_HOST"
echo "<h1>E2E Test Content</h1>" > "$MOUNT_HOST/e2e-test-static.html"

# Upload it via hulactl staging-update. The file lives at /mnt/hulactl-mount
# inside the hulactl-runner container.
su_out=$(hulactl staging-update testsite-staging /mnt/hulactl-mount/e2e-test-static.html /static/e2e-test-static.html 2>&1 || true)
if echo "$su_out" | grep -q "Uploaded"; then
    pass "staging-update reports upload"
else
    fail "staging-update reports upload" "output: $(echo "$su_out" | tail -3 | tr '\n' '|')"
fi
assert_not_contains "$su_out" "Error uploading" "staging-update did not report error"

# Clean up
rm -f "$MOUNT_HOST/e2e-test-static.html" 2>/dev/null || true
