#!/bin/bash
# Suite 13: gRPC smoke test.
#
# Confirms the unified server serves gRPC on the HTTPS port. Uses the
# native StatusService.Status RPC — the simplest thing reachable on the
# gRPC side.
#
# Depends on: grpcurl in the test runner container. We install it lazily
# if not already present.

# Each runner_shell call spins up a fresh alpine container, so installs
# don't persist across invocations — batch the install and probe into
# one invocation. Alpine's community repo ships grpcurl under that name
# in 3.19+; apk adds the --repository flag if the binary is missing from
# the default main repo.
grpc_install='
    if ! command -v grpcurl >/dev/null 2>&1; then
        apk add --no-cache grpcurl >/dev/null 2>&1 \
            || apk add --no-cache --repository https://dl-cdn.alpinelinux.org/alpine/v3.19/community grpcurl >/dev/null 2>&1 \
            || (
                arch=$(uname -m)
                case "$arch" in
                    x86_64) tgz=grpcurl_1.9.1_linux_x86_64.tar.gz ;;
                    aarch64|arm64) tgz=grpcurl_1.9.1_linux_arm64.tar.gz ;;
                    *) echo "unsupported arch $arch" >&2 ; exit 1 ;;
                esac
                cd /tmp && wget -q "https://github.com/fullstorydev/grpcurl/releases/download/v1.9.1/$tgz" -O grpcurl.tgz \
                    && tar -xzf grpcurl.tgz && mv grpcurl /usr/local/bin/ && chmod +x /usr/local/bin/grpcurl
            )
    fi
'

# Probe: StatusService.Status is a public RPC (no auth required).
status_output=$(runner_shell "$grpc_install
    grpcurl -insecure -d '{}' ${HULA_HOST}:443 hulation.v1.status.StatusService/Status 2>&1" || true)

assert_contains "$status_output" '"ok"' "gRPC StatusService.Status returns ok field"
assert_contains "$status_output" 'true' "gRPC StatusService.Status reports alive"

# Probe: reflection should list our services.
list_output=$(runner_shell "$grpc_install
    grpcurl -insecure ${HULA_HOST}:443 list 2>&1" || true)

assert_contains "$list_output" "hulation.v1.status.StatusService" "reflection lists StatusService"
assert_contains "$list_output" "hulation.v1.forms.FormsService" "reflection lists FormsService"
assert_contains "$list_output" "hulation.v1.auth.AuthService" "reflection lists AuthService"
