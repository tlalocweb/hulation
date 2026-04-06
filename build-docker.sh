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
        echo "Building for local platform..."
        docker buildx build \
            --network=host \
            --load \
            -f "$(dirname "$0")/Dockerfile.local" \
            --build-arg hulaversion="${hulaversion}" \
            --build-arg hulabuilddate="${hulabuilddate}" \
            --tag "${IMAGE}:${TAG}" \
            ${LATEST_TAG} \
            "$(dirname "$0")/.."
        echo "Image built: ${IMAGE}:${TAG}"
        if [ "${TAG_LATEST}" = true ]; then
            echo "Also tagged: ${IMAGE}:latest"
        fi
        ;;
    push)
        echo "Building multi-platform and pushing..."
        cd "$(dirname "$0")/.."
        docker buildx create --use --platform=linux/arm64,linux/amd64 --name multi-platform-builder 2>/dev/null || true
        docker buildx inspect --bootstrap
        docker buildx build \
            -f hulation/Dockerfile \
            --build-arg hulaversion="${hulaversion}" \
            --build-arg hulabuilddate="${hulabuilddate}" \
            --platform linux/amd64,linux/arm64 \
            --tag "${IMAGE}:${TAG}" \
            ${LATEST_TAG} \
            --push .
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
