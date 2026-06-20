#!/usr/bin/env bash
# Build and launch eval runs on AWS spot instances (one task per instance).
#
# Usage:
#   ./tools/eval/run_eval.sh                                     # all 89 tasks (3 reps)
#   ./tools/eval/run_eval.sh --tasks "chess-best-move,kv-store"  # specific tasks
#   ./tools/eval/run_eval.sh --tasks failing                     # all currently failing
#   ./tools/eval/run_eval.sh --tasks untested                    # all untested
#   ./tools/eval/run_eval.sh --tasks hard                        # historically hard 16
#   ./tools/eval/run_eval.sh --tasks vision                       # 9 tasks using vision side-channel
#   ./tools/eval/run_eval.sh --dry-run                           # preview without launching
#
# Options:
#   --tasks STR        Comma-separated task names, or: failing, untested, hard
#                      Default: all 89 from scoreboard.json
#   --run-id NAME      Override auto-generated run ID
#   --reps N           Number of reps (default: 3)
#   --model STR        Model (default: openai/gpt-5.4-mini)
#   --instance-type    EC2 instance type (default: r6i.large)
#   --max-vcpu N       vCPU quota ceiling (default: 128)
#   --variant STR      Description saved to launch metadata
#   --dry-run          Preview without launching
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"

# Defaults
TASKS=""
REPS=3
INSTANCE_TYPE="r6i.large"
MAX_VCPU=128
MODEL="openai/gpt-5.4-mini"
RUN_ID=""
VARIANT=""
AGENT_KWARGS=""
DRY_RUN=false
BACKFILL=false
ON_DEMAND=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tasks)         TASKS="$2"; shift 2 ;;
        --run-id)        RUN_ID="$2"; shift 2 ;;
        --reps)          REPS="$2"; shift 2 ;;
        --model)         MODEL="$2"; shift 2 ;;
        --instance-type) INSTANCE_TYPE="$2"; shift 2 ;;
        --max-vcpu)      MAX_VCPU="$2"; shift 2 ;;
        --variant)       VARIANT="$2"; shift 2 ;;
        --agent-kwargs)  AGENT_KWARGS="$2"; shift 2 ;;
        --dry-run)       DRY_RUN=true; shift ;;
        --backfill)      BACKFILL=true; shift ;;
        --on-demand)     ON_DEMAND=true; shift ;;
        --wave)          shift ;;  # accepted for backward compat, always wave
        --help|-h)       head -22 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)               echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

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
        discriminators)
            # 62 tasks that sometimes pass and sometimes fail — the signal tasks.
            # Excludes 19 never-passed (always 0) and 8 always-perfect (always 1.0).
            # Use this for experiment iterations: saves ~25% of instances.
            TASKS="adaptive-rejection-sampler,bn-fit-modify,break-filter-js-from-html,build-cython-ext,build-pmars,build-pov-ray,caffe-cifar-10,cancel-async-tasks,chess-best-move,circuit-fibsqrt,cobol-modernization,compile-compcert,configure-git-webserver,constraints-scheduling,count-dataset-tokens,crack-7z-hash,custom-memory-heap-crash,db-wal-recovery,extract-elf,extract-moves-from-video,feal-differential-cryptanalysis,feal-linear-cryptanalysis,financial-document-processor,fix-code-vulnerability,fix-git,fix-ocaml-gc,git-leak-recovery,git-multibranch,gpt2-codegolf,hf-model-inference,kv-store-grpc,large-scale-text-editing,largest-eigenval,llm-inference-batching-scheduler,log-summary-date-ranges,mailman,mcmc-sampling-stan,merge-diff-arc-agi-task,modernize-scientific-stack,mteb-retrieve,openssl-selfsigned-cert,password-recovery,path-tracing,polyglot-c-py,polyglot-rust-c,portfolio-optimization,pytorch-model-cli,pytorch-model-recovery,qemu-alpine-ssh,qemu-startup,query-optimize,raman-fitting,reshard-c4-data,sanitize-git-repo,schemelike-metacircular-eval,sparql-university,sqlite-db-truncate,sqlite-with-gcov,torch-tensor-parallelism,tune-mjcf,vulnerable-secret,winning-avg-corewars"
            echo "Discriminator tasks: 62 (excludes 19 never-pass + 8 always-pass)"
            ;;
        crosscheck)
            # 8 always-perfect tasks. Run after discriminators pass as a safety net.
            TASKS="code-from-image,distribution-search,headless-terminal,multi-source-data-merger,nginx-request-logging,prove-plus-comm,pypi-server,regex-log"
            echo "Cross-check tasks (always-perfect): 8"
            ;;
        quick-baseline)
            # 8 historically-passable but recently-variable tasks.
            # Quick sanity check for shipped experiments (~8 min at 3 reps).
            TASKS="sparql-university,sqlite-with-gcov,qemu-startup,llm-inference-batching-scheduler,fix-ocaml-gc,count-dataset-tokens,cobol-modernization,schemelike-metacircular-eval"
            echo "Quick-baseline tasks: 8"
            ;;
        vision)
            # 9 tasks that have previously triggered the vision side-channel
            # (images or PDFs read via read_file). Use this to test vision-
            # prompt or vision-architecture changes.
            TASKS="chess-best-move,code-from-image,financial-document-processor,gcode-to-text,path-tracing,path-tracing-reverse,pytorch-model-cli,sam-cell-seg,video-processing"
            echo "Vision tasks: 9"
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
    RUN_ID="wave-${GIT_SHA}-$(date -u +%Y%m%d-%H%M)"
fi

# --- Print summary ---
echo "=== Eval launch ==="
echo "Branch:      $BRANCH"
echo "SHA:         $GIT_SHA"
echo "Run ID:      $RUN_ID"
echo "Model:       $MODEL"
echo "Tasks:       $TASK_COUNT"
echo "Reps:        $REPS"
echo "Instance:    $INSTANCE_TYPE"
echo "Max vCPU:    $MAX_VCPU"
if [[ -n "$VARIANT" ]]; then
    echo "Variant:     $VARIANT"
fi
echo ""

if $DRY_RUN; then
    echo "=== DRY RUN ==="
    echo "Tasks: $TASKS"
    TOTAL=$((TASK_COUNT * REPS))
    VCPU=$(python3 -c "
m = {'c6i.large': 2, 'c6i.xlarge': 4, 'c6i.2xlarge': 8, 'c6i.4xlarge': 16,
     'r6i.large': 2, 'r6i.xlarge': 4, 'r6i.2xlarge': 8,
     'm6i.large': 2, 'm6i.xlarge': 4}
print(m.get('$INSTANCE_TYPE', 4))
")
    MAX_INSTANCES=$((MAX_VCPU / VCPU))
    echo ""
    echo "  Total work items: $TOTAL ($TASK_COUNT tasks x $REPS reps)"
    echo "  Max concurrent:   $MAX_INSTANCES instances ($VCPU vCPU each, $MAX_VCPU quota)"
    echo "  Estimated waves:  $(( (TOTAL + MAX_INSTANCES - 1) / MAX_INSTANCES ))"
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
if ! grep -q "plugins/coordinator-workflow/agents/coordinator.md" /tmp/serf-strings-check.$$; then
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
cp tools/eval/serf_agent.py "$AGENT_DIR/"
cp tools/eval/install-serf.sh.j2 "$AGENT_DIR/"
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
    'task_count': $TASK_COUNT,
    'variant': '$VARIANT' if '$VARIANT' else None,
    'tasks': '$TASKS'.split(','),
    'launched_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
}
# Remove None values
meta = {k: v for k, v in meta.items() if v is not None}
with open('$LAUNCH_META', 'w') as f:
    json.dump(meta, f, indent=2)
"
echo "  Launch metadata saved"

# --- Launch (one task per instance) ---
echo ""
BACKFILL_FLAG=""
if $BACKFILL; then
    BACKFILL_FLAG="--backfill"
fi

LAUNCHER_CMD=(env PYTHONUNBUFFERED=1 python3 "$REPO_ROOT/tools/eval/wave_launcher.py"
    --run-id "$RUN_ID"
    --agent-dir "$AGENT_DIR"
    --model "$MODEL"
    --tasks "$TASKS"
    --reps "$REPS"
    --instance-type "$INSTANCE_TYPE"
    --concurrency 1
    --max-vcpu "$MAX_VCPU"
    --harbor-dir "$HARBOR_DIR")
if [[ -n "$AGENT_KWARGS" ]]; then
    LAUNCHER_CMD+=(--agent-kwargs "$AGENT_KWARGS")
fi
if [[ -n "$BACKFILL_FLAG" ]]; then
    LAUNCHER_CMD+=("$BACKFILL_FLAG")
fi
if $ON_DEMAND; then
    LAUNCHER_CMD+=(--on-demand)
fi

exec "${LAUNCHER_CMD[@]}"
