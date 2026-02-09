# Comprehensive Run Reliability Fix Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate all known Attractor run failure classes (session teardown loss, restart churn, branch-prefix drift, state-root drift, provider deterministic misconfig) with reproducible red/green/refactor tests.

**Architecture:** Add a first-class detached launch mode at CLI layer, strict provider/model preflight at run bootstrap, and centralized invariants for branch/state identity in engine/resume/parallel code paths. Keep loop-restart behavior class-gated (`transient_infra` only), enforce deterministic-failure circuit breaking, and guarantee terminal `final.json` on all fatal exits.

**Tech Stack:** Go 1.22, stdlib `testing`, subprocess integration tests, existing CXDB test server harness, Attractor engine tests.

---

### Task 1: Reproduce Session-Teardown Failure With a Failing CLI Test

**Files:**
- Create: `cmd/kilroy/main_detach_test.go`
- Reuse: `cmd/kilroy/main_exit_codes_test.go` helpers (`buildKilroyBinary`, `newCXDBTestServer`, `initTestRepo`)
- Test: `cmd/kilroy/main_detach_test.go`

**Step 1: Write the failing test**

```go
func TestAttractorRun_DetachedModeSurvivesLauncherExit(t *testing.T) {
	bin := buildKilroyBinary(t)
	cxdb := newCXDBTestServer(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdb.URL(), cxdb.BinaryAddr(), catalog)
	graph := filepath.Join(t.TempDir(), "g.dot")
	_ = os.WriteFile(graph, []byte(`
	digraph G {
	  start [shape=Mdiamond]
	  t [shape=parallelogram, tool_command="sleep 1"]
	  exit [shape=Msquare]
	  start -> t -> exit
	}`), 0o644)
	logs := filepath.Join(t.TempDir(), "logs")

	cmd := exec.Command(bin, "attractor", "run", "--detach", "--graph", graph, "--config", cfg, "--run-id", "detach-smoke", "--logs-root", logs)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detached launch failed: %v\n%s", err, out)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(logs, "final.json")); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("final.json not written in detached run")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/kilroy -run TestAttractorRun_DetachedModeSurvivesLauncherExit -count=1`
Expected: FAIL with `unknown arg: --detach`.

**Step 3: Write minimal implementation placeholder to parse `--detach` (still red for now)**

```go
// in attractorRun arg parse
case "--detach":
	detach = true
```

**Step 4: Re-run test**

Run: `go test ./cmd/kilroy -run TestAttractorRun_DetachedModeSurvivesLauncherExit -count=1`
Expected: still FAIL (launch path not implemented yet).

**Step 5: Commit**

```bash
git add cmd/kilroy/main_detach_test.go cmd/kilroy/main.go
git commit -m "test(cli): reproduce run teardown failure with missing detached mode"
```

---

### Task 2: Implement First-Class Detached Launch Path (`--detach`)

**Files:**
- Modify: `cmd/kilroy/main.go`
- Create: `cmd/kilroy/run_detach.go`
- Test: `cmd/kilroy/main_detach_test.go`

**Step 1: Write a second failing test for pid metadata**

```go
func TestAttractorRun_DetachedWritesPIDFile(t *testing.T) {
	// same setup as previous test
	// assert logs/run.pid exists and contains numeric pid
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/kilroy -run TestAttractorRun_DetachedWritesPIDFile -count=1`
Expected: FAIL (`run.pid` missing).

**Step 3: Implement detached launcher**

```go
// cmd/kilroy/run_detach.go
func launchDetached(args []string, logsRoot string) error {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdin = nil
	outPath := filepath.Join(logsRoot, "run.out")
	_ = os.MkdirAll(logsRoot, 0o755)
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil { return err }
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil { return err }
	pidPath := filepath.Join(logsRoot, "run.pid")
	return os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
}
```

```go
// cmd/kilroy/main.go
if detach {
	childArgs := append([]string{"attractor", "run"}, filterDetachArg(args)...) // no --detach in child
	if err := launchDetached(childArgs, logsRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("detached=true\nlogs_root=%s\npid_file=%s\n", logsRoot, filepath.Join(logsRoot, "run.pid"))
	os.Exit(0)
}
```

**Step 4: Run tests to verify green**

Run:
- `go test ./cmd/kilroy -run TestAttractorRun_DetachedModeSurvivesLauncherExit -count=1`
- `go test ./cmd/kilroy -run TestAttractorRun_DetachedWritesPIDFile -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/kilroy/main.go cmd/kilroy/run_detach.go cmd/kilroy/main_detach_test.go
git commit -m "feat(cli): add detached attractor run mode with pid/log metadata"
```

---

### Task 3: Reproduce Provider Misconfiguration as Deterministic Preflight Failure

**Files:**
- Create: `internal/attractor/engine/provider_preflight_test.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/codergen_router.go` (shared model normalization helper if needed)
- Test: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Write failing tests**

```go
func TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider(t *testing.T) {
	// graph has box node llm_provider=google llm_model=gemini-3-pro (no exact catalog match)
	// cfg sets google backend=cli
	// expect RunWithConfig returns config/preflight error before node execution
}

func TestRunWithConfig_AllowsCLIModel_WhenCatalogHasProviderMatch(t *testing.T) {
	// llm_model=gemini-3-pro-preview (catalog-supported)
	// expect preflight passes (run may continue)
}
```

**Step 2: Run tests to verify red**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider -count=1`
Expected: FAIL (currently late runtime failure, no preflight error).

**Step 3: Implement preflight**

```go
func validateProviderModelPairs(g *model.Graph, cfg *RunConfigFile, catalog *modeldb.LiteLLMCatalog) error {
	for _, n := range g.Nodes {
		if n == nil || n.Shape() != "box" { continue }
		prov := normalizeProviderKey(n.Attr("llm_provider", ""))
		mid := strings.TrimSpace(n.Attr("llm_model", ""))
		if prov == "" || mid == "" { continue }
		if backendFor(cfg, prov) != BackendCLI { continue }
		if !catalogHasProviderModel(catalog, prov, mid) {
			return fmt.Errorf("preflight: llm_provider=%s backend=cli model=%s not present in run catalog", prov, mid)
		}
	}
	return nil
}
```

Call immediately after catalog load in `RunWithConfig` (before any stage run).

**Step 4: Run tests to verify green**

Run:
- `go test ./internal/attractor/engine -run TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider -count=1`
- `go test ./internal/attractor/engine -run TestRunWithConfig_AllowsCLIModel_WhenCatalogHasProviderMatch -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_preflight_test.go internal/attractor/engine/run_with_config.go internal/attractor/engine/codergen_router.go
git commit -m "feat(engine): add provider/model preflight for CLI backends"
```

---

### Task 4: Reproduce Branch Prefix Drift on Parallel+Resume and Fix with Centralized Builders

**Files:**
- Modify/Create: `internal/attractor/engine/resume_parallel_branch_prefix_test.go`
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/engine/branch_names.go`

**Step 1: Write/strengthen failing test**

```go
func TestResume_ParallelBranchNamesUseConfiguredPrefix(t *testing.T) {
	// force resume before parallel fanout
	// assert branch_name starts with "attractor/run/parallel/"
	// assert branch_name never starts with "/parallel/"
}
```

**Step 2: Run test to verify red**

Run: `go test ./internal/attractor/engine -run TestResume_ParallelBranchNamesUseConfiguredPrefix -count=1`
Expected: FAIL on buggy path.

**Step 3: Implement centralized branch builders**

```go
func buildRunBranch(prefix, runID string) string {
	return strings.TrimSuffix(strings.TrimSpace(prefix), "/") + "/" + strings.TrimSpace(runID)
}

func buildParallelBranch(prefix, runID, fanNodeID, childNodeID string) string {
	return buildRunBranch(prefix, "parallel/"+runID+"/"+fanNodeID+"/"+childNodeID)
}
```

Replace ad-hoc branch string concatenation in engine + parallel + resume code.

**Step 4: Run tests to verify green**

Run: `go test ./internal/attractor/engine -run 'TestResume_ParallelBranchNamesUseConfiguredPrefix|TestRun_LoopRestartLimitExceeded_WritesTerminalFinalJSON' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/branch_names.go internal/attractor/engine/parallel_handlers.go internal/attractor/engine/engine.go internal/attractor/engine/resume_parallel_branch_prefix_test.go
git commit -m "fix(engine): centralize run/parallel branch naming invariants"
```

---

### Task 5: Reproduce State-Root Path Drift on Restart/Resume and Fix Canonical Path Invariant

**Files:**
- Modify/Create: `internal/attractor/engine/resume_from_restart_dir_test.go`
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/resume.go`

**Step 1: Write failing tests**

```go
func TestResume_RestoresAbsoluteStateRootForCodex(t *testing.T) {
	// simulate checkpoint from restart dir
	// assert restored state_root/CODEX_HOME is absolute and under restart logs
}

func TestCodexCLIInvocation_StateRootIsAbsolute(t *testing.T) {
	// verify cli_invocation.json state_root is absolute path
}
```

**Step 2: Run test to verify red**

Run: `go test ./internal/attractor/engine -run 'TestResume_RestoresAbsoluteStateRootForCodex|TestCodexCLIInvocation_StateRootIsAbsolute' -count=1`
Expected: FAIL on relative-path behavior.

**Step 3: Implement canonicalization**

```go
absStageDir, err := filepath.Abs(stageDir)
if err != nil { return nil, nil, err }
stateRoot := filepath.Join(absStageDir, "codex-home", ".codex")
```

Persist/restore absolute paths only; reject relative state roots at resume boundaries.

**Step 4: Run tests to verify green**

Run: `go test ./internal/attractor/engine -run 'TestResume_RestoresAbsoluteStateRootForCodex|TestCodexCLIInvocation_StateRootIsAbsolute' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/resume.go internal/attractor/engine/resume_from_restart_dir_test.go
git commit -m "fix(engine): enforce absolute codex state-root invariants across resume"
```

---

### Task 6: Reproduce Loop-Restart Runaway and Verify Deterministic Block + Circuit Breaker

**Files:**
- Modify/Create: `internal/attractor/engine/loop_restart_guardrails_test.go`
- Modify: `internal/attractor/engine/loop_restart_policy.go`
- Modify: `internal/attractor/engine/engine.go`

**Step 1: Write failing tests (both classes)**

```go
func TestRun_LoopRestartBlockedForDeterministicFailureClass(t *testing.T) {
	// tool node: "exit 1"
	// check -> work loop_restart=true
	// expect immediate block, no restart-1 dir
}

func TestRun_LoopRestartCircuitBreakerOnRepeatedTransientSignature(t *testing.T) {
	// tool node: timeout="1s", command="sleep 2"
	// expect restart-1 then circuit-break at signature limit 2
}
```

**Step 2: Run tests to verify red**

Run: `go test ./internal/attractor/engine -run 'TestRun_LoopRestartBlockedForDeterministicFailureClass|TestRun_LoopRestartCircuitBreakerOnRepeatedTransientSignature' -count=1`
Expected: FAIL on old generic restart behavior.

**Step 3: Implement/finish policy wiring**

```go
if isFailureLoopRestartOutcome(out) && normalizedFailureClassOrDefault(failureClass) != failureClassTransientInfra {
	return fmt.Errorf("loop_restart blocked: failure_class=%s ...", failureClass)
}
// track signature counts and abort when count >= limit
```

Emit progress events: `loop_restart_blocked`, `loop_restart_signature`, `loop_restart_circuit_breaker`.

**Step 4: Run tests to verify green**

Run: `go test ./internal/attractor/engine -run 'TestRun_LoopRestartBlockedForDeterministicFailureClass|TestRun_LoopRestartCircuitBreakerOnRepeatedTransientSignature' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/loop_restart_guardrails_test.go internal/attractor/engine/loop_restart_policy.go internal/attractor/engine/engine.go
git commit -m "fix(engine): gate loop_restart by failure class and enforce signature circuit breaker"
```

---

### Task 7: Reproduce Missing Terminal Finalization and Guarantee `final.json` on Fatal Paths

**Files:**
- Modify/Create: `internal/attractor/engine/loop_restart_test.go`
- Modify/Create: `internal/attractor/engine/resume_from_restart_dir_test.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/resume.go`
- Modify: `internal/attractor/runtime/final.go`

**Step 1: Write failing tests**

```go
func TestRun_LoopRestartLimitExceeded_WritesTerminalFinalJSON(t *testing.T) {
	// expect final.json at base logs root and current restart root
}

func TestResume_FatalBeforeEngineInit_WritesFallbackFinalJSON(t *testing.T) {
	// force resume bootstrap failure; expect logs_root/final.json with status=fail
}
```

**Step 2: Run tests to verify red**

Run: `go test ./internal/attractor/engine -run 'TestRun_LoopRestartLimitExceeded_WritesTerminalFinalJSON|TestResume_FatalBeforeEngineInit_WritesFallbackFinalJSON' -count=1`
Expected: FAIL on missing finalization path(s).

**Step 3: Implement unified fatal persistence**

```go
func (e *Engine) persistFatalOutcome(ctx context.Context, runErr error) {
	final := runtime.FinalOutcome{Status: runtime.StatusFail, RunID: e.Options.RunID, FailureReason: runErr.Error(), ...}
	e.persistTerminalOutcome(ctx, final)
}
```

Use defer in `run()` and `resumeFromLogsRoot()`; mirror to base logs root when restart logs root differs.

**Step 4: Run tests to verify green**

Run: `go test ./internal/attractor/engine -run 'TestRun_LoopRestartLimitExceeded_WritesTerminalFinalJSON|TestResume_FatalBeforeEngineInit_WritesFallbackFinalJSON' -count=1`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/resume.go internal/attractor/runtime/final.go internal/attractor/engine/loop_restart_test.go internal/attractor/engine/resume_from_restart_dir_test.go
git commit -m "fix(engine): always persist terminal final.json on fatal paths"
```

---

### Task 8: Refactor + End-to-End Verification Matrix (Red/Green Gate)

**Files:**
- Modify: `docs/strongdm/attractor/README.md`
- Modify: `AGENTS.md`
- Create: `scripts/e2e-guardrail-matrix.sh`

**Step 1: Write failing e2e matrix script checks**

```bash
#!/usr/bin/env bash
set -euo pipefail
# scenario 1 deterministic fail -> loop_restart_blocked + final.json present
# scenario 2 transient timeout -> circuit breaker + final.json in base + restart
# scenario 3 detached launch -> run.pid exists and final.json eventually appears
```

**Step 2: Run script to capture current failures**

Run: `bash scripts/e2e-guardrail-matrix.sh`
Expected: FAIL until all previous tasks are complete.

**Step 3: Finalize docs + monitoring guidance**

Add docs for:
- detached launch command
- monitoring (`tail -f <logs_root>/progress.ndjson`, `cat <logs_root>/final.json`)
- restart artifact locations

**Step 4: Run full verification**

Run:
- `go test ./cmd/kilroy -count=1`
- `go test ./internal/attractor/engine -count=1`
- `go test ./internal/attractor/runtime -count=1`
- `bash scripts/e2e-guardrail-matrix.sh`
Expected: all PASS.

**Step 5: Commit**

```bash
git add docs/strongdm/attractor/README.md AGENTS.md scripts/e2e-guardrail-matrix.sh
git commit -m "docs+e2e: add guardrail runbook and end-to-end reliability matrix"
```

---

### Task 9: Real CXDB DTTF Validation Run (Post-Green)

**Files:**
- No code changes required.

**Step 1: Start clean run root**

Run:

```bash
RUN_ROOT="/tmp/kilroy-dttf-real-cxdb-$(date -u +%Y%m%dT%H%M%SZ)-postfix"
```

**Step 2: Launch with detached mode (new behavior)**

Run:

```bash
./kilroy attractor run --detach --graph "$RUN_ROOT/repo/demo/dttf/dttf.dot" --config "$RUN_ROOT/run_config.json" --run-id dttf-postfix --logs-root "$RUN_ROOT/logs"
```

**Step 3: Monitor**

Run:

```bash
tail -f "$RUN_ROOT/logs/progress.ndjson"
```

Expected:
- no invalid branch names (`/parallel/...`)
- restart behavior only for transient failures
- deterministic failures block restart quickly

**Step 4: Validate terminal artifacts**

Run:

```bash
cat "$RUN_ROOT/logs/final.json"
```

Expected:
- terminal status present
- `failure_reason` explicit if failed
- CXDB ids present (`cxdb_context_id`, `cxdb_head_turn_id`)

**Step 5: Commit validation note**

```bash
# if you keep run notes in docs/plans or a run log file, commit that evidence
```

