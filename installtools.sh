#!/bin/bash
set -e

# Hulation CLI tools installer
# Usage: curl -fsSL https://raw.githubusercontent.com/tlalocweb/hulation/main/installtools.sh | bash
#
# Environment variables:
#   INSTALL_DIR     Where to install (default: ~/.local/bin)
#   HULA_VERSION    Specific version to install (default: latest release)

INSTALL_DIR="${INSTALL_DIR:-${HOME}/.local/bin}"
REPO="tlalocweb/hulation"

echo "=============================="
echo "  Hula CLI Tools Installer"
echo "=============================="
echo ""

# Detect OS
OS="$(uname -s)"
case "${OS}" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)
        echo "Error: Unsupported OS: ${OS}"
        echo "This installer supports Linux and macOS."
        exit 1
        ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64|amd64)   ARCH="amd64" ;;
    aarch64|arm64)   ARCH="arm64" ;;
    *)
        echo "Error: Unsupported architecture: ${ARCH}"
        echo "This installer supports amd64 and arm64."
        exit 1
        ;;
esac

echo "Platform: ${OS}/${ARCH}"

# Determine version
if [ -n "${HULA_VERSION}" ]; then
    VERSION="${HULA_VERSION}"
    echo "Version:  ${VERSION} (pinned)"
else
    echo "Fetching latest release..."
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
    if [ -z "${VERSION}" ]; then
        echo "Error: Could not determine latest release."
        echo "Check https://github.com/${REPO}/releases or set HULA_VERSION manually."
        exit 1
    fi
    echo "Version:  ${VERSION} (latest)"
fi

# Download
TARBALL="hulactl-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"

echo ""
echo "Downloading ${TARBALL}..."
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

curl -fsSL "${URL}" -o "${TMPDIR}/${TARBALL}"
if [ $? -ne 0 ]; then
    echo "Error: Failed to download ${URL}"
    echo "Check that version ${VERSION} has a release for ${OS}/${ARCH}."
    exit 1
fi

# Extract
echo "Extracting..."
tar xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}"

# Install
mkdir -p "${INSTALL_DIR}"
mv "${TMPDIR}/hulactl" "${INSTALL_DIR}/hulactl"
chmod +x "${INSTALL_DIR}/hulactl"

echo ""
echo "Installed hulactl to ${INSTALL_DIR}/hulactl"

# Check if INSTALL_DIR is in PATH
if ! echo "${PATH}" | tr ':' '\n' | grep -qx "${INSTALL_DIR}"; then
    echo ""
    echo "Note: ${INSTALL_DIR} is not in your PATH."
    echo "Add it with:"
    echo "  export PATH=\"${INSTALL_DIR}:\${PATH}\""
    echo ""
    echo "Or add that line to your ~/.bashrc or ~/.zshrc."
fi

# Verify
if "${INSTALL_DIR}/hulactl" --help >/dev/null 2>&1 || "${INSTALL_DIR}/hulactl" 2>&1 | head -1 | grep -qi "hula\|command\|usage"; then
    echo ""
    echo "hulactl is ready. Try:"
    echo "  hulactl generatehash"
else
    echo ""
    echo "hulactl installed. Run: ${INSTALL_DIR}/hulactl"
fi
