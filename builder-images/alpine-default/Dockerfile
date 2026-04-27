# Hula Builder Image: Alpine (Default)
# Includes Hugo, Astro, Gatsby, MkDocs, and hulabuild

FROM alpine:3.19

ARG HUGO_VERSION=0.147.6
ARG TARGETARCH

# System packages
RUN apk add --no-cache \
    ca-certificates \
    curl \
    git \
    tar \
    xz \
    build-base \
    nodejs \
    npm \
    python3 \
    py3-pip \
    libc6-compat \
    gcompat

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

# Copy hulabuild binary (per-arch, selected by buildx via TARGETARCH)
COPY hulabuild-linux-${TARGETARCH} /usr/local/bin/hulabuild
RUN chmod +x /usr/local/bin/hulabuild

# Default working directory
WORKDIR /builder

ENTRYPOINT ["/usr/local/bin/hulabuild"]
