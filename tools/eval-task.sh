#!/usr/bin/env bash
# Deploy serf to flower-garden and run a focused benchmark eval.
#
# Usage:
#   ./tools/eval-task.sh <job-name> <task-name> [reps] [extra-ak...]
#
# Examples:
#   ./tools/eval-task.sh reviewer-v3 build-cython-ext 3 enable_reviewer_gate=true
#   ./tools/eval-task.sh baseline fix-code-vulnerability 5
#
# Requires: GOOS/GOARCH for cross-compile, SSH access to flower-garden.
set -euo pipefail

REMOTE=jesse@192.168.118.101
REMOTE_DIR=~/git/terminal-bench
REMOTE_BIN=$REMOTE_DIR/serf-linux-amd64

JOB_NAME="${1:?Usage: eval-task.sh <job-name> <task-name> [reps] [extra-ak...]}"
TASK_NAME="${2:?Usage: eval-task.sh <job-name> <task-name> [reps] [extra-ak...]}"
REPS="${3:-3}"
shift 3 || shift $#

# Build extra --ak flags
AK_FLAGS=""
for arg in "$@"; do
    AK_FLAGS="$AK_FLAGS --ak $arg"
done

echo "=== Building linux binary ==="
GOOS=linux GOARCH=amd64 go build -o /tmp/serf-linux-amd64 ./cmd/serf/

echo "=== Deploying to flower-garden ==="
scp /tmp/serf-linux-amd64 "$REMOTE:$REMOTE_BIN"

echo "=== Launching: $TASK_NAME x$REPS (job: $JOB_NAME) ==="
ssh "$REMOTE" bash <<REMOTE
cd $REMOTE_DIR
set -a; source .env; set +a
export PATH="\$HOME/.local/bin:\$PATH"
rm -rf /tmp/$JOB_NAME

nohup harbor run \\
  --agent-import-path "serf_agent:SerfAgent" \\
  --dataset "terminal-bench@2.0" \\
  --task-name "$TASK_NAME" \\
  -k $REPS \\
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

echo ""
echo "=== Monitor with ==="
echo "ssh $REMOTE 'tail -f /tmp/$JOB_NAME.log'"
echo ""
echo "=== Check results with ==="
echo "ssh $REMOTE 'for d in /tmp/$JOB_NAME/*/; do r=\$(cat \"\$d/reward.txt\" 2>/dev/null || echo \"?\"); echo \"\$(basename \$d): \$r\"; done'"
