#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

run_test_checked() {
  local label="$1"
  shift

  echo "$label"

  local out rc
  set +e
  out="$("$@" 2>&1)"
  rc=$?
  set -e
  printf '%s\n' "$out"

  if grep -q '\[no tests to run\]' <<<"$out"; then
    echo "FAIL: targeted command executed zero tests"
    return 1
  fi
  if [[ $rc -ne 0 ]]; then
    echo "FAIL: targeted command failed"
    return "$rc"
  fi
  return 0
}

run_test_checked "[1/7] shared retry policy helper" \
  go test ./internal/attractor/engine -run '^TestShouldRetryOutcome_ClassGated$' -count=1

run_test_checked "[2/7] deterministic failures block stage retries" \
  go test ./internal/attractor/engine -run '^TestRun_DeterministicFailure_DoesNotRetry$' -count=1

run_test_checked "[3/7] provider CLI classification contract (anthropic)" \
  go test ./internal/attractor/engine -run '^TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose$' -count=1

run_test_checked "[4/7] preflight report is always written" \
  go test ./internal/attractor/engine -run '^TestRunWithConfig_WritesPreflightReport_Always$' -count=1

run_test_checked "[5/7] deterministic CLI fail does not consume stage retries" \
  go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget$' -count=1

run_test_checked "[6/7] deterministic CLI fail blocks loop_restart and writes terminal final" \
  go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIDeterministicFailure_BlocksStageRetryAndLoopRestart_WritesTerminalFinal$' -count=1

run_test_checked "[7/7] fan-in all-fail deterministic precedence" \
  go test ./internal/attractor/engine -run '^TestFanIn_AllStatusFail_MixedClasses_AggregatesDeterministic$' -count=1

echo "guardrail matrix: PASS"
