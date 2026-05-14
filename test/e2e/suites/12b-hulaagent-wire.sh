#!/bin/bash
# Suite 12b — hulaagent HLAP wire plumbing.
#
# Phase 4 gating suite. Per HULAAGENT_PLAN.md §e2e test plan, this
# suite eventually covers four numbered cases:
#
#   1. Banner handshake
#   2. mTLS handshake at hula
#   3. BUILD verb roundtrip (success)
#   4. BUILD permission denial
#
# This commit (Phase 4 step 1) lands case 1 plus generic envelope-
# hygiene checks. Cases 2-4 are appended in step 2's commit when the
# mTLS client + BUILD verb land. Each case prints `PASS:` / `FAIL:`
# via the standard harness.sh helpers.
#
# The hula-agent rust binary is built once at suite-time (cargo
# release). If cargo isn't on PATH the entire suite is skipped — we
# can't validate wire plumbing without the binary.

OUT_DIR="$WORKDIR/hulaagent-wire"
mkdir -p "$OUT_DIR"
YAML_FILE="$OUT_DIR/wire-test-agent.yaml"
SOCK_PATH="$OUT_DIR/hlap.sock"
PROBE_PY="$OUT_DIR/probe.py"

HULAAGENT_BIN="$REPO_ROOT/hulaagent/target/release/hula-agent"
# Always rebuild when cargo is on PATH — incremental builds are
# cheap when nothing changed, and stale binaries from earlier
# phases trip the suite silently (the Phase-1 placeholder exits
# 64 instead of binding a socket).
if command -v cargo >/dev/null 2>&1; then
    echo "  building hula-agent (release) for wire-plumbing checks..."
    (cd "$REPO_ROOT/hulaagent" && cargo build --release >/dev/null 2>&1) || true
fi
if [ ! -x "$HULAAGENT_BIN" ]; then
    echo "  SKIP suite 12b — hula-agent binary not available (cargo missing or build failed)"
    return 0 2>/dev/null || exit 0
fi

# Generate a throwaway ECDSA cert+key pair for the agent yaml.
# Step-1's cases don't dial hula, but step 2a's HulaClient
# constructs reqwest::Identity at startup and refuses to run on
# malformed PEMs — so the fixture needs material that actually
# parses. The cert is self-signed and is reused as its own CA;
# none of the step-1 cases exercise handshake verification.
openssl ecparam -genkey -name prime256v1 -noout -out "$OUT_DIR/_key.pem" 2>/dev/null
openssl req -new -x509 -days 7 -sha256 \
    -key "$OUT_DIR/_key.pem" \
    -out "$OUT_DIR/_cert.pem" \
    -subj "/CN=hulaagent-wire-test" 2>/dev/null
cp "$OUT_DIR/_cert.pem" "$OUT_DIR/_ca.pem"

python3 - "$OUT_DIR/_cert.pem" "$OUT_DIR/_key.pem" "$OUT_DIR/_ca.pem" "$YAML_FILE" <<'PY'
import sys
cert, key, ca, out = sys.argv[1:5]
def indent(path, prefix="      "):
    return "\n".join(prefix + line for line in open(path).read().rstrip().split("\n"))
open(out, "w").write(f"""agent:
  id: wire_test_step2a
  mTLS:
    ca: |
{indent(ca)}
    cert: |
{indent(cert)}
    key: |
{indent(key)}
  hula_host: hula.test.local:443
sites:
  testsite:
    allow:
      build: ""
""")
PY

# Probe script — opens a unix-socket connection, optionally sends a
# JSON line, reads everything back, prints it. Stdlib-only so the
# e2e fixture doesn't need a python venv.
#
# Args:  SOCK [PAYLOAD] [TIMEOUT] [MODE]
#   PAYLOAD  — optional JSON envelope (one line) to send after
#              connecting
#   TIMEOUT  — recv timeout in seconds (default 2.0)
#   MODE     — "until-done" to exit early when a `"done":true` or
#              `"err":` envelope appears (used by step-2c BUILD
#              cases where the agent stays online after the verb
#              completes); default mode reads until EOF or timeout
cat > "$PROBE_PY" <<'PY'
import socket, sys
sock_path = sys.argv[1]
payload = sys.argv[2] if len(sys.argv) > 2 else ""
timeout = float(sys.argv[3]) if len(sys.argv) > 3 else 2.0
mode = sys.argv[4] if len(sys.argv) > 4 else ""
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(timeout)
s.connect(sock_path)
if payload:
    s.sendall((payload + "\n").encode())
if mode != "until-done":
    s.shutdown(socket.SHUT_WR)
buf = b""
done = False
try:
    while not done:
        chunk = s.recv(4096)
        if not chunk:
            break
        buf += chunk
        if mode == "until-done":
            for line in buf.split(b"\n"):
                if b'"done":true' in line or b'"err":' in line:
                    done = True
                    break
except socket.timeout:
    pass
sys.stdout.write(buf.decode(errors="replace"))
PY

# Start the agent. Capture its PID so we can SIGTERM it at the end.
rm -f "$SOCK_PATH"
"$HULAAGENT_BIN" -c "$YAML_FILE" --socket "$SOCK_PATH" >"$OUT_DIR/agent.log" 2>&1 &
AGENT_PID=$!

# Wait up to 3s for the socket file to appear — startup is fast but
# we don't want a race where the first probe runs before bind.
for _ in $(seq 1 30); do
    [ -S "$SOCK_PATH" ] && break
    sleep 0.1
done
if [ ! -S "$SOCK_PATH" ]; then
    fail "agent bound the HLAP socket" "$(cat "$OUT_DIR/agent.log")"
    kill $AGENT_PID 2>/dev/null
    return 0 2>/dev/null || exit 0
fi
pass "agent bound the HLAP socket"

# --- Case 1a: socket mode is 0600 ---
mode=$(stat -c '%a' "$SOCK_PATH" 2>/dev/null || stat -f '%Lp' "$SOCK_PATH" 2>/dev/null)
if [ "$mode" = "600" ]; then
    pass "socket file mode is 600"
else
    fail "socket file mode is 600" "got $mode"
fi

# Probe wrapper — runs the python script, dumps both stdout AND any
# stderr noise to a per-call tempfile, returns the path. Using files
# instead of $(...) avoids a bash quirk where shell-snapshot
# sourcing under `set -u` propagates into command-substitution
# subshells and clobbers stdout when the snapshot references an
# unbound var (ZSH_VERSION in interactive snapshots).
PROBE_OUT="$OUT_DIR/probe.out"
probe() {
    python3 "$PROBE_PY" "$SOCK_PATH" "$@" > "$PROBE_OUT" 2>/dev/null
}

# --- Case 1b: banner shape on accept ---
probe
banner=$(sed -n '1p' "$PROBE_OUT")
assert_contains "$banner" '"hulaagent":1'  "banner has hulaagent:1"
assert_contains "$banner" '"hlap":1'       "banner has hlap:1"
assert_contains "$banner" '"streams":true' "banner has streams:true"
assert_contains "$banner" '"max_inflight"' "banner has max_inflight"

# --- Case 1c: a not-yet-implemented verb echoes its stream and gets unknown_verb ---
# `pull` lands in step 3 alongside the other staging verbs; until
# then it's the canonical "valid envelope, unimplemented verb" case.
# (`build` was used here in the step-1 commit but lands in step 2a,
# so this case migrated to a verb that's still unimplemented.)
probe '{"verb":"pull","stream":7,"site":"testsite"}'
resp=$(sed -n '2p' "$PROBE_OUT")
assert_contains "$resp" '"err":"unknown_verb"' "unimplemented verb returns unknown_verb"
assert_contains "$resp" '"stream":7'           "response echoes the verb's stream id"

# --- Case 1d: malformed JSON gets bad_envelope ---
probe 'not json at all'
resp=$(sed -n '2p' "$PROBE_OUT")
assert_contains "$resp" '"err":"bad_envelope"'  "garbage line gets bad_envelope"
assert_contains "$resp" '"code":"bad_envelope"' "bad_envelope carries the bad_envelope code"

# --- Case 1e: missing required fields are caught explicitly ---
probe '{"verb":"build"}'
resp=$(sed -n '2p' "$PROBE_OUT")
assert_contains "$resp" '"err":"bad_envelope"' "missing stream rejected"
# Detail message contains JSON-escaped field name (\"stream\"), so
# match on the bare word — sufficient signal that the field was
# named in the rejection.
assert_contains "$resp" 'stream'                "missing-stream detail mentions the field"

probe '{"stream":1}'
resp=$(sed -n '2p' "$PROBE_OUT")
assert_contains "$resp" '"err":"bad_envelope"' "missing verb rejected"
assert_contains "$resp" 'verb'                  "missing-verb detail mentions the field"

# --- Case 1f: multiple envelopes on one connection get tagged correctly ---
probe '{"verb":"build","stream":1}
{"verb":"pull","stream":2}'
line2=$(sed -n '2p' "$PROBE_OUT")
line3=$(sed -n '3p' "$PROBE_OUT")
assert_contains "$line2" '"stream":1' "first response tagged with stream 1"
assert_contains "$line3" '"stream":2' "second response tagged with stream 2"

# --- Case 1g: SIGTERM unlinks the socket on graceful shutdown ---
kill -TERM $AGENT_PID 2>/dev/null
# Give the agent up to 2s to clean up the socket path on its way out.
for _ in $(seq 1 20); do
    [ ! -S "$SOCK_PATH" ] && break
    sleep 0.1
done
wait $AGENT_PID 2>/dev/null || true
if [ ! -e "$SOCK_PATH" ]; then
    pass "SIGTERM unlinks the socket on shutdown"
else
    fail "SIGTERM unlinks the socket on shutdown" "$SOCK_PATH still present"
fi

# =========================================================================
# Phase 4 step 2a cases — Go-side /api/agent/build via direct curl.
#
# Validates the server-side wiring of the BUILD verb (mTLS gate,
# permission check, build-trigger delegation) independently of the
# Rust hulaagent process. Step 2b will add the full HLAP-roundtrip
# cases (hula-agent process → /api/agent/build → response) once the
# log-streaming path is in place.
# =========================================================================

SUITE_12A_YAML="$WORKDIR/agent-yaml/test-agent.yaml"
if [ ! -f "$SUITE_12A_YAML" ]; then
    echo "  (skipping step-2a cases — suite 12a yaml not found at $SUITE_12A_YAML)"
    return 0 2>/dev/null || exit 0
fi
pass "suite 12a produced an agent yaml for step-2a cases"

# Extract cert / key / CA from the yaml. Python is on the host;
# yaml→file is one line. Files land in $OUT_DIR (host) and we mount
# them read-only into test-runner per-curl.
python3 - "$SUITE_12A_YAML" "$OUT_DIR" <<'PY'
import sys
try:
    import yaml
except ImportError:
    sys.exit(2)
y = yaml.safe_load(open(sys.argv[1]))
out = sys.argv[2]
for field, fname in (("cert", "agent-cert.pem"),
                     ("key",  "agent-key.pem"),
                     ("ca",   "agent-ca.pem")):
    with open(f"{out}/{fname}", "w") as f:
        f.write(y["agent"]["mTLS"][field])
PY
if [ $? -ne 0 ] || [ ! -s "$OUT_DIR/agent-cert.pem" ]; then
    echo "  (skipping step-2a cases — python yaml unavailable or extraction failed)"
    return 0 2>/dev/null || exit 0
fi

# Helper: direct curl to /api/agent/build with optional cert/key
# mounts. Returns the HTTP status code on stdout.
curl_agent_build() {
    local site="$1" with_cert="$2"
    local extra=()
    if [ "$with_cert" = "yes" ]; then
        extra=(
            -v "$OUT_DIR/agent-cert.pem:/tmp/agent-cert.pem:ro"
            -v "$OUT_DIR/agent-key.pem:/tmp/agent-key.pem:ro"
        )
    fi
    dcq run --rm -T "${extra[@]}" test-runner \
        sh -c "
            apk add --quiet --no-cache curl ca-certificates >/dev/null 2>&1
            update-ca-certificates >/dev/null 2>&1
            HULA_IP=\$(getent hosts hula | awk '{print \$1}')
            echo \"\$HULA_IP hula.test.local\" >> /etc/hosts
            $(if [ "$with_cert" = "yes" ]; then echo "CERT_ARGS='--cert /tmp/agent-cert.pem --key /tmp/agent-key.pem'"; else echo "CERT_ARGS=''"; fi)
            curl -sS -o /dev/null -w '%{http_code}' \$CERT_ARGS \
                -H 'Content-Type: application/json' \
                -d '{\"site\":\"$site\"}' \
                'https://hula.test.local:443/api/agent/build'
        " 2>/dev/null | tail -1
}

# --- Case 2a: no client cert → 401 ---
code=$(curl_agent_build testsite no)
if [ "$code" = "401" ]; then
    pass "no-cert POST /api/agent/build returns 401"
else
    fail "no-cert POST /api/agent/build returns 401" "got $code"
fi

# --- Case 2b: agent cert + allowed site → 200 / 202 / 409 ---
# 200 = build_triggered, 409 = build_in_progress (already building
# from suite 12a or a prior run). Both indicate auth + permission
# passed; the build manager just dedupes.
code=$(curl_agent_build testsite yes)
case "$code" in
    200|202|409)
        pass "agent-cert POST /api/agent/build for allowed site returns 2xx/409 (got $code)"
        ;;
    *)
        fail "agent-cert POST /api/agent/build for allowed site returns 2xx/409" "got $code"
        ;;
esac

# --- Case 2c: agent cert + disallowed site → 403 ---
# Suite 12a registered allow.build for `testsite` only; the
# `testsite-staging` server exists in the e2e config but the agent
# has allow.staging-build for it, NOT allow.build. So this is a
# clean 'cert OK, permission denied' case.
code=$(curl_agent_build testsite-staging yes)
if [ "$code" = "403" ]; then
    pass "agent-cert POST /api/agent/build for disallowed site returns 403"
else
    fail "agent-cert POST /api/agent/build for disallowed site returns 403" "got $code"
fi

# =========================================================================
# Phase 4 step 2c cases — full HLAP roundtrip via host-side hula-agent.
#
# Runs hula-agent on the host with --resolve and --extra-ca flags so
# the agent can reach hula.test.local via the host-port mapping and
# trust hula's serving cert (signed by the e2e test root). The
# alternative — running hula-agent inside test-runner — needs a
# musl-built binary; the --resolve/--extra-ca flags are useful in
# real deployments behind LBs / private CAs so adding them is no
# worse than the musl-tooling path.
# =========================================================================

E2E_ROOT_CA="$HULA_E2E_ROOT/workdir/certs/rootCA.pem"
if [ ! -f "$E2E_ROOT_CA" ]; then
    echo "  (skipping step-2c cases — e2e test root not found at $E2E_ROOT_CA)"
    return 0 2>/dev/null || exit 0
fi
if [ -z "${HULA_HOST_PORT:-}" ]; then
    echo "  (skipping step-2c cases — HULA_HOST_PORT not set in env)"
    return 0 2>/dev/null || exit 0
fi

# --- Case 3: BUILD HLAP roundtrip — banner, initial OK, log envelopes,
#             terminal envelope ---
SOCK_C3="$OUT_DIR/case3.sock"
rm -f "$SOCK_C3"
"$HULAAGENT_BIN" -c "$SUITE_12A_YAML" --socket "$SOCK_C3" \
    --resolve "hula.test.local=127.0.0.1:${HULA_HOST_PORT}" \
    --extra-ca "$E2E_ROOT_CA" \
    >"$OUT_DIR/case3-agent.log" 2>&1 &
C3_PID=$!
for _ in $(seq 1 30); do [ -S "$SOCK_C3" ] && break; sleep 0.1; done
if [ ! -S "$SOCK_C3" ]; then
    fail "case 3 — hula-agent bound HLAP socket" "$(head -5 "$OUT_DIR/case3-agent.log")"
    kill $C3_PID 2>/dev/null
else
    pass "case 3 — hula-agent bound HLAP socket"
    # Probe with a 120s timeout — real testsite build can take
    # 30-90s end-to-end depending on git clone + Docker pull
    # caches. until-done exits as soon as we see done:true or err:
    # so a fast build doesn't pay the full timeout.
    python3 "$PROBE_PY" "$SOCK_C3" \
        '{"verb":"build","stream":1,"site":"testsite"}' 120 until-done \
        > "$OUT_DIR/case3.out" 2>/dev/null
    kill -TERM $C3_PID 2>/dev/null
    wait $C3_PID 2>/dev/null

    # Banner is line 1; line 2 should be the initial OK envelope
    # with streaming:true; the last line should be the terminal
    # envelope with done:true; somewhere in between we want at
    # least one log envelope.
    line1=$(sed -n '1p' "$OUT_DIR/case3.out")
    line2=$(sed -n '2p' "$OUT_DIR/case3.out")
    last=$(tail -1 "$OUT_DIR/case3.out")
    assert_contains "$line1" '"hlap":1'            "case 3 — banner emitted"
    assert_contains "$line2" '"streaming":true'    "case 3 — initial OK has streaming:true"
    assert_contains "$line2" '"build_id"'          "case 3 — initial OK carries build_id"
    assert_contains "$last"  '"done":true'         "case 3 — terminal envelope has done:true"
    if echo "$last" | grep -qE '"status":"(complete|failed)"'; then
        pass "case 3 — terminal envelope reports a real build status"
    else
        fail "case 3 — terminal envelope reports a real build status" "last line: $last"
    fi
    # At least one log envelope between initial and terminal. A
    # real build emits multiple — assert >= 1 to keep the suite
    # robust against very fast cached builds.
    log_count=$(grep -c '"log":' "$OUT_DIR/case3.out" || echo 0)
    if [ "$log_count" -ge 1 ]; then
        pass "case 3 — at least one log envelope streamed ($log_count seen)"
    else
        fail "case 3 — at least one log envelope streamed" "got 0; output: $(cat "$OUT_DIR/case3.out")"
    fi
fi

# --- Case 4: HLAP permission denial — BUILD against disallowed site
#             returns an err envelope (not just an HTTP 403 on the wire,
#             which is what case 2c validated) ---
SOCK_C4="$OUT_DIR/case4.sock"
rm -f "$SOCK_C4"
"$HULAAGENT_BIN" -c "$SUITE_12A_YAML" --socket "$SOCK_C4" \
    --resolve "hula.test.local=127.0.0.1:${HULA_HOST_PORT}" \
    --extra-ca "$E2E_ROOT_CA" \
    >"$OUT_DIR/case4-agent.log" 2>&1 &
C4_PID=$!
for _ in $(seq 1 30); do [ -S "$SOCK_C4" ] && break; sleep 0.1; done
if [ ! -S "$SOCK_C4" ]; then
    fail "case 4 — hula-agent bound HLAP socket" "$(head -5 "$OUT_DIR/case4-agent.log")"
    kill $C4_PID 2>/dev/null
else
    pass "case 4 — hula-agent bound HLAP socket"
    python3 "$PROBE_PY" "$SOCK_C4" \
        '{"verb":"build","stream":1,"site":"testsite-staging"}' 15 until-done \
        > "$OUT_DIR/case4.out" 2>/dev/null
    kill -TERM $C4_PID 2>/dev/null
    wait $C4_PID 2>/dev/null

    # Banner first, then a forbidden err envelope. No initial OK
    # (the trigger HTTP request returns 403 before any envelope is
    # emitted).
    line1=$(sed -n '1p' "$OUT_DIR/case4.out")
    line2=$(sed -n '2p' "$OUT_DIR/case4.out")
    assert_contains "$line1" '"hlap":1'             "case 4 — banner emitted"
    assert_contains "$line2" '"err":"forbidden"'    "case 4 — disallowed site yields forbidden err envelope"
    assert_contains "$line2" '"code":"forbidden"'   "case 4 — forbidden envelope carries the forbidden code"
    assert_contains "$line2" '"stream":1'           "case 4 — err envelope echoes the request's stream id"
fi
