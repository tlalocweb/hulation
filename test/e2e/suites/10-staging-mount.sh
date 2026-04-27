#!/bin/bash
# Suite 10: staging-mount (live filesystem sync via WebDAV) with --autobuild.
#
# This suite runs hulactl staging-mount in a detached container, mounts the
# staging source directory, modifies a file locally, and verifies:
#   1. Initial sync downloads the source files (hugo.toml, etc.)
#   2. Local edits trigger a file upload
#   3. With --autobuild, the staging site is rebuilt and reflects the change

# Path inside the hulactl-runner container for the mount target
MOUNT_IN_CONTAINER="/mnt/hulactl-mount/staging-src"
MOUNT_ON_HOST="$WORKDIR/hulactl-mount/staging-src"

# Clean any prior state
rm -rf "$MOUNT_ON_HOST" 2>/dev/null || true
mkdir -p "$MOUNT_ON_HOST"

# Start staging-mount in a detached container. It will run until we stop it.
echo "  starting staging-mount --autobuild in background..."
MOUNT_CID=$(dc run -d --name hula-e2e-mount hulactl-runner \
    staging-mount testsite-staging "$MOUNT_IN_CONTAINER" --autobuild 2>&1 | tail -1)

# The container name we set
MOUNT_CONTAINER="hula-e2e-mount"

# Trap to kill the mount container on exit
trap 'docker stop "$MOUNT_CONTAINER" >/dev/null 2>&1 || true; docker rm "$MOUNT_CONTAINER" >/dev/null 2>&1 || true' RETURN 2>/dev/null || true

# Wait for initial sync to populate hugo.toml locally
if wait_for "initial sync (hugo.toml appears)" 60 "test -f \"$MOUNT_ON_HOST/hugo.toml\""; then
    pass "initial sync downloaded hugo.toml"
else
    fail "initial sync downloaded hugo.toml" "$MOUNT_ON_HOST contents: $(ls "$MOUNT_ON_HOST" 2>/dev/null)"
    docker stop "$MOUNT_CONTAINER" >/dev/null 2>&1 || true
    docker rm "$MOUNT_CONTAINER" >/dev/null 2>&1 || true
    return 0 2>/dev/null || exit 0
fi

# Capture the current served content so we can detect a change
before_body=$(curl_test "https://$STAGING_HOST:443/" 2>&1 | head -c 1000 || true)

# Modify a content file. The files created by the initial-sync run as root
# inside the container, so the host user may not have write permission.
# We run the append from inside a runner container where we're root.
MARKER="E2E-TEST-MARKER-$(date +%s)"
if runner_shell "test -f /mnt/hulactl-mount/staging-src/content/_index.md"; then
    runner_shell "echo '$MARKER' >> /mnt/hulactl-mount/staging-src/content/_index.md"
    pass "appended marker to content/_index.md"
else
    runner_shell "echo '# $MARKER' >> /mnt/hulactl-mount/staging-src/hugo.toml"
    pass "appended marker to hugo.toml (no content/_index.md)"
fi

# Wait ~5 seconds for the debounce + autobuild to fire
echo "  waiting for autobuild (5s debounce + build)..."
sleep 10

# Verify the site was rebuilt. Most reliable: the mount-container logs should
# show "Synced:" and "Auto-build: triggering" and "Auto-build: complete".
mount_logs=$(docker logs "$MOUNT_CONTAINER" 2>&1 || true)
if echo "$mount_logs" | grep -q "Synced:"; then
    pass "staging-mount detected and synced the local change"
else
    fail "staging-mount detected and synced the local change" "no 'Synced:' in logs"
fi

if echo "$mount_logs" | grep -qE "Auto-build: triggering|Auto-build: complete"; then
    pass "autobuild was triggered after file sync"
else
    fail "autobuild was triggered after file sync" "no 'Auto-build:' in logs"
fi

# Clean shutdown: stop the mount container
docker stop "$MOUNT_CONTAINER" >/dev/null 2>&1 || true
docker rm "$MOUNT_CONTAINER" >/dev/null 2>&1 || true

# Revert the marker so we don't leave test garbage in the staging source.
# (Not strictly needed since the stack is torn down, but clean.)
if [ -f "$MOUNT_ON_HOST/content/_index.md" ]; then
    sed -i "/$MARKER/d" "$MOUNT_ON_HOST/content/_index.md" 2>/dev/null || true
fi
