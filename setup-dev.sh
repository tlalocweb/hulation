#!/bin/bash

# Display trace of commands executed
set -x

# Function to load versions from external-versions.env
load_versions() {
    local versions_file="${REPO_DIR}/external-versions.env"
    if [ ! -f "${versions_file}" ]; then
        echo "external-versions.env not found at ${versions_file}"
        echo "Please create this file with the following format:"
        echo "GO_VERSION=1.25.0"
        echo "GOLANGCI_LINT_VERSION=v2.1.6"
        exit 1
    fi

    # Source the versions file
    source "${versions_file}"

    # Validate required versions are set
    if [ -z "${GO_VERSION}" ]; then
        echo "Missing required GO_VERSION in external-versions.env"
        exit 1
    fi
}

# Function to check if a version is already downloaded
check_version_downloaded() {
    local component="$1"
    local version="$2"
    local file_pattern="$3"

    # Check in .external directory
    if [ -d "${EXTERNAL_DIR}/${component}" ]; then
        if ls "${EXTERNAL_DIR}/${component}/${file_pattern}" 1> /dev/null 2>&1; then
            echo "${component} version ${version} already downloaded"
            return 0
        fi
    fi

    return 1
}

# Function to download and store a file
download_and_store() {
    local component="$1"
    local version="$2"
    local url="$3"
    local filename="$4"

    # Create component directory if it doesn't exist
    mkdir -p "${EXTERNAL_DIR}/${component}"

    # Download the file
    echo "Downloading ${component} version ${version}..."
    curl -L -o "${EXTERNAL_DIR}/${component}/${filename}" "${url}"

    if [ $? -ne 0 ]; then
        echo "Failed to download ${component} version ${version}"
        return 1
    fi

    echo "Downloaded ${component} version ${version} to ${EXTERNAL_DIR}/${component}/${filename}"
    return 0
}

# Function to temporarily disable error checking for a command
run_with_error_handling() {
    local PREV_OPTS=$(set +o)
    set +e
    "$@"
    local EXIT_CODE=$?
    eval "$PREV_OPTS"
    return $EXIT_CODE
}

# Parse command-line arguments
INSTALL_DOCKER=false
INSTALL_ALL=false
INSTALL_CORE=true
CLEAN_BIN=false

# Display help message
show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo "Options:"
    echo "  --help                 Display this help message"
    echo "  --all                  Install everything including Docker"
    echo "  --docker               Install Docker"
    echo "  --cleanbin             Remove previously built/installed items in .bin"
    echo "  --only <component>     Only install specific component(s)"
    echo "                         Valid components: go, docker, golangci-lint,"
    echo "                         dependencies, env-script"
    echo ""
    echo "Examples:"
    echo "  $0                     Install core components (default)"
    echo "  $0 --all               Install everything including Docker"
    echo "  $0 --docker            Install core components plus Docker"
    echo "  $0 --only go           Only install Go"
    echo "  $0 --cleanbin          Clean .bin directory"
}

# Parse arguments
COMPONENTS_TO_INSTALL=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --help)
            show_help
            exit 0
            ;;
        --all)
            INSTALL_ALL=true
            INSTALL_DOCKER=true
            shift
            ;;
        --docker)
            INSTALL_DOCKER=true
            shift
            ;;
        --cleanbin)
            CLEAN_BIN=true
            shift
            ;;
        --only)
            INSTALL_CORE=false
            INSTALL_ALL=false
            shift
            while [[ $# -gt 0 && ! "$1" =~ ^-- ]]; do
                COMPONENTS_TO_INSTALL+=("$1")
                shift
            done
            ;;
        *)
            echo "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Helper function to check if a component should be installed
should_install_component() {
    local component="$1"

    if [[ "$INSTALL_ALL" == "true" ]]; then
        return 0
    fi

    # Core components are installed by default unless --only is specified
    if [[ "$INSTALL_CORE" == "true" ]]; then
        if [[ "$component" == "go" || "$component" == "dependencies" ||
              "$component" == "golangci-lint" || "$component" == "go-modules" ||
              "$component" == "env-script" ]]; then
            return 0
        fi
    fi

    # Docker needs explicit flag
    if [[ "$component" == "docker" && "$INSTALL_DOCKER" == "true" ]]; then
        return 0
    fi

    # Check specific components in --only list
    for c in "${COMPONENTS_TO_INSTALL[@]}"; do
        if [[ "$c" == "$component" ]]; then
            return 0
        fi
    done

    return 1
}

# Function to clean .bin directory
clean_bin_directories() {
    echo "Cleaning .bin directory..."

    if [ -d "${BIN_DIR}" ]; then
        echo "Removing contents of ${BIN_DIR}..."
        rm -rf "${BIN_DIR:?}"/*
        echo "Cleaned ${BIN_DIR}"
    else
        echo "${BIN_DIR} does not exist, nothing to clean"
    fi

    echo "Bin directory cleanup complete"
}

# Architecture and OS detection
ARCH=$(uname -m)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')

# Convert architecture to canonical form
case "${ARCH}" in
    x86_64|amd64)
        ARCH="amd64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    armv7l)
        ARCH="armv7"
        ;;
    *)
        echo "Unsupported architecture: ${ARCH}"
        echo "This script might not work correctly"
        ;;
esac

# Convert OS to canonical form
case "${OS}" in
    linux)
        OS="linux"
        ;;
    darwin)
        OS="darwin"
        ;;
    *)
        echo "Unsupported OS: ${OS}"
        echo "This script might not work correctly"
        ;;
esac

echo "Detected OS: ${OS}, Architecture: ${ARCH}"

# Variable definitions - these can be overridden by setting them before running this script
REPO_DIR=${REPO_DIR:-"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"}
BIN_DIR=${BIN_DIR:-"${REPO_DIR}/.bin"}
EXTERNAL_DIR=${EXTERNAL_DIR:-"${REPO_DIR}/.external"}

# Load versions at the start of the script
load_versions

# Create our local directories if they don't exist
mkdir -p "${BIN_DIR}"
mkdir -p "${EXTERNAL_DIR}"

# Install system dependencies based on OS
install_dependencies() {
    DEPS_TO_INSTALL=""

    # Check for make
    if ! command -v make &> /dev/null; then
        echo "make not found, will install"
        DEPS_TO_INSTALL="${DEPS_TO_INSTALL} make"
    else
        echo "make already installed"
    fi

    # Check for git
    if ! command -v git &> /dev/null; then
        echo "git not found, will install"
        DEPS_TO_INSTALL="${DEPS_TO_INSTALL} git"
    else
        echo "git already installed"
    fi

    # Check for curl
    if ! command -v curl &> /dev/null; then
        echo "curl not found, will install"
        DEPS_TO_INSTALL="${DEPS_TO_INSTALL} curl"
    else
        echo "curl already installed"
    fi

    # If no dependencies needed, return early
    if [ -z "$DEPS_TO_INSTALL" ]; then
        echo "All required dependencies are already installed"
        return 0
    fi

    # Install any missing dependencies
    case "${OS}" in
        linux)
            if command -v apt-get &> /dev/null; then
                echo "Installing dependencies via apt: ${DEPS_TO_INSTALL}"
                run_with_error_handling sudo apt-get update -y --allow-releaseinfo-change
                run_with_error_handling sudo apt-get install -y ${DEPS_TO_INSTALL}
                if [ $? -ne 0 ]; then
                    echo "Failed to install some dependencies through apt."
                    echo "Please install the following packages manually: ${DEPS_TO_INSTALL}"
                fi
            elif command -v dnf &> /dev/null; then
                echo "Installing dependencies via dnf: ${DEPS_TO_INSTALL}"
                run_with_error_handling sudo dnf install -y ${DEPS_TO_INSTALL}
            elif command -v yum &> /dev/null; then
                echo "Installing dependencies via yum: ${DEPS_TO_INSTALL}"
                run_with_error_handling sudo yum install -y ${DEPS_TO_INSTALL}
            else
                echo "Unsupported Linux distribution. Please install missing dependencies manually: ${DEPS_TO_INSTALL}"
            fi
            ;;
        darwin)
            if command -v brew &> /dev/null; then
                echo "Installing dependencies via brew: ${DEPS_TO_INSTALL}"
                run_with_error_handling brew install ${DEPS_TO_INSTALL}
            else
                echo "Homebrew not found. Please install missing dependencies manually: ${DEPS_TO_INSTALL}"
            fi
            ;;
        *)
            echo "Unsupported OS for automatic dependency installation."
            echo "Please ensure you have these dependencies installed: make, git, curl"
            ;;
    esac

    return 0
}

install_go() {
    # Check if Go is already installed in our local directory with the right version
    if [ -d "${BIN_DIR}/go" ]; then
        INSTALLED_VERSION=$(${BIN_DIR}/go/bin/go version 2>/dev/null | grep -oP 'go\K[0-9]+\.[0-9]+(\.[0-9]+)?' || true)
        if [ "${INSTALLED_VERSION}" = "${GO_VERSION}" ]; then
            echo "Go ${GO_VERSION} is already installed locally"
            ${BIN_DIR}/go/bin/go version
            return
        else
            echo "Go version mismatch: installed=${INSTALLED_VERSION}, wanted=${GO_VERSION}. Reinstalling..."
        fi
    fi

    GO_TARBALL="go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
    GO_URL="https://go.dev/dl/${GO_TARBALL}"

    # Check if version is already downloaded
    if check_version_downloaded "go" "${GO_VERSION}" "${GO_TARBALL}"; then
        GO_TARBALL="${EXTERNAL_DIR}/go/${GO_TARBALL}"
    else
        if ! download_and_store "go" "${GO_VERSION}" "${GO_URL}" "${GO_TARBALL}"; then
            echo "Failed to download Go ${GO_VERSION}"
            return 1
        fi
        GO_TARBALL="${EXTERNAL_DIR}/go/${GO_TARBALL}"
    fi

    echo "Removing any previous local Go installation..."
    rm -rf "${BIN_DIR}/go"

    echo "Extracting Go to local directory..."
    tar -C "${BIN_DIR}" -xzf "${GO_TARBALL}"

    echo "Verifying Go installation..."
    ${BIN_DIR}/go/bin/go version
}

install_golangci_lint() {
    # Check if golangci-lint is already installed locally
    if [ -f "${BIN_DIR}/golangci-lint" ]; then
        echo "golangci-lint is already installed locally"
        ${BIN_DIR}/golangci-lint version
        return
    fi

    if [ -z "${GOLANGCI_LINT_VERSION}" ]; then
        echo "GOLANGCI_LINT_VERSION not set in external-versions.env, skipping golangci-lint"
        return 0
    fi

    GOLANGCI_LINT_TARBALL="golangci-lint-${GOLANGCI_LINT_VERSION#v}-${OS}-${ARCH}.tar.gz"
    GOLANGCI_LINT_URL="https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/${GOLANGCI_LINT_TARBALL}"

    # Check if version is already downloaded
    if check_version_downloaded "golangci-lint" "${GOLANGCI_LINT_VERSION}" "${GOLANGCI_LINT_TARBALL}"; then
        GOLANGCI_LINT_TARBALL="${EXTERNAL_DIR}/golangci-lint/${GOLANGCI_LINT_TARBALL}"
    else
        if ! download_and_store "golangci-lint" "${GOLANGCI_LINT_VERSION}" "${GOLANGCI_LINT_URL}" "${GOLANGCI_LINT_TARBALL}"; then
            echo "Failed to download golangci-lint ${GOLANGCI_LINT_VERSION}"
            return 1
        fi
        GOLANGCI_LINT_TARBALL="${EXTERNAL_DIR}/golangci-lint/${GOLANGCI_LINT_TARBALL}"
    fi

    echo "Extracting golangci-lint to local directory..."
    tar -C "${BIN_DIR}" -xzf "${GOLANGCI_LINT_TARBALL}" --strip-components=1 "golangci-lint-${GOLANGCI_LINT_VERSION#v}-${OS}-${ARCH}/golangci-lint"

    if [ $? -ne 0 ]; then
        echo "Failed to extract golangci-lint"
        return 1
    fi

    chmod +x "${BIN_DIR}/golangci-lint"

    echo "Verifying golangci-lint installation..."
    ${BIN_DIR}/golangci-lint version
}

install_go_modules() {
    # Ensure Go is installed before proceeding
    if [ ! -f "${BIN_DIR}/go/bin/go" ]; then
        echo "Go is not installed. Please install Go first."
        return 1
    fi

    echo "Downloading Go module dependencies..."
    cd "${REPO_DIR}"
    ${BIN_DIR}/go/bin/go mod download
    echo "Go module dependencies downloaded"
}

install_docker() {
    # Check if Docker is already installed
    if command -v docker >/dev/null 2>&1; then
        echo "Docker is already installed"
        docker --version
        return 0
    fi

    echo "Installing Docker for ${OS}..."

    case "${OS}" in
        linux)
            # Remove any old versions
            run_with_error_handling sudo apt-get remove -y docker docker-engine docker.io containerd runc || true

            # Install prerequisites
            run_with_error_handling sudo apt-get update
            run_with_error_handling sudo apt-get install -y \
                ca-certificates \
                curl \
                gnupg \
                lsb-release

            if [ $? -ne 0 ]; then
                echo "Failed to install Docker prerequisites. Please install Docker manually."
                return 0
            fi

            # Add Docker's official GPG key
            run_with_error_handling sudo mkdir -p /etc/apt/keyrings
            run_with_error_handling curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg

            # Set up Docker repository
            run_with_error_handling echo \
                "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
                $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

            run_with_error_handling sudo apt-get update
            run_with_error_handling sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin

            if [ $? -ne 0 ]; then
                echo "Failed to install Docker packages. Please install Docker manually."
                return 0
            fi

            # Add current user to docker group
            run_with_error_handling sudo usermod -aG docker $USER

            # Start and enable Docker service
            run_with_error_handling sudo systemctl start docker
            run_with_error_handling sudo systemctl enable docker
            ;;
        darwin)
            echo "For macOS, please install Docker Desktop manually from https://www.docker.com/products/docker-desktop/"
            echo "Alternatively, you can use Homebrew: brew install --cask docker"
            return 0
            ;;
        *)
            echo "Unsupported OS for Docker installation: ${OS}"
            echo "Please install Docker manually according to your OS instructions"
            return 0
            ;;
    esac

    echo "Verifying Docker installation..."
    run_with_error_handling docker --version
    run_with_error_handling docker compose version

    echo "Docker installed successfully!"
    if [ "${OS}" = "linux" ]; then
        echo "Please log out and back in for group changes to take effect"
    fi

    return 0
}

# Create environment setup script
create_env_script() {
    ENV_SCRIPT="${REPO_DIR}/env.sh"
    echo "Creating environment script: ${ENV_SCRIPT}"

    cat > "${ENV_SCRIPT}" << 'ENVEOF'
#!/bin/bash

# Hulation development environment setup
# Source this file with: source env.sh

# Handle symbolic links properly by resolving the real path of this script
get_script_path() {
    local SOURCE="${BASH_SOURCE[0]}"

    # Resolve $SOURCE until the file is no longer a symlink
    while [ -h "$SOURCE" ]; do
        DIR="$( cd -P "$( dirname "$SOURCE" )" && pwd )"
        SOURCE="$(readlink "$SOURCE")"
        # If $SOURCE was a relative symlink, resolve it relative to the symlink location
        [[ $SOURCE != /* ]] && SOURCE="$DIR/$SOURCE"
    done

    DIR="$( cd -P "$( dirname "$SOURCE" )" && pwd )"
    echo "$DIR"
}

# Variable definitions - these can be overridden by setting them before sourcing this script
REPO_DIR=${REPO_DIR:-"$(get_script_path)"}
BIN_DIR=${BIN_DIR:-"${REPO_DIR}/.bin"}
GOROOT=${GOROOT:-"${BIN_DIR}/go"}

# Add local bin directory and Go to PATH
export PATH="${BIN_DIR}:${GOROOT}/bin:${PATH}"

# Set Go environment variables
export GOROOT="${GOROOT}"
export GOPATH="${REPO_DIR}/.gopath"
export GOBIN="${BIN_DIR}"

# CGO is needed for ClickHouse driver
export CGO_ENABLED=1

# Hulation-specific
export HULATION_REPO="${REPO_DIR}"

echo "Hulation dev environment configured"
echo "  GOROOT:  ${GOROOT}"
echo "  GOPATH:  ${GOPATH}"
echo "  GOBIN:   ${GOBIN}"
echo "  BIN_DIR: ${BIN_DIR}"
echo "  Go:      $(go version 2>/dev/null || echo 'not found')"
ENVEOF

    chmod +x "${ENV_SCRIPT}"
    echo "Environment script created: ${ENV_SCRIPT}"
    echo "To activate the environment, run: source ${ENV_SCRIPT}"
}

# Run installation steps based on command-line arguments

# Handle --cleanbin option
if [[ "$CLEAN_BIN" == "true" ]]; then
    clean_bin_directories
    # If only cleaning was requested, exit here
    if [[ "$INSTALL_ALL" == "false" && "$INSTALL_CORE" == "false" && "${#COMPONENTS_TO_INSTALL[@]}" -eq 0 ]]; then
        echo "Cleanup complete. No other installation requested."
        exit 0
    fi
fi

# Install components based on flags
if should_install_component "dependencies"; then
    echo "=== Installing system dependencies ==="
    install_dependencies
fi

if should_install_component "go"; then
    echo "=== Installing Go ==="
    install_go
fi

if should_install_component "go-modules"; then
    echo "=== Downloading Go modules ==="
    install_go_modules
fi

if should_install_component "golangci-lint"; then
    echo "=== Installing golangci-lint ==="
    install_golangci_lint
fi

if should_install_component "docker" || [[ "$INSTALL_DOCKER" == "true" ]]; then
    echo "=== Installing Docker ==="
    install_docker
fi

if should_install_component "env-script"; then
    echo "=== Creating environment script ==="
    create_env_script
fi

echo ""
echo "Dev environment setup complete."
echo "To activate the environment, run: source env.sh"
