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

# ss is available in the hula container via the base image. Filter for
# the 'hula' process name (or pid 1 depending on how it's run).
listeners=$(dc exec -T hula sh -c 'ss -tlnp 2>/dev/null | grep -E "hula|LISTEN" | grep -v "clickhouse" || true' || true)

# Should see exactly one tcp-listen on 443 (the unified HTTPS listener).
# Depending on host networking we may also see port 80 if an HTTP
# redirect server is enabled — that's acceptable per the plan.
count_443=$(echo "$listeners" | grep -c ":443 " || true)
assert_eq "$count_443" "1" "exactly one process listens on :443"

# No other random ports.
count_other=$(echo "$listeners" | grep -cE ":(8|9)[0-9]{3} " || true)
assert_eq "$count_other" "0" "no unexpected listeners on 8xxx/9xxx ports"
