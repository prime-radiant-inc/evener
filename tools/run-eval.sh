#!/usr/bin/env bash
# Unified orchestration wrapper for benchmark eval runs.
#
# Handles the full lifecycle: preflight -> build -> deploy -> manifest -> launch -> monitor -> collect -> summarize.
#
# Usage:
#   ./tools/run-eval.sh --job NAME [options]
#
# Examples:
#   ./tools/run-eval.sh --job baseline-v1                           # full suite, 3 reps
#   ./tools/run-eval.sh --job test-run --task build-cython-ext      # single task
#   ./tools/run-eval.sh --job reviewer --reps 5 --ak enable_reviewer_gate=true
#   ./tools/run-eval.sh --job baseline-v1 --status                  # check running job
#   ./tools/run-eval.sh --job baseline-v1 --collect-only            # collect finished job
#   ./tools/run-eval.sh --job quick --no-build --model openai/gpt-5.3-codex
#   ./tools/run-eval.sh --job full --allow-dirty --dry-run
#
# Options:
#   --job NAME          Job name (required)
#   --model MODEL       Model identifier (default: openai/gpt-5.2-codex)
#   --task TASK         Single task name (omit for full suite)
#   --reps N            Repetitions per task (default: 3)
#   --concurrency N     Parallel tasks (default: 4)
#   --ak KEY=VALUE      Agent kwarg, repeatable
#   --adapter PATH      Agent import path (default: serf_agent:SerfAgent)
#   --no-build          Skip cross-compile and binary deploy
#   --allow-dirty       Allow dirty git tree (stores diff in manifest)
#   --collect-only      Collect and summarize an already-finished job
#   --status            Show status of a running job
#   --force             Kill existing job before launching
#   --archive-dir DIR   Archive root (default: /data/serf-evals)
#   --dry-run           Print what would be done without doing it
#   --help              Show this help
#
# Requires: SSH access to flower-garden, Go toolchain for cross-compile.
set -euo pipefail

# --- Constants ---

REMOTE=jesse@192.168.118.101
REMOTE_DIR=git/terminal-bench
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Defaults ---

MODEL="openai/gpt-5.2-codex"
REPS=3
CONCURRENCY=4
ADAPTER="serf_agent:SerfAgent"
NO_BUILD=0
ALLOW_DIRTY=0
COLLECT_ONLY=0
STATUS_ONLY=0
FORCE=0
DRY_RUN=0
JOB_NAME=""
TASK_NAME=""
ARCHIVE_ROOT="/data/serf-evals"
AK_ARGS=()

# --- Argument parsing ---

usage() {
    cat <<'USAGE'
Unified orchestration wrapper for benchmark eval runs.

Handles the full lifecycle: preflight -> build -> deploy -> manifest -> launch -> monitor -> collect -> summarize.

Usage:
  ./tools/run-eval.sh --job NAME [options]

Options:
  --job NAME          Job name (required)
  --model MODEL       Model identifier (default: openai/gpt-5.2-codex)
  --task TASK         Single task name (omit for full suite)
  --reps N            Repetitions per task (default: 3)
  --concurrency N     Parallel tasks (default: 4)
  --ak KEY=VALUE      Agent kwarg, repeatable
  --adapter PATH      Agent import path (default: serf_agent:SerfAgent)
  --no-build          Skip cross-compile and binary deploy
  --allow-dirty       Allow dirty git tree (stores diff in manifest)
  --collect-only      Collect and summarize an already-finished job
  --status            Show status of a running job
  --force             Kill existing job before launching
  --archive-dir DIR   Archive root (default: /data/serf-evals)
  --dry-run           Print what would be done without doing it
  --help              Show this help

Examples:
  ./tools/run-eval.sh --job baseline-v1                           # full suite, 3 reps
  ./tools/run-eval.sh --job test-run --task build-cython-ext      # single task
  ./tools/run-eval.sh --job reviewer --reps 5 --ak enable_reviewer_gate=true
  ./tools/run-eval.sh --job baseline-v1 --status                  # check running job
  ./tools/run-eval.sh --job baseline-v1 --collect-only            # collect finished job
USAGE
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --job)         JOB_NAME="$2"; shift 2 ;;
        --model)       MODEL="$2"; shift 2 ;;
        --task)        TASK_NAME="$2"; shift 2 ;;
        --reps)        REPS="$2"; shift 2 ;;
        --concurrency) CONCURRENCY="$2"; shift 2 ;;
        --ak)          AK_ARGS+=("$2"); shift 2 ;;
        --adapter)     ADAPTER="$2"; shift 2 ;;
        --no-build)    NO_BUILD=1; shift ;;
        --allow-dirty) ALLOW_DIRTY=1; shift ;;
        --collect-only) COLLECT_ONLY=1; shift ;;
        --status)      STATUS_ONLY=1; shift ;;
        --force)       FORCE=1; shift ;;
        --archive-dir) ARCHIVE_ROOT="$2"; shift 2 ;;
        --dry-run)     DRY_RUN=1; shift ;;
        --help|-h)     usage ;;
        *)
            echo "ERROR: Unknown argument: $1" >&2
            echo "Run with --help for usage." >&2
            exit 1
            ;;
    esac
done

# ============================================================
# Preflight
# ============================================================

if [[ -z "$JOB_NAME" ]]; then
    echo "ERROR: --job is required" >&2
    exit 1
fi

# Clean tree check (skip for status/collect-only)
if [[ "$ALLOW_DIRTY" -eq 0 && "$STATUS_ONLY" -eq 0 && "$COLLECT_ONLY" -eq 0 ]]; then
    if ! git -C "$REPO_ROOT" diff --quiet || ! git -C "$REPO_ROOT" diff --cached --quiet; then
        echo "ERROR: Dirty working tree. Commit changes or use --allow-dirty." >&2
        exit 1
    fi
fi

# ============================================================
# Status (early exit)
# ============================================================

if [[ "$STATUS_ONLY" -eq 1 ]]; then
    ssh "$REMOTE" bash <<REMOTE_SCRIPT
echo "=== Process ==="
ps aux | grep "job-name $JOB_NAME" | grep -v grep || echo "(not running)"

echo ""
echo "=== Results ==="
pass=0; fail=0; pending=0

RESULTS_DIR=""
for candidate in "/tmp/$JOB_NAME/$JOB_NAME" "/tmp/$JOB_NAME"; do
    if [ -d "\$candidate" ]; then
        if ls "\$candidate"/*/verifier/reward.txt >/dev/null 2>&1 || \
           ls "\$candidate"/*/reward.txt >/dev/null 2>&1 || \
           ls "\$candidate"/*/result.json >/dev/null 2>&1; then
            RESULTS_DIR="\$candidate"
            break
        fi
    fi
done

if [ -z "\$RESULTS_DIR" ]; then
    RESULTS_DIR="/tmp/$JOB_NAME"
    if [ ! -d "\$RESULTS_DIR" ]; then
        echo "  No results directory found for $JOB_NAME"
        exit 0
    fi
fi

echo "(results in \$RESULTS_DIR)"
echo ""

for d in \$RESULTS_DIR/*/; do
    [ -d "\$d" ] || continue
    task=\$(basename "\$d" | sed 's/__.*\$//')
    [ -f "\$d/result.json" ] || [ -f "\$d/verifier/reward.txt" ] || [ -f "\$d/reward.txt" ] || continue
    reward=\$(cat "\$d/verifier/reward.txt" "\$d/reward.txt" 2>/dev/null | head -1)
    if [ -z "\$reward" ]; then
        echo "  \$task: RUNNING"
        pending=\$((pending+1))
    elif [ "\$reward" = "1.0" ] || [ "\$reward" = "1" ]; then
        echo "  \$task: PASS"
        pass=\$((pass+1))
    else
        echo "  \$task: FAIL (\$reward)"
        fail=\$((fail+1))
    fi
done
total=\$((pass+fail+pending))
echo ""
echo "=== Summary: \$pass/\$total pass, \$fail fail, \$pending running ==="

echo ""
echo "=== Build ==="
cat /tmp/$JOB_NAME/manifest.json 2>/dev/null \
  || cat /tmp/$JOB_NAME/$JOB_NAME/manifest.json 2>/dev/null \
  || echo "(no manifest)"

echo ""
echo "=== Recent log ==="
tail -10 /tmp/$JOB_NAME/$JOB_NAME/job.log 2>/dev/null \
  || tail -10 /tmp/$JOB_NAME.log 2>/dev/null \
  || echo "(no log)"
REMOTE_SCRIPT
    exit 0
fi

# ============================================================
# Collect-only (early exit)
# ============================================================

if [[ "$COLLECT_ONLY" -eq 1 ]]; then
    GIT_SHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
    RUN_ID="$(date -u +%Y-%m-%dT%H%M%SZ)_${JOB_NAME}_${GIT_SHA}"
    RUN_DIR="$ARCHIVE_ROOT/runs/$RUN_ID"

    echo "=== Collecting job: $JOB_NAME ==="
    echo "  Run ID: $RUN_ID"

    TMPDIR_COLLECT="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR_COLLECT"' EXIT

    echo "=== Syncing harbor output from remote ==="
    rsync -az "$REMOTE:/tmp/$JOB_NAME/$JOB_NAME/" "$TMPDIR_COLLECT/"

    echo "=== Running collect-run.sh ==="
    "$SCRIPT_DIR/collect-run.sh" \
        --harbor-dir "$TMPDIR_COLLECT" \
        --archive-dir "$RUN_DIR" \
        --run-id "$RUN_ID"

    echo "=== Generating summary ==="
    python3 "$SCRIPT_DIR/generate_summary.py" "$RUN_DIR" "$RUN_ID" > "$RUN_DIR/summary.json"

    echo ""
    echo "=== Summary ==="
    python3 -c "
import json, sys
s = json.load(open('$RUN_DIR/summary.json'))
print(f\"  Tasks:    {s['task_count']}\")
print(f\"  Majority: {s['pass_count_majority']}/{s['task_count']} ({s['pass_rate_majority']:.1%})\")
print(f\"  Strict:   {s['pass_count_strict']}/{s['task_count']} ({s['pass_rate_strict']:.1%})\")
print(f\"  Any:      {s['pass_count_any']}/{s['task_count']} ({s['pass_rate_any']:.1%})\")
ci = s['pass_rate_majority_ci_95']
print(f\"  95% CI:   [{ci[0]:.1%}, {ci[1]:.1%}]\")
fc = s.get('failure_categories', {})
if fc:
    print(f\"  Failures: {fc}\")
"
    echo ""
    echo "  Archive: $RUN_DIR"
    echo "  Summary: $RUN_DIR/summary.json"
    exit 0
fi

# ============================================================
# Build
# ============================================================

if [[ "$DRY_RUN" -eq 0 ]]; then
    if [[ "$NO_BUILD" -eq 0 ]]; then
        echo "=== Building linux binary ==="
        LDFLAGS="-X primeradiant.com/serf/buildinfo.GitSHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD) \
                 -X primeradiant.com/serf/buildinfo.GitDirty=$(git -C "$REPO_ROOT" diff --quiet && echo '' || echo 'true') \
                 -X primeradiant.com/serf/buildinfo.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        (cd "$REPO_ROOT" && GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/serf-linux-amd64 ./cmd/serf/)

        echo "=== Deploying binary ==="
        scp /tmp/serf-linux-amd64 "$REMOTE:$REMOTE_DIR/serf-linux-amd64"
    else
        echo "=== Skipping build (--no-build) ==="
    fi

    # ============================================================
    # Deploy adapter
    # ============================================================

    echo "=== Deploying adapter ==="
    scp "$REPO_ROOT/tools/serf_agent.py" "$REMOTE:$REMOTE_DIR/serf_agent.py"
fi

# ============================================================
# Manifest
# ============================================================

GIT_SHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
GIT_DIRTY=$(git -C "$REPO_ROOT" diff --quiet && echo "false" || echo "true")
GIT_BRANCH=$(git -C "$REPO_ROOT" branch --show-current)

RUN_ID="$(date -u +%Y-%m-%dT%H%M%SZ)_${JOB_NAME}_${GIT_SHA}"

LOCAL_MANIFEST="$(mktemp)"
trap 'rm -f "$LOCAL_MANIFEST"' EXIT

# Build JSON array of ak_args
AK_JSON="[]"
if [[ ${#AK_ARGS[@]} -gt 0 ]]; then
    AK_JSON="["
    for i in "${!AK_ARGS[@]}"; do
        [[ "$i" -gt 0 ]] && AK_JSON+=", "
        AK_JSON+="\"${AK_ARGS[$i]}\""
    done
    AK_JSON+="]"
fi

cat > "$LOCAL_MANIFEST" <<MANIFEST
{
  "run_id": "$RUN_ID",
  "job_name": "$JOB_NAME",
  "git_sha": "$GIT_SHA",
  "git_dirty": $GIT_DIRTY,
  "git_branch": "$GIT_BRANCH",
  "model": "$MODEL",
  "adapter": "$ADAPTER",
  "task_name": "${TASK_NAME:-all}",
  "reps": $REPS,
  "concurrency": $CONCURRENCY,
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "ak_args": $AK_JSON
}
MANIFEST

echo "=== Manifest ==="
cat "$LOCAL_MANIFEST"

# ============================================================
# Force kill (if --force)
# ============================================================

if [[ "$FORCE" -eq 1 && "$DRY_RUN" -eq 0 ]]; then
    echo "=== Force: killing existing job ==="
    ssh "$REMOTE" "pkill -f 'job-name $JOB_NAME' 2>/dev/null || true; rm -rf /tmp/$JOB_NAME" || true
fi

# ============================================================
# Build harbor command
# ============================================================

HARBOR_CMD="harbor run"
HARBOR_CMD+=" --agent-import-path \"$ADAPTER\""
HARBOR_CMD+=" --dataset \"terminal-bench@2.0\""
if [[ -n "$TASK_NAME" ]]; then
    HARBOR_CMD+=" --task-name \"$TASK_NAME\""
fi
HARBOR_CMD+=" --model \"$MODEL\""
HARBOR_CMD+=" -k $REPS"
HARBOR_CMD+=" -n $CONCURRENCY"
HARBOR_CMD+=" --job-name \"$JOB_NAME\""
HARBOR_CMD+=" --jobs-dir \"/tmp/$JOB_NAME\""
for ak in "${AK_ARGS[@]+"${AK_ARGS[@]}"}"; do
    HARBOR_CMD+=" --ak $ak"
done

# ============================================================
# Dry run (early exit)
# ============================================================

if [[ "$DRY_RUN" -eq 1 ]]; then
    echo ""
    echo "=== Dry run: would execute ==="
    echo "  $HARBOR_CMD"
    echo ""
    echo "  Run ID: $RUN_ID"
    echo "  Model:  $MODEL"
    echo "  Task:   ${TASK_NAME:-all}"
    echo "  Reps:   $REPS"
    echo "  Conc:   $CONCURRENCY"
    exit 0
fi

# ============================================================
# Launch
# ============================================================

echo ""
echo "=== Launching: ${TASK_NAME:-FULL SUITE} x$REPS (job: $JOB_NAME) ==="
ssh "$REMOTE" bash <<REMOTE_SCRIPT
cd $REMOTE_DIR
set -a; source .env; set +a
export PATH="\$HOME/.local/bin:\$PATH"
rm -rf /tmp/$JOB_NAME
mkdir -p /tmp/$JOB_NAME

nohup $HARBOR_CMD > /tmp/$JOB_NAME.log 2>&1 &
PID=\$!
echo "PID: \$PID"
sleep 3
if kill -0 \$PID 2>/dev/null; then
    echo "Job running."
    tail -5 /tmp/$JOB_NAME.log 2>/dev/null || true
else
    echo "ERROR: Job exited immediately!"
    cat /tmp/$JOB_NAME.log
    exit 1
fi
REMOTE_SCRIPT

# Upload manifest to remote job directory
scp -q "$LOCAL_MANIFEST" "$REMOTE:/tmp/$JOB_NAME/manifest.json" 2>/dev/null || true

# ============================================================
# Post-launch output
# ============================================================

echo ""
echo "=== Job launched ==="
echo "  Job:    $JOB_NAME"
echo "  Run ID: $RUN_ID"
echo "  Model:  $MODEL"
echo ""
echo "=== Monitor ==="
echo "  $0 --job $JOB_NAME --status"
echo ""
echo "=== Collect when done ==="
echo "  $0 --job $JOB_NAME --collect-only"
echo ""
echo "=== Tail log ==="
echo "  ssh $REMOTE 'tail -f /tmp/$JOB_NAME.log'"
