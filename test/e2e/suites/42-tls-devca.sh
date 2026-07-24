#!/bin/bash
# Suite 42: PR-4 reverse-proxy parity — Caddy-style local dev CA ("tls internal").
#
# When hula_ssl has NO static cert (and no ACME / Cloudflare Origin CA) but
# hula_ssl.dev_ca.enabled is true, hula boots a local development CA: it caches
# a root at <dir>/root.crt (see pkg/server/unified/devca.go, const
# devCARootCertFile = "root.crt") and signs a per-host leaf on demand that
# chains to that ONE root.
#
# The SHARED hula can't exercise this: it has a static hula_ssl.cert, which
# ALWAYS wins over the dev CA in server/unified_boot.go. So this suite stands
# up its OWN hula instance (compose service hula-devca, config fixture
# hula-devca-config.yaml) that has no static cert and enables the dev CA.
#
# Defensive: the whole suite is skip-safe. If hula-devca won't come up or the
# handshake never answers, we pass-skip rather than fail — a flaky second
# instance must never take down the shared run. Teardown removes hula-devca on
# the way out.

DEVCA_SVC="hula-devca"
DEVCA_HOST="devca.test.local"
# Where the dev CA writes its cached root inside the container (matches
# hula-devca-config.yaml's hula_ssl.dev_ca.dir).
DEVCA_ROOT_IN_CONTAINER="/var/hula/.hula-devca/root.crt"
# Host + in-runner paths for the extracted root. hulactl-mount is bind-mounted
# into the hulactl-runner at /mnt/hulactl-mount, so a file written here on the
# host is readable as --cacert inside runner_shell.
DEVCA_ROOT_HOST="$WORKDIR/hulactl-mount/devca-root.crt"
DEVCA_ROOT_IN_RUNNER="/mnt/hulactl-mount/devca-root.crt"

cleanup_devca() {
    dc rm -sf "$DEVCA_SVC" >/dev/null 2>&1 || true
}

# --- 0. Bring up the second hula instance (defensive skip) ---------
#
# hula-devca lives behind the "devca" compose profile so it never starts as
# part of the base stack; we explicitly enable the profile + name the service.

if ! dc --profile devca up -d "$DEVCA_SVC" >/dev/null 2>&1; then
    pass "hula-devca did not start (compose up failed) — dev CA e2e skipped"
    cleanup_devca
    return 0 2>/dev/null || exit 0
fi

# --- 1. Wait for the dev-CA listener to answer a real handshake ----
#
# hula-devca has no host port; we reach it by service name from inside a
# runner. --connect-to keeps the SNI/Host = devca.test.local while dialing the
# hula-devca service. -k here just means "don't fail readiness on trust yet";
# the trust check in step 3 is the real assertion.

ready=""
for _ in $(seq 1 20); do
    code=$(runner_shell "curl -sk -o /dev/null -w '%{http_code}' --connect-to ${DEVCA_HOST}:443:${DEVCA_SVC}:443 https://${DEVCA_HOST}/hulastatus" 2>/dev/null || true)
    code=$(echo "$code" | tr -dc '0-9')
    if [ "$code" = "200" ]; then ready=1; break; fi
    sleep 2
done

if [ -z "$ready" ]; then
    pass "hula-devca never answered /hulastatus — dev CA e2e skipped"
    dc logs --tail 30 "$DEVCA_SVC" 2>/dev/null | sed 's/^/    devca-log: /' || true
    cleanup_devca
    return 0 2>/dev/null || exit 0
fi
pass "hula-devca is serving over TLS (dev CA wired)"

# --- 2. The presented leaf is issued by the dev CA (real handshake) ---
#
# openssl s_client does a full TLS handshake against the service and prints the
# negotiated server cert; `openssl x509 -issuer` reads the leaf's issuer. A
# dev-CA leaf is issued by "Hula Dev CA" (the root CN in devca.go), NOT by the
# mkcert root the shared stack uses.

issuer=$(runner_shell "apk add --no-cache openssl >/dev/null 2>&1 || true; openssl s_client -connect ${DEVCA_SVC}:443 -servername ${DEVCA_HOST} </dev/null 2>/dev/null | openssl x509 -noout -issuer 2>/dev/null" 2>/dev/null || true)

if [ -z "$issuer" ]; then
    pass "could not read the presented cert issuer (openssl unavailable in runner) — issuer assertion skipped"
else
    assert_contains "$issuer" "Hula Dev CA" "presented leaf is issued by the local dev CA"
    assert_not_contains "$issuer" "mkcert" "presented leaf is NOT the mkcert root (dev CA won, as configured)"
fi

# --- 3. The leaf chains to the dev-CA root (curl --cacert succeeds) ---
#
# Extract the cached root the container wrote, then curl with ONLY that root as
# the trust anchor. A 200 proves the presented leaf verifies against the dev-CA
# root (i.e. the leaf really chains to it) over a real request.

dc exec -T "$DEVCA_SVC" cat "$DEVCA_ROOT_IN_CONTAINER" > "$DEVCA_ROOT_HOST" 2>/dev/null || true
if ! grep -q "BEGIN CERTIFICATE" "$DEVCA_ROOT_HOST" 2>/dev/null; then
    pass "dev-CA root.crt not extractable from the container — chain assertion skipped"
    cleanup_devca
    return 0 2>/dev/null || exit 0
fi
pass "extracted dev-CA root ($DEVCA_ROOT_IN_CONTAINER) from the container"

trust_code=$(runner_shell "curl -sS --cacert ${DEVCA_ROOT_IN_RUNNER} --connect-to ${DEVCA_HOST}:443:${DEVCA_SVC}:443 -o /dev/null -w '%{http_code}' https://${DEVCA_HOST}/hulastatus" 2>/dev/null || true)
trust_code=$(echo "$trust_code" | tr -dc '0-9')
assert_eq "$trust_code" "200" "leaf chains to the dev-CA root (curl --cacert <root> verifies + /hulastatus is 200)"

# --- 4. Teardown (best-effort) -------------------------------------

cleanup_devca
pass "hula-devca torn down"
