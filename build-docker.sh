#!/bin/bash
set -e

hulaversion=$(git describe --tags 2>/dev/null || echo "dev")
hulabuilddate=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
IMAGE="${DOCKER_IMAGE:-ghcr.io/tlalocweb/hula}"
TAG="${DOCKER_TAG:-${hulaversion}}"

echo "Building hula version ${hulaversion} built on ${hulabuilddate}"

case "${1}" in
    --local)
        # Build for local platform only, no cross-compilation toolchain needed
        echo "Building for local platform..."
        docker build \
            --network=host \
            -f "$(dirname "$0")/Dockerfile.local" \
            --build-arg hulaversion="${hulaversion}" \
            --build-arg hulabuilddate="${hulabuilddate}" \
            --tag "${IMAGE}:${TAG}" \
            --tag "${IMAGE}:latest" \
            "$(dirname "$0")/.."
        echo "Image built: ${IMAGE}:${TAG}"
        ;;
    --push)
        # Multi-platform build and push
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
            --tag "${IMAGE}:latest" \
            --push .
        ;;
    *)
        echo "Usage: $0 [--local|--push]"
        echo ""
        echo "  --local   Build for local platform only (no buildx, loads into docker)"
        echo "  --push    Build multi-platform (amd64+arm64) and push to registry"
        echo ""
        echo "Environment variables:"
        echo "  DOCKER_IMAGE   Image name (default: ghcr.io/tlalocweb/hula)"
        echo "  DOCKER_TAG     Image tag (default: git tag or 'dev')"
        ;;
esac
