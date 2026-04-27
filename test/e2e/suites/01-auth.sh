#!/bin/bash
# Suite 01: hulactl auth flow (multi-server config).
#
# Commands exercised: auth, authok
# Note: `logout` is declared but not implemented in hulactl — not tested here.

# Clean any prior hulactl config so auth tests start fresh.
runner_shell 'rm -f /root/.hula/hulactl.yaml' >/dev/null 2>&1 || true

# --- auth ---
auth_output=$(hulactl_auth "$HULA_HOST" "admin" "$ADMIN_PASS" 2>&1 || true)
assert_contains "$auth_output" "Credentials saved" "auth succeeds and saves credentials"
assert_contains "$auth_output" "$HULA_HOST" "auth output references the host"

# Verify config file was written under the `servers.<host>` map.
cfg=$(runner_shell 'cat /root/.hula/hulactl.yaml 2>/dev/null' || true)
assert_contains "$cfg" "servers:" "hulactl.yaml has 'servers' map"
assert_contains "$cfg" "$HULA_HOST" "hulactl.yaml has entry for $HULA_HOST"
assert_contains "$cfg" "token:" "hulactl.yaml has token field"

# --- authok ---
authok_output=$(hulactl authok 2>&1 || true)
assert_contains "$authok_output" "Status auth ok" "authok reports auth working"
