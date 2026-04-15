#!/bin/bash
# Start the serf eval dashboard server.
#
# Usage: ./start.sh [--port PORT]
#
# Environment:
#   DASHBOARD_PORT          - Port to listen on (default: 8181)
#   DASHBOARD_HOST          - Host to bind to (default: 0.0.0.0)
#   DASHBOARD_DATA_DIR      - Cache directory for synced S3 data
#   DASHBOARD_SYNC_CACHE    - Persistent storage for S3 run data

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Defaults
PORT="${DASHBOARD_PORT:-8181}"
HOST="${DASHBOARD_HOST:-0.0.0.0}"

# Data directories - prefer persistent storage if available
PERSISTENT_CACHE="/Volumes/Local Archives/serf-s3-cache"
if [[ -d "$PERSISTENT_CACHE" ]]; then
    DATA_DIR="${DASHBOARD_DATA_DIR:-$PERSISTENT_CACHE}"
    SYNC_CACHE="${DASHBOARD_SYNC_CACHE:-$PERSISTENT_CACHE}"
else
    DATA_DIR="${DASHBOARD_DATA_DIR:-/tmp/serf-s3-cache}"
    SYNC_CACHE="${DASHBOARD_SYNC_CACHE:-/tmp/serf-s3-cache}"
fi

# Harbor state directory (auto-detect from repo structure)
HARBOR_STATE="${REPO_ROOT}/../harbor-runner/state/runs"
if [[ ! -d "$HARBOR_STATE" ]]; then
    echo "Warning: Harbor state directory not found at $HARBOR_STATE"
    HARBOR_STATE=""
fi

# Experiments directory
EXPERIMENTS_DIR="${REPO_ROOT}/docs/experiments"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --port)
            PORT="$2"
            shift 2
            ;;
        --host)
            HOST="$2"
            shift 2
            ;;
        --data-dir)
            DATA_DIR="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --port PORT       Port to listen on (default: 8181)"
            echo "  --host HOST       Host to bind to (default: 0.0.0.0)"
            echo "  --data-dir DIR    Data directory for S3 cache"
            echo "  -h, --help        Show this help"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Ensure data directories exist
mkdir -p "$DATA_DIR"
mkdir -p "$SYNC_CACHE"

echo "Starting dashboard server..."
echo "  Host: $HOST"
echo "  Port: $PORT"
echo "  Data dir: $DATA_DIR"
echo "  Sync cache: $SYNC_CACHE"
echo "  Harbor state: ${HARBOR_STATE:-not configured}"
echo "  Experiments: $EXPERIMENTS_DIR"
echo ""

# Build command
CMD=(
    python "$SCRIPT_DIR/server.py"
    --data-dir "$DATA_DIR"
    --experiments-dir "$EXPERIMENTS_DIR"
    --sync-cache-dir "$SYNC_CACHE"
    --host "$HOST"
    --port "$PORT"
)

if [[ -n "$HARBOR_STATE" ]]; then
    CMD+=(--harbor-state-dir "$HARBOR_STATE")
fi

exec "${CMD[@]}"
