#!/bin/bash
# Suite 20: single-listener rule.
#
# Phase-0 mandate: every web service hula exposes lives on one unified
# HTTPS listener. Verify by inspecting the process's listening ports
# inside the hula container — there should be exactly one TCP port
# bound by the hula process, regardless of what features are enabled.
#
# ClickHouse listens on its own ports inside its own container; those
# are unrelated and expected.

# The alpine-based hula image ships busybox's netstat but not iproute2's ss,
# so we shell out to netstat (-t tcp, -l listening, -n numeric).
listeners=$(dc exec -T hula sh -c 'netstat -tln 2>/dev/null || true' || true)

# Exactly one tcp-listen on :443 (the unified HTTPS listener).
count_443=$(echo "$listeners" | grep -cE "[[:space:]]:::443[[:space:]]|[[:space:]]0\.0\.0\.0:443[[:space:]]" || true)
assert_eq "$count_443" "1" "exactly one process listens on :443"

# No other ephemeral listeners (legacy Fiber used to bind 8xxx/9xxx).
count_other=$(echo "$listeners" | grep -cE ":(8|9)[0-9]{3}[[:space:]]" || true)
assert_eq "$count_other" "0" "no unexpected listeners on 8xxx/9xxx ports"
