#!/usr/bin/env bash
# Backfill the scoreboard from all harbor-runner era experiments.
# Uses --light mode (rewards only) to avoid downloading GB of transcripts.
set -euo pipefail

cd "$(dirname "$0")/.."

collect() {
    local run=$1 model=$2 date=$3 variant=$4
    echo "=== $run ==="
    python3 tools/collect_results.py "$run" \
        --light --model "$model" --date "$date" --variant "$variant" \
        2>&1 | tail -5
    echo ""
}

MODEL="openai/gpt-5.4-mini"

# --- Broad baselines ---
collect "disc-3rep-v6-fixed" "$MODEL" "2026-03-26" \
    "broad 56-task baseline with template engine fixes"

collect "v17-broad-20" "$MODEL" "2026-03-26" \
    "20-task regression check, v17 harmonize gate"

# --- v10-v18: single-task targeted experiments ---
collect "v10-deleg-goldplate" "$MODEL" "2026-03-25" \
    "chess-best-move + polyglot-c-py delegation/goldplating fixes"

collect "v11-positive-framing" "$MODEL" "2026-03-25" \
    "positive authority ordering for reviewer"

collect "v12-easy-sweep" "$MODEL" "2026-03-25" \
    "easy task sweep"

collect "v13-coordinator-verify" "$MODEL" "2026-03-26" \
    "coordinator verify soft prohibition"

collect "v14-hard-ban" "$MODEL" "2026-03-26" \
    "coordinator verify hard prohibition"

collect "v15-positive-verify" "$MODEL" "2026-03-26" \
    "positive framing verification"

collect "v16-no-scratch" "$MODEL" "2026-03-26" \
    "reading not computing + no scratch dir"

collect "v17-harmonize-gate-mini" "$MODEL" "2026-03-26" \
    "log-summary-date-ranges: harmonize HARD GATE with step 3"

collect "v17-log-summary-5.4" "openai/gpt-5.4" "2026-03-26" \
    "log-summary-date-ranges: v17 on gpt-5.4"

collect "v18-no-tests-mini" "$MODEL" "2026-03-26" \
    "log-summary-date-ranges: no-tests case handling"

collect "v18-no-tests-5.4" "openai/gpt-5.4" "2026-03-26" \
    "log-summary-date-ranges: no-tests case on gpt-5.4"

# --- v19: delegation/state targeted experiments ---
for v in v19-deleg-{a,b,c,d,e} v19-tasklist-{a,b} v19-state-{a,b}; do
    collect "$v" "$MODEL" "2026-03-27" "v19 variant: $v"
done

# --- v20: verification depth experiments ---
for v in v20-{tasklist,verify,impl-test,combined}-{a,b,c}; do
    # Skip non-existent combinations and already collected
    aws s3 ls "s3://harbor-eval-results-526275945504/runs/$v/" --region us-west-1 \
        >/dev/null 2>&1 || continue
    collect "$v" "$MODEL" "2026-03-27" "v20 variant: $v"
done

# Rebuild final scoreboard
python3 tools/collect_results.py --rebuild-scoreboard --date "2026-03-27"
echo "=== Backfill complete ==="
