#!/bin/bash
# Codex baseline evaluation: 10 tasks × N replicates
# Uses Codex CLI with default model (gpt-5.3-codex) and default context management
set -euo pipefail

RESULTS_DIR="/tmp/serfeval-v2/results/codex"
REPOS_DIR="/tmp/serfeval-v2/repos"
TASKS_DIR="/tmp/serfeval-v2/tasks"
PARSER="/tmp/serfeval-v2/parse-codex-jsonl.py"
MAX_REPLICATE="${1:-1}"

mkdir -p "$RESULTS_DIR"

TASKS=(
    "django__django-11276"
    "astropy__astropy-13977"
    "pylint-dev__pylint-4604"
    "pydata__xarray-6992"
    "sympy__sympy-13091"
    "pytest-dev__pytest-5787"
    "django__django-11138"
    "scikit-learn__scikit-learn-25102"
    "sphinx-doc__sphinx-11510"
    "astropy__astropy-13398"
)

BASE_COMMITS=(
    "28d5262fa3315690395f04e3619ed554dbaf725b"
    "5250b2442501e6c671c6b380536f1edb352602d1"
    "1e55ae64624d28c5fe8b63ad7979880ee2e6ef3f"
    "45c0a114e2b7b27b83c9618bc05b36afac82183c"
    "d1320814eda6549996190618a21eaf212cfd4d1e"
    "955e54221008aba577ecbaefa15679f6777d3bf8"
    "c84b91b7603e488f7171fdff8f08368ef3d6b856"
    "f9a1cf072da9d7375d6c2163f68a6038b13b310f"
    "6cb783c0024a873722952a67ebb9f41771c8eb6d"
    "6500928dc0e57be8f06d1162eacc3ba5e2eff692"
)

COMPLETED=0
FAILED=0

for rep in $(seq 1 "$MAX_REPLICATE"); do
    for i in "${!TASKS[@]}"; do
        TASK="${TASKS[$i]}"
        BASE="${BASE_COMMITS[$i]}"
        REPO_DIR="$REPOS_DIR/$TASK"
        TASK_FILE="$TASKS_DIR/${TASK}.txt"

        if [ "$MAX_REPLICATE" -eq 1 ]; then
            RESULT_FILE="$RESULTS_DIR/codex_${TASK}.json"
            JSONL_FILE="$RESULTS_DIR/codex_${TASK}.jsonl"
            DIFF_FILE="$RESULTS_DIR/codex_${TASK}.diff"
        else
            RESULT_FILE="$RESULTS_DIR/codex_${TASK}_r${rep}.json"
            JSONL_FILE="$RESULTS_DIR/codex_${TASK}_r${rep}.jsonl"
            DIFF_FILE="$RESULTS_DIR/codex_${TASK}_r${rep}.diff"
        fi

        if [ -f "$RESULT_FILE" ]; then
            echo "SKIP: $RESULT_FILE already exists"
            COMPLETED=$((COMPLETED + 1))
            continue
        fi

        echo ""
        echo "=== [$((COMPLETED + FAILED + 1))/$(($MAX_REPLICATE * ${#TASKS[@]}))] codex × $TASK (rep $rep) ==="
        START_TIME=$(date +%s)

        # Reset repo to base commit
        cd "$REPO_DIR"
        git reset HEAD -- . 2>/dev/null || true
        git checkout -- . 2>/dev/null || true
        git clean -fd 2>/dev/null || true

        # Run Codex
        codex exec \
            -C "$REPO_DIR" \
            --dangerously-bypass-approvals-and-sandbox \
            --json \
            "$(cat "$TASK_FILE")" \
            > "$JSONL_FILE" 2>/dev/null || true

        END_TIME=$(date +%s)
        DURATION=$((END_TIME - START_TIME))

        # Capture git diff
        cd "$REPO_DIR"
        git add -N . 2>/dev/null || true
        git diff > "$DIFF_FILE" 2>/dev/null || true
        git reset -q 2>/dev/null || true

        # Parse JSONL and write result
        python3 "$PARSER" "$JSONL_FILE" "$TASK_FILE" "$DIFF_FILE" "$DURATION" "$RESULT_FILE"

        # Check if completed
        if python3 -c "import json; r=json.load(open('$RESULT_FILE')); exit(0 if r['completed'] else 1)" 2>/dev/null; then
            COMPLETED=$((COMPLETED + 1))
            echo "  OK ($COMPLETED completed, $FAILED failed)"
        else
            FAILED=$((FAILED + 1))
            echo "  FAILED ($COMPLETED completed, $FAILED failed)"
        fi

        # Clean up diff file to save disk
        rm -f "$DIFF_FILE"
    done
done

echo ""
echo "=== CODEX BASELINE COMPLETE ==="
echo "Completed: $COMPLETED  Failed: $FAILED"
echo "Results: $RESULTS_DIR"
ls "$RESULTS_DIR"/*.json 2>/dev/null | wc -l | xargs -I{} echo "Result files: {}"
