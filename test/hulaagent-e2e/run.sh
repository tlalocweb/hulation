#!/bin/bash
# Self-contained hulaagent e2e — brings up hula + clickhouse via docker,
# creates an agent, runs hula-agent on the host, probes the BUILD verb
# end-to-end, and asserts the full HLAP envelope sequence.
#
# Designed to be the smallest possible "does hulaagent actually work"
# test. Reuses hula:local (the standard build target) and the
# extracted hulactl from inside that image; otherwise self-contained.
#
# Usage:
#   ./test/hulaagent-e2e/run.sh             # full lifecycle
#   ./test/hulaagent-e2e/run.sh --keep      # leave the stack up
#   HULA_PORT=14443 ./test/hulaagent-e2e/run.sh
#
# Distinct from test/e2e/run.sh — that's the heavy main fixture
# (multiple sites, OPAQUE-bootstrapped admin, mkdocs/hugo fixtures,
# etc.). This script just brings up enough to validate the BUILD
# verb roundtrip + permission denial paths.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORKDIR="$SCRIPT_DIR/workdir"
COMPOSE_PROJECT="hulaagent-e2e"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yaml"
CONFIG_TMPL="$SCRIPT_DIR/hula-config.yaml.tmpl"

HULA_PORT="${HULA_PORT:-14443}"
KEEP_STACK=0
PASSED=0
FAILED=0
AGENT_PID=""

# --- CLI ---
while [ $# -gt 0 ]; do
    case "$1" in
        --keep) KEEP_STACK=1; shift ;;
        -h|--help)
            head -16 "$0" | sed -n 's/^# *//p'
            exit 0
            ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
done

export WORKDIR HULA_PORT REPO_ROOT

# --- Helpers ---
pass() { PASSED=$((PASSED + 1)); echo "  PASS: $*"; }
fail() { FAILED=$((FAILED + 1)); echo "  FAIL: $*"; }
section() { echo; echo "=== $* ==="; }

dc() { docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"; }

cleanup() {
    if [ -n "$AGENT_PID" ]; then
        kill -TERM "$AGENT_PID" 2>/dev/null || true
        wait "$AGENT_PID" 2>/dev/null || true
    fi
    if [ "$KEEP_STACK" -eq 0 ]; then
        echo
        echo "--- tearing down stack ---"
        dc down -v --remove-orphans 2>/dev/null || true
    else
        echo
        echo "(--keep set — stack left running; tear down with: dc down -v)"
    fi
}
trap cleanup EXIT

require_tools() {
    local missing=()
    command -v docker >/dev/null 2>&1 || missing+=(docker)
    command -v openssl >/dev/null 2>&1 || missing+=(openssl)
    command -v cargo >/dev/null 2>&1 || missing+=(cargo)
    command -v python3 >/dev/null 2>&1 || missing+=(python3)
    docker compose version >/dev/null 2>&1 || missing+=("docker-compose-v2")
    # Need a Go binary for the argon2 hash generator. Prefer the
    # repo-local toolchain; fall back to PATH.
    if [ -x "$REPO_ROOT/.bin/go/bin/go" ]; then
        GO_BIN="$REPO_ROOT/.bin/go/bin/go"
    elif command -v go >/dev/null 2>&1; then
        GO_BIN=$(command -v go)
    else
        missing+=(go)
    fi
    if [ ${#missing[@]} -ne 0 ]; then
        echo "missing prerequisites: ${missing[*]}" >&2
        exit 1
    fi
}

ensure_hula_image() {
    if docker image inspect hula:local >/dev/null 2>&1; then
        return 0
    fi
    section "Building hula:local image (one-time, ~1-2 min)"
    cd "$REPO_ROOT"
    DOCKER_IMAGE=hula DOCKER_TAG=local ./build-docker.sh --local
    docker image inspect hula:local >/dev/null 2>&1 || {
        echo "ERROR: hula:local image build failed" >&2
        exit 1
    }
}

extract_hulactl() {
    # The host-built .bin/hulactl is glibc-linked; alpine needs musl.
    # Extract the in-image hulactl (built against musl) for the
    # hulactl-runner container's bind mount.
    local tmpcid
    tmpcid=$(docker create hula:local /bin/true)
    docker cp "$tmpcid:/hula/hulactl" "$WORKDIR/hulactl"
    docker rm "$tmpcid" >/dev/null
    chmod +x "$WORKDIR/hulactl"
}

gen_certs() {
    section "Generating CA + leaf TLS cert"
    mkdir -p "$WORKDIR/certs"
    cd "$WORKDIR/certs"

    # Proper two-cert chain: a CA that's the trust root, and a
    # leaf (signed by the CA) that hula serves. rustls — which
    # reqwest uses inside the agent — strictly rejects a CA cert
    # used as a leaf with `CaUsedAsEndEntity`, so we can't reuse
    # one self-signed cert as both serving cert AND trust root.
    # SANs cover the listener vhost + every server-host configured
    # in hula-config.yaml.tmpl.

    # CA
    openssl ecparam -genkey -name prime256v1 -noout -out ca-key.pem
    openssl req -x509 -new -nodes -days 30 -sha256 \
        -key ca-key.pem -out ca.pem \
        -subj "/CN=hulaagent-e2e-root" \
        -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
        -addext "keyUsage=critical,keyCertSign,cRLSign" \
        >/dev/null 2>&1

    # Leaf
    openssl ecparam -genkey -name prime256v1 -noout -out key.pem
    openssl req -new -key key.pem -out leaf.csr \
        -subj "/CN=hulaagent-e2e-leaf" \
        >/dev/null 2>&1
    cat > leaf.ext <<'EOF'
subjectAltName=DNS:hula.test.local,DNS:testsite.test.local,DNS:othersite.test.local
basicConstraints=critical,CA:FALSE
extendedKeyUsage=serverAuth,clientAuth
keyUsage=critical,digitalSignature,keyEncipherment
EOF
    openssl x509 -req -in leaf.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
        -days 30 -sha256 -out cert.pem -extfile leaf.ext \
        >/dev/null 2>&1

    cp ca.pem rootCA.pem
    chmod 644 cert.pem key.pem rootCA.pem
    rm -f leaf.csr leaf.ext ca.srl
    cd - >/dev/null
}

gen_admin_creds() {
    section "Generating admin password + argon2 hash"
    ADMIN_PASS=$(openssl rand -hex 16)
    # Reuse the existing gen-hash-from-password.go from the main e2e
    # harness — it's the source of truth for argon2 parameters that
    # match hula's expected format.
    ADMIN_ARGON_HASH=$(PASSWORD="$ADMIN_PASS" \
        "$GO_BIN" run "$REPO_ROOT/test/e2e/lib/gen-hash-from-password.go")
    JWT_KEY=$(openssl rand -base64 32)
    export ADMIN_PASS ADMIN_ARGON_HASH JWT_KEY
    echo "  password ${ADMIN_PASS:0:4}*** generated"
}

render_config() {
    section "Rendering hula-config.yaml"
    envsubst '${ADMIN_ARGON_HASH} ${JWT_KEY}' \
        < "$CONFIG_TMPL" > "$WORKDIR/hula-config.yaml"
    echo "  wrote $WORKDIR/hula-config.yaml"
}

start_stack() {
    section "Starting docker compose stack"
    dc down -v --remove-orphans 2>/dev/null || true
    dc up -d hula-clickhouse hula
    echo "  waiting for hula /hulastatus on https://localhost:$HULA_PORT ..."
    local elapsed=0 status=""
    while [ $elapsed -lt 60 ]; do
        # curl from the host with --cacert; hula serves on
        # localhost:HULA_PORT but the cert is for hula.test.local
        # — --resolve maps the SNI / hostname to localhost.
        status=$(curl -sS -o /dev/null -w '%{http_code}' \
            --cacert "$WORKDIR/certs/rootCA.pem" \
            --resolve "hula.test.local:$HULA_PORT:127.0.0.1" \
            "https://hula.test.local:$HULA_PORT/hulastatus" 2>/dev/null || echo "000")
        if [ "$status" = "200" ]; then
            echo "  hula ready"
            return 0
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    echo "  hula did not become ready within 60s (last status: $status)" >&2
    dc logs hula >&2
    exit 1
}

bootstrap_admin() {
    section "Bootstrapping admin OPAQUE record"
    # The set-password flow's readline still prompts for the current
    # password even when only HULACTL_NEW_PASSWORD is set. Feed
    # newlines on stdin to satisfy the prompt — the server's
    # bootstrap path accepts an empty current password when no
    # record exists yet.
    local out
    out=$(printf '\n\n\n' | dc run --rm -T \
        -e HULACTL_HOST="hula.test.local" \
        -e HULACTL_NEW_PASSWORD="$ADMIN_PASS" \
        hulactl-runner set-password --username admin --provider admin 2>&1 || true)
    if echo "$out" | grep -q "Password for admin/admin set via OPAQUE"; then
        echo "  OPAQUE bootstrap ok"
    else
        echo "ERROR: OPAQUE bootstrap failed:" >&2
        echo "$out" | tail -10 >&2
        exit 1
    fi
}

create_agent() {
    section "Creating agent via hulactl create-agent"
    # Authenticate first so create-agent's server mode has a JWT.
    local auth_out
    auth_out=$(dc run --rm -T \
        -e HULACTL_HOST="hula.test.local" \
        -e HULACTL_IDENTITY="admin" \
        -e HULACTL_PASSWORD="$ADMIN_PASS" \
        hulactl-runner auth hula.test.local 2>&1 || true)
    if ! echo "$auth_out" | grep -q "^Credentials saved for"; then
        echo "ERROR: hulactl auth failed:" >&2
        echo "$auth_out" | tail -10 >&2
        exit 1
    fi
    # create-agent writes to stdout. Strip stderr (log noise that
    # would otherwise corrupt the yaml).
    local agent_yaml
    agent_yaml=$(dc run --rm -T \
        -e HULACTL_HOST="hula.test.local" \
        hulactl-runner create-agent \
            --allow-build=testsite \
            --expires-in=30d \
            --hula-host="hula.test.local:$HULA_PORT" \
            2>/dev/null || true)
    if ! echo "$agent_yaml" | grep -q "^agent:"; then
        echo "ERROR: create-agent did not emit a valid yaml" >&2
        echo "$agent_yaml" | head -10 >&2
        exit 1
    fi
    echo "$agent_yaml" > "$WORKDIR/agent.yaml"
    echo "  wrote $WORKDIR/agent.yaml"
}

build_hulaagent() {
    section "Building hula-agent (release)"
    cd "$REPO_ROOT/hulaagent"
    cargo build --release >/dev/null 2>&1
    HULAAGENT_BIN="$REPO_ROOT/hulaagent/target/release/hula-agent"
    if [ ! -x "$HULAAGENT_BIN" ]; then
        echo "ERROR: hula-agent binary not at $HULAAGENT_BIN after build" >&2
        exit 1
    fi
    echo "  built $HULAAGENT_BIN"
}

start_hula_agent() {
    section "Starting hula-agent on host"
    SOCK="$WORKDIR/hlap.sock"
    rm -f "$SOCK"
    "$HULAAGENT_BIN" \
        -c "$WORKDIR/agent.yaml" \
        --socket "$SOCK" \
        --resolve "hula.test.local=127.0.0.1:$HULA_PORT" \
        --extra-ca "$WORKDIR/certs/rootCA.pem" \
        >"$WORKDIR/hula-agent.log" 2>&1 &
    AGENT_PID=$!
    local i
    for i in $(seq 1 30); do
        [ -S "$SOCK" ] && break
        sleep 0.1
    done
    if [ ! -S "$SOCK" ]; then
        echo "ERROR: hula-agent did not bind socket within 3s" >&2
        cat "$WORKDIR/hula-agent.log" >&2
        exit 1
    fi
    echo "  hula-agent listening on $SOCK (pid $AGENT_PID)"
}

# --- Probe helper: send one envelope, read until 'done':true or 'err':
# appears in the response, then exit with whatever's been received.
probe() {
    local payload="$1" timeout="${2:-15}" outfile="$3"
    python3 - "$SOCK" "$payload" "$timeout" "$outfile" <<'PY'
import socket, sys
sock_path, payload, timeout, out_path = sys.argv[1], sys.argv[2], float(sys.argv[3]), sys.argv[4]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(timeout)
s.connect(sock_path)
s.sendall((payload + "\n").encode())
buf = b""
done = False
try:
    while not done:
        chunk = s.recv(4096)
        if not chunk: break
        buf += chunk
        for line in buf.split(b"\n"):
            if b'"done":true' in line or b'"err":' in line:
                done = True
                break
except socket.timeout:
    pass
open(out_path, "wb").write(buf)
PY
}

run_assertions() {
    section "Probing BUILD verb (allowed site → expect failure-path stream)"
    probe '{"verb":"build","stream":1,"site":"testsite"}' 30 "$WORKDIR/case-allowed.out"
    local out="$WORKDIR/case-allowed.out"
    local line1 line2 last
    line1=$(sed -n '1p' "$out")
    line2=$(sed -n '2p' "$out")
    last=$(tail -1 "$out")

    if echo "$line1" | grep -q '"hlap":1'; then
        pass "banner emitted on connect"
    else
        fail "banner emitted on connect" ; echo "    line1: $line1" >&2
    fi

    if echo "$line2" | grep -q '"streaming":true' && \
       echo "$line2" | grep -q '"build_id"'; then
        pass "initial OK envelope carries streaming:true and build_id"
    else
        fail "initial OK envelope carries streaming:true and build_id" ; echo "    line2: $line2" >&2
    fi

    if echo "$last" | grep -q '"done":true'; then
        pass "terminal envelope has done:true"
    else
        fail "terminal envelope has done:true" ; echo "    last: $last" >&2
    fi

    # The bogus repo URL means the build fails at git clone, so
    # we expect status:failed. status:complete would mean someone
    # plugged a working repo into the fixture — surface the
    # mismatch loudly.
    if echo "$last" | grep -q '"status":"failed"'; then
        pass "terminal envelope reports status:failed (bogus repo URL)"
    elif echo "$last" | grep -q '"status":"complete"'; then
        fail "expected status:failed (bogus repo) but got complete" "$last"
    else
        fail "terminal envelope has a recognisable status" "$last"
    fi

    local log_count
    log_count=$(grep -c '"log":' "$out" || true)
    if [ "${log_count:-0}" -ge 1 ]; then
        pass "at least one log envelope streamed ($log_count seen)"
    else
        fail "at least one log envelope streamed"
        echo "    full output:" >&2
        cat "$out" >&2
    fi

    section "Probing BUILD verb (disallowed site → expect forbidden err envelope)"
    probe '{"verb":"build","stream":2,"site":"othersite"}' 10 "$WORKDIR/case-forbidden.out"
    local fout="$WORKDIR/case-forbidden.out"
    local fline2
    fline2=$(sed -n '2p' "$fout")
    if echo "$fline2" | grep -q '"err":"forbidden"' && \
       echo "$fline2" | grep -q '"stream":2'; then
        pass "disallowed site returns forbidden err envelope with stream echoed"
    else
        fail "disallowed site returns forbidden err envelope" "line2: $fline2"
    fi

    section "Probing BUILD verb (missing site field → expect missing_field)"
    probe '{"verb":"build","stream":3}' 5 "$WORKDIR/case-missing.out"
    local mout="$WORKDIR/case-missing.out"
    local mline2
    mline2=$(sed -n '2p' "$mout")
    if echo "$mline2" | grep -q '"err":"missing_field"' && \
       echo "$mline2" | grep -q '"stream":3'; then
        pass "missing site field returns missing_field err envelope"
    else
        fail "missing site field returns missing_field err envelope" "line2: $mline2"
    fi
}

# --- Main ---
require_tools
mkdir -p "$WORKDIR"
ensure_hula_image
extract_hulactl
gen_certs
gen_admin_creds
render_config
start_stack
bootstrap_admin
create_agent
build_hulaagent
start_hula_agent
run_assertions

section "Summary"
echo "  $PASSED passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
