#!/usr/bin/env bash
# Check results for a harbor-runner eval run.
# Usage: ./tools/eval/check_run.sh RUN_ID
#
# Auto-discovers tasks from downloaded results.
# Downloads results from S3 if not already local.

set -euo pipefail

HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"
RESULTS_DIR="$HARBOR_DIR/state/results"

RUN_ID="${1:?Usage: check_run.sh RUN_ID}"

# Download if not already present or no reward files found locally
has_rewards=$(find "$RESULTS_DIR/$RUN_ID" -name "reward.txt" -path "*/verifier/*" 2>/dev/null | head -1)
if [ ! -d "$RESULTS_DIR/$RUN_ID" ] || [ -z "$has_rewards" ]; then
    echo "Downloading results for $RUN_ID..."
    cd "$HARBOR_DIR" && ./results.sh --run-id "$RUN_ID" 2>&1 | tail -5
fi

RUN_DIR="$RESULTS_DIR/$RUN_ID"

pass=0
fail=0
pending=0

for rep_dir in "$RUN_DIR"/rep-*; do
    [ -d "$rep_dir" ] || continue
    rep=$(basename "$rep_dir")

    # Find reward.txt anywhere under this rep
    reward_file=$(find "$rep_dir" -name "reward.txt" -path "*/verifier/*" 2>/dev/null | head -1)

    if [ -z "$reward_file" ]; then
        printf "%-8s %-45s %s\n" "$rep" "?" "PENDING"
        pending=$((pending+1))
        continue
    fi

    # Extract task name from path: .../<task>__<hash>/verifier/reward.txt
    task_dir=$(dirname "$(dirname "$reward_file")")
    task_base=$(basename "$task_dir")
    task_name=$(echo "$task_base" | sed 's/__[A-Za-z0-9]*$//')

    reward=$(cat "$reward_file")
    if [ "$reward" = "1" ]; then
        status="PASS"
        pass=$((pass+1))
    elif [ "$reward" = "0" ]; then
        status="FAIL"
        fail=$((fail+1))
    else
        status="?"
        pending=$((pending+1))
    fi
    printf "%-8s %-45s %s\n" "$rep" "$task_name" "$status"
done

total=$((pass+fail))
echo "---"
echo "$pass/$total passed$([ $pending -gt 0 ] && echo ", $pending pending")"
