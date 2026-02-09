# Codex CLI State Isolation and Watchdog Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Attractor Codex CLI stages deterministic by isolating Codex runtime state per stage, adding process-group watchdog teardown, and retrying once on known Codex state-db discrepancy errors.

**Architecture:** Refactor `runCLI()` to execute through a small `codex`-specific runner that builds an isolated env rooted in the stage directory, starts the process in its own process group, and enforces idle-timeout cleanup. Add a targeted one-shot retry path for `state db missing rollout path` / `record_discrepancy` stderr signatures. Keep behavior provider-scoped (OpenAI/Codex) and preserve existing schema-fallback behavior.

**Tech Stack:** Go (`os/exec`, `syscall`, `context`, `time`), existing Attractor engine integration tests, shell-backed fake CLI scripts in tests.

---

## Context for the Implementer

### Why this exists

The failed run at `/tmp/kilroy-runs/dttf-20260207-200529` stalled in `impl_loader` after:

- repeated Codex stderr: `state db missing rollout path for thread ...`
- no `status.json`, `cli_timing.json`, or `output.json` in stage dir
- no `stage_attempt_end`, therefore no `final.json`

Current `runCLI()` behavior in `internal/attractor/engine/codergen_router.go`:

- uses `exec.CommandContext(...)` with inherited environment
- records `env_mode: "inherit"` in `cli_invocation.json`
- does not isolate Codex state per stage
- does not create a process group or idle watchdog

### Current files you will touch

- `internal/attractor/engine/codergen_router.go`
- `internal/attractor/engine/run_with_config_integration_test.go`
- `internal/attractor/engine/codergen_schema_test.go`
- `internal/attractor/engine/codergen_cli_invocation_test.go`
- `docs/strongdm/attractor/README.md`

### New files you will add

- `internal/attractor/engine/codergen_process_test.go`

---

### Task 1: Add failing tests for env isolation and state-db retry metadata

**Files:**
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`
- Modify: `internal/attractor/engine/codergen_schema_test.go`

**Step 1: Add a failing assertion for isolated env metadata**

In `TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB`, replace the old expectation:

```go
if inv["env_mode"] != "inherit" { ... }
```

with new expectations:

```go
if inv["env_mode"] != "isolated" {
	t.Fatalf("env_mode: got %v want isolated", inv["env_mode"])
}
if strings.TrimSpace(anyToString(inv["env_scope"])) != "codex" {
	t.Fatalf("env_scope: %#v", inv["env_scope"])
}
if _, ok := inv["state_root"]; !ok {
	t.Fatalf("state_root missing: %#v", inv)
}
```

**Step 2: Add a new failing integration test for state-db retry**

Append a new test to `codergen_schema_test.go`:

```go
func TestRunWithConfig_CLIBackend_OpenAIStateDBFallbackRetry(t *testing.T) {
	// fake codex script:
	// first invocation -> write stderr "state db missing rollout path..." and exit 1
	// second invocation -> write status.json success + output json and exit 0
	// assert: run succeeds and cli_invocation.json records state_db_fallback_retry=true
}
```

Use a sentinel file in `t.TempDir()` to distinguish first vs second invocation.

**Step 3: Run tests to verify failures**

Run:

```bash
go test ./internal/attractor/engine -run 'TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB|TestRunWithConfig_CLIBackend_OpenAIStateDBFallbackRetry' -v
```

Expected:

- env metadata test fails (still `inherit`)
- retry test fails (no state-db fallback path yet)

**Step 4: Commit failing tests**

```bash
git add internal/attractor/engine/run_with_config_integration_test.go internal/attractor/engine/codergen_schema_test.go
git commit -m "test(attractor): add failing coverage for codex env isolation and state-db retry metadata"
```

---

### Task 2: Implement Codex state isolation helpers and invocation metadata

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`

**Step 1: Add helper to construct isolated Codex env**

Add:

```go
func buildCodexIsolatedEnv(stageDir string) ([]string, map[string]any, error) {
	// create:
	//   {stageDir}/codex-home
	//   {stageDir}/codex-home/.codex
	//   {stageDir}/codex-home/.config
	//   {stageDir}/codex-home/.local/share
	//   {stageDir}/codex-home/.local/state
	//
	// seed auth/config best-effort from current HOME/.codex/{auth.json,config.toml}
	// return env overrides:
	//   HOME
	//   CODEX_HOME
	//   XDG_CONFIG_HOME
	//   XDG_DATA_HOME
	//   XDG_STATE_HOME
}
```

Copy with best-effort semantics (missing source files is allowed; copy errors are surfaced in metadata but do not fail stage setup).

**Step 2: Wire helper into `runCLI()` for provider=openai only**

In `runCLI()` before writing `cli_invocation.json`:

- Build `inv` metadata with:
  - `env_mode: "isolated"` for OpenAI
  - `env_scope: "codex"`
  - `state_root: "<stageDir>/codex-home/.codex"`
  - `env_seeded_files: [...]`
- Keep existing behavior for non-OpenAI providers (`env_mode: "inherit"`, `env_allowlist: ["*"]`).

**Step 3: Pass env into command execution**

When running `exec.CommandContext(...)`, set:

```go
cmd.Env = isolatedEnv // for openai
```

For non-openai, leave `cmd.Env` unset (inherit).

**Step 4: Run updated tests**

Run:

```bash
go test ./internal/attractor/engine -run TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/run_with_config_integration_test.go
git commit -m "feat(attractor): isolate codex CLI state per stage and record env metadata"
```

---

### Task 3: Add process-group runner and idle-timeout watchdog

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Create: `internal/attractor/engine/codergen_process_test.go`

**Step 1: Add failing watchdog test**

Create `codergen_process_test.go` with:

```go
func TestRunWithConfig_CLIBackend_OpenAIIdleTimeoutKillsProcessGroup(t *testing.T) {
	// fake codex:
	// - starts a child process with unique marker (sleep loop)
	// - writes one line to stderr
	// - then hangs forever
	//
	// set env: KILROY_CODEX_IDLE_TIMEOUT=2s, KILROY_CODEX_KILL_GRACE=200ms
	// run a single openai stage
	// assert stage fails with failure_reason mentioning idle timeout
	// assert child marker process no longer exists after run
}
```

Use `pgrep -f <marker>` or `ps`-based check in test helper to confirm cleanup.

**Step 2: Refactor command execution from `cmd.Run()` to start/wait monitor**

In `runCLI()`, replace `runOnce` implementation with:

```go
cmd := exec.CommandContext(ctx, exe, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
...
if err := cmd.Start(); err != nil { ... }
waitCh := make(chan error, 1)
go func() { waitCh <- cmd.Wait() }()
```

**Step 3: Add idle watchdog loop**

Implement helper:

```go
func waitWithIdleWatchdog(ctx context.Context, cmd *exec.Cmd, stdoutPath, stderrPath string, idleTimeout, killGrace time.Duration) (runErr error, timedOut bool, err error)
```

Behavior:

- Poll file mtimes/sizes every ~250ms
- Reset idle clock on output growth
- On idle timeout:
  - SIGTERM process group (`kill(-pgid, SIGTERM)`)
  - wait `killGrace`
  - SIGKILL process group
  - return error that includes `idle timeout`

**Step 4: Make idle timeout configurable for tests**

Add tiny helpers in `codergen_router.go`:

```go
func codexIdleTimeout() time.Duration   // default 2m, env override KILROY_CODEX_IDLE_TIMEOUT
func codexKillGrace() time.Duration     // default 2s, env override KILROY_CODEX_KILL_GRACE
```

Use `time.ParseDuration` for overrides.

**Step 5: Run tests**

Run:

```bash
go test ./internal/attractor/engine -run TestRunWithConfig_CLIBackend_OpenAIIdleTimeoutKillsProcessGroup -v
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_process_test.go
git commit -m "feat(attractor): enforce codex idle watchdog with process-group teardown"
```

---

### Task 4: Add one-shot retry for Codex state-db discrepancies

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/codergen_schema_test.go`

**Step 1: Add signature detector**

Implement:

```go
func isStateDBDiscrepancy(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "state db missing rollout path") ||
		strings.Contains(s, "state db record_discrepancy")
}
```

**Step 2: Add one-shot retry branch for OpenAI failures**

After first failed run and before returning failure:

- Read `stderr.log`
- if OpenAI + isolated env + signature matched:
  - copy first-attempt logs to:
    - `stdout.state_db_failure.log`
    - `stderr.state_db_failure.log`
  - rebuild fresh isolated env rooted at:
    - `{stageDir}/codex-home-retry1`
  - rerun once
  - set metadata in `cli_invocation.json`:
    - `state_db_fallback_retry: true`
    - `state_db_fallback_reason: "state_db_record_discrepancy"`
    - `state_db_retry_state_root: ...`

**Step 3: Ensure retry composes with schema fallback path**

Guard ordering:

1. run attempt
2. schema fallback (existing behavior)
3. state-db fallback retry (new behavior)

Do not allow unbounded retries.

**Step 4: Run tests**

Run:

```bash
go test ./internal/attractor/engine -run 'TestRunWithConfig_CLIBackend_OpenAISchemaFallback|TestRunWithConfig_CLIBackend_OpenAIStateDBFallbackRetry' -v
```

Expected: both PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_schema_test.go
git commit -m "feat(attractor): retry codex stage once on state-db discrepancy signatures"
```

---

### Task 5: Update compatibility tests and README runbook notes

**Files:**
- Modify: `internal/attractor/engine/codergen_cli_invocation_test.go`
- Modify: `internal/attractor/engine/run_with_config_integration_test.go`
- Modify: `docs/strongdm/attractor/README.md`

**Step 1: Update CLI invocation expectations**

Keep existing assertions for:

- no deprecated `--ask-for-approval`
- strict schema output behavior

Add a lightweight metadata expectation test if needed:

```go
// openai cli invocation should now record isolated env_mode in integration artifacts
```

**Step 2: Update runbook notes**

In `README.md` add bullets:

- OpenAI Codex CLI uses stage-local isolated state (`env_mode=isolated`, `env_scope=codex`)
- Idle watchdog enforces process-group cleanup
- One-shot fallback retry on state-db discrepancy signatures

**Step 3: Run doc/test validation**

Run:

```bash
go test ./internal/attractor/engine -run 'TestDefaultCLIInvocation|TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB' -v
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/attractor/engine/codergen_cli_invocation_test.go internal/attractor/engine/run_with_config_integration_test.go docs/strongdm/attractor/README.md
git commit -m "docs/tests(attractor): document codex isolation+watchdog and align invocation tests"
```

---

### Task 6: Full verification pass and cleanup

**Files:**
- No new code; verification artifacts only

**Step 1: Run focused engine suite**

```bash
go test ./internal/attractor/engine -v
```

Expected: PASS.

**Step 2: Run broader attractor tests**

```bash
go test ./internal/attractor/... -v
```

Expected: PASS.

**Step 3: Smoke run config path**

Run one integration test that exercises `RunWithConfig` end-to-end:

```bash
go test ./internal/attractor/engine -run TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB -v
```

Expected: PASS, artifacts include `cli_invocation.json`, `status.json`, `final.json`.

**Step 4: Final commit (if needed for cleanup-only deltas)**

```bash
git add -A
git commit -m "chore(attractor): finalize codex isolation and watchdog verification updates"
```

(Skip if no remaining changes.)

---

## Rollout Notes

- This is behavior-hardening for OpenAI CLI backend only.
- If an emergency rollback is needed, set:
  - `KILROY_CODEX_IDLE_TIMEOUT=0` to disable idle watchdog temporarily.
- Do not disable state isolation in production unless actively debugging.

