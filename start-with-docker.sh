#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
HULA_IMAGE="${HULA_IMAGE:-ghcr.io/tlalocweb/hula:latest}"
HULA_CONFIG="${HULA_CONFIG:-${SCRIPT_DIR}/config.yaml}"
HULA_PORT="${HULA_PORT:-8088}"
HULA_CONTAINER_NAME="${HULA_CONTAINER_NAME:-hula}"
CH_CONTAINER_NAME="${CH_CONTAINER_NAME:-hula-clickhouse}"
NETWORK_NAME="${NETWORK_NAME:-hula-net}"

show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Start Hulation server and ClickHouse with Docker."
    echo ""
    echo "Options:"
    echo "  --help              Show this help message"
    echo "  --stop              Stop and remove containers"
    echo "  --restart           Restart containers"
    echo "  --logs              Follow hula container logs"
    echo "  --pull              Pull latest images before starting"
    echo "  --no-clickhouse     Don't start ClickHouse (use if running externally)"
    echo ""
    echo "Environment variables:"
    echo "  HULA_IMAGE          Docker image (default: ghcr.io/tlalocweb/hula:latest)"
    echo "  HULA_CONFIG         Path to config.yaml (default: ./config.yaml)"
    echo "  HULA_PORT           Host port to expose (default: 8088)"
    echo "  HULA_CONTAINER_NAME Container name (default: hula)"
    echo "  CH_CONTAINER_NAME   ClickHouse container name (default: hula-clickhouse)"
    echo "  NETWORK_NAME        Docker network name (default: hula-net)"
}

stop_containers() {
    echo "Stopping containers..."
    docker rm -f "${HULA_CONTAINER_NAME}" 2>/dev/null || true
    docker rm -f "${CH_CONTAINER_NAME}" 2>/dev/null || true
    docker network rm "${NETWORK_NAME}" 2>/dev/null || true
    echo "Stopped."
}

START_CLICKHOUSE=true
DO_PULL=false
ACTION="start"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --help)        show_help; exit 0 ;;
        --stop)        ACTION="stop"; shift ;;
        --restart)     ACTION="restart"; shift ;;
        --logs)        ACTION="logs"; shift ;;
        --pull)        DO_PULL=true; shift ;;
        --no-clickhouse) START_CLICKHOUSE=false; shift ;;
        *)             echo "Unknown option: $1"; show_help; exit 1 ;;
    esac
done

if [ "${ACTION}" = "stop" ]; then
    stop_containers
    exit 0
fi

if [ "${ACTION}" = "logs" ]; then
    docker logs -f "${HULA_CONTAINER_NAME}"
    exit 0
fi

if [ "${ACTION}" = "restart" ]; then
    stop_containers
fi

# Verify config exists
if [ ! -f "${HULA_CONFIG}" ]; then
    echo "Config file not found: ${HULA_CONFIG}"
    echo ""
    echo "Create one from the example:"
    echo "  cp docker-example-config.yaml config.yaml"
    echo "  # edit config.yaml to suit your deployment"
    exit 1
fi

HULA_CONFIG="$(cd "$(dirname "${HULA_CONFIG}")" && pwd)/$(basename "${HULA_CONFIG}")"

# Pull latest images if requested
if [ "${DO_PULL}" = true ]; then
    echo "Pulling images..."
    docker pull "${HULA_IMAGE}"
    if [ "${START_CLICKHOUSE}" = true ]; then
        docker pull clickhouse/clickhouse-server:latest
    fi
fi

# Create shared network
docker network create "${NETWORK_NAME}" 2>/dev/null || true

# Start ClickHouse
if [ "${START_CLICKHOUSE}" = true ]; then
    if docker ps -q -f name="^${CH_CONTAINER_NAME}$" | grep -q .; then
        echo "ClickHouse already running (${CH_CONTAINER_NAME})"
    else
        echo "Starting ClickHouse..."
        docker rm -f "${CH_CONTAINER_NAME}" 2>/dev/null || true
        mkdir -p "${SCRIPT_DIR}/ch_data" "${SCRIPT_DIR}/ch_logs"
        docker run -d \
            --name "${CH_CONTAINER_NAME}" \
            --network "${NETWORK_NAME}" \
            --cap-add=SYS_NICE --cap-add=NET_ADMIN --cap-add=IPC_LOCK \
            --ulimit nofile=262144:262144 \
            -v "${SCRIPT_DIR}/ch_data":/var/lib/clickhouse \
            -v "${SCRIPT_DIR}/ch_logs":/var/log/clickhouse-server \
            -e CLICKHOUSE_DB=hula \
            -e CLICKHOUSE_USER=hula \
            -e CLICKHOUSE_PASSWORD=hula \
            -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 \
            --restart unless-stopped \
            clickhouse/clickhouse-server:latest

        # Wait for ClickHouse to be ready
        echo -n "Waiting for ClickHouse..."
        for i in $(seq 1 30); do
            if docker exec "${CH_CONTAINER_NAME}" clickhouse-client --query "SELECT 1" >/dev/null 2>&1; then
                echo " ready."
                break
            fi
            echo -n "."
            sleep 1
        done
    fi
fi

# Start Hulation
if docker ps -q -f name="^${HULA_CONTAINER_NAME}$" | grep -q .; then
    echo "Hula already running (${HULA_CONTAINER_NAME})"
else
    echo "Starting Hulation..."
    docker rm -f "${HULA_CONTAINER_NAME}" 2>/dev/null || true

    EXTRA_ARGS=""
    # Mount docker.sock if config uses backends or site deployment features
    if grep -q "backends:\|root_git_autodeploy:" "${HULA_CONFIG}" 2>/dev/null; then
        EXTRA_ARGS="-v /var/run/docker.sock:/var/run/docker.sock"
    fi
    # Mount sitedeploy data volume if git autodeploy is configured
    if grep -q "root_git_autodeploy:" "${HULA_CONFIG}" 2>/dev/null; then
        EXTRA_ARGS="${EXTRA_ARGS} -v hula-sitedeploy:/var/hula/sitedeploy"
    fi

    docker run -d \
        --name "${HULA_CONTAINER_NAME}" \
        --network "${NETWORK_NAME}" \
        -p "${HULA_PORT}:${HULA_PORT}" \
        -v "${HULA_CONFIG}":/etc/hula/config.yaml:ro \
        -v "${SCRIPT_DIR}/hula_certs":/var/hula/certs \
        ${EXTRA_ARGS} \
        --restart unless-stopped \
        "${HULA_IMAGE}"

    echo "Hula started on port ${HULA_PORT}"
fi

echo ""
echo "Containers:"
docker ps --filter "network=${NETWORK_NAME}" --format "  {{.Names}}\t{{.Status}}\t{{.Ports}}"
echo ""
echo "Logs:  $0 --logs"
echo "Stop:  $0 --stop"
