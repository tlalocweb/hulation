#!/bin/bash 
hulaversion=$(git describe --tags)
hulabuilddate=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
echo "Building hula version ${hulaversion} built on ${hulabuilddate}"
cd ..
docker buildx create --use --platform=linux/arm64,linux/amd64 --name multi-platform-builder
docker buildx inspect --bootstrap
docker buildx build -f hulation/Dockerfile --build-arg hulaversion=${hulaversion} --build-arg hulabuilddate=${hulabuilddate} --platform linux/amd64,linux/arm64 --tag ghcr.io/tlalocweb/hula:latest --push .