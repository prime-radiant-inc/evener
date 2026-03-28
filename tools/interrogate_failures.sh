#!/usr/bin/env bash
# Auto-interrogate all failures in a completed eval run.
#
# Usage:
#   ./tools/interrogate_failures.sh RUN_ID [--question "custom question"]
#
# For each failing rep, runs interrogate_session.py with standard questions
# against the coordinator. Outputs a summary of all interrogation results.
#
# Prerequisites:
#   - .env with OPENAI_API_KEY
#   - Results already collected (run post_run.sh first)
#   - serf binary built (uses local serf for resume)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

RUN_ID="${1:?Usage: interrogate_failures.sh RUN_ID [--question \"...\"]}"
shift

CUSTOM_Q=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --question) CUSTOM_Q="$2"; shift 2 ;;
        --help|-h)  head -13 "$0" | grep '^#' | sed 's/^# \?//'; exit 0 ;;
        *)          echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# Load env
if [[ -f .env ]]; then
    set -a; source .env; set +a
fi

# Build serf for local resume (macOS)
echo "Building serf for interrogation..."
go build -o /tmp/serf-interrogate ./cmd/serf/ 2>&1 | tail -2

CACHE_DIR="$HOME/.serf-evals/tasks"
OUTPUT_DIR="/tmp/interrogation-${RUN_ID}"
mkdir -p "$OUTPUT_DIR"

echo "=== Scanning for failures in $RUN_ID ==="

# Find all failing reps from the run metadata
RUN_META="$REPO_ROOT/docs/experiments/runs/${RUN_ID}.json"
if [[ ! -f "$RUN_META" ]]; then
    echo "Error: no run metadata at $RUN_META. Run post_run.sh first." >&2
    exit 1
fi

FAILURES=$(python3 -c "
import json
with open('$RUN_META') as f:
    meta = json.load(f)
for task, info in sorted(meta.get('tasks', {}).items()):
    reps = info.get('reps', {})
    for rep_num, reward in sorted(reps.items()):
        if reward == 0:
            print(f'{task}\t{rep_num}')
")

if [[ -z "$FAILURES" ]]; then
    echo "No failures found in $RUN_ID."
    exit 0
fi

FAIL_COUNT=$(echo "$FAILURES" | wc -l | tr -d ' ')
echo "  $FAIL_COUNT failing reps to interrogate"
echo ""

# Standard questions per agent type
COORD_Q1="What was your plan? Walk me through your decision process step by step."
COORD_Q2="Your prompt says you must spawn an implementer. Did you? If not, why not?"
COORD_Q3="What specific changes to your instructions would have made you produce the correct output?"

SUB_Q1="Walk me through your approach. What did you try and why?"
SUB_Q2="Did you verify your answer was correct? How?"
SUB_Q3="What specific changes to your instructions would have made you produce the correct output?"

# Interrogate each failure — coordinator AND subagents
while IFS=$'\t' read -r task rep; do
    echo "=== $task rep $rep ==="

    # List all sessions for this rep
    SESSIONS_OUTPUT=$(python3 tools/interrogate_session.py \
        --run "$RUN_ID" --rep "$rep" --task "$task" --list-sessions 2>&1) || true
    echo "$SESSIONS_OUTPUT"
    echo ""

    # Interrogate coordinator (session 1, the default)
    echo "--- coordinator ---"
    OUTFILE="$OUTPUT_DIR/${task}_rep${rep}_coordinator.txt"

    QUESTIONS=("$COORD_Q1" "$COORD_Q2" "$COORD_Q3")
    if [[ -n "$CUSTOM_Q" ]]; then
        QUESTIONS+=("$CUSTOM_Q")
    fi

    Q_ARGS=()
    for q in "${QUESTIONS[@]}"; do
        Q_ARGS+=(--question "$q")
    done

    python3 tools/interrogate_session.py \
        --run "$RUN_ID" --rep "$rep" --task "$task" \
        "${Q_ARGS[@]}" \
        2>&1 | tee "$OUTFILE" | tail -5
    echo ""

    # Interrogate each subagent (sessions 2+)
    SESSION_COUNT=$(echo "$SESSIONS_OUTPUT" | grep -c "^\s*[0-9]" || true)
    if [[ "$SESSION_COUNT" -gt 1 ]]; then
        for idx in $(seq 2 "$SESSION_COUNT"); do
            ROLE=$(echo "$SESSIONS_OUTPUT" | awk "NR==$((idx+1)){print}" | grep -o 'role=[^ ]*' || echo "subagent")
            echo "--- session $idx ($ROLE) ---"
            OUTFILE="$OUTPUT_DIR/${task}_rep${rep}_session${idx}.txt"

            Q_ARGS=()
            for q in "$SUB_Q1" "$SUB_Q2" "$SUB_Q3"; do
                Q_ARGS+=(--question "$q")
            done
            if [[ -n "$CUSTOM_Q" ]]; then
                Q_ARGS+=(--question "$CUSTOM_Q")
            fi

            python3 tools/interrogate_session.py \
                --run "$RUN_ID" --rep "$rep" --task "$task" \
                --session "$idx" \
                "${Q_ARGS[@]}" \
                2>&1 | tee "$OUTFILE" | tail -5
            echo ""
        done
    fi

    echo ""
done <<< "$FAILURES"

echo "=== Interrogation complete ==="
echo "Full outputs saved to: $OUTPUT_DIR/"
echo "  Coordinator files: *_coordinator.txt"
echo "  Subagent files:    *_session*.txt"
