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
  --provider "${SERF_PROVIDER:-openai}" \
  --model "${SERF_MODEL:-gpt-4o-mini}" \
  "$@"
