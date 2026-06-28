#!/bin/bash
# Suite 02: admin/util commands.
#
# Commands exercised: generatehash (hulactl), totp-key-update (hula), reload (hulactl)

# --- generatehash ---
# Interactive: prompts for password. Feed via stdin.
gh_out=$(printf 'testpassword\n' | hulactl generatehash 2>&1 || true)
# Output should contain an argon2id hash string.
assert_contains "$gh_out" '$argon2id$' "generatehash outputs an argon2id hash"

# --- totp-key-update (moved from hulactl to the hula binary) ---
# Run it inside the hula service container (the binary lives at /hula/hula).
# Stdout mode (no -c) prints just the new key.
tk_out=$(dc exec -T hula /hula/hula totp-key-update 2>&1 || true)
# Output should be 32 base64 chars (approx; we check for reasonable length output)
# The encoding is base64url of 32 bytes = 43 chars without padding (or 44 with).
if echo "$tk_out" | grep -qE '[A-Za-z0-9_/+=-]{40,}'; then
    pass "totp-key-update outputs a base64-encoded key"
else
    fail "totp-key-update outputs a base64-encoded key" "got: $tk_out"
fi

# --- reload ---
# Send SIGHUP; hula should reload without crashing.
reload_out=$(hulactl reload 2>&1 || true)
# Give hula a moment to reload.
sleep 2
# Verify it's still responsive.
status=$(curl_status "https://$HULA_HOST:443/hulastatus")
assert_eq "$status" "200" "hula is responsive after reload"
