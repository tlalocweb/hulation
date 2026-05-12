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

# Minimal agent yaml fixture. PEM blobs are placeholders — step 1
# only exercises the unix-socket plumbing, the mTLS client isn't
# wired yet, so the cert contents are never inspected at runtime.
cat > "$YAML_FILE" <<'YAML'
agent:
  id: wire_test_step1
  mTLS:
    ca: |
      -----BEGIN CERTIFICATE-----
      placeholder
      -----END CERTIFICATE-----
    cert: |
      -----BEGIN CERTIFICATE-----
      placeholder
      -----END CERTIFICATE-----
    key: |
      -----BEGIN EC PRIVATE KEY-----
      placeholder
      -----END EC PRIVATE KEY-----
  hula_host: hula.test.local:443
sites:
  testsite:
    allow:
      build: ""
YAML

# Probe script — opens a unix-socket connection, optionally sends a
# JSON line, reads everything back, prints it. Used for every wire
# assertion below. Stdlib-only so the e2e fixture doesn't need a
# python venv.
cat > "$PROBE_PY" <<'PY'
import socket, sys
sock_path = sys.argv[1]
payload = sys.argv[2] if len(sys.argv) > 2 else ""
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(2.0)
s.connect(sock_path)
if payload:
    s.sendall((payload + "\n").encode())
s.shutdown(socket.SHUT_WR)
buf = b""
try:
    while True:
        chunk = s.recv(4096)
        if not chunk:
            break
        buf += chunk
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

# --- Case 1c: a recognised envelope echoes its stream and gets unknown_verb ---
probe '{"verb":"build","stream":7,"site":"testsite"}'
resp=$(sed -n '2p' "$PROBE_OUT")
assert_contains "$resp" '"err":"unknown_verb"' "valid envelope returns unknown_verb (step-1 placeholder)"
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
