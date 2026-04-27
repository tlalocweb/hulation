#!/bin/bash
# Install the proto toolchain used by `make protobuf` into .bin/.
# Pins versions from external-versions.env.
#
# Usage:
#   ./hack/install-protoc.sh
#
# Produces .bin/protoc, .bin/include/, and .bin/protoc-gen-{go,go-grpc,grpc-gateway,openapiv2,gotag}.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${REPO_DIR}/.bin"
EXTERNAL_DIR="${REPO_DIR}/.external"
GO_BIN="${BIN_DIR}/go/bin/go"

# Source pinned versions
if [ ! -f "${REPO_DIR}/external-versions.env" ]; then
    echo "error: ${REPO_DIR}/external-versions.env not found" >&2
    exit 1
fi
# shellcheck disable=SC1090
. "${REPO_DIR}/external-versions.env"

for v in PROTOC_VERSION PROTOC_GEN_GO_VERSION PROTOC_GEN_GO_GRPC_VERSION \
         PROTOC_GEN_GRPC_GATEWAY_VERSION PROTOC_GEN_OPENAPIV2_VERSION \
         PROTOC_GEN_GOTAG_VERSION; do
    if [ -z "${!v:-}" ]; then
        echo "error: ${v} not set in external-versions.env" >&2
        exit 1
    fi
done

mkdir -p "${BIN_DIR}" "${BIN_DIR}/include" "${EXTERNAL_DIR}/protoc"

# Detect platform for protoc
OS="$(uname -s)"
ARCH="$(uname -m)"
case "${OS}" in
    Linux)
        case "${ARCH}" in
            x86_64) PROTOC_ZIP="protoc-${PROTOC_VERSION}-linux-x86_64.zip" ;;
            aarch64|arm64) PROTOC_ZIP="protoc-${PROTOC_VERSION}-linux-aarch_64.zip" ;;
            *) echo "error: unsupported arch ${ARCH}" >&2; exit 1 ;;
        esac ;;
    Darwin)
        PROTOC_ZIP="protoc-${PROTOC_VERSION}-osx-universal_binary.zip" ;;
    *) echo "error: unsupported OS ${OS}" >&2; exit 1 ;;
esac

# Install protoc
if [ -x "${BIN_DIR}/protoc" ] && "${BIN_DIR}/protoc" --version 2>/dev/null | grep -q "${PROTOC_VERSION}"; then
    echo "protoc ${PROTOC_VERSION} already installed"
else
    echo "Installing protoc ${PROTOC_VERSION}..."
    if [ ! -f "${EXTERNAL_DIR}/protoc/${PROTOC_ZIP}" ]; then
        curl -fSL \
            "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/${PROTOC_ZIP}" \
            -o "${EXTERNAL_DIR}/protoc/${PROTOC_ZIP}"
    fi
    tmp="$(mktemp -d)"
    trap "rm -rf '${tmp}'" EXIT
    unzip -q -o "${EXTERNAL_DIR}/protoc/${PROTOC_ZIP}" -d "${tmp}"
    mv -f "${tmp}/bin/protoc" "${BIN_DIR}/protoc"
    chmod +x "${BIN_DIR}/protoc"
    cp -R "${tmp}/include/"* "${BIN_DIR}/include/"
    "${BIN_DIR}/protoc" --version
fi

# Go plugins (installed via `go install`)
if [ ! -x "${GO_BIN}" ]; then
    echo "error: ${GO_BIN} not found; run setup-dev.sh first to install Go" >&2
    exit 1
fi

export GOBIN="${BIN_DIR}"
export GOPATH="${REPO_DIR}/.gopath"
export GOCACHE="${REPO_DIR}/.cache/go-build"

install_plugin() {
    local tool="$1" mod="$2" ver="$3"
    if [ -x "${BIN_DIR}/${tool}" ]; then
        # No reliable per-plugin version check; trust the presence.
        echo "${tool} already present (skipping)"
        return
    fi
    echo "Installing ${tool} ${ver}..."
    "${GO_BIN}" install "${mod}@${ver}"
}

install_plugin protoc-gen-go           google.golang.org/protobuf/cmd/protoc-gen-go                           "${PROTOC_GEN_GO_VERSION}"
install_plugin protoc-gen-go-grpc      google.golang.org/grpc/cmd/protoc-gen-go-grpc                         "${PROTOC_GEN_GO_GRPC_VERSION}"
install_plugin protoc-gen-grpc-gateway github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway     "${PROTOC_GEN_GRPC_GATEWAY_VERSION}"
install_plugin protoc-gen-openapiv2    github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2        "${PROTOC_GEN_OPENAPIV2_VERSION}"
install_plugin protoc-gen-gotag        github.com/srikrsna/protoc-gen-gotag                                  "${PROTOC_GEN_GOTAG_VERSION}"

echo ""
echo "Proto toolchain ready:"
"${BIN_DIR}/protoc" --version
for p in protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-gateway protoc-gen-openapiv2 protoc-gen-gotag; do
    if [ -x "${BIN_DIR}/${p}" ]; then
        echo "  .bin/${p}"
    fi
done
