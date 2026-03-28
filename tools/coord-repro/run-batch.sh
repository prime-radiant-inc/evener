#!/usr/bin/env bash
# Run all coordinator.md variants in a directory and report results.
#
# Usage:
#   OPENAI_API_KEY=... ./run-batch.sh /path/to/variants/ [reps]
#
# The variants directory should contain files like:
#   01-baseline.md
#   02-shell-restricted.md
#   ...
#
# Each file is a complete coordinator.md replacement.
# Runs each variant [reps] times (default 2) and reports DELEGATE/BYPASS.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VARIANTS_DIR="${1:?Usage: $0 /path/to/variants/ [reps]}"
REPS="${2:-2}"

RESULTS_FILE="/tmp/coord-repro-batch-results.txt"
echo "variant,rep,result" > "$RESULTS_FILE"

for variant_file in "$VARIANTS_DIR"/*.md; do
  variant_name=$(basename "$variant_file" .md)
  echo "=== $variant_name ==="

  delegate_count=0
  bypass_count=0
  unclear_count=0

  for rep in $(seq 1 "$REPS"); do
    label="${variant_name}-rep${rep}"
    result=$("$SCRIPT_DIR/run-test.sh" "$label" "$variant_file" 2>/dev/null | head -1)

    if echo "$result" | grep -q "DELEGATE"; then
      delegate_count=$((delegate_count + 1))
      echo "  rep $rep: DELEGATE"
      echo "$variant_name,$rep,DELEGATE" >> "$RESULTS_FILE"
    elif echo "$result" | grep -q "BYPASS"; then
      bypass_count=$((bypass_count + 1))
      echo "  rep $rep: BYPASS"
      echo "$variant_name,$rep,BYPASS" >> "$RESULTS_FILE"
    else
      unclear_count=$((unclear_count + 1))
      echo "  rep $rep: UNCLEAR"
      echo "$variant_name,$rep,UNCLEAR" >> "$RESULTS_FILE"
    fi
  done

  echo "  => ${delegate_count}/${REPS} delegate, ${bypass_count}/${REPS} bypass"
  echo ""
done

echo "Results saved to $RESULTS_FILE"
echo ""
echo "=== SUMMARY ==="
column -t -s, "$RESULTS_FILE"
