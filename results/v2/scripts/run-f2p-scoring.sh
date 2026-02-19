#!/bin/bash
# F2P Scoring: Run 30 evaluations (3 strategies × 10 tasks) with diff capture
# The rebuilt serfeval now saves git diff in the result JSON
set -euo pipefail

export $(grep OPENAI_API_KEY /Users/jesse/prime-radiant/serf/.env)

SERFEVAL="/Users/jesse/prime-radiant/serf/.worktrees/rlm-context/cmd/serfeval/serfeval"
BASE="/tmp/serfeval-v2"
RESULTS="$BASE/results/f2p"
mkdir -p "$RESULTS"

STRATEGIES=(compact recursive-distill memory-crystals)
TASKS=(
  django__django-11276
  astropy__astropy-13977
  pylint-dev__pylint-4604
  pydata__xarray-6992
  sympy__sympy-13091
  pytest-dev__pytest-5787
  django__django-11138
  scikit-learn__scikit-learn-25102
  sphinx-doc__sphinx-11510
  astropy__astropy-13398
)

completed=0
skipped=0
failed=0

for task_id in "${TASKS[@]}"; do
  repo_dir="$BASE/repos/${task_id}"
  task_prompt=$(cat "$BASE/tasks/${task_id}.txt")
  probes_file="$BASE/probes/${task_id}.json"

  for strategy in "${STRATEGIES[@]}"; do
    outfile="$RESULTS/${strategy}_${task_id}.json"
    if [ -f "$outfile" ]; then
      echo "SKIP: ${strategy}/${task_id}"
      skipped=$((skipped + 1))
      continue
    fi

    # Reset repo to clean state
    (cd "$repo_dir" && git checkout -- . && git clean -fd) 2>/dev/null || true

    echo "START: ${strategy}/${task_id} ($(date '+%H:%M:%S'))"
    if "$SERFEVAL" \
      --provider openai \
      --model gpt-4.1-mini \
      --strategy "$strategy" \
      --task "$task_prompt" \
      --dir "$repo_dir" \
      --output "$outfile" \
      --probes "$probes_file" \
      --max-turns 30 \
      2>"$RESULTS/${strategy}_${task_id}.log"; then
      echo "DONE: ${strategy}/${task_id} ($(date '+%H:%M:%S'))"
      completed=$((completed + 1))
    else
      echo "FAIL: ${strategy}/${task_id} (exit $?, $(date '+%H:%M:%S'))"
      failed=$((failed + 1))
    fi
  done
done

echo ""
echo "=== F2P Scoring Summary ==="
echo "Completed: $completed"
echo "Skipped:   $skipped"
echo "Failed:    $failed"
echo "Total results: $(ls "$RESULTS"/*.json 2>/dev/null | wc -l)"
