#!/bin/bash 
hula_version=$(git describe --tags)
cd ..
docker buildx create --use --platform=linux/arm64,linux/amd64 --name multi-platform-builder
docker buildx inspect --bootstrap
docker buildx build -f hulation/Dockerfile --build-arg hula_version=${hula_version} --platform linux/amd64,linux/arm64 --tag ghcr.io/tlalocweb/hula:latest --push .