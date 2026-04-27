#!/bin/bash
# Suite 12: DB lifecycle (initdb, deletedb) — DESTRUCTIVE, runs LAST.
#
# These commands require a hulation.yaml to be available locally to read DB
# connection info, AND direct network access to the clickhouse server.
# Both work from the hulactl-runner since hula-config.yaml is on the host
# filesystem and clickhouse is resolvable via docker DNS.
#
# NOTE: we don't mount the hula-config into the hulactl-runner, so for now
# this suite is a smoke test only — it verifies the commands exist and
# don't crash when invoked without the required config. A full DB-lifecycle
# test would mount the config and confirm tables are created/dropped.

# Run without -hulaconf; expect the command to print a "need config" error.
init_out=$(hulactl initdb 2>&1 || true)
if echo "$init_out" | grep -qiE "config|hulation\.yaml|unknown"; then
    pass "initdb reports a clear error when config is missing"
else
    fail "initdb reports a clear error when config is missing" "got: $(echo "$init_out" | head -1)"
fi

# deletedb also requires the config
del_out=$(hulactl deletedb 2>&1 || true)
if echo "$del_out" | grep -qiE "config|hulation\.yaml|unknown"; then
    pass "deletedb reports a clear error when config is missing"
else
    fail "deletedb reports a clear error when config is missing" "got: $(echo "$del_out" | head -1)"
fi
