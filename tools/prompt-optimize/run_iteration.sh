#!/bin/bash
# Run one iteration of prompt optimization for Qwen 3.5 Flash.
#
# Usage: ./run_iteration.sh <iteration_number> <persona_file>
#
# Deploys the persona, runs 2 tasks on magic-kingdom, waits for results.
# Results are stored locally in tools/prompt-optimize/results/iter-N/

set -euo pipefail

ITER="${1:?Usage: $0 <iteration> <persona_file>}"
PERSONA_FILE="${2:?Usage: $0 <iteration> <persona_file>}"
REMOTE="jesse@magic-kingdom"
STAGING="/home/jesse/git/terminal-bench/runs/lace-qwen-chainfix"
JOBS_DIR="/data/agent-evals/runs"
JOB_NAME="qwen-opt-iter${ITER}"
MODEL="openrouter/qwen/qwen3.5-flash-02-23"
TASKS="openssl-selfsigned-cert count-dataset-tokens"
RESULTS_DIR="$(dirname "$0")/results/iter-${ITER}"

mkdir -p "$RESULTS_DIR"

# Save the persona used for this iteration
cp "$PERSONA_FILE" "$RESULTS_DIR/persona.md"

echo "=== Iteration $ITER ==="
echo "  Persona: $PERSONA_FILE"
echo "  Job: $JOB_NAME"
echo "  Tasks: $TASKS"

# 1. Deploy persona to staging bundle (for reference) and also modify install
#    template to copy it to the state dir where PromptManager will find it.
echo "--- Deploying persona ---"
scp "$PERSONA_FILE" "$REMOTE:$STAGING/lace/packages/agent/config/agent-personas/benchmark-opt.md"

# The PromptManager looks for personas in $LACE_DIR/agent-personas/ (=/logs/agent/agent-state/agent-personas/)
# NOT in the bundle config dir. We need the install script to copy persona files there.
ssh "$REMOTE" "grep -q 'agent-personas' $STAGING/install-lace.sh.j2 || cat >> $STAGING/install-lace.sh.j2 << 'PATCH'

# Copy all personas from bundle config to state dir so PromptManager can find them
mkdir -p /logs/agent/agent-state/agent-personas
cp /opt/lace/packages/agent/config/agent-personas/*.md /logs/agent/agent-state/agent-personas/ 2>/dev/null || true
PATCH"

# 2. Kill any previous iteration's job
ssh "$REMOTE" "pkill -f 'job-name $JOB_NAME' 2>/dev/null; rm -rf $JOBS_DIR/$JOB_NAME 2>/dev/null; true"

# 3. Launch harbor
echo "--- Launching harbor ---"
TASK_ARGS=""
for t in $TASKS; do
    TASK_ARGS="$TASK_ARGS --task-name $t"
done

ssh "$REMOTE" bash -s <<REMOTE_SCRIPT
set -euo pipefail
cd $STAGING
set -a; source .env; set +a
export PATH="\$HOME/.local/bin:\$PATH"
mkdir -p $JOBS_DIR/$JOB_NAME

nohup harbor run \
  --agent-import-path lace_agent:LaceAgent \
  --dataset "terminal-bench@2.0" \
  $TASK_ARGS \
  --model $MODEL \
  -k 1 -n 10 \
  --job-name $JOB_NAME \
  --jobs-dir $JOBS_DIR \
  --no-delete \
  --ak persona=benchmark-opt \
  > /tmp/$JOB_NAME.log 2>&1 &

PID=\$!
echo "PID: \$PID"
sleep 3
if kill -0 \$PID 2>/dev/null; then
    echo "Job running."
else
    echo "ERROR: Job exited immediately!"
    cat /tmp/$JOB_NAME.log
    exit 1
fi
REMOTE_SCRIPT

# 4. Poll for completion (up to 20 min)
echo "--- Waiting for completion ---"
for i in $(seq 1 40); do
    sleep 30
    # Count completed tasks
    DONE=$(ssh "$REMOTE" "find $JOBS_DIR/$JOB_NAME -name 'reward.txt' 2>/dev/null | wc -l" 2>/dev/null || echo 0)
    RUNNING=$(ssh "$REMOTE" "docker ps --format '{{.Names}}' 2>/dev/null | grep -c '__' || true" 2>/dev/null || echo "?")
    echo "  [$(date +%H:%M:%S)] $DONE/2 tasks done, $RUNNING containers running"
    if [ "$DONE" -ge 2 ]; then
        echo "  All tasks complete!"
        break
    fi
    # Check if harbor process is still alive
    ALIVE=$(ssh "$REMOTE" "pgrep -f 'job-name $JOB_NAME' >/dev/null 2>&1 && echo yes || echo no" 2>/dev/null || echo "?")
    if [ "$ALIVE" = "no" ] && [ "$DONE" -lt 2 ]; then
        # Harbor may have finished even if not all rewards are written yet
        sleep 10
        DONE2=$(ssh "$REMOTE" "find $JOBS_DIR/$JOB_NAME -name 'reward.txt' 2>/dev/null | wc -l" 2>/dev/null || echo 0)
        if [ "$DONE2" -ge 2 ]; then
            echo "  All tasks complete!"
            break
        fi
        echo "  WARNING: Harbor exited with only $DONE2/2 tasks done"
        break
    fi
done

# 5. Collect results
echo "--- Collecting results ---"
for task in $TASKS; do
    TASK_DIR=$(ssh "$REMOTE" "ls -d $JOBS_DIR/$JOB_NAME/${task}__* 2>/dev/null | head -1")
    if [ -z "$TASK_DIR" ]; then
        echo "  $task: NO RESULTS"
        echo "no_results" > "$RESULTS_DIR/${task}.reward"
        continue
    fi

    REWARD=$(ssh "$REMOTE" "cat $TASK_DIR/verifier/reward.txt $TASK_DIR/reward.txt 2>/dev/null | head -1")
    echo "  $task: reward=$REWARD"
    echo "$REWARD" > "$RESULTS_DIR/${task}.reward"

    # Download trajectory
    scp -q "$REMOTE:$TASK_DIR/agent/trajectory.json" "$RESULTS_DIR/${task}.trajectory.json" 2>/dev/null || true

    # Download verifier output
    ssh "$REMOTE" "cat $TASK_DIR/verifier/stdout.txt 2>/dev/null" > "$RESULTS_DIR/${task}.verifier.txt" 2>/dev/null || true

    # Download agent artifacts list
    ssh "$REMOTE" "ls -la $TASK_DIR/agent/artifacts/ 2>/dev/null" > "$RESULTS_DIR/${task}.artifacts.txt" 2>/dev/null || true
done

# 6. Print summary
echo ""
echo "=== Iteration $ITER Results ==="
for task in $TASKS; do
    REWARD=$(cat "$RESULTS_DIR/${task}.reward" 2>/dev/null || echo "?")
    if [ "$REWARD" = "1.0" ] || [ "$REWARD" = "1" ]; then
        echo "  $task: PASS"
    else
        echo "  $task: FAIL ($REWARD)"
    fi
done
echo ""
echo "Results saved to: $RESULTS_DIR"
