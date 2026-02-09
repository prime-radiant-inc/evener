# Architectural Reliability Completion Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Attractor fail fast on deterministic failures, retry only transient failures, and always emit complete terminal/preflight artifacts so failures are diagnosable without retry/restart churn.

**Architecture:** This is a strict delta plan against current `codex-state-isolation-watchdog` branch state. Reuse existing loop-restart classification/circuit-breaker primitives and add missing stage retry gating, source-level provider CLI classification, robust-but-pragmatic CLI preflight, and fan-in all-fail classification metadata. Preserve the existing two-class taxonomy (`transient_infra`, `deterministic`) and apply fail-closed behavior for unknown classes.

**Tech Stack:** Go 1.25, stdlib `testing`, existing engine integration harness, shell guardrail matrix.

---

## Current-State Baseline (Verified)

Already implemented and must be reused:

1. Loop-restart failure class primitives in `internal/attractor/engine/loop_restart_policy.go`:
   - `failureClassTransientInfra`
   - `failureClassDeterministic`
   - `classifyFailureClass(...)`
   - `normalizedFailureClass(...)`
   - `normalizedFailureClassOrDefault(...)`
   - `restartFailureSignature(...)`
2. Loop-restart class gating + signature circuit-breaker in `internal/attractor/engine/engine.go`.
3. Terminal fatal persistence (`final.json`) via `persistFatalOutcome(...)` + `persistTerminalOutcome(...)` in `internal/attractor/engine/engine.go`.
4. Existing provider/model catalog preflight check in `internal/attractor/engine/run_with_config.go` + `TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider`.
5. Existing guardrail matrix script in `scripts/e2e-guardrail-matrix.sh`.

Missing deltas this plan delivers:

1. Shared stage retry gate helper (`shouldRetryOutcome`) + executeWithRetry wiring.
2. Provider CLI failure classification at source in `runCLI` with `failure_class` + `failure_signature` metadata.
3. Anthropic invocation contract fix (`--verbose`) as its own behavior change.
4. Deterministic CLI preflight with always-written `preflight_report.json` and explicit rollback switch.
5. E2E coverage for:
   - source classification -> stage retry gate
   - source classification -> stage retry blocked -> loop_restart blocked -> terminal final
6. Fan-in all-fail aggregate classification with deterministic precedence, with explicit handling note for current `StatusRetry` winner behavior.
7. Guardrail script no-false-pass behavior (`[no tests to run]` + exit-code correctness).

No dependency additions are planned. `go.mod`/`go.sum` should remain unchanged.

---

## Execution Test Helpers (Required)

Use these helpers for all targeted test commands in this plan.

```bash
run_test_checked() {
  local expect="$1"   # red | green
  local label="$2"
  shift 2

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

  if [[ "$expect" == "red" && $rc -eq 0 ]]; then
    echo "FAIL: expected red but command passed"
    return 1
  fi

  if [[ "$expect" == "green" && $rc -ne 0 ]]; then
    echo "FAIL: expected green but command failed"
    return $rc
  fi

  return 0
}
```

Rules:

1. Red step must fail for a real assertion or compile reason.
2. Green step must pass with exit code 0.
3. Do not commit compile-broken red states.

---

### Task 1: Add Shared Stage Retry Policy Helper (Delta Only)

**Files:**
- Create: `internal/attractor/engine/failure_policy.go`
- Create: `internal/attractor/engine/failure_policy_test.go`
- Reuse: `internal/attractor/engine/loop_restart_policy.go`

**Step 1: Add red tests with correct runtime types**

In `failure_policy_test.go`, add:

```go
func TestShouldRetryOutcome_ClassGated(t *testing.T) {
	cases := []struct {
		name  string
		out   runtime.Outcome
		class string
		want  bool
	}{
		{
			name:  "fail transient retries",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary timeout"},
			class: failureClassTransientInfra,
			want:  true,
		},
		{
			name:  "fail deterministic does not retry",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "contract mismatch"},
			class: failureClassDeterministic,
			want:  false,
		},
		{
			name:  "retry transient retries",
			out:   runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "retry please"},
			class: failureClassTransientInfra,
			want:  true,
		},
		{
			name:  "retry deterministic does not retry",
			out:   runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "permanent"},
			class: failureClassDeterministic,
			want:  false,
		},
		{
			name:  "unknown class defaults fail-closed",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "unknown"},
			class: "",
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetryOutcome(tc.out, tc.class); got != tc.want {
				t.Fatalf("shouldRetryOutcome(%q,%q)=%v want %v", tc.out.Status, tc.class, got, tc.want)
			}
		})
	}
}

func TestShouldRetryOutcome_NonFailureStatusesNeverRetry(t *testing.T) {
	statuses := []runtime.StageStatus{
		runtime.StatusSuccess,
		runtime.StatusPartialSuccess,
		runtime.StatusSkipped,
	}
	for _, st := range statuses {
		if shouldRetryOutcome(runtime.Outcome{Status: st}, failureClassTransientInfra) {
			t.Fatalf("status=%q should never retry", st)
		}
	}
}
```

**Step 2: Run red tests**

```bash
run_test_checked red "task1 red" go test ./internal/attractor/engine -run '^TestShouldRetryOutcome_ClassGated$|^TestShouldRetryOutcome_NonFailureStatusesNeverRetry$' -count=1
```

Expected: red (missing symbol before implementation is acceptable in this step).

**Step 3: Implement helper**

In `failure_policy.go`, add:

```go
func shouldRetryOutcome(out runtime.Outcome, failureClass string) bool {
	if out.Status != runtime.StatusFail && out.Status != runtime.StatusRetry {
		return false
	}
	return normalizedFailureClassOrDefault(failureClass) == failureClassTransientInfra
}
```

**Step 4: Run green tests**

```bash
run_test_checked green "task1 green" go test ./internal/attractor/engine -run '^TestShouldRetryOutcome_ClassGated$|^TestShouldRetryOutcome_NonFailureStatusesNeverRetry$' -count=1
```

**Step 5: Commit**

```bash
git add internal/attractor/engine/failure_policy.go internal/attractor/engine/failure_policy_test.go
git commit -m "feat(engine): add shared class-gated stage retry helper"
```

---

### Task 2: Gate `executeWithRetry` by Classified Failure Policy

**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/engine/retry_failure_class_test.go`
- Reuse: `internal/attractor/engine/retry_policy_test.go`
- Reuse: `internal/attractor/engine/retry_on_retry_status_test.go`

**Step 1: Add red integration tests**

In `retry_failure_class_test.go`:

1. `TestRun_DeterministicFailure_DoesNotRetry`
   - node configured with retry budget (`default_max_retry=3`)
   - first outcome `StatusFail` with `Meta.failure_class=deterministic`
   - assert no retry attempt, no `stage_retry_sleep`, and `stage_retry_blocked` exists.
2. `TestRun_TransientFailure_StillRetries`
   - attempt1: fail + `failure_class=transient_infra`
   - attempt2: success
   - assert retry occurred and final success.

**Step 2: Run red tests**

```bash
run_test_checked red "task2 red" go test ./internal/attractor/engine -run '^TestRun_DeterministicFailure_DoesNotRetry$|^TestRun_TransientFailure_StillRetries$' -count=1
```

**Step 3: Wire retry gate**

In `executeWithRetry`:

1. Compute `failureClass := classifyFailureClass(out)` each attempt.
2. Replace unconditional retry on fail/retry with:
   - `attempt < maxAttempts && shouldRetryOutcome(out, failureClass)`.
3. Emit `stage_retry_blocked` progress event when retry is blocked.
4. Preserve allow-partial and terminal canonicalization behavior.

**Step 4: Run green tests**

```bash
run_test_checked green "task2 green new" go test ./internal/attractor/engine -run '^TestRun_DeterministicFailure_DoesNotRetry$|^TestRun_TransientFailure_StillRetries$' -count=1
run_test_checked green "task2 green regressions" go test ./internal/attractor/engine -run '^TestRun_RetriesOnFail_ThenSucceeds$|^TestRun_RetriesOnRetryStatus$|^TestRun_AllowPartialAfterRetryExhaustion$' -count=1
```

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/retry_failure_class_test.go
git commit -m "fix(engine): gate stage retry loop by normalized failure class"
```

---

### Task 3: Add Provider CLI Failure Classification at Source

**Files:**
- Create: `internal/attractor/engine/provider_error_classification.go`
- Create: `internal/attractor/engine/provider_error_classification_test.go`
- Modify: `internal/attractor/engine/codergen_router.go`

**Step 1: Add red classifier tests**

Add tests with concrete assertions:

1. `TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose`
   - class deterministic
   - stable signature prefix: `provider_contract|anthropic|...`
2. `TestClassifyProviderCLIError_GeminiModelNotFound`
   - class deterministic
   - signature prefix: `provider_model_unavailable|google|...`
3. `TestClassifyProviderCLIError_CodexIdleTimeout_RunErrSignal`
   - class transient_infra
   - signature prefix: `provider_timeout|openai|...`
4. `TestClassifyProviderCLIError_UnknownFallbackIsDeterministic`

**Step 2: Run red tests**

```bash
run_test_checked red "task3 red" go test ./internal/attractor/engine -run '^TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose$|^TestClassifyProviderCLIError_GeminiModelNotFound$|^TestClassifyProviderCLIError_CodexIdleTimeout_RunErrSignal$|^TestClassifyProviderCLIError_UnknownFallbackIsDeterministic$' -count=1
```

**Step 3: Implement classifier**

Create in `provider_error_classification.go`:

```go
type providerCLIClassifiedError struct {
	FailureClass     string
	FailureSignature string
	FailureReason    string
}

func classifyProviderCLIError(provider string, stderr string, runErr error) providerCLIClassifiedError
```

Classifier contract:

1. provider-specific deterministic signatures first.
2. transient infra hints second.
3. deterministic fallback.
4. outputs normalized class values consumed by shared policy.

**Step 4: Wire classification in `runCLI` with explicit path coverage**

Apply classification metadata on all stage-level failure returns in `runCLI`:

1. final `runErr != nil` path (after internal schema/state-db fallback attempts).
2. `runErrDetail != nil` path from `runOnce`.
3. command-prep failures that produce stage fail outcomes (where applicable).

Do not emit stage outcome classification for internal fallback probes that are retried in-process; only classify the final stage-visible failure outcome.

Outcome contract on classified failures:

1. `FailureReason` = classifier reason
2. `Meta["failure_class"]`
3. `Meta["failure_signature"]`
4. `ContextUpdates["failure_class"]`

**Step 5: Run green tests**

```bash
run_test_checked green "task3 green" go test ./internal/attractor/engine -run '^TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose$|^TestClassifyProviderCLIError_GeminiModelNotFound$|^TestClassifyProviderCLIError_CodexIdleTimeout_RunErrSignal$|^TestClassifyProviderCLIError_UnknownFallbackIsDeterministic$' -count=1
```

**Step 6: Commit**

```bash
git add internal/attractor/engine/provider_error_classification.go internal/attractor/engine/provider_error_classification_test.go internal/attractor/engine/codergen_router.go
git commit -m "feat(engine): classify provider cli failures across all stage-visible failure paths"
```

---

### Task 4: Fix Anthropic CLI Invocation Contract (Dedicated Change)

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/codergen_cli_invocation_test.go`

**Step 1: Add red invocation test**

Add `TestDefaultCLIInvocation_AnthropicIncludesVerboseForStreamJSON` with concrete assertion for `--verbose`.

**Step 2: Run red test**

```bash
run_test_checked red "task4 red" go test ./internal/attractor/engine -run '^TestDefaultCLIInvocation_AnthropicIncludesVerboseForStreamJSON$' -count=1
```

**Step 3: Implement fix**

In `defaultCLIInvocation(...)`, include `--verbose` for provider `anthropic` stream-json invocation.

**Step 4: Run green tests**

```bash
run_test_checked green "task4 green" go test ./internal/attractor/engine -run '^TestDefaultCLIInvocation_AnthropicIncludesVerboseForStreamJSON$|^TestDefaultCLIInvocation_OpenAI_DoesNotUseDeprecatedAskForApproval$|^TestDefaultCLIInvocation_GoogleGeminiNonInteractive$' -count=1
```

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_cli_invocation_test.go
git commit -m "fix(engine): include anthropic --verbose for stream-json contract compatibility"
```

---

### Task 5: Add Deterministic CLI Preflight + Always-Persisted `preflight_report.json`

**Files:**
- Create: `internal/attractor/engine/provider_preflight.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/provider_preflight_test.go`
- Optional: `internal/attractor/engine/run_with_config_integration_test.go`

**Step 1: Add red tests (non-empty)**

Add tests:

1. `TestRunWithConfig_PreflightFails_WhenProviderCLIBinaryMissing`
2. `TestRunWithConfig_PreflightFails_WhenAnthropicCapabilityMissingVerbose`
3. `TestRunWithConfig_WritesPreflightReport_Always`
4. `TestRunWithConfig_PreflightCapabilityProbeFailure_WarnsWhenNonStrict`
5. `TestRunWithConfig_PreflightCapabilityProbeFailure_FailsWhenStrict`
6. keep existing `TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider` green as regression.

**Step 2: Run red tests**

```bash
run_test_checked red "task5 red" go test ./internal/attractor/engine -run '^TestRunWithConfig_PreflightFails_WhenProviderCLIBinaryMissing$|^TestRunWithConfig_PreflightFails_WhenAnthropicCapabilityMissingVerbose$|^TestRunWithConfig_WritesPreflightReport_Always$|^TestRunWithConfig_PreflightCapabilityProbeFailure_WarnsWhenNonStrict$|^TestRunWithConfig_PreflightCapabilityProbeFailure_FailsWhenStrict$' -count=1
```

**Step 3: Implement preflight report model and writer**

In `provider_preflight.go`:

1. report schema with check entries and summary counts.
2. support statuses: `pass`, `warn`, `fail`.
3. persist report on all preflight paths.

**Step 4: Integrate preflight ordering in `RunWithConfig`**

Required execution order:

1. graph `Prepare(...)`
2. provider backend presence checks
3. `opts.applyDefaults()`
4. model catalog resolve/load + provider/model validation
5. CLI preflight
6. CXDB health/binary/context setup
7. engine run

**Step 5: Preflight capability strategy (risk-controlled)**

Mandatory checks:

1. provider CLI binary exists and is executable.
2. capability probe command runs with short timeout.

Capability checks:

1. Anthropic must advertise required args for used invocation mode, including `--verbose` for stream-json.
2. Google must advertise non-interactive approval arg (`--yolo` or supported equivalent).
3. OpenAI probe checks only required options used by invocation builder.

Brittleness control:

1. If probe command itself fails or output is unparseable:
   - default mode: warn in report and continue.
   - strict mode (`KILROY_PREFLIGHT_STRICT_CAPABILITIES=1`): fail deterministic.
2. If probe succeeds and required token is definitively missing: fail deterministic.

Rollback switch:

1. `KILROY_PREFLIGHT_CAPABILITY_PROBES=off` disables capability probing and keeps binary-existence checks only.
2. document this as emergency rollback for CI/ops regressions.

**Step 6: Run green tests**

```bash
run_test_checked green "task5 green" go test ./internal/attractor/engine -run '^TestRunWithConfig_PreflightFails_WhenProviderCLIBinaryMissing$|^TestRunWithConfig_PreflightFails_WhenAnthropicCapabilityMissingVerbose$|^TestRunWithConfig_WritesPreflightReport_Always$|^TestRunWithConfig_PreflightCapabilityProbeFailure_WarnsWhenNonStrict$|^TestRunWithConfig_PreflightCapabilityProbeFailure_FailsWhenStrict$|^TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider$' -count=1
```

**Step 7: Commit**

```bash
git add internal/attractor/engine/provider_preflight.go internal/attractor/engine/run_with_config.go internal/attractor/engine/provider_preflight_test.go internal/attractor/engine/run_with_config_integration_test.go
git commit -m "feat(engine): add deterministic cli preflight with strictness/rollback controls and always-written reports"
```

---

### Task 6: Add End-to-End Verification Coverage for Combined Failure Flow

**Files:**
- Create: `internal/attractor/engine/retry_classification_integration_test.go`
- Reuse: CLI fake-script patterns from `internal/attractor/engine/codergen_process_test.go`

This task is an integration verification task. If tasks 2-5 are complete, tests should be green immediately.

**Step 1: Add integration test A (source classification -> stage retry gate)**

`TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget`:

1. fake CLI returns deterministic contract/model error.
2. stage `max_retries=3`.
3. assert one invocation only.
4. assert `stage_retry_blocked` and no `stage_retry_sleep`.

**Step 2: Add integration test B (stage gate + loop-restart gate combined)**

`TestRunWithConfig_CLIDeterministicFailure_BlocksStageRetryAndLoopRestart_WritesTerminalFinal`:

1. graph has `loop_restart=true` fail edge.
2. deterministic classified CLI failure occurs.
3. assert no stage retries.
4. assert `loop_restart_blocked` event.
5. assert terminal `final.json` exists with fail reason indicating deterministic class block.

**Step 3: Run integration verification tests**

```bash
run_test_checked green "task6 integration" go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget$|^TestRunWithConfig_CLIDeterministicFailure_BlocksStageRetryAndLoopRestart_WritesTerminalFinal$' -count=1
```

If red, fix wiring gaps in tasks 2-5 scope before continuing.

**Step 4: Commit**

```bash
git add internal/attractor/engine/retry_classification_integration_test.go internal/attractor/engine/engine.go internal/attractor/engine/codergen_router.go
git commit -m "test(engine): verify deterministic cli failures are blocked at both stage retry and loop restart boundaries"
```

---

### Task 7: Add Fan-In All-Fail Classification with Deterministic Precedence

**Files:**
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Create: `internal/attractor/engine/fanin_failure_class_test.go`
- Optionally update: `internal/attractor/engine/parallel_guardrails_test.go`

Current behavior caveat to preserve explicitly:

1. In current `selectHeuristicWinner`, only all-`StatusFail` branches produce `!okWinner`.
2. `StatusRetry` branches are currently considered candidate winners and therefore bypass all-fail aggregation.
3. This plan does not change that selection behavior; it only classifies the existing all-fail branch.

Add follow-up engineering note in code comments/docs to evaluate whether `StatusRetry` should be treated as non-winning in a future refactor.

**Step 1: Add red tests for all-fail branch only**

1. `TestFanIn_AllStatusFail_MixedClasses_AggregatesDeterministic`
2. `TestFanIn_AllStatusFail_AllTransient_AggregatesTransient`
3. `TestFanIn_AllStatusFail_UnknownClass_AggregatesDeterministic`

Assertions:

1. `Meta.failure_class`
2. `Meta.failure_signature` prefix `parallel_all_failed|...`
3. `ContextUpdates.failure_class`

**Step 2: Run red tests**

```bash
run_test_checked red "task7 red" go test ./internal/attractor/engine -run '^TestFanIn_AllStatusFail_MixedClasses_AggregatesDeterministic$|^TestFanIn_AllStatusFail_AllTransient_AggregatesTransient$|^TestFanIn_AllStatusFail_UnknownClass_AggregatesDeterministic$' -count=1
```

**Step 3: Implement aggregate classifier**

Policy:

1. Any deterministic class among failing branches => deterministic aggregate.
2. Else if all known branch classes are transient => transient aggregate.
3. Empty/unknown => deterministic (fail closed).

**Step 4: Run green tests**

```bash
run_test_checked green "task7 green" go test ./internal/attractor/engine -run '^TestFanIn_AllStatusFail_MixedClasses_AggregatesDeterministic$|^TestFanIn_AllStatusFail_AllTransient_AggregatesTransient$|^TestFanIn_AllStatusFail_UnknownClass_AggregatesDeterministic$' -count=1
```

**Step 5: Commit**

```bash
git add internal/attractor/engine/parallel_handlers.go internal/attractor/engine/fanin_failure_class_test.go internal/attractor/engine/parallel_guardrails_test.go
git commit -m "fix(engine): classify all-fail fan-in outcomes with deterministic-precedence fail-closed policy"
```

---

### Task 8: Harden Guardrail Matrix + Update Runbook

**Files:**
- Modify: `scripts/e2e-guardrail-matrix.sh`
- Modify: `docs/strongdm/attractor/README.md`

**Step 1: Replace matrix test runner with exit-code-safe helper**

In matrix script, add `run_test_checked green ...` semantics equivalent to plan helper:

1. fail on non-zero exit.
2. fail on `[no tests to run]`.

**Step 2: Extend matrix checks**

Include targeted checks for:

1. `TestShouldRetryOutcome_ClassGated`
2. `TestRun_DeterministicFailure_DoesNotRetry`
3. `TestClassifyProviderCLIError_AnthropicStreamJSONRequiresVerbose`
4. `TestRunWithConfig_WritesPreflightReport_Always`
5. `TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget`
6. `TestRunWithConfig_CLIDeterministicFailure_BlocksStageRetryAndLoopRestart_WritesTerminalFinal`
7. `TestFanIn_AllStatusFail_MixedClasses_AggregatesDeterministic`

**Step 3: Update runbook semantics**

Document:

1. stage retry and loop-restart both consume normalized `failure_class`.
2. deterministic failures are blocked at both gates.
3. source-level provider classification and signatures.
4. preflight behavior including strictness and rollback env switches.
5. fan-in all-fail classification scope and current `StatusRetry` caveat.

**Step 4: Run matrix**

```bash
bash scripts/e2e-guardrail-matrix.sh
```

**Step 5: Commit**

```bash
git add scripts/e2e-guardrail-matrix.sh docs/strongdm/attractor/README.md
git commit -m "docs+tests: enforce reliable targeted test execution and document dual-gate failure semantics"
```

---

### Task 9: Full Verification + Safe Validation Procedure

**Files:**
- No source additions expected.

**Step 1: Run full suites**

```bash
go test ./cmd/kilroy -count=1
go test ./internal/attractor/runtime -count=1
go test ./internal/attractor/engine -count=1
go test ./internal/llm/providers/... -count=1
bash scripts/e2e-guardrail-matrix.sh
```

**Step 2: Verify no dependency drift**

```bash
git diff -- go.mod go.sum
```

Expected: no changes.

**Step 3: Re-run DTTF from clean logs root (detached)**

```bash
RUN_ROOT="/tmp/kilroy-dttf-real-cxdb-$(date -u +%Y%m%dT%H%M%SZ)-postfix-v3"
mkdir -p "$RUN_ROOT"
setsid -f bash -lc 'cd /home/user/code/kilroy-wt-state-isolation-watchdog && ./kilroy attractor run --dot docs/strongdm/attractor/examples/dttf.dot --config docs/strongdm/attractor/examples/run.dttf.real-cxdb.yaml --logs-root "'"$RUN_ROOT"'"/logs >> "'"$RUN_ROOT"'"/run.out" 2>&1'
```

**Step 4: Validate outputs from logs**

Confirm:

1. root `final.json` exists.
2. deterministic failures do not consume retries.
3. loop restart is blocked for deterministic class.
4. no deterministic restart storm.
5. preflight report exists and is interpretable.

**Step 5: Safe commit protocol (no `git add -A`)**

Validation task usually should not produce commits. If any intentional code/doc updates were made during validation:

```bash
git add <explicit-file-1> <explicit-file-2>
if ! git diff --cached --quiet; then
  git commit -m "test(attractor): validate reliability changes against clean dttf run"
fi
```

---

## Required Green Exit Criteria

Done means all conditions are met:

1. `shouldRetryOutcome` exists with passing unit tests.
2. Deterministic stage failures block retries; transient failures still retry.
3. Provider CLI failures are classified at source for all stage-visible failure returns.
4. Anthropic invocation includes `--verbose` with dedicated regression coverage.
5. `RunWithConfig` runs CLI preflight after catalog validation and before CXDB health.
6. `preflight_report.json` is always written (pass/fail).
7. Preflight strictness/rollback switches are implemented and documented.
8. Combined E2E path is covered: CLI deterministic fail -> no stage retry -> no loop restart -> terminal `final.json`.
9. Fan-in all-fail outcomes emit deterministic-precedence class/signature metadata.
10. Guardrail matrix enforces both exit-code correctness and no `[no tests to run]` false passes.
11. Full suites pass; no unintended `go.mod`/`go.sum` changes.
12. Real DTTF rerun from clean logs root shows stable behavior without deterministic restart churn.

---

## Scope and Follow-Up

1. This plan keeps the current two-class taxonomy by design for compatibility.
2. Future enhancement: evaluate `selectHeuristicWinner` handling of `StatusRetry` candidates; this plan documents but does not change it.
3. Future enhancement: if richer classes are introduced (auth/quota/rate-limit), migration must include compatibility mapping and explicit retry semantics.
