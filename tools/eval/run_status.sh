#!/usr/bin/env bash
# Check the live status of an eval run.
#
# Usage:
#   ./tools/eval/run_status.sh RUN_ID
#
# Shows: instance states (running/terminated), S3 upload status per rep,
# and a pass/fail summary for completed reps.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"

RUN_ID="${1:?Usage: run_status.sh RUN_ID}"

# Show launch metadata if available
LAUNCH_META="$REPO_ROOT/.serf-launches/${RUN_ID}.json"
if [[ -f "$LAUNCH_META" ]]; then
    echo "=== Launch metadata ==="
    python3 -c "
import json
with open('$LAUNCH_META') as f:
    m = json.load(f)
print(f\"  Branch: {m.get('branch')}  SHA: {m.get('git_sha')}  Model: {m.get('model')}\")
print(f\"  Reps: {m.get('reps')}  Tasks: {m.get('task_count')}  Instance: {m.get('instance_type')}\")
print(f\"  Launched: {m.get('launched_at')}\")
"
    echo ""
fi

# Show harbor-runner status
cd "$HARBOR_DIR"
./status.sh --run-id "$RUN_ID" 2>&1
