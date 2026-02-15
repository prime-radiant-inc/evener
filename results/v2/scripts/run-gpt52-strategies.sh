#!/bin/bash
# Like-for-like evaluation: 3 strategies × 10 tasks, all on gpt-5.2
set -uo pipefail

export $(grep OPENAI_API_KEY /Users/jesse/prime-radiant/serf/.env)

SERFEVAL="/Users/jesse/prime-radiant/serf/.worktrees/rlm-context/cmd/serfeval/serfeval"
BASE="/tmp/serfeval-v2"
RESULTS="$BASE/results/gpt52"
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
total=$((${#STRATEGIES[@]} * ${#TASKS[@]}))

echo "=== GPT-5.2 LIKE-FOR-LIKE EVALUATION ==="
echo "Strategies: ${STRATEGIES[*]}"
echo "Tasks: ${#TASKS[@]}"
echo "Total runs: $total"
echo "Started: $(date)"
echo ""

for task_id in "${TASKS[@]}"; do
  repo_dir="$BASE/repos/${task_id}"
  task_prompt=$(cat "$BASE/tasks/${task_id}.txt")
  probes_file="$BASE/probes/${task_id}.json"

  for strategy in "${STRATEGIES[@]}"; do
    outfile="$RESULTS/${strategy}_${task_id}.json"
    logfile="$RESULTS/${strategy}_${task_id}.log"
    if [ -f "$outfile" ]; then
      echo "SKIP: ${strategy}/${task_id} ($(( completed + skipped + failed + 1 ))/$total)"
      skipped=$((skipped + 1))
      continue
    fi

    # Reset repo to clean state
    (cd "$repo_dir" && git checkout -- . && git clean -fd) 2>/dev/null || true

    echo ""
    echo "=== [$(( completed + skipped + failed + 1 ))/$total] ${strategy} × ${task_id} ==="
    echo "  Started: $(date '+%H:%M:%S')"

    if "$SERFEVAL" \
      --provider openai \
      --model gpt-5.2 \
      --strategy "$strategy" \
      --task "$task_prompt" \
      --dir "$repo_dir" \
      --output "$outfile" \
      --probes "$probes_file" \
      --max-turns 30 \
      2>"$logfile"; then

      # Extract key metrics from result
      tokens=$(python3 -c "import json; d=json.load(open('$outfile')); print(f\"  tokens={d['total_tokens']} turns={d['turn_count']} duration={d['duration_seconds']:.0f}s completed={d['completed']}\")")
      echo "$tokens"
      echo "  OK ($(( completed + 1 )) completed, $failed failed)"
      completed=$((completed + 1))
    else
      echo "  FAIL (exit $?, $(date '+%H:%M:%S'))"
      failed=$((failed + 1))
    fi
  done
done

echo ""
echo "=== GPT-5.2 STRATEGY EVALUATION COMPLETE ==="
echo "Completed: $completed"
echo "Skipped:   $skipped"
echo "Failed:    $failed"
echo "Finished:  $(date)"
echo "Results:   $RESULTS"
echo "Result files: $(ls "$RESULTS"/*.json 2>/dev/null | wc -l)"
