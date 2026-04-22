#!/bin/bash
# Suite 13: gRPC smoke test.
#
# Confirms the unified server serves gRPC on the HTTPS port. Uses the
# native StatusService.Status RPC — the simplest thing reachable on the
# gRPC side.
#
# Depends on: grpcurl in the test runner container. We install it lazily
# if not already present.

# Install grpcurl if missing (alpine runner image doesn't ship it).
runner_shell 'command -v grpcurl >/dev/null 2>&1 || apk add --no-cache grpcurl >/dev/null 2>&1 || (
    cd /tmp && wget -q https://github.com/fullstorydev/grpcurl/releases/download/v1.9.1/grpcurl_1.9.1_linux_x86_64.tar.gz -O grpcurl.tgz && tar -xzf grpcurl.tgz && mv grpcurl /usr/local/bin/ && chmod +x /usr/local/bin/grpcurl
)' >/dev/null 2>&1 || true

# Probe: StatusService.Status is a public RPC (no auth required).
status_output=$(runner_shell "grpcurl -insecure -d '{}' ${HULA_HOST}:443 hulation.v1.status.StatusService/Status 2>&1" || true)

assert_contains "$status_output" '"ok"' "gRPC StatusService.Status returns ok field"
assert_contains "$status_output" 'true' "gRPC StatusService.Status reports alive"

# Probe: reflection should list our services.
list_output=$(runner_shell "grpcurl -insecure ${HULA_HOST}:443 list 2>&1" || true)

assert_contains "$list_output" "hulation.v1.status.StatusService" "reflection lists StatusService"
assert_contains "$list_output" "hulation.v1.forms.FormsService" "reflection lists FormsService"
assert_contains "$list_output" "hulation.v1.auth.AuthService" "reflection lists AuthService"
