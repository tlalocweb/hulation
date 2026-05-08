#!/bin/bash
# Suite 11a: hulactl stage / commit / push / pull round-trip.
#
# Verifies the new staging git verbs:
#   - stage  → server-side `git add` against staging_src_dir
#   - commit → server-side `git commit` with a "Committed-by: Hula" trailer
#   - push   → pushes HEAD back to the bare-repo origin
#   - pull   → fast-forwards staging_src_dir to origin/<branch>, refuses
#              cleanly when the working tree is dirty
#
# Negative cases:
#   - stage refuses for a production server (hula_build != staging)
#   - stage refuses for an unknown server id
#
# Setup expectations (already in place via the e2e fixture):
#   - testsite-staging is configured with hula_build: staging
#   - testsite-staging.repo is the bare repo at /var/hula/testsite-bare.git
#     (bind-mounted from $WORKDIR/testsite-bare.git, prepared by setup.sh)
#   - StagingSrcDir was auto-seeded by hula's StartupStaging on first boot

# --- 0. Auto-seed sanity: staging_src_dir IS a git working tree ---
# If auto-seed didn't fire, every subsequent test fails with the
# "not a git working tree" 412. Surface that condition early with a
# clear assertion.
seed_check=$(dc exec -T hula sh -c '
    test -d /var/hula/sitedeploy/testsite-staging/staging-src/.git && echo OK || echo MISSING
' 2>&1 | tr -d "\r\n " | tail -c 16)
if [ "$seed_check" = "OK" ]; then
    pass "auto-seed: staging_src_dir is a git working tree"
else
    fail "auto-seed: staging_src_dir is a git working tree" "marker=$seed_check"
    return 0 2>/dev/null || exit 0
fi

# --- 1. Stage refuses non-staging server ---
out=$(hulactl stage testsite something.txt 2>&1 || true)
if echo "$out" | grep -qiE "not a staging server|hula_build must be"; then
    pass "stage refuses production server testsite"
else
    fail "stage refuses production server testsite" "got: $(echo "$out" | head -3)"
fi

# --- 2. Stage refuses unknown server id ---
out=$(hulactl stage no-such-server foo.txt 2>&1 || true)
if echo "$out" | grep -qiE "server not found|404"; then
    pass "stage refuses unknown server id"
else
    fail "stage refuses unknown server id" "got: $(echo "$out" | head -3)"
fi

# --- 3. Make a real edit via WebDAV so there's something to commit ---
MOUNT_HOST="$WORKDIR/hulactl-mount"
mkdir -p "$MOUNT_HOST"
MARKER="suite-11a-marker-$(date +%s)"
echo "<!-- $MARKER -->" > "$MOUNT_HOST/suite-11a-edit.html"

su_out=$(hulactl staging-update testsite-staging \
    /mnt/hulactl-mount/suite-11a-edit.html /suite-11a-edit.html 2>&1 || true)
assert_contains "$su_out" "Uploaded" "uploaded edit via WebDAV"

# --- 4. Stage everything ---
stage_out=$(hulactl stage testsite-staging 2>&1 || true)
if echo "$stage_out" | grep -qiE "Stage failed"; then
    fail "stage all on staging server" "$(echo "$stage_out" | head -5)"
else
    pass "stage all on staging server"
fi

# --- 5. Commit with a marker message ---
COMMIT_MSG="suite-11a: $MARKER"
commit_out=$(hulactl commit testsite-staging "$COMMIT_MSG" 2>&1 || true)
if echo "$commit_out" | grep -qE "Committed [a-f0-9]{4,}"; then
    pass "commit returns a SHA"
else
    fail "commit returns a SHA" "got: $(echo "$commit_out" | head -3)"
fi

# --- 6. Verify the "Committed-by: Hula" trailer landed in the commit ---
# Read git log directly from the staging container's working tree
# (bind-mounted at /var/hula/sitedeploy/testsite-staging/staging-src
# inside the hula container).
last_msg=$(dc exec -T hula sh -c '
    cd /var/hula/sitedeploy/testsite-staging/staging-src 2>/dev/null && \
    git log -1 --format=%B
' 2>&1 || true)
if echo "$last_msg" | grep -q "Committed-by: Hula"; then
    pass "commit message has 'Committed-by: Hula' trailer"
else
    fail "commit message has 'Committed-by: Hula' trailer" "last commit: $(echo "$last_msg" | head -5)"
fi
if echo "$last_msg" | grep -qF "$MARKER"; then
    pass "commit message preserves operator's message"
else
    fail "commit message preserves operator's message" "last commit: $(echo "$last_msg" | head -5)"
fi

# --- 7. Push to bare-repo origin ---
push_out=$(hulactl push testsite-staging 2>&1 || true)
if echo "$push_out" | grep -qE "Pushed [a-f0-9]+ to origin/main"; then
    pass "push reports SHA pushed to origin/main"
else
    fail "push reports SHA pushed to origin/main" "got: $(echo "$push_out" | head -5)"
fi

# --- 8. Bare repo's main now contains the marker commit ---
# Inspect the bare repo from inside the hula container — we mounted it
# rw at /var/hula/testsite-bare.git, so git log against it works.
bare_log=$(dc exec -T hula sh -c '
    git --git-dir=/var/hula/testsite-bare.git log -1 --format=%B main
' 2>&1 || true)
if echo "$bare_log" | grep -qF "$MARKER"; then
    pass "bare repo origin/main has the pushed commit"
else
    fail "bare repo origin/main has the pushed commit" "got: $(echo "$bare_log" | head -5)"
fi

# --- 9a. Pull when up-to-date is a clean no-op ---
# Working tree was just pushed and is now clean; pulling should report
# already-up-to-date.
pull_out=$(hulactl pull testsite-staging 2>&1 || true)
if echo "$pull_out" | grep -qE "Already up to date|Pulled origin/main"; then
    pass "pull on clean tree returns a sane status"
else
    fail "pull on clean tree returns a sane status" "got: $(echo "$pull_out" | head -3)"
fi

# --- 9b. Pull while the working tree is dirty refuses ---
# Drop a stray file so the tree is dirty without going through WebDAV
# (saves us a round-trip for this assertion).
dc exec -T hula sh -c '
    cd /var/hula/sitedeploy/testsite-staging/staging-src && \
    echo "dirty-file" > suite-11a-dirty.txt
' >/dev/null 2>&1 || true
dirty_out=$(hulactl pull testsite-staging 2>&1 || true)
if echo "$dirty_out" | grep -qiE "uncommitted|working tree"; then
    pass "pull refuses with dirty working tree"
else
    fail "pull refuses with dirty working tree" "got: $(echo "$dirty_out" | head -3)"
fi
# Cleanup the stray file so subsequent assertions see a clean tree.
dc exec -T hula sh -c '
    rm -f /var/hula/sitedeploy/testsite-staging/staging-src/suite-11a-dirty.txt
' >/dev/null 2>&1 || true

# --- 9c. Pull refuses for non-staging server ---
out=$(hulactl pull testsite 2>&1 || true)
if echo "$out" | grep -qiE "not a staging server|hula_build must be"; then
    pass "pull refuses production server testsite"
else
    fail "pull refuses production server testsite" "got: $(echo "$out" | head -3)"
fi

# --- 9. Second commit with no staged changes returns an actionable error ---
empty_out=$(hulactl commit testsite-staging "no-op commit" 2>&1 || true)
if echo "$empty_out" | grep -qiE "nothing staged"; then
    pass "commit with nothing staged surfaces a clear error"
else
    fail "commit with nothing staged surfaces a clear error" "got: $(echo "$empty_out" | head -3)"
fi

# --- 10. sync round-trip: edit → sync (clean) ---
# After the push above, working tree should be clean and at origin tip.
# A fresh edit + sync should pull (no-op) + push, returning a single
# success.
echo "<!-- $MARKER-sync -->" > "$MOUNT_HOST/suite-11a-sync.html"
hulactl staging-update testsite-staging \
    /mnt/hulactl-mount/suite-11a-sync.html /suite-11a-sync.html >/dev/null 2>&1 || true
hulactl stage testsite-staging >/dev/null 2>&1 || true
hulactl commit testsite-staging "suite-11a sync round-trip" >/dev/null 2>&1 || true

sync_out=$(hulactl sync testsite-staging 2>&1 || true)
if echo "$sync_out" | grep -qE "^Synced:"; then
    pass "sync round-trip succeeds"
else
    fail "sync round-trip succeeds" "got: $(echo "$sync_out" | head -3)"
fi

# --- 11. sync refuses for a dirty tree (same gate as pull) ---
dc exec -T hula sh -c '
    cd /var/hula/sitedeploy/testsite-staging/staging-src && \
    echo "dirty-for-sync" > suite-11a-dirty-sync.txt
' >/dev/null 2>&1 || true
sync_dirty=$(hulactl sync testsite-staging 2>&1 || true)
if echo "$sync_dirty" | grep -qiE "uncommitted|working tree"; then
    pass "sync refuses dirty working tree"
else
    fail "sync refuses dirty working tree" "got: $(echo "$sync_dirty" | head -3)"
fi
dc exec -T hula sh -c '
    rm -f /var/hula/sitedeploy/testsite-staging/staging-src/suite-11a-dirty-sync.txt
' >/dev/null 2>&1 || true

# --- 12. sync refuses non-staging server ---
out=$(hulactl sync testsite 2>&1 || true)
if echo "$out" | grep -qiE "not a staging server|hula_build must be"; then
    pass "sync refuses production server testsite"
else
    fail "sync refuses production server testsite" "got: $(echo "$out" | head -3)"
fi

# Cleanup local files (the staging-src copy stays — that's the operator's
# now-pushed work, exactly the round-trip we want to leave intact).
rm -f "$MOUNT_HOST/suite-11a-edit.html" 2>/dev/null || true
rm -f "$MOUNT_HOST/suite-11a-sync.html" 2>/dev/null || true
