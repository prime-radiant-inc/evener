#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

# Load API keys
set -a
source ../../.env
set +a

# Build
go build -o serf-tui ./cmd/serf-tui/

# Run
exec ./serf-tui \
  --provider "${SERF_PROVIDER:-anthropic}" \
  --model "${SERF_MODEL:-claude-haiku-4-5-20251001}" \
  "$@"
