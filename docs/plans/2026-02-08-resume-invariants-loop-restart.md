# Resume Invariants And Loop-Restart Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix resume so parallel fan-out and loop restart behave identically to fresh runs, eliminating invalid git refs, relative restart paths, and restart storms.

**Architecture:** Remove resume/run drift by restoring all required engine invariants at resume time (`RunOptions`, branch prefix, restart bookkeeping). Persist restart bookkeeping in checkpoint metadata so resume can faithfully continue mid-restart. Add fail-fast validation around branch ref construction and restart path derivation to surface config/state defects immediately.

**Tech Stack:** Go 1.22, internal Attractor engine (`internal/attractor/engine`), runtime checkpointing (`internal/attractor/runtime`), git worktree/ref orchestration (`internal/attractor/gitutil`), Go test tooling.

**Execution Skill:** `@executing-plans`

---

### Task 1: Reproduce And Lock In Parallel Branch Prefix Regression

**Files:**
- Create: `internal/attractor/engine/resume_parallel_branch_prefix_test.go`
- Modify: `internal/attractor/engine/resume.go`
- Test: `internal/attractor/engine/resume_parallel_branch_prefix_test.go`

**Step 1: Write the failing test**

```go
package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestResume_ParallelBranchNamesUseConfiguredPrefix(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
	digraph P {
	  graph [goal="test"]
	  start [shape=Mdiamond]
	  par [shape=component]
	  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
	  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
	  join [shape=tripleoctagon]
	  exit [shape=Msquare]
	  start -> par
	  par -> a
	  par -> b
	  a -> join
	  b -> join
	  join -> exit
	}
	`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunBranchPrefix: "attractor/run"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "start"
	cp.CompletedNodes = []string{"start"}
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	_, err = Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	for _, row := range rows {
		got := strings.TrimSpace(anyToString(row["branch_name"]))
		if strings.HasPrefix(got, "/parallel/") {
			t.Fatalf("invalid branch namespace after resume: %q", got)
		}
		if !strings.HasPrefix(got, "attractor/run/parallel/") {
			t.Fatalf("expected attractor/run/parallel prefix, got %q", got)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResume_ParallelBranchNamesUseConfiguredPrefix -v`
Expected: FAIL with branch names like `"/parallel/..."` or git error `not a valid branch name`.

**Step 3: Write minimal implementation**

Add explicit resume run-option reconstruction and branch-prefix derivation in `internal/attractor/engine/resume.go`:

```go
func deriveRunBranchPrefix(m *manifest, cfg *RunConfigFile) string {
	if cfg != nil {
		if p := strings.TrimSpace(cfg.Git.RunBranchPrefix); p != "" {
			return p
		}
	}
	rb := strings.TrimSpace(m.RunBranch)
	rid := strings.TrimSpace(m.RunID)
	if rb != "" && rid != "" {
		suffix := "/" + rid
		if strings.HasSuffix(rb, suffix) {
			return strings.TrimSuffix(rb, suffix)
		}
	}
	return ""
}
```

Then build `RunOptions` in resume from manifest/config, apply defaults, and fail fast when prefix cannot be recovered:

```go
prefix := deriveRunBranchPrefix(m, cfg)
opts := RunOptions{
	RepoPath:        m.RepoPath,
	RunID:           m.RunID,
	LogsRoot:        logsRoot,
	WorktreeDir:     filepath.Join(logsRoot, "worktree"),
	RunBranchPrefix: prefix,
	RequireClean:    true,
}
if err := opts.applyDefaults(); err != nil {
	return nil, err
}
if strings.TrimSpace(prefix) == "" {
	return nil, fmt.Errorf("resume: unable to derive run_branch_prefix from manifest/config")
}
```

Use `opts` for `Engine.Options` (instead of inline partial struct).

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestResume_ParallelBranchNamesUseConfiguredPrefix -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/resume.go internal/attractor/engine/resume_parallel_branch_prefix_test.go
git commit -m "fix: restore run branch prefix invariants on resume"
```

---

### Task 2: Reproduce And Fix Resume Loop-Restart Base Logs Root Drift

**Files:**
- Create: `internal/attractor/engine/resume_loop_restart_state_test.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/resume.go`
- Test: `internal/attractor/engine/resume_loop_restart_state_test.go`

**Step 1: Write the failing test**

```go
func TestResume_LoopRestartUsesBaseLogsRoot(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
	digraph G {
	  graph [goal="resume-restart", max_restarts="1"]
	  start [shape=Mdiamond]
	  check [shape=diamond]
	  work  [shape=parallelogram, tool_command="/bin/bash -lc 'exit 1'"]
	  exit  [shape=Msquare]
	  start -> check
	  check -> exit [condition="outcome=success"]
	  check -> work [condition="outcome=fail", loop_restart=true]
	  work -> check
	}
	`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunBranchPrefix: "attractor/run"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Force resume to route from check -> work(loop_restart=true).
	_ = os.WriteFile(filepath.Join(res.LogsRoot, "check", "status.json"), []byte(`{"status":"fail","failure_reason":"forced"}`), 0o644)
	cp, err := runtime.LoadCheckpoint(filepath.Join(res.LogsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "check"
	cp.CompletedNodes = []string{"start", "check"}
	if err := cp.Save(filepath.Join(res.LogsRoot, "checkpoint.json")); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	_, err = Resume(ctx, res.LogsRoot)
	if err == nil || !strings.Contains(err.Error(), "loop_restart limit exceeded") {
		t.Fatalf("expected loop_restart limit error, got: %v", err)
	}

	restartDir := filepath.Join(res.LogsRoot, "restart-1")
	if _, err := os.Stat(restartDir); err != nil {
		t.Fatalf("expected restart dir under logs root: %v", err)
	}

	if _, err := os.Stat("restart-1"); err == nil {
		t.Fatalf("unexpected relative restart-1 dir in process CWD")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResume_LoopRestartUsesBaseLogsRoot -v`
Expected: FAIL with missing `<logsRoot>/restart-1` and/or unexpected relative `restart-1` creation.

**Step 3: Write minimal implementation**

Persist and restore restart state in checkpoint metadata.

In `internal/attractor/engine/engine.go` `checkpoint(...)`:

```go
if cp.Extra == nil {
	cp.Extra = map[string]any{}
}
cp.Extra["base_logs_root"] = e.baseLogsRoot
cp.Extra["restart_count"] = e.restartCount
```

In `internal/attractor/engine/resume.go`, restore before `runLoop`:

```go
func restoreRestartState(logsRoot string, cp *runtime.Checkpoint) (string, int) {
	base := strings.TrimSpace(logsRoot)
	restarts := 0
	if cp != nil && cp.Extra != nil {
		if v := strings.TrimSpace(fmt.Sprint(cp.Extra["base_logs_root"])); v != "" {
			base = v
		}
		if n, ok := anyToInt(cp.Extra["restart_count"]); ok && n >= 0 {
			restarts = n
		}
	}
	if m := restartSuffixRE.FindStringSubmatch(filepath.Base(logsRoot)); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			if restarts == 0 || n > restarts {
				restarts = n
			}
			if base == logsRoot {
				base = filepath.Dir(logsRoot)
			}
		}
	}
	return base, restarts
}
```

Then set:

```go
eng.baseLogsRoot, eng.restartCount = restoreRestartState(logsRoot, cp)
eng.baseSHA = cp.GitCommitSHA
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestResume_LoopRestartUsesBaseLogsRoot -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine.go internal/attractor/engine/resume.go internal/attractor/engine/resume_loop_restart_state_test.go
git commit -m "fix: persist and restore loop restart state across resume"
```

---

### Task 3: Centralize Engine Bootstrap To Remove Resume/Run Drift

**Files:**
- Create: `internal/attractor/engine/engine_bootstrap.go`
- Modify: `internal/attractor/engine/engine.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/resume.go`
- Test: `internal/attractor/engine/resume_test.go`

**Step 1: Write the failing test**

Add a focused invariant test:

```go
func TestResume_EngineOptionsAreFullyHydrated(t *testing.T) {
	// Build a run, resume it from checkpoint, and assert the resumed engine-derived
	// artifacts include the configured branch namespace and absolute logs/worktree paths.
	// (Use existing resume test setup pattern.)
}
```

Use observable assertions from artifacts (manifest + parallel results + checkpoint) rather than private fields.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResume_EngineOptionsAreFullyHydrated -v`
Expected: FAIL prior to refactor.

**Step 3: Write minimal implementation**

Create a shared constructor for common engine fields:

```go
func newBaseEngine(g *model.Graph, dotSource []byte, opts RunOptions) *Engine {
	e := &Engine{
		Graph:       g,
		Options:     opts,
		DotSource:   append([]byte{}, dotSource...),
		LogsRoot:    opts.LogsRoot,
		WorktreeDir: opts.WorktreeDir,
		Context:     runtime.NewContext(),
		Registry:    NewDefaultRegistry(),
		Interviewer: &AutoApproveInterviewer{},
	}
	e.RunBranch = fmt.Sprintf("%s/%s", opts.RunBranchPrefix, opts.RunID)
	return e
}
```

Then use it in:
- `Run(...)`
- `RunWithConfig(...)`
- `resumeFromLogsRoot(...)`

Set backend/CXDB/config-specific fields immediately after construction.

**Step 4: Run tests to verify they pass**

Run:
- `go test ./internal/attractor/engine -run TestResume_EngineOptionsAreFullyHydrated -v`
- `go test ./internal/attractor/engine -run TestResume_FromCheckpoint_RewindsBranchAndContinues -v`
- `go test ./internal/attractor/engine -run TestRun_ParallelFanOutAndFanIn_FastForwardsWinner -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/engine_bootstrap.go internal/attractor/engine/engine.go internal/attractor/engine/run_with_config.go internal/attractor/engine/resume.go internal/attractor/engine/resume_test.go
git commit -m "refactor: share engine bootstrap across run and resume"
```

---

### Task 4: Add Fail-Fast Guardrails For Invalid Parallel Refs And Restart Roots

**Files:**
- Modify: `internal/attractor/engine/parallel_handlers.go`
- Modify: `internal/attractor/engine/engine.go`
- Create: `internal/attractor/engine/parallel_guardrails_test.go`
- Create: `internal/attractor/engine/loop_restart_guardrails_test.go`

**Step 1: Write the failing tests**

```go
func TestParallelHandler_FailsFastOnEmptyRunBranchPrefix(t *testing.T) {
	// Build execution with empty RunBranchPrefix and assert failure_reason is explicit,
	// without attempting git branch creation.
}

func TestLoopRestart_FailsFastOnEmptyBaseLogsRoot(t *testing.T) {
	// Call loopRestart on an engine with empty baseLogsRoot and assert descriptive error.
}
```

**Step 2: Run tests to verify they fail**

Run:
- `go test ./internal/attractor/engine -run TestParallelHandler_FailsFastOnEmptyRunBranchPrefix -v`
- `go test ./internal/attractor/engine -run TestLoopRestart_FailsFastOnEmptyBaseLogsRoot -v`

Expected: FAIL.

**Step 3: Write minimal implementation**

In `parallel_handlers.go` before constructing `branchName`:

```go
prefix := strings.TrimSpace(exec.Engine.Options.RunBranchPrefix)
if prefix == "" {
	return parallelBranchResult{
		BranchKey: key,
		Error:     "parallel fan-out requires non-empty run_branch_prefix",
		Outcome:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "parallel fan-out requires non-empty run_branch_prefix"},
	}
}
```

In `engine.go` `loopRestart(...)`:

```go
if strings.TrimSpace(e.baseLogsRoot) == "" {
	return nil, fmt.Errorf("loop_restart: base logs root is empty (resume invariants not restored)")
}
```

**Step 4: Run tests to verify they pass**

Run:
- `go test ./internal/attractor/engine -run TestParallelHandler_FailsFastOnEmptyRunBranchPrefix -v`
- `go test ./internal/attractor/engine -run TestLoopRestart_FailsFastOnEmptyBaseLogsRoot -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/parallel_handlers.go internal/attractor/engine/engine.go internal/attractor/engine/parallel_guardrails_test.go internal/attractor/engine/loop_restart_guardrails_test.go
git commit -m "fix: add guardrails for parallel refs and loop restart roots"
```

---

### Task 5: Full Verification Sweep And Cleanup

**Files:**
- Modify: `internal/attractor/engine/resume.go` (only if cleanup needed)
- Modify: `internal/attractor/engine/engine.go` (only if cleanup needed)
- Test: `internal/attractor/engine/*_test.go`

**Step 1: Run focused regression suite**

Run:

```bash
go test ./internal/attractor/engine -run 'TestResume_ParallelBranchNamesUseConfiguredPrefix|TestResume_LoopRestartUsesBaseLogsRoot|TestResume_FromCheckpoint_RewindsBranchAndContinues|TestRun_LoopRestartCreatesNewLogDirectory|TestRun_ParallelFanOutAndFanIn_FastForwardsWinner' -v
```

Expected: PASS.

**Step 2: Run package test suite**

Run: `go test ./internal/attractor/engine -v`
Expected: PASS.

**Step 3: Run broader attractor tests**

Run: `go test ./internal/attractor/... -v`
Expected: PASS.

**Step 4: Validate no accidental behavior drift in restart artifacts**

Run:

```bash
go test ./internal/attractor/engine -run TestRun_LoopRestartCreatesNewLogDirectory -v
```

Expected: `restart-1/manifest.json` exists and run succeeds.

**Step 5: Commit final polish**

```bash
git add internal/attractor/engine internal/attractor/runtime
git commit -m "test: add resume regression coverage for parallel and loop restart"
```

---

### Task 6: Optional Hardening Follow-Up (If Time Allows)

**Files:**
- Modify: `internal/attractor/engine/resume.go`
- Create: `internal/attractor/engine/resume_from_restart_dir_test.go`

**Step 1: Write the failing test**

```go
func TestResume_WhenLogsRootIsRestartDir_RestartsFromParentBaseLogsRoot(t *testing.T) {
	// Simulate resume starting from <base>/restart-N and ensure next loop restart creates restart-(N+1) under <base>.
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResume_WhenLogsRootIsRestartDir_RestartsFromParentBaseLogsRoot -v`
Expected: FAIL before fallback inference is complete.

**Step 3: Write minimal implementation**

Finalize fallback parser in resume restart-state restoration:

```go
var restartSuffixRE = regexp.MustCompile(`^restart-(\d+)$`)
```

Ensure parent base root is used when `logsRoot` basename matches `restart-N`.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestResume_WhenLogsRootIsRestartDir_RestartsFromParentBaseLogsRoot -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/attractor/engine/resume.go internal/attractor/engine/resume_from_restart_dir_test.go
git commit -m "fix: make resume restart-aware when invoked from restart subdirectories"
```
