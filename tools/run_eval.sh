#!/usr/bin/env bash
# Build and launch eval runs on AWS spot instances.
#
# Usage:
#   ./tools/run_eval.sh                                     # all 89 tasks (baseline)
#   ./tools/run_eval.sh --tasks "chess-best-move,kv-store"  # specific tasks
#   ./tools/run_eval.sh --tasks failing                     # all currently failing
#   ./tools/run_eval.sh --tasks untested                    # all untested
#   ./tools/run_eval.sh --tasks hard                        # historically hard 16
#   ./tools/run_eval.sh --wave                              # all 89, one task per instance
#   ./tools/run_eval.sh --wave --tasks failing              # failing, one task per instance
#
# Options:
#   --tasks STR        Comma-separated task names, or: failing, untested, hard
#                      Default: all 89 from scoreboard.json
#   --wave             Fan-out mode: one task per instance, backfill as slots free
#   --run-id NAME      Override auto-generated run ID
#   --reps N           Number of reps (default: 3)
#   --model STR        Model (default: openai/gpt-5.4-mini)
#   --instance-type    EC2 instance type (default: c6i.xlarge)
#   --concurrency N    Tasks per instance (default: 8, or 1 in wave mode)
#   --max-vcpu N       vCPU quota ceiling for wave mode (default: 128)
#   --variant STR      Description saved to launch metadata
#   --dry-run          Preview without launching
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"

# Defaults
TASKS=""
WAVE=false
REPS=3
INSTANCE_TYPE="c6i.xlarge"
CONCURRENCY=""  # set after parsing based on wave mode
MAX_VCPU=128
MODEL="openai/gpt-5.4-mini"
RUN_ID=""
VARIANT=""
DRY_RUN=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks)         TASKS="$2"; shift 2 ;;
        --wave)          WAVE=true; shift ;;
        --run-id)        RUN_ID="$2"; shift 2 ;;
        --reps)          REPS="$2"; shift 2 ;;
        --model)         MODEL="$2"; shift 2 ;;
        --instance-type) INSTANCE_TYPE="$2"; shift 2 ;;
        --concurrency)   CONCURRENCY="$2"; shift 2 ;;
        --max-vcpu)      MAX_VCPU="$2"; shift 2 ;;
        --variant)       VARIANT="$2"; shift 2 ;;
        --dry-run)       DRY_RUN=true; shift ;;
        --help|-h)       head -24 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)               echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# Default concurrency: 1 for wave (single task per instance), 8 otherwise
if [[ -z "$CONCURRENCY" ]]; then
    if $WAVE; then
        CONCURRENCY=1
    else
        CONCURRENCY=8
    fi
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

# --- Resolve task list ---
if [[ -z "$TASKS" ]]; then
    # Default: all tasks from scoreboard
    TASKS=$(python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
print(','.join(sorted(sb['tasks'].keys())))
")
else
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
fi

if [[ -z "$TASKS" ]]; then
    echo "No tasks matched." >&2
    exit 1
fi

TASK_COUNT=$(echo "$TASKS" | tr ',' '\n' | wc -l | tr -d ' ')

# --- Generate run ID ---
if [[ -z "$RUN_ID" ]]; then
    if $WAVE; then
        RUN_ID="wave-${GIT_SHA}-$(date -u +%Y%m%d-%H%M)"
    elif [[ "$TASK_COUNT" -eq 89 ]]; then
        RUN_ID="baseline-${GIT_SHA}-$(date -u +%Y%m%d)"
    else
        RUN_ID="subset-${GIT_SHA}-$(date -u +%Y%m%d-%H%M)"
    fi
fi

# --- Print summary ---
MODE="batch"
$WAVE && MODE="wave"

echo "=== Eval launch ($MODE) ==="
echo "Branch:      $BRANCH"
echo "SHA:         $GIT_SHA"
echo "Run ID:      $RUN_ID"
echo "Model:       $MODEL"
echo "Tasks:       $TASK_COUNT"
echo "Reps:        $REPS"
echo "Instance:    $INSTANCE_TYPE"
echo "Concurrency: $CONCURRENCY"
if $WAVE; then
    echo "Max vCPU:    $MAX_VCPU"
fi
if [[ -n "$VARIANT" ]]; then
    echo "Variant:     $VARIANT"
fi
echo ""

if $DRY_RUN; then
    echo "=== DRY RUN ==="
    echo "Tasks: $TASKS"
    if $WAVE; then
        TOTAL=$((TASK_COUNT * REPS))
        # vCPU per instance type
        VCPU=$(python3 -c "
m = {'c6i.large': 2, 'c6i.xlarge': 4, 'c6i.2xlarge': 8, 'c6i.4xlarge': 16}
print(m.get('$INSTANCE_TYPE', 4))
")
        MAX_INSTANCES=$((MAX_VCPU / VCPU))
        echo ""
        echo "Wave mode:"
        echo "  Total work items: $TOTAL ($TASK_COUNT tasks x $REPS reps)"
        echo "  Max concurrent:   $MAX_INSTANCES instances ($VCPU vCPU each, $MAX_VCPU quota)"
        echo "  Estimated waves:  $(( (TOTAL + MAX_INSTANCES - 1) / MAX_INSTANCES ))"
    fi
    exit 0
fi

# --- Build ---
echo "Building linux binary..."
make build-linux 2>&1 | tail -2

if ! strings serf-linux-amd64 > /tmp/serf-strings-check.$$ 2>&1; then
    echo "Error: strings command failed on binary" >&2
    rm -f /tmp/serf-strings-check.$$
    exit 1
fi
if ! grep -q "agents/coordinator.md" /tmp/serf-strings-check.$$; then
    echo "Error: binary missing embedded agent prompts" >&2
    rm -f /tmp/serf-strings-check.$$
    exit 1
fi
rm -f /tmp/serf-strings-check.$$
echo "  Binary OK ($(du -h serf-linux-amd64 | cut -f1))"

# --- Stage ---
AGENT_DIR="/tmp/eval-${RUN_ID}/agent"
rm -rf "/tmp/eval-${RUN_ID}"
mkdir -p "$AGENT_DIR"
cp serf-linux-amd64 "$AGENT_DIR/"
cp tools/serf_agent.py "$AGENT_DIR/"
cp tools/install-serf.sh.j2 "$AGENT_DIR/"
echo "  Staged to $AGENT_DIR"

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
    'variant': '$VARIANT' if '$VARIANT' else None,
    'tasks': '$TASKS'.split(','),
    'launch_mode': 'wave' if $($WAVE && echo True || echo False) else 'batch',
    'launched_at': datetime.datetime.utcnow().isoformat() + 'Z',
}
# Remove None values
meta = {k: v for k, v in meta.items() if v is not None}
with open('$LAUNCH_META', 'w') as f:
    json.dump(meta, f, indent=2)
"
echo "  Launch metadata saved"

# --- Launch ---
echo ""
if $WAVE; then
    # Wave mode: Python orchestrator handles per-task-rep fan-out
    exec python3 "$REPO_ROOT/tools/wave_launcher.py" \
        --run-id "$RUN_ID" \
        --agent-dir "$AGENT_DIR" \
        --model "$MODEL" \
        --tasks "$TASKS" \
        --reps "$REPS" \
        --instance-type "$INSTANCE_TYPE" \
        --concurrency "$CONCURRENCY" \
        --max-vcpu "$MAX_VCPU" \
        --harbor-dir "$HARBOR_DIR"
else
    # Batch mode: one instance per rep, all tasks on each
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
    echo "  ./tools/post_run.sh $RUN_ID${VARIANT:+ --variant '$VARIANT'}"
fi
