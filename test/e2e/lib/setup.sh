#!/bin/bash
# Setup: build images, generate certs, render config, start docker-compose.
# Sets and exports the variables documented in harness.sh.
#
# Requires: HULA_E2E_ROOT, REPO_ROOT already in env.
# Loads .env from HULA_E2E_ROOT/.env.

# Do not set `-e` at top level — this file is sourced by run.sh. Enforce via
# functions internally when needed.
set -u

if [ -z "${HULA_E2E_ROOT:-}" ] || [ -z "${REPO_ROOT:-}" ]; then
    echo "setup.sh: HULA_E2E_ROOT and REPO_ROOT must be set" >&2
    return 1 2>/dev/null || exit 1
fi

# --- Paths and names (always exported so suites can use them) ---
WORKDIR="$HULA_E2E_ROOT/workdir"
COMPOSE_PROJECT="hula-e2e"
COMPOSE_FILE="$HULA_E2E_ROOT/fixtures/docker-compose.yaml"

HULA_HOST="hula.test.local"
SITE_HOST="site.test.local"
STAGING_HOST="staging.test.local"
HULA_HOST_PORT="${HULA_HOST_PORT:-4443}"

export HULA_E2E_ROOT REPO_ROOT WORKDIR COMPOSE_PROJECT COMPOSE_FILE
export HULA_HOST SITE_HOST STAGING_HOST HULA_HOST_PORT

# --- Load .env (runs inside e2e_setup, not at source time) ---
load_env() {
    local env_file="$HULA_E2E_ROOT/.env"
    if [ ! -f "$env_file" ]; then
        echo "ERROR: $env_file not found. Copy .env.example and fill in values." >&2
        exit 1
    fi
    # shellcheck disable=SC1090
    set -a
    . "$env_file"
    set +a
    if [ -z "${GITHUB_AUTH_TOKEN:-}" ]; then
        echo "ERROR: GITHUB_AUTH_TOKEN not set in $env_file" >&2
        exit 1
    fi
    export GITHUB_AUTH_TOKEN
}

# --- Preflight ---
preflight() {
    local missing=()
    command -v docker >/dev/null 2>&1 || missing+=(docker)
    command -v go >/dev/null 2>&1 || [ -x "$REPO_ROOT/.bin/go/bin/go" ] || missing+=(go)
    command -v jq >/dev/null 2>&1 || missing+=(jq)
    command -v openssl >/dev/null 2>&1 || missing+=(openssl)
    # mkcert is optional — we fall back to openssl self-signed if absent
    if [ ${#missing[@]} -ne 0 ]; then
        echo "ERROR: missing prerequisites: ${missing[*]}" >&2
        echo "See test/e2e/README.md for installation instructions." >&2
        exit 1
    fi
    docker compose version >/dev/null 2>&1 || {
        echo "ERROR: 'docker compose' (V2) not available" >&2
        exit 1
    }
}

# --- Pick a go binary (prefer repo-local) ---
pick_go() {
    if [ -x "$REPO_ROOT/.bin/go/bin/go" ]; then
        echo "$REPO_ROOT/.bin/go/bin/go"
    else
        command -v go
    fi
}

# --- Build images and binaries ---
build_all() {
    local go_bin
    go_bin=$(pick_go)
    echo "--- Building hula image and builder images (via build-docker.sh --local) ---"
    cd "$REPO_ROOT"
    # Tag the hula image as "hula:local" (DOCKER_IMAGE=hula, DOCKER_TAG=local)
    # so our compose file can reference it reliably.
    DOCKER_IMAGE=hula DOCKER_TAG=local ./build-docker.sh --local

    # Verify the image exists
    if ! docker image inspect hula:local >/dev/null 2>&1; then
        echo "ERROR: hula:local image was not built" >&2
        exit 1
    fi

    # Extract hulactl from the hula:local image. The host-built .bin/hulactl
    # is dynamically linked against glibc and won't run in our alpine runner
    # container; but the hulactl inside hula:local is built with musl (alpine)
    # and works there. We copy it out to workdir/hulactl for the bind mount.
    echo "--- Extracting hulactl from hula:local image ---"
    mkdir -p "$WORKDIR"
    local tmpcid
    tmpcid=$(docker create hula:local /bin/true)
    docker cp "$tmpcid:/hula/hulactl" "$WORKDIR/hulactl"
    docker rm "$tmpcid" >/dev/null
    chmod +x "$WORKDIR/hulactl"
    if [ ! -x "$WORKDIR/hulactl" ]; then
        echo "ERROR: failed to extract hulactl from hula:local" >&2
        exit 1
    fi
    echo "  extracted $WORKDIR/hulactl"
}

# --- Generate admin credentials + argon2 hash ---
gen_credentials() {
    echo "--- Generating admin password and argon2 hash ---"
    local go_bin
    go_bin=$(pick_go)
    cd "$REPO_ROOT"
    # Random 16-byte hex password; gen-hash-from-password.go (run via `go run`)
    # emits the argon2id hash on stdout. Used to populate the vestigial
    # admin.hash field in the rendered config.
    local password
    password=$(openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | xxd -p -c 16)

    # Use an inline Go program to produce the argon2 hash of the plaintext
    # password. Legacy SHA256 "network hash" was removed when auth went
    # OPAQUE-only; admin.hash in the rendered config is vestigial but still
    # required to be a valid argon2 string.
    ADMIN_ARGON_HASH=$(PASSWORD="$password" "$go_bin" run "$HULA_E2E_ROOT/lib/gen-hash-from-password.go")
    ADMIN_PASS="$password"
    if [ -z "$ADMIN_ARGON_HASH" ] || [ -z "$ADMIN_PASS" ]; then
        echo "ERROR: failed to generate admin credentials" >&2
        exit 1
    fi
    export ADMIN_PASS ADMIN_ARGON_HASH
    echo "  password: ${ADMIN_PASS:0:4}*** (saved to workdir)"
    echo "  argon hash: ${ADMIN_ARGON_HASH:0:30}..."
    echo "$ADMIN_PASS" > "$WORKDIR/admin_password.txt"
}

# --- Generate TLS certs ---
# Prefers mkcert (trusted local CA) when available. Falls back to a self-signed
# openssl-generated cert that we treat as our own root CA for curl trust.
gen_certs() {
    mkdir -p "$WORKDIR/certs"
    # Hostnames the test certs must cover. Adding a new server entry
    # in fixtures/hula-config.yaml.tmpl with its own host means adding
    # it here too — otherwise curl from the test-runner fails with
    # "subjectAltName does not match hostname".
    local cert_hosts=(
        "$HULA_HOST"
        "$SITE_HOST"
        "$STAGING_HOST"
        "hugo-min.test.local"
        "mkdocs-min.test.local"
    )
    # Prefer the repo-local mkcert at .bin/mkcert (matches the Go-
    # binary pattern in this file). Without a real CA+leaf chain
    # rustls clients reject the openssl-fallback cert with
    # CaUsedAsEndEntity — suite 12b's hula-agent (Phase 4) is the
    # canary.
    local mkcert_bin=""
    if [ -x "$REPO_ROOT/.bin/mkcert" ]; then
        mkcert_bin="$REPO_ROOT/.bin/mkcert"
    elif command -v mkcert >/dev/null 2>&1; then
        mkcert_bin=$(command -v mkcert)
    fi
    if [ -n "$mkcert_bin" ]; then
        echo "--- Generating TLS certs with mkcert ($mkcert_bin) ---"
        local caroot
        caroot=$("$mkcert_bin" -CAROOT)
        cp "$caroot/rootCA.pem" "$WORKDIR/certs/rootCA.pem"
        cd "$WORKDIR/certs"
        "$mkcert_bin" -cert-file cert.pem -key-file key.pem \
            "${cert_hosts[@]}" 2>/dev/null
    else
        echo "--- Generating self-signed TLS cert with openssl (mkcert not installed) ---"
        cd "$WORKDIR/certs"
        # Comma-joined SAN list for openssl's -addext. The "root CA"
        # trusted by curl is the same self-signed cert.
        local san_list=""
        for h in "${cert_hosts[@]}"; do
            [ -n "$san_list" ] && san_list+=,
            san_list+="DNS:$h"
        done
        openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
            -nodes -days 30 \
            -keyout key.pem -out cert.pem \
            -subj "/CN=hula-e2e-test" \
            -addext "subjectAltName=$san_list" \
            >/dev/null 2>&1
        cp cert.pem rootCA.pem
    fi
    chmod 644 cert.pem key.pem rootCA.pem
    echo "  generated cert.pem, key.pem, rootCA.pem in $WORKDIR/certs/"
}

# --- Render hula config ---
render_config() {
    echo "--- Rendering hula-config.yaml ---"
    local tmpl="$HULA_E2E_ROOT/fixtures/hula-config.yaml.tmpl"
    local out="$WORKDIR/hula-config.yaml"
    # Generate a JWT key (32 random bytes, base64url)
    local jwt_key
    jwt_key=$(openssl rand -base64 32 2>/dev/null || head -c 32 /dev/urandom | base64)
    export JWT_KEY="$jwt_key"
    # envsubst replaces ${VAR} references but leaves {{env:...}} alone (hula's own substitution).
    envsubst '${ADMIN_ARGON_HASH} ${JWT_KEY} ${TEST_SITE_SRC}' < "$tmpl" > "$out"
    echo "  wrote $out"
}

# --- Start the stack ---
start_stack() {
    echo "--- Starting docker-compose ---"
    mkdir -p "$WORKDIR/hulactl-mount"
    dc() {
        docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"
    }
    # Clean any stale stack first
    dc down -v --remove-orphans 2>/dev/null || true
    # forwarder-recorder is the http-recorder sidecar used by suite 36
    # (Phase 4c.2 forwarder e2e). Tiny python service; bringing it up
    # unconditionally keeps the suite from pass-skipping silently.
    dc up -d hula-clickhouse hula forwarder-recorder

    echo "--- Waiting for /hulastatus ---"
    # Use the test-runner profile to hit hulastatus with the self-signed cert trusted
    local elapsed=0 timeout=120
    while [ $elapsed -lt $timeout ]; do
        local status
        status=$(docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" \
                 run --rm -T test-runner curl -sS -o /dev/null -w '%{http_code}' \
                 "https://hula.test.local:443/hulastatus" 2>/dev/null || echo "000")
        if [ "$status" = "200" ]; then
            echo "  hula is ready (hulastatus = 200)"
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    if [ "$status" != "200" ]; then
        echo "ERROR: hula did not become ready within $timeout seconds" >&2
        docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" logs hula >&2
        exit 1
    fi

    # Bootstrap the admin OPAQUE record. After Phase 5a, hulactl
    # auth uses OPAQUE — the argon2 hash in config is no longer
    # enough by itself. We need to register a matching OPAQUE
    # record so the very first `hulactl auth` (suite 01) has
    # something to talk to. The server's bootstrap path allows
    # this without authentication when no record exists yet for
    # the configured admin user (provider=admin, matching
    # config.Admin.Username).
    echo "--- Bootstrapping admin OPAQUE record ---"
    # set-password's readline still prompts for the current
    # password even when only --newpassword is set. We feed
    # empty newlines on stdin to satisfy the prompt — the
    # server's bootstrap path accepts an empty current password
    # when no record exists yet.
    local bootstrap_out
    bootstrap_out=$(printf '\n\n\n' | docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" \
        run --rm -T \
        -e HULACTL_HOST="hula.test.local" \
        -e HULACTL_NEW_PASSWORD="$ADMIN_PASS" \
        hulactl-runner set-password --username admin --provider admin 2>&1 || true)
    if echo "$bootstrap_out" | grep -q "Password for admin/admin set via OPAQUE"; then
        echo "  OPAQUE record registered for admin"
    else
        echo "WARNING: OPAQUE bootstrap may have failed — suite 01 (auth) and dependents may cascade." >&2
        echo "$bootstrap_out" | tail -5 >&2
    fi

    # Wait for the staging container to finish its initial build and enter
    # staging mode. This involves: git clone, hugo build, EXEC_BUILD. Can
    # take a minute or two on first run.
    echo "--- Waiting for staging container to be ready ---"
    local elapsed=0 timeout=300
    while [ $elapsed -lt $timeout ]; do
        if docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" logs hula 2>/dev/null \
            | grep -q "staging container ready for server testsite-staging"; then
            echo "  staging container is ready"
            return 0
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    echo "WARNING: staging container did not become ready within $timeout seconds" >&2
    echo "Recent hula logs:" >&2
    docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" logs --tail 50 hula >&2
    # Continue anyway — suites will individually fail if staging is unavailable
    return 0
}

# --- Prepare local test site repo ---
# Bind-mount a local git working tree into the hula container so hula can
# clone from file:// URL. Avoids depending on GitHub push access and lets
# us seed .hula/sitebuild.yaml without modifying the remote repo.
prepare_test_site() {
    local src="${TEST_SITE_SRC:-/home/ubuntu/work/tlaloc-hula-test-site}"
    if [ ! -d "$src" ]; then
        echo "ERROR: test site source not found at $src" >&2
        echo "Set TEST_SITE_SRC in .env to override the path." >&2
        exit 1
    fi
    if [ ! -d "$src/.git" ]; then
        echo "ERROR: $src is not a git working tree" >&2
        exit 1
    fi
    # Ensure .hula/sitebuild.yaml exists and is committed.
    if [ ! -f "$src/.hula/sitebuild.yaml" ]; then
        echo "  adding .hula/sitebuild.yaml to test site repo"
        mkdir -p "$src/.hula"
        cp "$HULA_E2E_ROOT/fixtures/sitebuild.yaml.reference" "$src/.hula/sitebuild.yaml"
    fi
    # Commit it locally if not already committed.
    (
        cd "$src"
        if ! git ls-files --error-unmatch .hula/sitebuild.yaml >/dev/null 2>&1; then
            git config user.email "e2e@test.local" 2>/dev/null || true
            git config user.name "e2e test" 2>/dev/null || true
            git add .hula/sitebuild.yaml
            git commit -m "e2e: add sitebuild.yaml (local, not pushed)" >/dev/null
            echo "  committed .hula/sitebuild.yaml locally (not pushed)"
        fi
    )
    # Export for the compose template
    export TEST_SITE_SRC="$src"

    # --- Bare clone for stage/commit/push tests (suite 11.5) ---
    # The new hulactl stage|commit|push verbs need a real push target.
    # We can't push into the bind-mounted working tree (it's :ro and
    # git refuses to push into a non-bare repo's checked-out branch),
    # so we make a bare clone at $WORKDIR/testsite-bare.git that
    # docker-compose binds in writable. testsite-staging's `repo:` is
    # then pointed at this bare repo so auto-seed → edit → commit →
    # push round-trip end-to-end.
    local bare="$WORKDIR/testsite-bare.git"
    if [ -d "$bare" ]; then
        # Previous e2e runs left root-owned files inside the bare repo
        # (the hula container pushed into it as root). Host-level `rm`
        # can't touch those, so wipe via a short-lived alpine container
        # that runs the rm as root.
        docker run --rm -v "$WORKDIR:/work" alpine:3.19 \
            rm -rf "/work/testsite-bare.git" >/dev/null 2>&1 || true
    fi
    if ! git clone --bare "$src" "$bare" 2>"$WORKDIR/bare-clone.err" >/dev/null; then
        echo "ERROR: bare-repo prep failed" >&2
        cat "$WORKDIR/bare-clone.err" >&2
        exit 1
    fi
    rm -f "$WORKDIR/bare-clone.err"
    # Bare clones from a non-bare working tree might end up with HEAD
    # set to whatever was checked out. Make sure the default branch
    # exists in the bare repo (CloneOrPull will ask for `main`).
    (cd "$bare" && git symbolic-ref HEAD refs/heads/main 2>/dev/null || true)
    export TEST_SITE_BARE="$bare"
    echo "  prepared bare repo at $bare for staging push tests"
}

# --- Self-contained builder fixtures (suite 07a) ---
# Each entry under fixtures/sites/<name>-min/ is initialised as its own
# git repo and bare-cloned. The bare repos are bind-mounted into the
# hula container at /var/hula/<name>-min-bare.git so the suite can
# trigger production builds against them via hulactl. Independent of
# tlaloc-hula-test-site — these run with no external dependencies.
prepare_min_sites() {
    # Always ensure the bind-mount target exists, even when no
    # fixtures are present — docker-compose mounts $WORKDIR/sites
    # unconditionally, and a missing path produces a confusing
    # bind-mount error rather than an empty directory.
    mkdir -p "$WORKDIR/sites"
    local fixtures_root="$HULA_E2E_ROOT/fixtures/sites"
    if [ ! -d "$fixtures_root" ]; then
        echo "  no min-site fixtures (skipping)"
        return 0
    fi
    for fixture in "$fixtures_root"/*-min; do
        [ -d "$fixture" ] || continue
        local name
        name="$(basename "$fixture")"
        local src="$WORKDIR/sites/$name"
        local bare="$WORKDIR/sites/$name-bare.git"

        # Wipe any prior run via short-lived alpine container — the
        # hula container pushes back as root so host `rm` can't touch
        # those files. Same trick prepare_test_site uses.
        if [ -d "$src" ] || [ -d "$bare" ]; then
            docker run --rm -v "$WORKDIR/sites:/work" alpine:3.19 \
                rm -rf "/work/$name" "/work/$name-bare.git" >/dev/null 2>&1 || true
        fi
        mkdir -p "$src"
        # Copy fixture contents into a fresh dir (cp -a preserves the
        # nested .hula/ when one exists). Then init + commit.
        (cd "$fixture" && tar c .) | (cd "$src" && tar x)
        (
            cd "$src"
            git init -q
            git config user.email "e2e@test.local"
            git config user.name "e2e test"
            git checkout -q -b main 2>/dev/null || git symbolic-ref HEAD refs/heads/main
            git add .
            git commit -q -m "e2e: initial $name fixture"
        )
        if ! git clone --bare "$src" "$bare" 2>"$WORKDIR/sites/$name-clone.err" >/dev/null; then
            echo "ERROR: bare-repo prep failed for $name" >&2
            cat "$WORKDIR/sites/$name-clone.err" >&2
            exit 1
        fi
        rm -f "$WORKDIR/sites/$name-clone.err"
        (cd "$bare" && git symbolic-ref HEAD refs/heads/main 2>/dev/null || true)
        echo "  prepared min-site fixture: $name (bare at $bare)"
    done
    export MIN_SITES_ROOT="$WORKDIR/sites"
}

# --- Main ---
e2e_setup() {
    # Always start with a fresh workdir (certs, rendered config, mount point).
    # Test state from prior runs is not preserved — each run gets a clean slate.
    if [ -d "$WORKDIR" ]; then
        rm -rf "$WORKDIR"
    fi
    mkdir -p "$WORKDIR"
    load_env
    preflight
    prepare_test_site
    prepare_min_sites
    build_all
    gen_credentials
    gen_certs
    render_config
    start_stack
}
