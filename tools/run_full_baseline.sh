#!/usr/bin/env bash
# Build and launch a full 89-task baseline eval on AWS.
#
# Usage:
#   ./tools/run_full_baseline.sh [--run-id NAME] [--reps N] [--dry-run]
#
# Builds the linux binary from current HEAD, stages it, and launches all 89 tasks
# on harbor-runner. Defaults to 3 reps on c6i.2xlarge with concurrency 8.
#
# Prerequisites:
#   - .env with OPENAI_API_KEY (for collect step later, not needed for launch)
#   - harbor-runner set up at ~/prime-radiant/harbor-runner
#   - Current code committed (enforced — uncommitted changes block launch)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"

# Defaults
REPS=3
INSTANCE_TYPE="c6i.2xlarge"
CONCURRENCY=8
MODEL="openai/gpt-5.4-mini"
RUN_ID=""
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --run-id)   RUN_ID="$2"; shift 2 ;;
        --reps)     REPS="$2"; shift 2 ;;
        --model)    MODEL="$2"; shift 2 ;;
        --instance-type) INSTANCE_TYPE="$2"; shift 2 ;;
        --concurrency)   CONCURRENCY="$2"; shift 2 ;;
        --dry-run)  DRY_RUN=true; shift ;;
        --help|-h)  head -14 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)          echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# --- Enforce clean working tree ---
cd "$REPO_ROOT"
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "Error: uncommitted changes. Commit before launching evals." >&2
    echo "  git status:" >&2
    git status --short >&2
    exit 1
fi

GIT_SHA=$(git rev-parse --short HEAD)
BRANCH=$(git rev-parse --abbrev-ref HEAD)

if [[ -z "$RUN_ID" ]]; then
    RUN_ID="baseline-${GIT_SHA}-$(date -u +%Y%m%d)"
fi

echo "=== Full baseline eval ==="
echo "Branch:   $BRANCH"
echo "SHA:      $GIT_SHA"
echo "Run ID:   $RUN_ID"
echo "Model:    $MODEL"
echo "Reps:     $REPS"
echo "Instance: $INSTANCE_TYPE"
echo "Concurrency: $CONCURRENCY"
echo ""

# --- Build ---
echo "Building linux binary..."
make build-linux 2>&1 | tail -2

# Verify binary
if ! strings serf-linux-amd64 | grep -q "You are a coordinator"; then
    echo "Error: binary missing coordinator prompt" >&2
    exit 1
fi
echo "  Binary OK ($(du -h serf-linux-amd64 | cut -f1))"

# --- Stage ---
AGENT_DIR="/tmp/eval-${RUN_ID}/agent"
rm -rf "/tmp/eval-${RUN_ID}"
mkdir -p "$AGENT_DIR"
cp serf-linux-amd64 "$AGENT_DIR/"
cp tools/serf_agent.py "$AGENT_DIR/"
cp tools/install-serf.sh.j2 "$AGENT_DIR/"
echo "  Staged to $AGENT_DIR"

# --- Task list ---
TASKS=$(python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
print(','.join(sorted(sb['tasks'].keys())))
")
TASK_COUNT=$(echo "$TASKS" | tr ',' '\n' | wc -l | tr -d ' ')
echo "  $TASK_COUNT tasks"

if $DRY_RUN; then
    echo ""
    echo "=== DRY RUN — would launch $REPS instances ==="
    echo "Tasks: $TASKS"
    exit 0
fi

# --- Save launch metadata (post_run.sh reads this) ---
LAUNCH_META="$REPO_ROOT/.serf-launches/${RUN_ID}.json"
mkdir -p "$REPO_ROOT/.serf-launches"
python3 -c "
import json, datetime
meta = {
    'run_id': '$RUN_ID',
    'git_sha': '$GIT_SHA',
    'branch': '$BRANCH',
    'model': '$MODEL',
    'reps': $REPS,
    'instance_type': '$INSTANCE_TYPE',
    'concurrency': $CONCURRENCY,
    'task_count': $TASK_COUNT,
    'launched_at': datetime.datetime.utcnow().isoformat() + 'Z',
}
with open('$LAUNCH_META', 'w') as f:
    json.dump(meta, f, indent=2)
"
echo "  Launch metadata saved to $LAUNCH_META"

# --- Launch ---
echo ""
cd "$HARBOR_DIR"
./launch.sh \
    --run-id "$RUN_ID" \
    --agent-dir "$AGENT_DIR" \
    --agent-import-path serf_agent:SerfAgent \
    --model "$MODEL" \
    --task-names "$TASKS" \
    --reps "$REPS" \
    --concurrency "$CONCURRENCY" \
    --instance-type "$INSTANCE_TYPE"

echo ""
echo "Next steps:"
echo "  ./tools/check_run.sh $RUN_ID                    # poll for results"
echo "  ./tools/post_run.sh $RUN_ID                     # collect + update scoreboard"
