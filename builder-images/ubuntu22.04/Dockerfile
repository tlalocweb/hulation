# Hula Builder Image: Ubuntu 22.04
# Includes Hugo, Astro, Gatsby, MkDocs, and hulabuild

FROM ubuntu:22.04

ARG HUGO_VERSION=0.147.6
ARG NODE_MAJOR=22
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive

# System packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    tar \
    xz-utils \
    build-essential \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Node.js
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Hugo extended (deploy variant)
RUN ARCH=$(case "${TARGETARCH}" in "amd64") echo "amd64" ;; "arm64") echo "arm64" ;; *) echo "amd64" ;; esac) && \
    curl -fsSL "https://github.com/gohugoio/hugo/releases/download/v${HUGO_VERSION}/hugo_extended_${HUGO_VERSION}_linux-${ARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin hugo && \
    hugo version

# Astro
RUN npm install -g astro

# Gatsby CLI
RUN npm install -g gatsby-cli

# MkDocs
RUN pip3 install --no-cache-dir --break-system-packages mkdocs mkdocs-material

# Clean up npm cache
RUN npm cache clean --force

# Copy hulabuild binary
COPY hulabuild /usr/local/bin/hulabuild
RUN chmod +x /usr/local/bin/hulabuild

# Default working directory
WORKDIR /builder

ENTRYPOINT ["/usr/local/bin/hulabuild"]
