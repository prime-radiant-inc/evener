#!/bin/bash
# Like-for-like evaluation: Codex CLI with gpt-5.2 × 10 tasks
set -uo pipefail

BASE="/tmp/serfeval-v2"
RESULTS="$BASE/results/gpt52-codex"
mkdir -p "$RESULTS"

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
total=${#TASKS[@]}

echo "=== CODEX GPT-5.2 BASELINE ==="
echo "Model: gpt-5.2"
echo "Tasks: $total"
echo "Started: $(date)"
echo ""

for task_id in "${TASKS[@]}"; do
  REPO_DIR="$BASE/repos/$task_id"
  TASK_FILE="$BASE/tasks/${task_id}.txt"
  OUTJSON="$RESULTS/codex_${task_id}.json"
  OUTJSONL="$RESULTS/codex_${task_id}.jsonl"

  if [ -f "$OUTJSON" ]; then
    echo "SKIP: $task_id ($(( completed + skipped + failed + 1 ))/$total)"
    skipped=$((skipped + 1))
    continue
  fi

  # Reset repo
  (cd "$REPO_DIR" && git checkout -- . && git clean -fd) 2>/dev/null || true

  echo ""
  echo "=== [$(( completed + skipped + failed + 1 ))/$total] codex × $task_id ==="
  echo "  Started: $(date '+%H:%M:%S')"

  START_TIME=$(date +%s)

  # Run Codex with gpt-5.2 and JSON output
  codex exec \
    -m gpt-5.2 \
    -C "$REPO_DIR" \
    --dangerously-bypass-approvals-and-sandbox \
    --json \
    "$(cat "$TASK_FILE")" \
    > "$OUTJSONL" 2>"$RESULTS/codex_${task_id}.stderr"

  CODEX_EXIT=$?
  END_TIME=$(date +%s)
  DURATION=$(( END_TIME - START_TIME ))

  # Capture the diff to a temp file
  DIFF_TMP=$(mktemp)
  (cd "$REPO_DIR" && git add -N . 2>/dev/null; git diff > "$DIFF_TMP" 2>/dev/null; git reset -q 2>/dev/null)

  # Parse the JSONL into our standard result format
  python3 "$BASE/parse-codex-jsonl.py" \
    "$OUTJSONL" \
    "$TASK_FILE" \
    "$DURATION" \
    "$OUTJSON" \
    gpt-5.2 \
    "$DIFF_TMP"

  rm -f "$DIFF_TMP"

  if [ $CODEX_EXIT -eq 0 ]; then
    tokens=$(python3 -c "import json; d=json.load(open('$OUTJSON')); print(f'  tokens={d[\"total_tokens\"]} turns={d[\"turn_count\"]} duration=${DURATION}s completed={d[\"completed\"]}')")
    echo "$tokens"
    echo "  OK ($(( completed + 1 )) completed, $failed failed)"
    completed=$((completed + 1))
  else
    echo "  FAIL (exit $CODEX_EXIT, duration=${DURATION}s)"
    failed=$((failed + 1))
  fi
done

echo ""
echo "=== CODEX GPT-5.2 BASELINE COMPLETE ==="
echo "Completed: $completed"
echo "Skipped:   $skipped"
echo "Failed:    $failed"
echo "Finished:  $(date)"
echo "Results:   $RESULTS"
