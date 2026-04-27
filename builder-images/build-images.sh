#!/bin/bash
set -euo pipefail

# Build hula builder images
# Usage: ./build-images.sh [--push] [--platform PLATFORMS]
#
# This script:
# 1. Cross-compiles hulabuild for the target platform(s)
# 2. Builds both builder Docker images
# 3. Optionally exports them as tarballs or pushes to a registry

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BIN_DIR="${PROJECT_ROOT}/.bin"

# Defaults
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
PUSH=false
EXPORT=true
OUTPUT_DIR="${SCRIPT_DIR}/output"

# Parse args
while [[ $# -gt 0 ]]; do
    case $1 in
        --push) PUSH=true; EXPORT=false; shift ;;
        --platform) PLATFORMS="$2"; shift 2 ;;
        --output) OUTPUT_DIR="$2"; shift 2 ;;
        --no-export) EXPORT=false; shift ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# Determine Go binary
if [ -f "${BIN_DIR}/go/bin/go" ]; then
    GO="${BIN_DIR}/go/bin/go"
else
    GO="go"
fi

echo "=== Building hulabuild binary ==="

# For each platform, compile hulabuild and build the Docker image
IFS=',' read -ra PLATFORM_LIST <<< "$PLATFORMS"

for PLATFORM in "${PLATFORM_LIST[@]}"; do
    OS=$(echo "$PLATFORM" | cut -d/ -f1)
    ARCH=$(echo "$PLATFORM" | cut -d/ -f2)

    echo "--- Building hulabuild for ${OS}/${ARCH} ---"

    # Cross-compile hulabuild
    CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" ${GO} build \
        -ldflags "-X github.com/tlalocweb/hulation/config.Version=$(git -C "${PROJECT_ROOT}" describe --tags 2>/dev/null || echo dev)" \
        -o "${SCRIPT_DIR}/hulabuild-${OS}-${ARCH}" \
        "${PROJECT_ROOT}/model/tools/hulabuild"
done

echo ""
echo "=== Building Docker images ==="

# Build Ubuntu 22.04 image
echo "--- Building hula-builder-ubuntu22.04 ---"
for PLATFORM in "${PLATFORM_LIST[@]}"; do
    ARCH=$(echo "$PLATFORM" | cut -d/ -f2)
    cp "${SCRIPT_DIR}/hulabuild-linux-${ARCH}" "${SCRIPT_DIR}/ubuntu22.04/hulabuild"
done

if [ "$PUSH" = true ]; then
    docker buildx build \
        --platform "${PLATFORMS}" \
        -t hula-builder-ubuntu22.04:latest \
        --push \
        "${SCRIPT_DIR}/ubuntu22.04"
else
    docker build \
        -t hula-builder-ubuntu22.04:latest \
        "${SCRIPT_DIR}/ubuntu22.04"
fi

# Build Alpine Default image
echo "--- Building hula-builder-alpine-default ---"
for PLATFORM in "${PLATFORM_LIST[@]}"; do
    ARCH=$(echo "$PLATFORM" | cut -d/ -f2)
    cp "${SCRIPT_DIR}/hulabuild-linux-${ARCH}" "${SCRIPT_DIR}/alpine-default/hulabuild"
done

if [ "$PUSH" = true ]; then
    docker buildx build \
        --platform "${PLATFORMS}" \
        -t hula-builder-alpine-default:latest \
        -t hula-builder-default:latest \
        --push \
        "${SCRIPT_DIR}/alpine-default"
else
    docker build \
        -t hula-builder-alpine-default:latest \
        -t hula-builder-default:latest \
        "${SCRIPT_DIR}/alpine-default"
fi

# Export as tarballs if requested
if [ "$EXPORT" = true ]; then
    echo ""
    echo "=== Exporting images as tarballs ==="
    mkdir -p "${OUTPUT_DIR}"

    docker save hula-builder-ubuntu22.04:latest | gzip > "${OUTPUT_DIR}/hula-builder-ubuntu22.04.tar.gz"
    echo "  -> ${OUTPUT_DIR}/hula-builder-ubuntu22.04.tar.gz"

    docker save hula-builder-alpine-default:latest hula-builder-default:latest | gzip > "${OUTPUT_DIR}/hula-builder-alpine-default.tar.gz"
    echo "  -> ${OUTPUT_DIR}/hula-builder-alpine-default.tar.gz"
fi

# Cleanup compiled binaries
rm -f "${SCRIPT_DIR}"/hulabuild-linux-*
rm -f "${SCRIPT_DIR}/ubuntu22.04/hulabuild"
rm -f "${SCRIPT_DIR}/alpine-default/hulabuild"

echo ""
echo "=== Done ==="
echo "Builder images ready:"
echo "  - hula-builder-ubuntu22.04:latest"
echo "  - hula-builder-alpine-default:latest (also tagged hula-builder-default:latest)"
if [ "$EXPORT" = true ]; then
    echo ""
    echo "To load on a target machine:"
    echo "  docker load < hula-builder-ubuntu22.04.tar.gz"
    echo "  docker load < hula-builder-alpine-default.tar.gz"
fi
