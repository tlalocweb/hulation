#!/bin/bash
set -e

hulaversion=$(git describe --tags 2>/dev/null || echo "dev")
hulabuilddate=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
IMAGE="${DOCKER_IMAGE:-ghcr.io/tlalocweb/hula}"
TAG="${DOCKER_TAG:-${hulaversion}}"

echo "Building hula version ${hulaversion} built on ${hulabuilddate}"

# Parse flags
ACTION=""
TAG_LATEST=false
for arg in "$@"; do
    case "${arg}" in
        --local)  ACTION="local" ;;
        --push)   ACTION="push" ;;
        --latest) TAG_LATEST=true ;;
        --help)   ACTION="help" ;;
        *)
            echo "Unknown option: ${arg}"
            ACTION="help"
            ;;
    esac
done

LATEST_TAG=""
if [ "${TAG_LATEST}" = true ]; then
    LATEST_TAG="--tag ${IMAGE}:latest"
fi

case "${ACTION}" in
    local)
        SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
        BUILD_CONTEXT="${SCRIPT_DIR}/.."

        # Install .dockerignore at build context root to exclude .bin, .gopath, .cache, etc.
        cp "${SCRIPT_DIR}/.dockerignore" "${BUILD_CONTEXT}/.dockerignore"
        trap 'rm -f "${BUILD_CONTEXT}/.dockerignore"' EXIT

        echo "Building for local platform..."
        docker buildx build \
            --network=host \
            --load \
            -f "${SCRIPT_DIR}/Dockerfile.local" \
            --build-arg hulaversion="${hulaversion}" \
            --build-arg hulabuilddate="${hulabuilddate}" \
            --tag "${IMAGE}:${TAG}" \
            ${LATEST_TAG} \
            "${BUILD_CONTEXT}"
        echo "Image built: ${IMAGE}:${TAG}"
        if [ "${TAG_LATEST}" = true ]; then
            echo "Also tagged: ${IMAGE}:latest"
        fi

        # Build hulabuild binary for the builder images
        echo ""
        echo "Building hulabuild binary..."
        ARCH=$(uname -m)
        case "${ARCH}" in
            x86_64)  GOARCH=amd64 ;;
            aarch64) GOARCH=arm64 ;;
            *)       GOARCH=amd64 ;;
        esac
        GO="${SCRIPT_DIR}/.bin/go/bin/go"
        if [ ! -f "${GO}" ]; then
            GO=go
        fi
        CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" ${GO} build \
            -ldflags "-X github.com/tlalocweb/hulation/config.Version=${hulaversion}" \
            -o "${SCRIPT_DIR}/builder-images/hulabuild-linux" \
            "${SCRIPT_DIR}/model/tools/hulabuild"

        # Build alpine-default builder image
        echo "Building hula-builder-alpine-default..."
        cp "${SCRIPT_DIR}/builder-images/hulabuild-linux" "${SCRIPT_DIR}/builder-images/alpine-default/hulabuild"
        docker build \
            --network=host \
            -t hula-builder-alpine-default:latest \
            -t hula-builder-default:latest \
            "${SCRIPT_DIR}/builder-images/alpine-default"
        echo "Image built: hula-builder-alpine-default:latest (also tagged hula-builder-default:latest)"

        # Build ubuntu22.04 builder image
        echo "Building hula-builder-ubuntu22.04..."
        cp "${SCRIPT_DIR}/builder-images/hulabuild-linux" "${SCRIPT_DIR}/builder-images/ubuntu22.04/hulabuild"
        docker build \
            --network=host \
            -t hula-builder-ubuntu22.04:latest \
            "${SCRIPT_DIR}/builder-images/ubuntu22.04"
        echo "Image built: hula-builder-ubuntu22.04:latest"

        # Cleanup
        rm -f "${SCRIPT_DIR}/builder-images/hulabuild-linux"
        rm -f "${SCRIPT_DIR}/builder-images/alpine-default/hulabuild"
        rm -f "${SCRIPT_DIR}/builder-images/ubuntu22.04/hulabuild"
        ;;
    push)
        SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
        BUILD_CONTEXT="${SCRIPT_DIR}/.."

        # Install .dockerignore at build context root
        cp "${SCRIPT_DIR}/.dockerignore" "${BUILD_CONTEXT}/.dockerignore"
        trap 'rm -f "${BUILD_CONTEXT}/.dockerignore"' EXIT

        echo "Building multi-platform and pushing..."
        docker buildx create --use --platform=linux/arm64,linux/amd64 --name multi-platform-builder 2>/dev/null || true
        docker buildx inspect --bootstrap
        docker buildx build \
            -f "${SCRIPT_DIR}/Dockerfile" \
            --build-arg hulaversion="${hulaversion}" \
            --build-arg hulabuilddate="${hulabuilddate}" \
            --platform linux/amd64,linux/arm64 \
            --tag "${IMAGE}:${TAG}" \
            ${LATEST_TAG} \
            --push "${BUILD_CONTEXT}"
        ;;
    *)
        echo "Usage: $0 <--local|--push> [--latest]"
        echo ""
        echo "  --local    Build for local platform only, loads into docker"
        echo "  --push     Build multi-platform (amd64+arm64) and push to registry"
        echo "  --latest   Also tag the image as :latest"
        echo ""
        echo "Examples:"
        echo "  $0 --local                Build with version tag only"
        echo "  $0 --local --latest       Build and also tag as :latest"
        echo "  $0 --push --latest        Build multi-platform, push with :latest"
        echo ""
        echo "Environment variables:"
        echo "  DOCKER_IMAGE   Image name (default: ghcr.io/tlalocweb/hula)"
        echo "  DOCKER_TAG     Image tag (default: git tag or 'dev')"
        ;;
esac
