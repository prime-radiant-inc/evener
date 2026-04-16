#!/usr/bin/env bash
# Deploy serf to flower-garden and run a focused benchmark eval.
#
# Usage:
#   ./tools/eval-task.sh <job-name> [task-name] [reps] [extra-ak...]
#
# Environment variables:
#   AGENT_IMPORT_PATH  Custom adapter (default: serf_agent:SerfAgent)
#   CONCURRENCY        Parallel tasks (default: 2)
#   NO_BUILD           Set to 1 to skip build+deploy (use existing binary)
#   MODEL              Model to use (default: openai/gpt-5.2-codex)
#
# If task-name is omitted or empty, runs the full suite (all tasks).
#
# Examples:
#   ./tools/eval-task.sh baseline-v2 build-cython-ext 3 max_rounds=100
#   ./tools/eval-task.sh baseline fix-code-vulnerability 5
#   ./tools/eval-task.sh full-run "" 1 max_rounds=100               # all tasks
#   ./tools/eval-task.sh full-run                                    # all tasks, 3 reps
#
#   # Custom adapter (skip build since binary is already deployed):
#   NO_BUILD=1 AGENT_IMPORT_PATH="my_adapter:MyAgent" \
#     ./tools/eval-task.sh my-test configure-git-webserver 1
#
# Requires: GOOS/GOARCH for cross-compile, SSH access to flower-garden.
set -euo pipefail

REMOTE=jesse@192.168.118.101
REMOTE_DIR=git/terminal-bench
REMOTE_BIN=$REMOTE_DIR/serf-linux-amd64
AGENT_IMPORT_PATH="${AGENT_IMPORT_PATH:-serf_agent:SerfAgent}"
MODEL="${MODEL:-}"

JOB_NAME="${1:?Usage: eval-task.sh <job-name> [task-name] [reps] [extra-ak...]}"
TASK_NAME="${2:-}"
REPS="${3:-3}"
shift 3 2>/dev/null || shift $#

# Build extra --ak flags
AK_FLAGS=""
for arg in "$@"; do
    AK_FLAGS="$AK_FLAGS --ak $arg"
done

# Build --task-name flag (omit for full suite)
TASK_FLAG=""
if [ -n "$TASK_NAME" ]; then
    TASK_FLAG="--task-name \"$TASK_NAME\""
    LABEL="$TASK_NAME x$REPS"
else
    LABEL="FULL SUITE x$REPS"
fi

# Build --model flag (omit to use adapter default)
MODEL_FLAG=""
if [ -n "$MODEL" ]; then
    MODEL_FLAG="--model \"$MODEL\""
fi

if [ "${NO_BUILD:-0}" != "1" ]; then
    echo "=== Building linux binary ==="
    LDFLAGS="-X primeradiant.com/serf/buildinfo.GitSHA=$(git rev-parse --short HEAD) \
             -X primeradiant.com/serf/buildinfo.GitDirty=$(git diff --quiet && echo '' || echo 'true') \
             -X primeradiant.com/serf/buildinfo.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/serf-linux-amd64 ./cmd/serf/

    echo "=== Deploying to flower-garden ==="
    scp /tmp/serf-linux-amd64 "$REMOTE:$REMOTE_BIN"
else
    echo "=== Skipping build (NO_BUILD=1) ==="
fi

echo "=== Writing manifest ==="
GIT_SHA=$(git rev-parse --short HEAD)
GIT_DIRTY=$(git diff --quiet && echo "false" || echo "true")
GIT_BRANCH=$(git branch --show-current)

MANIFEST_DIR="/tmp/${JOB_NAME}-manifest"
mkdir -p "$MANIFEST_DIR"
cat > "$MANIFEST_DIR/manifest.json" <<MANIFEST
{
  "job_name": "$JOB_NAME",
  "git_sha": "$GIT_SHA",
  "git_dirty": $GIT_DIRTY,
  "git_branch": "$GIT_BRANCH",
  "model": "${MODEL:-}",
  "adapter": "$AGENT_IMPORT_PATH",
  "task_name": "${TASK_NAME:-all}",
  "reps": $REPS,
  "concurrency": ${CONCURRENCY:-2},
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
MANIFEST
cat "$MANIFEST_DIR/manifest.json"

echo "=== Launching: $LABEL (job: $JOB_NAME, adapter: $AGENT_IMPORT_PATH) ==="
ssh "$REMOTE" bash <<REMOTE
cd $REMOTE_DIR
set -a; source .env; set +a
export PATH="\$HOME/.local/bin:\$PATH"
rm -rf /tmp/$JOB_NAME
mkdir -p /tmp/$JOB_NAME

nohup harbor run \\
  --agent-import-path "$AGENT_IMPORT_PATH" \\
  --dataset "terminal-bench@2.0" \\
  $TASK_FLAG \\
  $MODEL_FLAG \\
  -k $REPS \\
  -n ${CONCURRENCY:-2} \\
  --job-name "$JOB_NAME" \\
  --jobs-dir "/tmp/$JOB_NAME" \\
  $AK_FLAGS \\
  > /tmp/$JOB_NAME.log 2>&1 &

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
REMOTE

# Upload manifest to remote job directory.
scp -q "$MANIFEST_DIR/manifest.json" "$REMOTE:/tmp/$JOB_NAME/manifest.json" 2>/dev/null || true

echo ""
echo "=== Monitor with ==="
echo "./tools/check-eval.sh $JOB_NAME"
echo ""
echo "=== Or tail the log ==="
echo "ssh $REMOTE 'tail -f /tmp/$JOB_NAME.log'"
