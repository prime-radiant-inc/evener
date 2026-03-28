#!/usr/bin/env bash
# Build and launch an eval on a specific subset of tasks.
#
# Usage:
#   ./tools/run_eval_subset.sh --tasks "chess-best-move,kv-store-grpc" [options]
#   ./tools/run_eval_subset.sh --tasks failing    # all currently failing tasks
#   ./tools/run_eval_subset.sh --tasks untested   # all untested tasks
#   ./tools/run_eval_subset.sh --tasks hard       # historically hard tasks
#
# Options:
#   --tasks STR        Comma-separated task names, or: failing, untested, hard
#   --run-id NAME      Override auto-generated run ID
#   --reps N           Number of reps (default: 3)
#   --model STR        Model (default: openai/gpt-5.4-mini)
#   --instance-type    EC2 instance type (default: c6i.xlarge)
#   --concurrency N    Tasks per instance (default: 4)
#   --variant STR      Description saved to launch metadata
#   --dry-run          Preview without launching
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"

# Defaults
TASKS=""
REPS=3
INSTANCE_TYPE="c6i.xlarge"
CONCURRENCY=4
MODEL="openai/gpt-5.4-mini"
RUN_ID=""
VARIANT=""
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks)    TASKS="$2"; shift 2 ;;
        --run-id)   RUN_ID="$2"; shift 2 ;;
        --reps)     REPS="$2"; shift 2 ;;
        --model)    MODEL="$2"; shift 2 ;;
        --instance-type) INSTANCE_TYPE="$2"; shift 2 ;;
        --concurrency)   CONCURRENCY="$2"; shift 2 ;;
        --variant)  VARIANT="$2"; shift 2 ;;
        --dry-run)  DRY_RUN=true; shift ;;
        --help|-h)  head -16 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)          echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$TASKS" ]]; then
    echo "Error: --tasks is required" >&2
    echo "  Use comma-separated names, or: failing, untested, hard" >&2
    exit 1
fi

# --- Enforce clean working tree (skip for dry-run) ---
cd "$REPO_ROOT"
if ! $DRY_RUN; then
    if ! git diff --quiet || ! git diff --cached --quiet; then
        echo "Error: uncommitted changes. Commit before launching evals." >&2
        git status --short >&2
        exit 1
    fi
fi

GIT_SHA=$(git rev-parse --short HEAD)
BRANCH=$(git rev-parse --abbrev-ref HEAD)

# --- Resolve named task sets ---
case "$TASKS" in
    failing)
        TASKS=$(python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
tasks = [t for t, info in sb['tasks'].items()
         if info.get('score') is not None and info['score'] < 1.0]
print(','.join(sorted(tasks)))
")
        echo "Failing tasks: $(echo "$TASKS" | tr ',' '\n' | wc -l | tr -d ' ')"
        ;;
    untested)
        TASKS=$(python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
tasks = [t for t, info in sb['tasks'].items() if info.get('score') is None]
print(','.join(sorted(tasks)))
")
        echo "Untested tasks: $(echo "$TASKS" | tr ',' '\n' | wc -l | tr -d ' ')"
        ;;
    hard)
        TASKS="dna-assembly,make-doom-for-mips,sam-cell-seg,install-windows-3.11,caffe-cifar-10,filter-js-from-html,gpt2-codegolf,extract-moves-from-video,raman-fitting,train-fasttext,video-processing,torch-tensor-parallelism,db-wal-recovery,torch-pipeline-parallelism,dna-insert,mteb-leaderboard"
        echo "Historically hard tasks: 16"
        ;;
esac

if [[ -z "$TASKS" ]]; then
    echo "No tasks matched." >&2
    exit 1
fi

TASK_COUNT=$(echo "$TASKS" | tr ',' '\n' | wc -l | tr -d ' ')

if [[ -z "$RUN_ID" ]]; then
    RUN_ID="subset-${GIT_SHA}-$(date -u +%Y%m%d-%H%M)"
fi

echo "=== Subset eval ==="
echo "Branch:   $BRANCH"
echo "SHA:      $GIT_SHA"
echo "Run ID:   $RUN_ID"
echo "Model:    $MODEL"
echo "Tasks:    $TASK_COUNT"
echo "Reps:     $REPS"
echo "Instance: $INSTANCE_TYPE"
if [[ -n "$VARIANT" ]]; then
    echo "Variant:  $VARIANT"
fi
echo ""

if $DRY_RUN; then
    echo "=== DRY RUN ==="
    echo "Tasks: $TASKS"
    exit 0
fi

# --- Build ---
echo "Building linux binary..."
make build-linux 2>&1 | tail -2

if ! strings serf-linux-amd64 | grep -q "agents/coordinator.md"; then
    echo "Error: binary missing embedded agent prompts" >&2
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

# --- Save launch metadata ---
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
    'variant': '$VARIANT',
    'tasks': '$TASKS'.split(','),
    'launched_at': datetime.datetime.utcnow().isoformat() + 'Z',
}
with open('$LAUNCH_META', 'w') as f:
    json.dump(meta, f, indent=2)
"
echo "  Launch metadata saved"

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
echo "  ./tools/run_status.sh $RUN_ID"
echo "  ./tools/post_run.sh $RUN_ID --variant '$VARIANT'"
