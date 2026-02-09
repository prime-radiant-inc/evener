# Fresheyes Reliability Follow-Up Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Address the remaining high-signal reliability concerns from the fresh-eyes review while preserving current run behavior: keep Codex invocation simple, make schema handling tolerant-but-loud, harden subprocess lifecycle/cancellation, codify restart retry semantics, and eliminate test artifact pollution.

**Architecture:** Keep the existing Attractor engine flow and failure taxonomy. Do not re-introduce deprecated Codex flags. For Codex structured output, move from strict schema rejection to contract auditing: accept additional keys, emit explicit diagnostics, retry once in compatibility mode, then continue. For process management, unify probe execution around context-bound commands and manage autostarted process lifecycle explicitly.

**Tech Stack:** Go 1.25, stdlib `testing`, existing Attractor engine integration tests, shell validation via existing scripts.

---

## Scope Decisions (Locked)

1. `#1` is simplified by empirical CLI behavior: **do not add compatibility complexity for `--ask-for-approval`**.
2. For `#2`, Codex structured output should tolerate unknown keys but **surface loud diagnostics**, **retry once**, then continue.
3. `#6` (CXDB UI startup-path latency optimization) is explicitly out of scope for this pass.
4. `#7` is in scope and must include `.gitignore` hardening.
5. `#8` already handled externally via prompt/process updates; no code task needed.

---

## Mandatory Red/Green Rule

For code tasks, run a red test first, implement minimal fix, then run green. Do not commit knowingly failing package builds.

Suggested helper (optional):

```bash
run_test_checked() {
  local expect="$1"   # red|green
  local label="$2"
  shift 2

  echo "$label"
  local out rc
  set +e
  out="$($@ 2>&1)"
  rc=$?
  set -e
  printf '%s\n' "$out"

  if grep -q '\[no tests to run\]' <<<"$out"; then
    echo "FAIL: targeted command ran zero tests"
    return 1
  fi
  if [[ "$expect" == "red" && $rc -eq 0 ]]; then
    echo "FAIL: expected red"
    return 1
  fi
  if [[ "$expect" == "green" && $rc -ne 0 ]]; then
    echo "FAIL: expected green"
    return $rc
  fi
}
```

---

### Task 1: Freeze Codex CLI Invocation Contract (No Deprecated Approval Flag)

**Files:**
- Modify: `docs/strongdm/attractor/kilroy-metaspec.md`
- Modify: `internal/attractor/engine/codergen_cli_invocation_test.go`

**Step 1: Add/extend test coverage (red only if missing assertion)**

Add/update test to assert OpenAI invocation:
- includes `exec --json --sandbox workspace-write -m <model> -C <worktree>`
- does **not** include `--ask-for-approval`
- does **not** include `--skip-git-repo-check` for in-repo runs

Suggested test command:

```bash
run_test_checked green "task1 invocation contract" go test ./internal/attractor/engine -run '^TestDefaultCLIInvocation_OpenAI_.*' -count=1
```

**Step 2: Update metaspec docs to match current Codex CLI reality**

In `docs/strongdm/attractor/kilroy-metaspec.md`, replace the OpenAI section that currently claims `--ask-for-approval never` with:
- non-interactive execution is achieved via `codex exec --json --sandbox workspace-write ...`
- `--skip-git-repo-check` is only needed outside trusted git repos

**Step 3: Commit**

```bash
git add docs/strongdm/attractor/kilroy-metaspec.md internal/attractor/engine/codergen_cli_invocation_test.go
git commit -m "docs+test(attractor): lock codex invocation contract without deprecated approval flag"
```

---

### Task 2: Make Codex Structured Schema Tolerant but Loud on Unknown Keys

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/codergen_schema_test.go`
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`

**Step 1: Write failing tests for unknown-key diagnostics + retry behavior**

Add tests in `codergen_schema_test.go`:
1. `TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeysTriggersLoudFallback`
2. `TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeysStillAllowsSuccess`

Each test should verify all of:
- first attempt with `--output-schema` produces `output.json` containing extra keys
- engine emits explicit warning and artifact (for example `structured_output_unknown_keys.json`)
- engine retries once without `--output-schema`
- final run still succeeds when retry succeeds
- `cli_invocation.json` includes fallback metadata:
  - `schema_fallback_retry=true`
  - `schema_fallback_reason="unknown_structured_keys"`
  - `structured_output_unknown_keys=[...]`

Run red:

```bash
run_test_checked red "task2 red unknown-key fallback" go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeys' -count=1
```

**Step 2: Implement minimal code changes**

In `codergen_router.go`:
1. Change `defaultCodexOutputSchema` to:

```json
{
  "type": "object",
  "properties": {
    "final": { "type": "string" },
    "summary": { "type": "string" }
  },
  "required": ["final", "summary"],
  "additionalProperties": true
}
```

2. Add helper to inspect `output.json` contract after each Codex run:
- parse JSON object
- ensure required keys `final` and `summary` exist and are strings
- compute unknown keys (`keys - {final, summary}`)

3. If unknown keys are found on first schema-enabled attempt:
- call `warnEngine(...)` with a high-signal message
- write a stage artifact with key list and sample payload
- persist metadata to `cli_invocation.json`
- retry once without `--output-schema`

4. Continue with retry result even when unknown keys were present.

**Step 3: Run green tests (targeted + regression)**

```bash
run_test_checked green "task2 green unknown-key fallback" go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeys' -count=1
run_test_checked green "task2 schema regression" go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIBackend_OpenAISchemaFallback$|^TestDefaultCodexOutputSchema_.*$' -count=1
```

**Step 4: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_schema_test.go internal/attractor/engine/run_with_config_integration_test.go
git commit -m "feat(attractor): tolerate codex extra structured-output keys with loud fallback diagnostics"
```

---

### Task 3: Unify Provider Probe Cancellation With Context-Bound Process Execution

**Files:**
- Modify: `internal/attractor/engine/provider_preflight.go`
- Modify: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Add failing tests for cancellation/leak safety**

Add tests:
1. `TestRunProviderCapabilityProbe_TimesOutAndKillsProcessGroup`
2. `TestRunProviderCapabilityProbe_RespectsParentContextCancel`

Use a fake CLI script that:
- spawns a child sleeper
- writes child PID to temp file
- blocks until killed

Assertions:
- probe returns timeout/cancel error
- both parent and child processes are gone after probe returns

Run red:

```bash
run_test_checked red "task3 red probe cancellation" go test ./internal/attractor/engine -run '^TestRunProviderCapabilityProbe_(TimesOutAndKillsProcessGroup|RespectsParentContextCancel)$' -count=1
```

**Step 2: Implement with shared probe runner**

Refactor `provider_preflight.go`:
- add shared helper (for capability/model probes) that uses `exec.CommandContext`
- set `SysProcAttr{Setpgid:true}`
- on timeout/cancel, explicitly call `killProcessGroup` (TERM then KILL) and wait
- ensure helper returns collected stdout/stderr for diagnostics

Update both:
- `runProviderCapabilityProbe`
- `runProviderModelAccessProbe`

to use the same lifecycle/cancellation model.

**Step 3: Run green tests**

```bash
run_test_checked green "task3 green probe cancellation" go test ./internal/attractor/engine -run '^TestRunProviderCapabilityProbe_(TimesOutAndKillsProcessGroup|RespectsParentContextCancel)$' -count=1
run_test_checked green "task3 preflight suite" go test ./internal/attractor/engine -run '^TestRunWithConfig_Preflight' -count=1
```

**Step 4: Commit**

```bash
git add internal/attractor/engine/provider_preflight.go internal/attractor/engine/provider_preflight_test.go
git commit -m "fix(attractor): unify provider preflight probe cancellation and process-group teardown"
```

---

### Task 4: Fix CXDB Autostart Process Lifecycle and Background Log FD Ownership

**Files:**
- Modify: `internal/attractor/engine/cxdb_bootstrap.go`
- Modify: `internal/attractor/engine/cxdb_bootstrap_test.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/resume.go`

**Step 1: Add failing tests first**

Add tests:
1. `TestStartBackgroundCommand_KeepsLogOpenUntilWaitCompletes`
2. `TestEnsureCXDBReady_AutostartProcessTerminatedOnContextCancel`
3. `TestEnsureCXDBReady_UIAutostartProcessTerminatedOnRunShutdown`

Assertions:
- delayed writes from child process are present in log
- autostarted process PID is terminated when startup fails/cancels
- run-scoped shutdown closes launched helper processes

Run red:

```bash
run_test_checked red "task4 red cxdb lifecycle" go test ./internal/attractor/engine -run '^Test(StartBackgroundCommand_KeepsLogOpenUntilWaitCompletes|EnsureCXDBReady_AutostartProcessTerminatedOnContextCancel|EnsureCXDBReady_UIAutostartProcessTerminatedOnRunShutdown)$' -count=1
```

**Step 2: Implement run-scoped process management**

In `cxdb_bootstrap.go`:
- keep background log file open until `cmd.Wait()` completes; close in wait goroutine
- extend `startedProcess` with stop/terminate behavior
- add explicit shutdown method on startup handle/info for managed processes
- register autostarted CXDB/UI processes as managed only when launched by Kilroy
- on timeout/cancel/fatal path during readiness wait, terminate launched process group before returning

In `run_with_config.go` and `resume.go`:
- call startup shutdown method in `defer` for run-scoped cleanup
- ensure shutdown does not touch pre-existing externally managed CXDB

**Step 3: Run green tests**

```bash
run_test_checked green "task4 green cxdb lifecycle" go test ./internal/attractor/engine -run '^Test(StartBackgroundCommand_KeepsLogOpenUntilWaitCompletes|EnsureCXDBReady_AutostartProcessTerminatedOnContextCancel|EnsureCXDBReady_UIAutostartProcessTerminatedOnRunShutdown)$' -count=1
run_test_checked green "task4 cxdb bootstrap regression" go test ./internal/attractor/engine -run '^TestEnsureCXDBReady_' -count=1
```

**Step 4: Commit**

```bash
git add internal/attractor/engine/cxdb_bootstrap.go internal/attractor/engine/cxdb_bootstrap_test.go internal/attractor/engine/run_with_config.go internal/attractor/engine/resume.go
git commit -m "fix(attractor): manage cxdb autostart process lifecycle and background log fd ownership"
```

---

### Task 5: Codify Loop-Restart Retry Semantics (Retries Reset Per Iteration)

**Files:**
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/loop_restart_test.go`
- Modify: `docs/strongdm/attractor/README.md`

**Step 1: Add failing test that defines intended semantics**

Add `TestLoopRestart_ResetsRetryBudgetPerIteration`:
- configure node with finite retries
- force loop restart to new iteration
- verify retries are re-available in restarted iteration

Run red:

```bash
run_test_checked red "task5 red retry-reset semantics" go test ./internal/attractor/engine -run '^TestLoopRestart_ResetsRetryBudgetPerIteration$' -count=1
```

**Step 2: Implement/clarify behavior**

- If behavior already matches, add explicit code comment near `loopRestart -> runLoop(..., map[string]int{}, ...)` to lock intent.
- Add progress metadata on restart (example `"retry_budget_reset": true`) to make semantics observable.

**Step 3: Run green tests**

```bash
run_test_checked green "task5 green retry-reset semantics" go test ./internal/attractor/engine -run '^TestLoopRestart_ResetsRetryBudgetPerIteration$|^TestLoopRestart_' -count=1
```

**Step 4: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/loop_restart_test.go docs/strongdm/attractor/README.md
git commit -m "docs+test(attractor): codify loop-restart retry-budget reset semantics"
```

---

### Task 6: Eliminate Test Artifact Pollution and Add Ignore Rules

**Files:**
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`
- Modify: `internal/attractor/engine/status_json_worktree_test.go`
- Modify: `internal/attractor/engine/status_json_legacy_details_test.go`
- Modify: `.gitignore`

**Step 1: Add defensive cleanup helper and red check**

Create a small test helper in the engine test package (same file or shared test helper file):

```go
func cleanupStrayEngineArtifacts(t *testing.T) {
	t.Helper()
	cwd, _ := os.Getwd()
	for _, name := range []string{"cli_wrote.txt", "status.json"} {
		_ = os.Remove(filepath.Join(cwd, name))
	}
}
```

In affected tests, call:

```go
cleanupStrayEngineArtifacts(t)
t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })
```

Run red/green around the test most likely to create artifacts:

```bash
run_test_checked green "task6 targeted artifact test" go test ./internal/attractor/engine -run '^TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB$' -count=1
git status --short | rg '(^\?\?\s+internal/attractor/engine/(cli_wrote\.txt|status\.json)$|^\?\?\s+(cli_wrote\.txt|status\.json)$)' && echo "unexpected untracked artifacts" && exit 1 || true
```

**Step 2: Add ignore rules as defense in depth**

Update `.gitignore`:

```gitignore
/internal/attractor/engine/cli_wrote.txt
/internal/attractor/engine/status.json
/cli_wrote.txt
/status.json
```

**Step 3: Full package test and verify clean status**

```bash
run_test_checked green "task6 engine package" go test ./internal/attractor/engine -count=1
git status --short
```

Expected: no newly untracked artifact files from this class.

**Step 4: Commit**

```bash
git add internal/attractor/engine/run_with_config_integration_test.go internal/attractor/engine/status_json_worktree_test.go internal/attractor/engine/status_json_legacy_details_test.go .gitignore
git commit -m "test(chore): clean stray engine artifact files and ignore defensive leftovers"
```

---

### Task 7: End-to-End Reliability Verification Pass

**Files:**
- Modify (if needed): `scripts/e2e-guardrail-matrix.sh`
- Modify docs if behavior/ops guidance changed during implementation.

**Step 1: Run targeted reliability suites**

```bash
run_test_checked green "task7 engine reliability" go test ./internal/attractor/engine -run 'TestRunWithConfig_CLIBackend_OpenAISchemaFallback|TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeys|TestRunProviderCapabilityProbe_|TestEnsureCXDBReady_|TestLoopRestart_' -count=1
run_test_checked green "task7 cmd" go test ./cmd/kilroy -count=1
```

**Step 2: Run full Attractor package tests**

```bash
run_test_checked green "task7 full attractor" go test ./internal/attractor/... -count=1
```

**Step 3: Run guardrail matrix script**

```bash
./scripts/e2e-guardrail-matrix.sh
```

Expected: all checks pass and no false greens.

**Step 4: Final commit (if step 1-3 required file tweaks)**

```bash
git add scripts/e2e-guardrail-matrix.sh docs/strongdm/attractor/README.md
# only if there are staged changes
git diff --cached --quiet || git commit -m "chore(attractor): finalize reliability follow-up verification and docs"
```

---

## Explicitly Out of Scope in This Plan

1. CXDB UI startup-path latency optimization (`discoverUIURLFromLog` critical-path tuning).
2. New failure taxonomy dimensions beyond current two classes (`transient_infra`, `deterministic`).

---

## Final Exit Criteria

1. Codex invocation contract is documented/tested without deprecated approval flags.
2. Unknown structured-output keys produce loud diagnostics + one retry + continued execution.
3. Provider capability/model probes are context-cancel-safe with process-group cleanup.
4. Autostarted CXDB/UI processes are run-scoped and cleaned up on failure/exit.
5. Loop-restart retry semantics are codified in test + docs.
6. Artifact pollution (`cli_wrote.txt`, `status.json`) is cleaned and defensively ignored.
7. `go test ./internal/attractor/...` and `go test ./cmd/kilroy` both pass.
