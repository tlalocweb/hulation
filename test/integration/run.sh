#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WORKDIR=$(mktemp -d)
COMPOSE_PROJECT="hula-inttest-$$"
DOMAIN="example.com"
PORT=8443
PASSED=0
FAILED=0
CLEANUP_PIDS=()

cleanup() {
    echo ""
    echo "=== Cleanup ==="
    cd "$WORKDIR"
    docker logs "${COMPOSE_PROJECT}-hula" > /tmp/hula-full.log 2>&1 || true
    docker compose -p "$COMPOSE_PROJECT" down -v --remove-orphans 2>/dev/null || true
    docker rmi "${COMPOSE_PROJECT}-counter-backend" 2>/dev/null || true
    rm -rf "$WORKDIR"
    echo "Cleaned up $WORKDIR (hula logs at /tmp/hula-full.log)"
    echo ""
    echo "=== Results: $PASSED passed, $FAILED failed ==="
    if [ "$FAILED" -gt 0 ]; then
        exit 1
    fi
}
trap cleanup EXIT

pass() {
    PASSED=$((PASSED + 1))
    echo "  PASS: $1"
}

fail() {
    FAILED=$((FAILED + 1))
    echo "  FAIL: $1"
    if [ -n "${2:-}" ]; then
        echo "        $2"
    fi
}

assert_contains() {
    local body="$1" expected="$2" msg="$3"
    if echo "$body" | grep -q "$expected"; then
        pass "$msg"
    else
        fail "$msg" "expected '$expected' in response"
    fi
}

assert_status() {
    local status="$1" expected="$2" msg="$3"
    if [ "$status" = "$expected" ]; then
        pass "$msg"
    else
        fail "$msg" "expected status $expected, got $status"
    fi
}

# --- Curl helpers ---
# All curl calls resolve example.com to 127.0.0.1 and trust the mkcert CA
CURL_BASE="curl -s --resolve ${DOMAIN}:${PORT}:127.0.0.1 --cacert ${WORKDIR}/rootCA.pem"

curl11() {
    $CURL_BASE --http1.1 "$@"
}

curl2() {
    $CURL_BASE --http2 "$@"
}

echo "=== Hula Integration Test ==="
echo "Work dir: $WORKDIR"
echo ""

# -------------------------------------------------------
# Step 1: Generate admin credentials
# -------------------------------------------------------
echo "--- Step 1: Generate admin credentials ---"
cd "$REPO_ROOT"
HASHES=$(go run ./test/integration/gen-hash/ 2>/dev/null)
NETWORK_HASH=$(echo "$HASHES" | sed -n '1p')
ARGON_HASH=$(echo "$HASHES" | sed -n '2p')
echo "  Admin network hash: ${NETWORK_HASH:0:16}..."
echo "  Admin argon2 hash: ${ARGON_HASH:0:30}..."

# -------------------------------------------------------
# Step 2: Generate TLS certs with mkcert
# -------------------------------------------------------
echo "--- Step 2: Generate TLS certs ---"
CAROOT=$(mkcert -CAROOT)
cp "$CAROOT/rootCA.pem" "$WORKDIR/rootCA.pem"
cd "$WORKDIR"
mkcert -cert-file cert.pem -key-file key.pem "$DOMAIN" 2>/dev/null
echo "  Generated cert.pem and key.pem for $DOMAIN"

# -------------------------------------------------------
# Step 3: Build hula Docker image
# -------------------------------------------------------
echo "--- Step 3: Build hula Docker image ---"
cd "$REPO_ROOT/.."
docker buildx build --network=host --load \
    -f hulation/Dockerfile.local \
    --build-arg hulaversion=test \
    --build-arg hulabuilddate="$(date -u +'%Y-%m-%dT%H:%M:%SZ')" \
    --tag hula-inttest:latest \
    . 2>&1 | tail -20
echo "  Built hula-inttest:latest"

# -------------------------------------------------------
# Step 4: Build counter backend
# -------------------------------------------------------
echo "--- Step 4: Build counter backend ---"
docker build --network=host -t "${COMPOSE_PROJECT}-counter-backend:latest" \
    "$SCRIPT_DIR/counter-backend" 2>&1 | tail -10
echo "  Built counter backend image"

# -------------------------------------------------------
# Step 5: Create static site
# -------------------------------------------------------
echo "--- Step 5: Create static site ---"
mkdir -p "$WORKDIR/site"
cat > "$WORKDIR/site/index.html" <<'SITEEOF'
<!DOCTYPE html>
<html><head><title>Test Site</title></head>
<body><h1>Hello from Hula Test</h1></body>
</html>
SITEEOF
echo "  Created static site"

# -------------------------------------------------------
# Step 6: Write hula config
# -------------------------------------------------------
echo "--- Step 6: Write hula config ---"
JWT_KEY=$(openssl rand -hex 32)
cat > "$WORKDIR/hula-config.yaml" <<CFGEOF
admin:
  username: admin
  hash: "${ARGON_HASH}"
jwt_key: "${JWT_KEY}"
jwt_expiration: "1h"
port: ${PORT}
bounce_timeout: 500

dbconfig:
  host: clickhouse
  port: 9000
  user: hula
  pass: hula
  dbname: hula
  retries: 10
  delay_retry: 2

servers:
  - host: ${DOMAIN}
    id: testsite1
    aliases:
      - "127.0.0.1"
    publish_port: true
    http_scheme: https
    root: /var/hula/site
    root_index: index.html

    ssl:
      cert: /var/hula/certs/cert.pem
      key: /var/hula/certs/key.pem

    cors:
      unsafe_any_origin: true
      allow_credentials: true

    backends:
      - container_name: test-counter
        image: ${COMPOSE_PROJECT}-counter-backend:latest
        expose:
          - "8080"
        virtual_path: /api
        container_path: /api
        health_check: /api/health
        health_timeout: 30
        restart: "no"
CFGEOF
echo "  Config written"

# -------------------------------------------------------
# Step 7: Write docker-compose
# -------------------------------------------------------
echo "--- Step 7: Write docker-compose ---"
cat > "$WORKDIR/docker-compose.yaml" <<DCEOF
services:
  clickhouse:
    image: clickhouse/clickhouse-server:latest
    container_name: ${COMPOSE_PROJECT}-clickhouse
    environment:
      CLICKHOUSE_USER: hula
      CLICKHOUSE_PASSWORD: hula
      CLICKHOUSE_DB: hula
    healthcheck:
      test: ["CMD-SHELL", "clickhouse-client --user hula --password hula -q 'SELECT 1'"]
      interval: 3s
      timeout: 2s
      retries: 20

  hula:
    image: hula-inttest:latest
    container_name: ${COMPOSE_PROJECT}-hula
    depends_on:
      clickhouse:
        condition: service_healthy
    ports:
      - "127.0.0.1:${PORT}:${PORT}"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./hula-config.yaml:/etc/hula/config.yaml:ro
      - ./site:/var/hula/site:ro
      - ./cert.pem:/var/hula/certs/cert.pem:ro
      - ./key.pem:/var/hula/certs/key.pem:ro
    command: ["-config", "/etc/hula/config.yaml"]
DCEOF
echo "  Docker compose written"

# -------------------------------------------------------
# Step 8: Launch
# -------------------------------------------------------
echo "--- Step 8: Launch services ---"
cd "$WORKDIR"
docker compose -p "$COMPOSE_PROJECT" up -d 2>/dev/null

# Wait for hula to be ready
echo -n "  Waiting for hula"
for i in $(seq 1 60); do
    probe=$(curl11 -o /dev/null -w "%{http_code}" "https://${DOMAIN}:${PORT}/" 2>&1 || true)
    if echo "$probe" | grep -q "200"; then
        echo " ready (${i}s)"
        break
    fi
    if [ "$i" -eq 10 ]; then
        echo ""
        echo "  [debug] probe at 10s: $probe"
    fi
    if [ "$i" -eq 60 ]; then
        echo " TIMEOUT"
        echo "--- Hula logs (saved full to /tmp/hula-full.log) ---"
        docker logs "${COMPOSE_PROJECT}-hula" >/tmp/hula-full.log 2>&1
        docker logs "${COMPOSE_PROJECT}-hula" 2>&1 | tail -30
        echo "--- probe diagnostic ---"
        curl11 -v "https://${DOMAIN}:${PORT}/" 2>&1 | tail -40
        echo "--- hulastatus diagnostic ---"
        curl11 -v "https://${DOMAIN}:${PORT}/hulastatus" 2>&1 | tail -30
        echo "--- container site dir ---"
        docker exec "${COMPOSE_PROJECT}-hula" ls -la /var/hula/site 2>&1 | head -20
        fail "Hula did not start within 60s"
        exit 1
    fi
    echo -n "."
    sleep 1
done

echo ""
echo "=== Running Tests ==="
echo ""

# -------------------------------------------------------
# Test 1: Static page over HTTP/1.1
# -------------------------------------------------------
echo "--- Test: Static pages ---"
BODY=$(curl11 "https://${DOMAIN}:${PORT}/")
assert_contains "$BODY" "Hello from Hula Test" "HTTP/1.1 static page"

BODY=$(curl2 "https://${DOMAIN}:${PORT}/")
assert_contains "$BODY" "Hello from Hula Test" "HTTP/2 static page"

# Verify protocol negotiation
PROTO=$(curl11 -o /dev/null -w "%{http_version}" "https://${DOMAIN}:${PORT}/")
assert_status "$PROTO" "1.1" "HTTP/1.1 protocol negotiated"

PROTO=$(curl2 -o /dev/null -w "%{http_version}" "https://${DOMAIN}:${PORT}/")
assert_status "$PROTO" "2" "HTTP/2 protocol negotiated"

# -------------------------------------------------------
# Test 2: Admin login
# -------------------------------------------------------
echo "--- Test: Admin API ---"
LOGIN_RESP=$(curl11 -X POST "https://${DOMAIN}:${PORT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"userid\":\"admin\",\"hash\":\"${NETWORK_HASH}\"}")
JWT=$(echo "$LOGIN_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['jwt'])" 2>/dev/null || echo "")
if [ -n "$JWT" ]; then
    pass "Admin login (HTTP/1.1) - got JWT"
else
    fail "Admin login (HTTP/1.1)" "response: $LOGIN_RESP"
fi

LOGIN_RESP2=$(curl2 -X POST "https://${DOMAIN}:${PORT}/api/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"userid\":\"admin\",\"hash\":\"${NETWORK_HASH}\"}")
JWT2=$(echo "$LOGIN_RESP2" | python3 -c "import sys,json; print(json.load(sys.stdin)['jwt'])" 2>/dev/null || echo "")
if [ -n "$JWT2" ]; then
    pass "Admin login (HTTP/2) - got JWT"
else
    fail "Admin login (HTTP/2)" "response: $LOGIN_RESP2"
fi

# Test protected endpoint
STATUS=$(curl11 -o /dev/null -w "%{http_code}" "https://${DOMAIN}:${PORT}/api/status" \
    -H "Authorization: Bearer ${JWT}")
assert_status "$STATUS" "200" "Protected /api/status with JWT (HTTP/1.1)"

STATUS=$(curl2 -o /dev/null -w "%{http_code}" "https://${DOMAIN}:${PORT}/api/status" \
    -H "Authorization: Bearer ${JWT2}")
assert_status "$STATUS" "200" "Protected /api/status with JWT (HTTP/2)"

# Test denied without JWT
STATUS=$(curl11 -o /dev/null -w "%{http_code}" "https://${DOMAIN}:${PORT}/api/status")
if [ "$STATUS" != "200" ]; then
    pass "Protected /api/status denied without JWT (HTTP/1.1) - status $STATUS"
else
    fail "Protected /api/status should deny without JWT (HTTP/1.1)"
fi

# -------------------------------------------------------
# Test 3: Form CRUD
# -------------------------------------------------------
echo "--- Test: Form CRUD ---"
FORM_CREATE=$(curl11 -X POST "https://${DOMAIN}:${PORT}/api/form/create" \
    -H "Authorization: Bearer ${JWT}" \
    -H "Content-Type: application/json" \
    -d '{"name":"testform","schema":"{\"type\":\"object\",\"properties\":{\"email\":{\"type\":\"string\"}}}"}')
FORM_ID=$(echo "$FORM_CREATE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
if [ -n "$FORM_ID" ]; then
    pass "Form create - id: $FORM_ID"
else
    fail "Form create" "response: $FORM_CREATE"
fi

# Submit form as visitor (no auth needed)
if [ -n "$FORM_ID" ]; then
    SUBMIT_STATUS=$(curl11 -o /dev/null -w "%{http_code}" \
        -X POST "https://${DOMAIN}:${PORT}/v/sub/${FORM_ID}?h=testsite1&r=once1" \
        -H "Content-Type: application/json" \
        -d '{"url":"https://example.com/page1","fields":{"email":"test@example.com"}}')
    assert_status "$SUBMIT_STATUS" "200" "Form submit (HTTP/1.1)"

    SUBMIT_STATUS=$(curl2 -o /dev/null -w "%{http_code}" \
        -X POST "https://${DOMAIN}:${PORT}/v/sub/${FORM_ID}?h=testsite1&r=once2" \
        -H "Content-Type: application/json" \
        -d '{"url":"https://example.com/page2","fields":{"email":"test2@example.com"}}')
    assert_status "$SUBMIT_STATUS" "200" "Form submit (HTTP/2)"
fi

# Delete the form
if [ -n "$FORM_ID" ]; then
    DEL_STATUS=$(curl11 -o /dev/null -w "%{http_code}" \
        -X DELETE "https://${DOMAIN}:${PORT}/api/form/${FORM_ID}" \
        -H "Authorization: Bearer ${JWT}")
    assert_status "$DEL_STATUS" "200" "Form delete"
fi

# -------------------------------------------------------
# Test 4: Visitor tracking (simulate browser flow)
# -------------------------------------------------------
echo "--- Test: Visitor tracking ---"

# Step 4a: Load iframe (sets cookies, creates bounce)
# HTTP/1.1
IFRAME_RESP=$(curl11 -v -c "$WORKDIR/cookies11.txt" \
    -H "User-Agent: TestBot/1.0-h1" \
    "https://${DOMAIN}:${PORT}/v/hula_hello.html?h=testsite1&u=https%3A%2F%2Fexample.com%2Fpage-h1" \
    2>"$WORKDIR/iframe11_headers.txt" || true)
BOUNCE_H1=$(echo "$IFRAME_RESP" | grep -oP 'b=\K[A-Za-z0-9_+/=-]+' | head -1 || echo "")
if [ -n "$BOUNCE_H1" ]; then
    pass "HelloIframe (HTTP/1.1) - bounce: ${BOUNCE_H1:0:12}..."
else
    fail "HelloIframe (HTTP/1.1) - no bounce ID in response"
    cp "$WORKDIR/iframe11_headers.txt" /tmp/iframe11_headers.txt 2>/dev/null || true
    echo "        iframe-body-bytes: $(echo -n "$IFRAME_RESP" | wc -c)"
    echo "        first 200 of body: $(echo "$IFRAME_RESP" | head -c 200)"
fi

# Check cookies were set
if grep -q "hula_hello\b" "$WORKDIR/cookies11.txt" 2>/dev/null; then
    pass "Visitor cookie set (HTTP/1.1)"
else
    fail "Visitor cookie not set (HTTP/1.1)"
fi

# HTTP/2
IFRAME_RESP2=$(curl2 -v -c "$WORKDIR/cookies2.txt" \
    -H "User-Agent: TestBot/1.0-h2" \
    "https://${DOMAIN}:${PORT}/v/hula_hello.html?h=testsite1&u=https%3A%2F%2Fexample.com%2Fpage-h2" \
    2>"$WORKDIR/iframe2_headers.txt" || true)
BOUNCE_H2=$(echo "$IFRAME_RESP2" | grep -oP 'b=\K[A-Za-z0-9_+/=-]+' | head -1 || echo "")
if [ -n "$BOUNCE_H2" ]; then
    pass "HelloIframe (HTTP/2) - bounce: ${BOUNCE_H2:0:12}..."
else
    fail "HelloIframe (HTTP/2) - no bounce ID in response"
    cp "$WORKDIR/iframe2_headers.txt" /tmp/iframe2_headers.txt 2>/dev/null || true
    echo "        iframe2-body-bytes: $(echo -n "$IFRAME_RESP2" | wc -c)"
    echo "        --- full body begin ---"
    echo "$IFRAME_RESP2"
    echo "        --- full body end ---"
fi

if grep -q "hula_hello\b" "$WORKDIR/cookies2.txt" 2>/dev/null; then
    pass "Visitor cookie set (HTTP/2)"
else
    fail "Visitor cookie not set (HTTP/2)"
fi

# Step 4b: Hello POST (report page view back)
# Give bounce map a moment
sleep 1

if [ -n "$BOUNCE_H1" ]; then
    HELLO_STATUS=$(curl11 -o /dev/null -w "%{http_code}" \
        -X POST "https://${DOMAIN}:${PORT}/v/hello?b=${BOUNCE_H1}" \
        -H "Content-Type: application/json" \
        -H "User-Agent: TestBot/1.0-h1" \
        -b "$WORKDIR/cookies11.txt" \
        -d "{\"b\":\"${BOUNCE_H1}\",\"e\":1,\"u\":\"https://example.com/page-h1\"}")
    assert_status "$HELLO_STATUS" "200" "Hello POST (HTTP/1.1)"
fi

if [ -n "$BOUNCE_H2" ]; then
    HELLO_STATUS=$(curl2 -o /dev/null -w "%{http_code}" \
        -X POST "https://${DOMAIN}:${PORT}/v/hello?b=${BOUNCE_H2}" \
        -H "Content-Type: application/json" \
        -H "User-Agent: TestBot/1.0-h2" \
        -b "$WORKDIR/cookies2.txt" \
        -d "{\"b\":\"${BOUNCE_H2}\",\"e\":1,\"u\":\"https://example.com/page-h2\"}")
    assert_status "$HELLO_STATUS" "200" "Hello POST (HTTP/2)"
fi

# -------------------------------------------------------
# Test 5: Backend proxy (counter)
# -------------------------------------------------------
echo "--- Test: Backend proxy ---"
# Wait for backend to be ready
sleep 3

COUNT_RESP=$(curl11 "https://${DOMAIN}:${PORT}/api/count" 2>/dev/null || echo "")
if echo "$COUNT_RESP" | grep -q '"count"'; then
    pass "Backend GET /api/count (HTTP/1.1)"
else
    fail "Backend GET /api/count (HTTP/1.1)" "response: $COUNT_RESP"
fi

# Increment counter
curl11 -X POST "https://${DOMAIN}:${PORT}/api/count" > /dev/null 2>&1
curl11 -X POST "https://${DOMAIN}:${PORT}/api/count" > /dev/null 2>&1
COUNT_RESP=$(curl2 "https://${DOMAIN}:${PORT}/api/count" 2>/dev/null || echo "")
if echo "$COUNT_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['count']>=2" 2>/dev/null; then
    pass "Backend counter incremented and read via HTTP/2"
else
    fail "Backend counter" "response: $COUNT_RESP"
fi

# -------------------------------------------------------
# Test 6: Verify analytics in ClickHouse
# -------------------------------------------------------
echo "--- Test: Analytics in ClickHouse ---"
# Wait for bounce map to flush
sleep 4

CH_CONTAINER="${COMPOSE_PROJECT}-clickhouse"
EVENT_COUNT=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula \
    -q "SELECT count() FROM hula.events" 2>/dev/null || echo "0")
if [ "$EVENT_COUNT" -gt 0 ] 2>/dev/null; then
    pass "Events recorded in ClickHouse: $EVENT_COUNT"
else
    fail "No events found in ClickHouse" "count: $EVENT_COUNT"
fi

# Check for both h1 and h2 page views by UA
H1_EVENTS=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula \
    -q "SELECT count() FROM hula.events WHERE browser_ua LIKE '%h1%'" 2>/dev/null || echo "0")
H2_EVENTS=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula \
    -q "SELECT count() FROM hula.events WHERE browser_ua LIKE '%h2%'" 2>/dev/null || echo "0")

if [ "$H1_EVENTS" -gt 0 ] 2>/dev/null; then
    pass "HTTP/1.1 page view tracked in ClickHouse ($H1_EVENTS events)"
else
    fail "No HTTP/1.1 page views tracked" "h1 events: $H1_EVENTS"
fi

if [ "$H2_EVENTS" -gt 0 ] 2>/dev/null; then
    pass "HTTP/2 page view tracked in ClickHouse ($H2_EVENTS events)"
else
    fail "No HTTP/2 page views tracked" "h2 events: $H2_EVENTS"
fi

# Check visitor count
VISITOR_COUNT=$(docker exec "$CH_CONTAINER" clickhouse-client --user hula --password hula \
    -q "SELECT count() FROM hula.visitors" 2>/dev/null || echo "0")
if [ "$VISITOR_COUNT" -gt 0 ] 2>/dev/null; then
    pass "Visitors recorded in ClickHouse: $VISITOR_COUNT"
else
    fail "No visitors found in ClickHouse"
fi

# -------------------------------------------------------
# Test 7: Analytics API smoke (Phase 1 stage 1.3)
# -------------------------------------------------------
echo "--- Test: Analytics API (REST gateway) ---"
# Build a 48h window ending now so Summary picks raw events.
ANALYTICS_TO=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
ANALYTICS_FROM=$(date -u -d '2 days ago' +'%Y-%m-%dT%H:%M:%SZ')
SUMMARY_RESP=$(curl11 -X GET "https://${DOMAIN}:${PORT}/api/v1/analytics/summary?server_id=testsite1&filters.from=${ANALYTICS_FROM}&filters.to=${ANALYTICS_TO}" \
    -H "Authorization: Bearer ${JWT}" 2>&1 || true)
if ! echo "$SUMMARY_RESP" | grep -q '"visitors"'; then
    echo "        summary body: $(echo "$SUMMARY_RESP" | head -c 400)"
fi
assert_contains "$SUMMARY_RESP" '"visitors"' "/api/v1/analytics/summary returns visitors field"
assert_contains "$SUMMARY_RESP" '"pageviews"' "/api/v1/analytics/summary returns pageviews field"

TIMESERIES_RESP=$(curl11 -X GET "https://${DOMAIN}:${PORT}/api/v1/analytics/timeseries?server_id=testsite1&filters.from=${ANALYTICS_FROM}&filters.to=${ANALYTICS_TO}&filters.granularity=hour" \
    -H "Authorization: Bearer ${JWT}" 2>&1 || true)
assert_contains "$TIMESERIES_RESP" '"buckets"' "/api/v1/analytics/timeseries returns buckets array"

# Without a token, Summary must refuse (Unauthenticated → 401 at the gateway).
STATUS=$(curl11 -o /dev/null -w "%{http_code}" \
    "https://${DOMAIN}:${PORT}/api/v1/analytics/summary?server_id=testsite1&filters.from=${ANALYTICS_FROM}&filters.to=${ANALYTICS_TO}")
if [ "$STATUS" != "200" ]; then
    pass "/api/v1/analytics/summary rejects unauthenticated caller (status $STATUS)"
else
    fail "/api/v1/analytics/summary should reject unauthenticated caller"
fi

echo ""
echo "=== Done ==="
