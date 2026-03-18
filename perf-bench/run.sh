#!/usr/bin/env bash
# Run a perf benchmark comparing old (pre-perf) and new (current) serf builds.
#
# Usage: ./perf-bench/run.sh [provider/model]
#   model defaults to openai/gpt-4.1 (fast, cheap, good enough for a todo app)
#
# Builds two binaries, runs each against the same task spec, captures:
#   - Wall clock time
#   - CPU profile (go pprof)
#   - Round timings from transcript
#
# Results go in perf-bench/results/{old,new}/

set -euo pipefail
cd "$(dirname "$0")/.."

# Load API keys
if [ -f .env ]; then
  set -a; source .env; set +a
fi

FULL_MODEL="${1:-openai/gpt-4.1}"
PROVIDER="${FULL_MODEL%%/*}"
MODEL="${FULL_MODEL#*/}"
TASK="$(pwd)/perf-bench/task.md"
RESULTS="$(pwd)/perf-bench/results"

echo "=== Perf Benchmark ==="
echo "Provider: $PROVIDER"
echo "Model:    $MODEL"
echo "Task:     $TASK"
echo ""

# --- Build old binary (pre-perf commits) ---
OLD_COMMIT="6f3d17e"
echo "Building OLD binary from $OLD_COMMIT (pre-perf)..."
if [ ! -d /tmp/serf-old ]; then
  git worktree add -q /tmp/serf-old "$OLD_COMMIT" 2>/dev/null || true
fi
(cd /tmp/serf-old && go build -o /tmp/serf-old-bin ./cmd/serf/) 2>&1
echo "  -> /tmp/serf-old-bin"

# --- Build new binary (current HEAD) ---
echo "Building NEW binary from HEAD ($(git rev-parse --short HEAD))..."
go build -o /tmp/serf-new-bin ./cmd/serf/ 2>&1
echo "  -> /tmp/serf-new-bin"

# --- Prepare results dirs ---
rm -rf "$RESULTS"
mkdir -p "$RESULTS/old/work" "$RESULTS/new/work"

# Copy task spec into both work dirs
cp "$TASK" "$RESULTS/old/work/task.md"
cp "$TASK" "$RESULTS/new/work/task.md"

TASK_TEXT="Read task.md and complete all the work described in it. When done, run the test suite and make sure all tests pass."

# --- Run OLD ---
echo ""
echo "=== Running OLD binary ==="
OLD_STATE="$RESULTS/old/state"
mkdir -p "$OLD_STATE"
OLD_START=$(python3 -c "import time; print(time.time())")
/tmp/serf-old-bin \
  --provider "$PROVIDER" \
  --model "$MODEL" \
  --dir "$RESULTS/old/work" \
  --state-dir "$OLD_STATE" \
  "$TASK_TEXT" \
  2>"$RESULTS/old/stderr.log" \
  >"$RESULTS/old/stdout.log" || true
OLD_END=$(python3 -c "import time; print(time.time())")
OLD_ELAPSED=$(python3 -c "print(f'{$OLD_END - $OLD_START:.1f}')")
echo "  Elapsed: ${OLD_ELAPSED}s"
echo "$OLD_ELAPSED" > "$RESULTS/old/elapsed.txt"

# --- Run NEW (with --cpu-profile and --trace) ---
echo ""
echo "=== Running NEW binary ==="
NEW_STATE="$RESULTS/new/state"
mkdir -p "$NEW_STATE"
NEW_START=$(python3 -c "import time; print(time.time())")
/tmp/serf-new-bin \
  --provider "$PROVIDER" \
  --model "$MODEL" \
  --dir "$RESULTS/new/work" \
  --state-dir "$NEW_STATE" \
  --cpu-profile "$RESULTS/new/cpu.prof" \
  --trace "$RESULTS/new/trace.out" \
  "$TASK_TEXT" \
  2>"$RESULTS/new/stderr.log" \
  >"$RESULTS/new/stdout.log" || true
NEW_END=$(python3 -c "import time; print(time.time())")
NEW_ELAPSED=$(python3 -c "print(f'{$NEW_END - $NEW_START:.1f}')")
echo "  Elapsed: ${NEW_ELAPSED}s"
echo "$NEW_ELAPSED" > "$RESULTS/new/elapsed.txt"

# --- Results ---
echo ""
echo "=== Results ==="
echo "OLD: ${OLD_ELAPSED}s"
echo "NEW: ${NEW_ELAPSED}s"
echo ""

# Check if tests passed
echo "=== Test Results ==="
for ver in old new; do
  WORK="$RESULTS/$ver/work"
  if [ -f "$WORK/test_todo.py" ]; then
    echo "$ver:"
    (cd "$WORK" && python3 -m pytest test_todo.py -v 2>&1 | tail -5) || echo "  TESTS NOT RUN"
    echo ""
  else
    echo "$ver: test_todo.py not created"
  fi
done

echo "=== Profiling (NEW only) ==="
echo "CPU profile: $RESULTS/new/cpu.prof"
echo "  Analyze: go tool pprof -http=:8080 /tmp/serf-new-bin $RESULTS/new/cpu.prof"
echo "Trace:      $RESULTS/new/trace.out"
echo "  Analyze: go tool trace $RESULTS/new/trace.out"

# Extract round timings from transcript if available
echo ""
echo "=== Transcripts ==="
for ver in old new; do
  TRANSCRIPT=$(find "$RESULTS/$ver/state" -name "*.transcript.jsonl" 2>/dev/null | head -1)
  if [ -n "$TRANSCRIPT" ]; then
    ROUNDS=$(grep -c '"kind":"entry"' "$TRANSCRIPT" 2>/dev/null || echo 0)
    echo "$ver: $ROUNDS transcript entries"
    # Show API log timing if available
    APILOG="$RESULTS/$ver/state/api.jsonl"
    if [ -f "$APILOG" ]; then
      APICALLS=$(wc -l < "$APILOG" | tr -d ' ')
      TOTAL_LLM=$(python3 -c "
import json, sys
total = 0
for line in open('$APILOG'):
    try:
        total += json.loads(line)['latency_ms']
    except: pass
print(f'{total/1000:.1f}')
")
      echo "  API calls: $APICALLS, total LLM time: ${TOTAL_LLM}s"
    fi
  else
    echo "$ver: no transcript found"
  fi
done
