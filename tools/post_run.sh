#!/usr/bin/env bash
# Collect results from a completed eval run and update the scoreboard.
#
# Usage:
#   ./tools/post_run.sh RUN_ID [--variant "description"]
#
# Does:
#   1. Checks that all reps have uploaded results
#   2. Runs collect_results.py to download, normalize, and update metadata
#   3. Shows the scoreboard diff (new scores vs previous)
#   4. Reminds you to commit the metadata update
#
# Prerequisites:
#   - .env with OPENAI_API_KEY (needed by collect_results.py for S3)
#   - Run must be complete (all instances terminated, results in S3)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

RUN_ID="${1:?Usage: post_run.sh RUN_ID [--variant \"description\"]}"
shift

VARIANT=""
MODEL="openai/gpt-5.4-mini"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --variant) VARIANT="$2"; shift 2 ;;
        --model)   MODEL="$2"; shift 2 ;;
        --help|-h) head -15 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)         echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# --- Load env ---
if [[ -f .env ]]; then
    set -a; source .env; set +a
fi

# --- Check status ---
echo "=== Checking run status ==="
HARBOR_DIR="${HARBOR_DIR:-$HOME/prime-radiant/harbor-runner}"
cd "$HARBOR_DIR"
STATUS=$("$HARBOR_DIR/status.sh" --run-id "$RUN_ID" 2>&1)
cd "$REPO_ROOT"

# Count reps with results
REPS_WITH_RESULTS=$(echo "$STATUS" | grep -c "trials uploaded" || true)
RUNNING=$(echo "$STATUS" | grep -c "running" || true)

echo "  Reps with results: $REPS_WITH_RESULTS"
if [[ "$RUNNING" -gt 0 ]]; then
    echo "  WARNING: $RUNNING instances still running. Results may be incomplete."
    echo "  Wait for completion or re-run later."
    read -p "  Continue anyway? [y/N] " -n 1 -r
    echo
    [[ $REPLY =~ ^[Yy]$ ]] || exit 1
fi

# --- Snapshot current scoreboard ---
BEFORE_SCORES=$(python3 -c "
import json
with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)
for task in sorted(sb['tasks']):
    info = sb['tasks'][task]
    score = info.get('score')
    print(f'{task}\t{score}')
" 2>/dev/null || echo "")

# --- Read launch metadata (saved by run_full_baseline.sh) ---
LAUNCH_META="$REPO_ROOT/.serf-launches/${RUN_ID}.json"
GIT_SHA=$(git rev-parse --short HEAD)

if [[ -f "$LAUNCH_META" ]]; then
    echo "  Found launch metadata: $LAUNCH_META"
    META_SHA=$(python3 -c "import json; print(json.load(open('$LAUNCH_META')).get('git_sha', ''))" 2>/dev/null || echo "")
    META_MODEL=$(python3 -c "import json; print(json.load(open('$LAUNCH_META')).get('model', ''))" 2>/dev/null || echo "")
    META_BRANCH=$(python3 -c "import json; print(json.load(open('$LAUNCH_META')).get('branch', ''))" 2>/dev/null || echo "")
    if [[ -n "$META_SHA" ]]; then GIT_SHA="$META_SHA"; fi
    if [[ -n "$META_MODEL" ]]; then MODEL="$META_MODEL"; fi
    if [[ -n "$META_BRANCH" ]]; then
        echo "  Branch: $META_BRANCH  SHA: $GIT_SHA  Model: $MODEL"
    fi
else
    # Fall back to existing run metadata
    RUN_META="docs/experiments/runs/${RUN_ID}.json"
    if [[ -f "$RUN_META" ]]; then
        META_SHA=$(python3 -c "import json; print(json.load(open('$RUN_META')).get('git_sha', ''))" 2>/dev/null || echo "")
        if [[ -n "$META_SHA" ]]; then GIT_SHA="$META_SHA"; fi
    fi
fi

# --- Collect ---
echo ""
echo "=== Collecting results ==="
COLLECT_ARGS=(
    "$RUN_ID"
    --model "$MODEL"
    --git-sha "$GIT_SHA"
)
if [[ -n "$VARIANT" ]]; then
    COLLECT_ARGS+=(--variant "$VARIANT")
fi

python3 tools/collect_results.py "${COLLECT_ARGS[@]}" 2>&1 | tail -20

# --- Show diff ---
echo ""
echo "=== Score changes ==="
python3 -c "
import json, sys

before = {}
for line in '''$BEFORE_SCORES'''.strip().split('\n'):
    if '\t' not in line:
        continue
    task, score = line.split('\t')
    before[task] = score

with open('docs/experiments/scoreboard.json') as f:
    sb = json.load(f)

improved = []
regressed = []
new_scores = []

for task in sorted(sb['tasks']):
    info = sb['tasks'][task]
    after = info.get('score')
    prev = before.get(task)

    if prev is None or prev == 'None':
        if after is not None:
            new_scores.append((task, after))
    elif after is not None:
        prev_f = float(prev)
        after_f = float(after)
        if after_f > prev_f:
            improved.append((task, prev_f, after_f))
        elif after_f < prev_f:
            regressed.append((task, prev_f, after_f))

if improved:
    print('Improved:')
    for task, p, a in improved:
        print(f'  {task}: {p:.3f} -> {a:.3f}')

if regressed:
    print('Regressed:')
    for task, p, a in regressed:
        print(f'  {task}: {p:.3f} -> {a:.3f}')

if new_scores:
    print('New scores:')
    for task, s in new_scores:
        print(f'  {task}: {s:.3f}')

if not improved and not regressed and not new_scores:
    print('No score changes.')
"

# --- Remind ---
echo ""
echo "=== Next steps ==="
echo "  git add docs/experiments/ && git commit -m 'results: $RUN_ID'"
echo "  # Interrogate failures if needed"
