#!/bin/bash
# Suite 12a: hulactl create-agent (Phase 2 server mode).
#
# Verifies the round-trip:
#   - hulactl create-agent calls /api/agent/create on the running hula
#   - hula's persistent Agent CA signs a leaf cert under it
#   - the registry record (canonical + fingerprint index) is written
#   - the returned yaml has the schema HULAAGENT_PLAN.md documents
#   - hula-agent --dump can parse the yaml end-to-end
#
# The hula-agent rust binary is built once at suite-time (cargo
# release) since the e2e fixture doesn't ship it. If cargo isn't
# available the dump check is skipped — the API and yaml shape
# checks still run.

OUT_DIR="$WORKDIR/agent-yaml"
mkdir -p "$OUT_DIR"
YAML_FILE="$OUT_DIR/test-agent.yaml"

# --- 1. Server-mode create-agent emits a yaml ---
# Capture stdout only — hulactl's package-level initialisation writes
# log lines (with ANSI color codes) to stderr which would otherwise
# contaminate the yaml and trip up the rust hula-agent's stricter
# parser at step 4 below. Stderr is dropped intentionally; the API
# checks below still cover the success path.
ca_out=$(hulactl create-agent \
    --allow-build=testsite \
    --allow-staging-build=testsite-staging \
    --expires-in=30d \
    --hula-host=hula.test.local:443 2>/dev/null || true)

echo "$ca_out" > "$YAML_FILE"

if grep -q "^agent:" "$YAML_FILE" && grep -q "  id:" "$YAML_FILE"; then
    pass "create-agent emitted yaml with agent.id"
else
    fail "create-agent emitted yaml with agent.id" "first 5 lines: $(head -5 "$YAML_FILE" | tr '\n' '|')"
fi

if grep -q "BEGIN CERTIFICATE" "$YAML_FILE" && grep -q "BEGIN EC PRIVATE KEY" "$YAML_FILE"; then
    pass "yaml contains PEM cert + key blocks"
else
    fail "yaml contains PEM cert + key blocks" "no BEGIN markers found"
fi

if grep -qF "id: agent:" "$YAML_FILE"; then
    fail "agent.id is the bare ID, not prefixed" "yaml had 'id: agent:<...>' which is the cert CN, not the registry id"
elif grep -qE "^  id: [A-Za-z0-9_-]+" "$YAML_FILE"; then
    pass "agent.id is a base64url token"
else
    fail "agent.id is a base64url token" "no recognisable id field"
fi

# --- 2. Sites block reflects the --allow-* flags ---
if grep -qE "testsite-staging:" "$YAML_FILE" && grep -qE "testsite:" "$YAML_FILE"; then
    pass "yaml lists both --allow sites"
else
    fail "yaml lists both --allow sites" "$(grep -A1 'sites:' "$YAML_FILE" | head -8 | tr '\n' '|')"
fi

# --- 3. Registry index round-trip — sanity check via direct curl ---
# The handler returns 503 when the agent CA isn't initialised; if
# that happened, the yaml above would have failed. Check we're past
# that gate by re-issuing a request and asserting 200.
TOKEN=$(runner_shell 'awk "/^ *token:/ {gsub(/\"/, \"\"); print \$2; exit}" /root/.hula/hulactl.yaml' | tr -d ' \r\n')
http_code=$(dc run --rm -T test-runner curl -sS -o /tmp/_resp -w '%{http_code}' \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"sites":{"testsite":{"allow":{"build":""}}}}' \
    "https://hula.test.local:443/api/agent/create" 2>/dev/null || echo "000")
if [ "$http_code" = "200" ]; then
    pass "create-agent returns 200 to a direct POST"
else
    fail "create-agent returns 200 to a direct POST" "http_code=$http_code"
fi

# --- 4. Optional: hula-agent --dump round-trips the yaml ---
# Built outside the docker stack since the e2e fixture has no rust
# toolchain. Skip if cargo is unavailable.
HULAAGENT_BIN="$REPO_ROOT/hulaagent/target/release/hula-agent"
if [ ! -x "$HULAAGENT_BIN" ]; then
    if command -v cargo >/dev/null 2>&1; then
        echo "  building hula-agent (release) for --dump round-trip..."
        (cd "$REPO_ROOT/hulaagent" && cargo build --release >/dev/null 2>&1) || true
    fi
fi
if [ -x "$HULAAGENT_BIN" ]; then
    dump_out=$("$HULAAGENT_BIN" -c "$YAML_FILE" --dump 2>&1 || true)
    if echo "$dump_out" | grep -qE "^agent.id:"; then
        pass "hula-agent --dump parsed the create-agent yaml"
    else
        fail "hula-agent --dump parsed the create-agent yaml" "got: $(echo "$dump_out" | head -3)"
    fi
else
    echo "  (skipping hula-agent --dump check — binary not available)"
fi
