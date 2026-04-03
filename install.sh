#!/bin/bash
set -e

# Hulation quick-start installer
# Usage: curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/install.sh | bash
#
# Environment variables:
#   HULA_DIR        Install directory (default: ./hula)
#   HULA_PORT       Port to expose (default: 8088)
#   HULA_IMAGE      Docker image (default: ghcr.io/tlalocweb/hula:latest)
#   HULA_NO_START   Set to 1 to install without starting (default: start immediately)

HULA_DIR="${HULA_DIR:-./hula}"
HULA_PORT="${HULA_PORT:-8088}"
HULA_IMAGE="${HULA_IMAGE:-ghcr.io/tlalocweb/hula:latest}"
HULA_REPO_BASE="https://raw.githubusercontent.com/tlalocweb/hulation/main"

echo "=============================="
echo "  Hulation Quick Start"
echo "=============================="
echo ""

# Check for Docker
if ! command -v docker >/dev/null 2>&1; then
    echo "Error: Docker is not installed."
    echo ""
    echo "Install Docker first:"
    echo "  https://docs.docker.com/engine/install/"
    echo ""
    echo "Or on Ubuntu/Debian:"
    echo "  curl -fsSL https://get.docker.com | sh"
    exit 1
fi

# Check Docker is running
if ! docker info >/dev/null 2>&1; then
    echo "Error: Docker daemon is not running or current user lacks permissions."
    echo ""
    echo "Try: sudo systemctl start docker"
    echo "Or:  sudo usermod -aG docker \$USER  (then log out and back in)"
    exit 1
fi

echo "Docker: $(docker --version)"
echo ""

# Create install directory
mkdir -p "${HULA_DIR}"
cd "${HULA_DIR}"
HULA_DIR="$(pwd)"

echo "Installing to: ${HULA_DIR}"
echo ""

# Download start script
echo "Downloading start-with-docker.sh..."
curl -fsSL "${HULA_REPO_BASE}/start-with-docker.sh" -o start-with-docker.sh
chmod +x start-with-docker.sh

# Download default config if none exists
if [ ! -f config.yaml ]; then
    echo "Downloading default config..."
    curl -fsSL "${HULA_REPO_BASE}/docker-example-config.yaml" -o config.yaml
else
    echo "Using existing config.yaml"
fi

# Create data directories
mkdir -p ch_data ch_logs hula_certs public

# Install hulactl CLI tool
echo "Installing hulactl CLI..."
INSTALL_DIR="${HULA_DIR}" curl -fsSL "${HULA_REPO_BASE}/installtools.sh" | bash || {
    echo "Note: Could not install hulactl. You can install it later with:"
    echo "  curl -fsSL ${HULA_REPO_BASE}/installtools.sh | bash"
}

echo ""
echo "Pulling ${HULA_IMAGE}..."
docker pull "${HULA_IMAGE}"
docker pull clickhouse/clickhouse-server:latest

# Start unless told not to
if [ "${HULA_NO_START}" = "1" ]; then
    echo ""
    echo "Installation complete. To start:"
    echo "  cd ${HULA_DIR} && ./start-with-docker.sh"
else
    echo ""
    HULA_PORT="${HULA_PORT}" HULA_IMAGE="${HULA_IMAGE}" ./start-with-docker.sh
fi

echo ""
echo "=============================="
echo "  Hulation is ready"
echo "=============================="
echo ""
echo "  Directory:  ${HULA_DIR}"
echo "  Config:     ${HULA_DIR}/config.yaml"
echo "  Port:       ${HULA_PORT}"
echo ""
echo "  Logs:       cd ${HULA_DIR} && ./start-with-docker.sh --logs"
echo "  Stop:       cd ${HULA_DIR} && ./start-with-docker.sh --stop"
echo "  Restart:    cd ${HULA_DIR} && ./start-with-docker.sh --restart"
echo ""
echo "  Generate admin hash: ${HULA_DIR}/hulactl generatehash"
echo "  Edit config.yaml to customize, then restart."
echo ""
