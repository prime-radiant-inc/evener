# Remove Scratch Retention Implementation Plan

> **Superseded 2026-10-04** by "Revision 2" of [the design](../specs/2026-10-03-remove-scratch-retention-design.md). This plan deletes scratch at every environment end and sweeps at root close. What shipped keeps a session's named scratch (`<tmp>/evener-scratch-<root>/<session>/`) across every end and removes it only when the hub archives or deletes the session. There is no sweep at root close. The retention removal in this plan did ship.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A session's scratch directory is deleted when the environment that owns it ends, and the retention system that kept it alive is removed.

**Architecture:** One removal helper handles read-only trees. Every place that releases an owning environment's scratch with `Retain` at an end calls one disposal, `LocalExecutionEnvironment.DisposeSessionScratch`, instead. That disposal keeps only a detached command's `$TMPDIR` container. The root's current environment is disposed as the last step of close, after SessionEnd hooks and MCP shutdown. The retention files, their tests, and the sweep's pin and manifest checks are then deleted. The sweep also runs at root session end.

**Tech Stack:** Go (modules: root `agent`, `cmd/evener`, `cmd/evener-hub`, `tools/tool-fluency`), Markdown docs, Go text/template prompt.

**Spec:** `docs/superpowers/specs/2026-10-03-remove-scratch-retention-design.md`

## Global Constraints

- Format Go with `$(go env GOROOT)/bin/gofmt`, never the Homebrew gofmt on PATH.
- Every Go change passes `go vet ./...`, `go vet -tags evenerfuzz ./...`, and `GOOS=windows go vet -tags evenerfuzz ./...` in each touched module. Syscall and FIFO tests go in `*_unix_test.go`.
- Run golangci-lint in each touched module (`--allow-serial-runners`).
- Tests never assert prompt prose.
- Run targeted tests with `TMPDIR="$(cd "$TMPDIR" && pwd -P)"`.
- Shared-scratch delegates never delete the parent's scratch. Ownership comes from `environmentOwnedAtTeardown`, `ownedParkedWorktreeEnvironment` and `parentSharedEnv`.
- Detached commands keep today's `$TMPDIR` container (lease released, directory left for the sweep), and their environment drops `EVENER_SCRATCH_DIR`.
- Live ownership moves (`AdoptSessionScratch`, `EnableSandbox` re-provision) keep their lease-only `Retain`.
- PRs are stacked, each based on main. Every PR body states its /simplify outcome. Merge needs CI green on the merged head and RoboRev with no Medium or higher.

## Review Focus

1. A shared-scratch delegate ending while its parent still works: the parent's scratch must survive. Covered in Task 3 Step 1, case "shared child".
2. A sandboxed root whose SessionEnd hook runs after close begins: the hook must still launch, because the scratch is still there. Covered in Task 3 Step 1, case "hook order".
3. A scratch holding a read-only Go module cache: removal must leave nothing. Covered in Task 1 Step 1.
4. An unsandboxed session that started a detached command, then closed: the command's `$TMPDIR` must still exist. Covered in Task 2 Step 1.
5. A session resumed after retirement deleted its scratch: it must run with a fresh scratch. Covered in Task 3 Step 1, case "resume".

## Rulings already made

- **PR shape.** The spec lists "ends delete" and "remove retention" as separate PRs. With ends deleting, the retention tests assert directories that no longer exist, so the first PR could not go green. They land together as PR 2, Tasks 2–4. Cost if wrong: one larger PR.
- **Sweep at root end.** The sweep runs synchronously at the end of root close, because close is not a hot path. Cost if wrong: a slower close on a temp base with thousands of entries.

---

## PR 1: removal helper

### Task 1: One removal that handles read-only trees

**Files:**
- Create: `agent/sandbox/remove_tree.go`
- Test: `agent/sandbox/remove_tree_unix_test.go`
- Modify: `agent/sandbox/session_scratch.go` (`Cleanup`, both tombstone `os.RemoveAll` calls in `sweepCrashedSessionScratch`)
- Modify: `agent/sandbox/session_tmp.go` (`Remove`, and the `os.RemoveAll(container)` at :156)

**Interfaces:**
- Produces: `func removeTree(dir string) error`, unexported, package `sandbox`.

- [ ] **Step 1: Write the failing tests**

```go
package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// readOnlyModuleTree builds what `go mod download` leaves in GOMODCACHE: a
// 0555 directory holding a 0444 file, which a plain os.RemoveAll cannot unlink.
func readOnlyModuleTree(t *testing.T, root string) {
	t.Helper()
	mod := filepath.Join(root, "gomodcache", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mod, 0o755) })
}

func TestRemoveTreeRemovesAReadOnlyModuleCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch")
	readOnlyModuleTree(t, dir)
	if err := removeTree(dir); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after removeTree: %v", err)
	}
}

func TestRemoveTreeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o755) })
	dir := filepath.Join(t.TempDir(), "scratch")
	readOnlyModuleTree(t, dir)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(dir); err != nil {
		t.Fatalf("removeTree: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("symlink target removed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Errorf("symlink target mode = %04o, want 0555 untouched", got)
	}
}

func TestSessionScratchCleanupRemovesAReadOnlyModuleCache(t *testing.T) {
	scratch, err := NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	readOnlyModuleTree(t, scratch.Dir)
	if err := scratch.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Lstat(scratch.Dir); !os.IsNotExist(err) {
		t.Fatalf("scratch still present after Cleanup: %v", err)
	}
}
```

Put the two Unix-permission tests in `agent/sandbox/remove_tree_unix_test.go` with `//go:build unix`, since Windows has no 0555 directory semantics. `TestSessionScratchCleanupRemovesAReadOnlyModuleCache` goes there too.

- [ ] **Step 2: Run them and watch them fail**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/sandbox/ -run 'TestRemoveTree|TestSessionScratchCleanupRemovesAReadOnly' -count=1`
Expected: build failure, `undefined: removeTree`. Then, once a stub `func removeTree(dir string) error { return os.RemoveAll(dir) }` exists, `TestRemoveTreeRemovesAReadOnlyModuleCache` fails with `permission denied`.

- [ ] **Step 3: Implement**

`agent/sandbox/remove_tree.go`:

```go
package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// removeTree removes dir and everything under it. Go writes its module cache
// read-only (0555 directories, 0444 files), so a plain os.RemoveAll fails on a
// scratch that ran `go` with GOMODCACHE inside it. On failure this makes every
// directory in the tree owner-writable, as `go clean -modcache` does, and tries
// once more. WalkDir never follows a symlink, so nothing outside dir changes mode.
func removeTree(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || !d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil && info.Mode().Perm()&0o200 == 0 {
			_ = os.Chmod(path, info.Mode().Perm()|0o700)
		}
		return nil
	})
	if retryErr := os.RemoveAll(dir); retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}
```

Replace `os.RemoveAll(dir)` in `SessionScratch.Cleanup`, `os.RemoveAll(container)` in `SessionTmp.Remove` and at `session_tmp.go:156`, and both `os.RemoveAll(tombstone)` calls in `sweepCrashedSessionScratch` with `removeTree(...)`.

- [ ] **Step 4: Run the package tests**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/sandbox/ -count=1`
Expected: `ok`.

- [ ] **Step 5: Gates and commit**

Run `gofmt`, the three vet configurations and golangci-lint for the root module. Then:

```bash
git add agent/sandbox/remove_tree.go agent/sandbox/remove_tree_unix_test.go agent/sandbox/session_scratch.go agent/sandbox/session_tmp.go
git commit -m "fix(sandbox): remove scratch holding a read-only Go module cache"
```

---

## PR 2: scratch ends with its environment

### Task 2: `DisposeSessionScratch`, and detached commands

**Files:**
- Modify: `agent/execenv/local.go`:
  - rename `DisposeUnadoptedScratch` to `DisposeSessionScratch`, which now returns `error`;
  - add the `detachedSpawned` field;
  - set it in `DetachCommand`;
  - drop `EVENER_SCRATCH_DIR` from the detached command's env.
- Modify every caller of `DisposeUnadoptedScratch` (`grep -rn DisposeUnadoptedScratch --include='*.go' .`), including `cmd/evener/run.go`, `cmd/evener/serve.go`, `tools/tool-fluency/cmd/evener-fluency/main.go`, and `agent/subagents.go` (`disposeUnadoptedScratch` helper).
- Test: `agent/execenv/dispose_scratch_unix_test.go` (new).

**Interfaces:**
- Produces: `func (e *LocalExecutionEnvironment) DisposeSessionScratch() error`. It removes the sandbox scratch, the unsandboxed scratch and the `$TMPDIR` container. When a detached command was started, the container's lease is released and the directory kept. It returns the joined removal errors.

- [ ] **Step 1: Write the failing tests**

```go
//go:build unix

package execenv

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDisposeSessionScratchRemovesScratchAndTmp(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch, tmp := env.unsandboxedScratchDir(), env.unsandboxedTmpDir()
	if scratch == "" || tmp == "" {
		t.Fatalf("no scratch/tmp minted: %q %q", scratch, tmp)
	}
	if err := env.DisposeSessionScratch(); err != nil {
		t.Fatalf("DisposeSessionScratch: %v", err)
	}
	for _, dir := range []string{scratch, tmp} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("%s still present: %v", dir, err)
		}
	}
}

func TestDisposeSessionScratchKeepsADetachedCommandsTmp(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if !env.DetachSupported() {
		t.Skip("detach unsupported here")
	}
	started, err := env.DetachCommand(context.Background(), "sleep 30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killPID(t, started.PID); waitDone(started.Done, 5*time.Second) })
	scratch, tmp := env.unsandboxedScratchDir(), env.unsandboxedTmpDir()
	if err := env.DisposeSessionScratch(); err != nil {
		t.Fatalf("DisposeSessionScratch: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("detached command's TMPDIR %s removed: %v", tmp, err)
	}
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Errorf("session scratch %s kept: %v", scratch, err)
	}
}

func TestDetachedCommandHasNoScratchDirVar(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if !env.DetachSupported() {
		t.Skip("detach unsupported here")
	}
	out := t.TempDir() + "/env.txt"
	started, err := env.DetachCommand(context.Background(), "env > "+out, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(started.Done, 10*time.Second)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "EVENER_SCRATCH_DIR=") {
		t.Error("a detached command's environment names EVENER_SCRATCH_DIR, which is deleted when the session ends")
	}
	if !strings.Contains(string(data), "TMPDIR=") {
		t.Error("a detached command's environment lost TMPDIR")
	}
}
```

`killPID` and `waitDone` are small test helpers in the same file: `syscall.Kill(pid, syscall.SIGKILL)`, and a `select` on `done` with a timeout that calls `t.Fatalf`. Before writing them, check `agent/execenv/detach_test.go` for existing equivalents and reuse those. Check `unsandboxedTmpDir`'s exact name in `local.go` too.

- [ ] **Step 2: Run them and watch them fail**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/execenv/ -run 'TestDisposeSessionScratch|TestDetachedCommandHasNoScratchDirVar' -count=1`
Expected: build failure, `env.DisposeSessionScratch undefined`.

- [ ] **Step 3: Implement**

In `agent/execenv/local.go`, add the field next to `unsandboxedTmp`:

```go
	// detachedSpawned records that DetachCommand started a process from this env.
	// Such a process outlives the session on purpose and keeps the TMPDIR it was
	// spawned with, so DisposeSessionScratch keeps that container and leaves it to
	// the crashed-scratch sweep. Guarded by scratchMu.
	detachedSpawned bool
```

Replace `DisposeUnadoptedScratch` with:

```go
// DisposeSessionScratch removes every per-session scratch directory this env
// owns: the sandbox scratch EnableSandbox provisioned, the scratch an unsandboxed
// env mints on its first command, and the world-usable TMPDIR container. An env
// that started a detached command keeps that container, lease released, because
// the command still uses it; the crashed-scratch sweep reclaims it later. Call it
// only on an environment the caller owns, never on a parent's shared one whose
// live children point into its scratch. Idempotent.
func (e *LocalExecutionEnvironment) DisposeSessionScratch() error {
	e.invalidateSandboxFS()
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	var errs []error
	if tmp := e.ownedSessionTmp; tmp != nil {
		e.ownedSessionTmp = nil
		errs = append(errs, tmp.Cleanup())
	}
	if tmp := e.unsandboxedScratch; tmp != nil {
		e.unsandboxedScratch = nil
		errs = append(errs, tmp.Cleanup())
	}
	if e.detachedSpawned {
		if e.unsandboxedTmp != nil {
			errs = append(errs, e.unsandboxedTmp.Retain())
		}
	} else if tmp := e.unsandboxedTmp; tmp != nil {
		e.unsandboxedTmp = nil
		errs = append(errs, tmp.Remove())
	}
	return errors.Join(errs...)
}
```

Read `DisposeSandboxScratch` first: if its body differs from the first block above (beyond the `invalidateSandboxFS` call), keep calling it instead of inlining it.

In `DetachCommand`, after `cmd.Start()` succeeds:

```go
	e.scratchMu.Lock()
	e.detachedSpawned = true
	e.scratchMu.Unlock()
```

Before `cmd.Configure(config)`, drop the scratch variable from `env`:

```go
	// A detached process outlives the session; the scratch EVENER_SCRATCH_DIR names
	// is deleted when the session ends, so the process must not be told about it.
	env = slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, envvars.EVENERScratchDir.Name+"=")
	})
```

Rename every `DisposeUnadoptedScratch()` call to `DisposeSessionScratch()`. Callers that ignore the result write `_ = env.DisposeSessionScratch()`.

- [ ] **Step 4: Run the tests**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/execenv/ -count=1`
Expected: `ok`. If an existing test asserts the old name, update it to the new one.

- [ ] **Step 5: Commit**

```bash
git add agent/execenv/ cmd/evener/run.go cmd/evener/serve.go tools/tool-fluency/cmd/evener-fluency/main.go agent/subagents.go
git commit -m "feat(execenv): DisposeSessionScratch keeps only a detached command's TMPDIR"
```

### Task 3: Every end disposes its owned scratch

**Files:**
- Modify: `agent/session_lifecycle.go`:
  - close: dispose the current env after `mcpMgr.Close()`;
  - the parked and abandoned helpers dispose instead of retaining;
  - child teardown at step 3 uses `disposeChildScratch`.
- Modify: `agent/session_retirement_release.go`: `releaseRetirementScratch` becomes `disposeRetirementScratch`, which disposes the owned parked and abandoned envs; the current env is disposed at the end of close as above. `releaseChildRuntimeForRetirement` uses `disposeChildScratch`.
- Modify: `agent/subagents.go`: `releaseOwnedChildEnvironment` disposes; every `retainChildScratch` caller (`subagents.go`, `delegate_tree_stop.go`, `delegate_tree_reclaim.go`, `session_tools_worktree_dispose.go`, `session_retirement_release.go`) passes `disposeChildScratch`.
- Modify: `agent/session_env_swap.go:172`: a swap aborted by close disposes `next`.
- Tests: flip the existing retention assertions in `agent/sandbox_delegate_create_test.go` (`TestParentClose_RetainsPerDelegateSandboxScratch`), `agent/session_retirement_scratch_lease_test.go`, `agent/session_worktree_swap_scratch_unix_test.go` (the parked and abandoned close tests) and `agent/subagent_teardown_unix_test.go`. Add `agent/session_scratch_end_test.go`.

**Interfaces:**
- Consumes: `DisposeSessionScratch() error` from Task 2.
- Produces: `func (s *Session) disposeOwnedCurrentScratch()`, called once at the end of close for terminal close and for retirement.

- [ ] **Step 1: Flip the existing tests and add the new ones**

Rename each test whose name says it retains, and change its assertion so the directory is gone:

- `TestParentClose_RetainsPerDelegateSandboxScratch` → `TestParentCloseRemovesPerDelegateSandboxScratch`. After `s.Close()`:

```go
	if left := sandboxScratchDirs(t, isolated); len(left) != 0 {
		t.Errorf("parent close left the per-delegate sandbox scratch behind: %v", left)
	}
```

- `TestRetirementReleaseRetainsOwnEnvironmentScratch` → `TestRetirementRemovesOwnEnvironmentScratch`. Call `root.disposeRetirementScratch()` and `root.disposeOwnedCurrentScratch()`, then assert both `currentScratch` and `parkedScratch` are gone (`os.IsNotExist`). Delete the manifest assertions; the manifest stops existing in Task 4.
- `TestRetirementReleaseOfAnOwningChildReleasesItsOwnScratch`: assert the child's own scratch is gone.
- `TestRetirementReleaseOfASharedChildKeepsTheParentScratchLease`: keep as is. This is the **shared child** case; the parent's scratch and lease must survive.
- In `agent/session_worktree_swap_scratch_unix_test.go`, `TestParentCloseWhileEnteredRetainsTheParkedEnvironmentScratch`, `TestWorktreeSwap_CloseAfterTheEnterRetainsTheParkedEnvironmentScratch` and `TestParentCloseRetainsScratchOnAnEnvironmentASecondEnterAbandoned` are each renamed to `...Removes...`, and assert the directory is gone. `TestWorktreeSwap_CloseDuringASharedChildExitKeepsTheParentScratchLease` stays as is.
- In `agent/subagent_teardown_unix_test.go`, `TestParentCloseReleasesAnOwnedUnsandboxedChildScratchLease` and `TestRootCloseReleasesAnUntrackedResidentRuntimeScratchLease` are renamed to `...Removes...Scratch` and assert the directory is gone.

New file `agent/session_scratch_end_test.go`:

```go
package agent

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// TestRootCloseRemovesItsScratch: a root's own scratch does not outlive close.
func TestRootCloseRemovesItsScratch(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	local := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("no scratch minted")
	}
	root.Close()
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("root close left its scratch %s: %v", scratch, err)
	}
}

// sessionEndProbePluginDir writes a plugin whose SessionEnd hook touches marker
// only if the directory named in pathFile still exists when the hook runs.
// Modeled on newResumeHookPluginDir (agent/session_resume_hooks_test.go).
func sessionEndProbePluginDir(t *testing.T, pathFile, marker string) string {
	t.Helper()
	pluginDir := filepath.Join(t.TempDir(), "session-end-probe")
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("test -d \"$(cat %q)\" && touch %q", pathFile, marker)
	manifest := fmt.Sprintf(`{
  "name": "session-end-probe",
  "version": "0.0.1",
  "hooks": {
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": %q, "timeout": 5}]}
    ]
  }
}`, command)
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

// TestSessionEndHookRunsBeforeScratchIsRemoved ("hook order"): SessionEnd hooks
// and MCP servers run with TMPDIR inside the scratch, so close removes it last.
func TestSessionEndHookRunsBeforeScratchIsRemoved(t *testing.T) {
	work := t.TempDir()
	pathFile := filepath.Join(work, "scratch-path")
	marker := filepath.Join(work, "hook-saw-scratch")
	meta := schema.SessionMeta{
		ID:        "01KSESSIONENDPROBE000000000",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{PluginDirs: []string{sessionEndProbePluginDir(t, pathFile, marker)}},
		TurnCount: 1,
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	sess, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), env, meta,
		RestoreSessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch := env.SessionScratchDir()
	if err := os.WriteFile(pathFile, []byte(scratch), 0o600); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the SessionEnd hook did not see the scratch %s still present: %v", scratch, err)
	}
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("close left the scratch %s: %v", scratch, err)
	}
}

// TestResumeAfterCloseGetsAFreshScratch ("resume"): a session restored after its
// scratch was removed runs with a new one. Modeled on
// TestRetirementRootScratchRestoresAtOriginalPath, which Task 4 deletes.
func TestResumeAfterCloseGetsAFreshScratch(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	env1 := root1.env.(*execenv.LocalExecutionEnvironment)
	if _, err := env1.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	old := env1.SessionScratchDir()
	meta := root1.Meta()
	root1.Close()
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("close left the scratch %s: %v", old, err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer root2.Close()
	env2 := root2.env.(*execenv.LocalExecutionEnvironment)
	if _, err := env2.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("restored session cannot run a command: %v", err)
	}
	fresh := env2.SessionScratchDir()
	if fresh == "" || filepath.Clean(fresh) == filepath.Clean(old) {
		t.Fatalf("restored scratch = %q, want a fresh directory, not %q", fresh, old)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("restored scratch %s missing: %v", fresh, err)
	}
}
```

Add `fmt`, `path/filepath`, `schema` and `llm` to the file's imports. Before Task 4, `RestoreSessionFromMetaWithConfig` may still try to adopt the old scratch; the 2026-10-03 manual check shows it falls back to a fresh one.

- [ ] **Step 2: Run them and watch them fail**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/ -run 'TestParentCloseRemoves|TestRetirementRemoves|TestRetirementReleaseOf|TestRootCloseRemoves|TestSessionEndHookRunsBefore|TestResumeAfterClose|TestWorktreeSwap_CloseAfterTheEnterRemoves|TestParentCloseWhileEnteredRemoves' -count=1`
Expected: FAIL. Directories are still present, and `disposeRetirementScratch` / `disposeOwnedCurrentScratch` are undefined until Step 3.

- [ ] **Step 3: Implement**

In `agent/session_lifecycle.go`, after `s.mcpMgr.Close()`:

```go
		// Scratch goes last: SessionEnd hooks and MCP servers above still run with
		// TMPDIR inside it, and bubblewrap refuses a missing bind source.
		if cleanupEnv || retirement {
			s.disposeOwnedCurrentScratch()
		}
```

New helper, in `agent/session_lifecycle.go`:

```go
// disposeOwnedCurrentScratch removes the current environment's scratch when this
// session owns that environment. A child still holding its live parent's own
// environment owns none of it; the parent's close removes it.
func (s *Session) disposeOwnedCurrentScratch() {
	s.mu.Lock()
	current, parentShared := s.env, s.parentSharedEnv
	s.mu.Unlock()
	local, ok := current.(*execenv.LocalExecutionEnvironment)
	if !ok || sameEnvironment(current, parentShared) {
		return
	}
	if err := local.DisposeSessionScratch(); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "session scratch removal incomplete: " + err.Error()})
	}
}
```

Then:

- **Parked and abandoned.** `retainParkedWorktreeEnvironmentScratch` becomes `disposeParkedWorktreeEnvironmentScratch` and calls `_ = parked.DisposeSessionScratch()`. Its close call site passes `disposeChildScratch` to `settleAbandonedEnvironmentScratch`.
- **Children.** In close step 3, call `teardownChildSession(budgetCtx, sub.sess, disposeChildScratch)`.
- **`releaseOwnedChildEnvironment`.** Remove the retain branch, so it always calls `disposeUnadoptedScratch(env)` (Task 2's renamed helper).
- **Every other `retainChildScratch` caller** passes `disposeChildScratch`. Then delete `retainChildScratch` and the `childScratchDisposition` type, and drop the parameter from `teardownChildSession`, `teardownChildSessionWithPolicy`, `releaseOwnedChildEnvironment` and `settleAbandonedEnvironmentScratch`.
- **Retirement.** `releaseRetirementScratch` becomes `disposeRetirementScratch`: dispose the owned parked env and the abandoned envs with `_ = env.DisposeSessionScratch()`. The current env is handled by `disposeOwnedCurrentScratch` at the end of close. Keep `sealRetainedScratch` and `detachRetainedScratch` calls until Task 4 deletes them.
- **`session_env_swap.go:172`.** `next.RetainSessionScratch()` → `_ = next.DisposeSessionScratch()`.

- [ ] **Step 4: Run the flipped and new tests**

Run the Step 2 command. Expected: PASS. Retention-only tests elsewhere in `./agent/` may now fail. That's expected until Task 4 deletes them; record it in the ledger and do not fix them here.

- [ ] **Step 5: Commit**

```bash
git add agent/
git commit -m "feat(agent): every environment end removes the scratch it owns"
```

### Task 4: Delete the retention system

**Files:**
- Delete:
  - `agent/session_scratch_retention.go`, `agent/sandbox/scratch_retention.go`, `agent/execenv/scratch_retention.go`;
  - tests: `agent/session_scratch_refresh_test.go`, `agent/sandbox/scratch_retention_test.go`, `agent/session_scratch_retention_test.go`, `agent/session_scratch_restore_failure_test.go`, `agent/execenv/scratch_retention_test.go`, `agent/session_terminal_scratch_release_test.go`, `agent/session_scratch_prune_test.go`, `agent/session_scratch_pool_adopt_test.go`, `cmd/evener/root_scratch_failure_test.go`, `cmd/evener-hub/scratch_retention_delete_test.go`.
- Modify, compile-driven, removing only retention code:
  - `agent/session_init.go` (`installScratchRetention` calls, `prepareRetainedScratch`, `adoptResumedRootScratch`, the manifest settle helpers at ~2626-2720);
  - `agent/delegate_runtime.go` (`installChildScratchRetention`, `settleFailedRestoreScratch` → `disposeUnadoptedScratch` when `ownsFresh`, `settleOwnedScratchByManifest` → `disposeUnadoptedScratch`, `adoptRestoredConsumerScratch`);
  - `agent/session_retirement_release.go` and `agent/session_retirement_prepare.go` (pin checks);
  - `agent/session_lifecycle.go` (`releaseTerminalScratchRetention` call, `discardRestoredCandidate` → `disposeUnadoptedScratch(env)`);
  - `agent/subagents.go` (seal and detach);
  - `agent/session_env_swap.go` (`stageScratchSwapBinding`, `registerScratchConsumerRoles`);
  - `agent/session_worktree_resume.go` (binding inherit and assign, `parkedWorktreeBindingID`);
  - `agent/session.go` (`retainedScratch` pool and sealed flag);
  - `agent/session_config.go` (scratch test hooks);
  - `agent/execenv/local.go` (`ReleaseSessionScratch`, `SettleScratchByReferences`, `PinOwnedScratch`, `pinOwnedScratchAfterMint`, `MarkRetainedSlotPending`, `ScratchRetentionReferences` / `Binding`, the retention fields, and `RetainSessionScratch` with its users, now that ends dispose);
  - `cmd/evener/run.go`, `cmd/evener/serve.go`, `tools/tool-fluency/cmd/evener-fluency/main.go` (launch-failure settle → `DisposeSessionScratch`);
  - `cmd/evener-hub/project_delete.go` (`releaseProjectDeletionScratchRetention` and its calls);
  - `agent/sandbox/session_scratch.go` (the sweep's pin read, manifest lock, `scratchReclamationMu`, `ScratchDirectoryRetained` gate, `sweepTombstoneReferenceSurvived`, the conditional rename-back, the `scratchResetBeforeReclaimLock` seam, and the doc comment's retention text).
- Partly affected tests: `agent/session_retirement_preservation_test.go`, `agent/session_worktree_swap_scratch_unix_test.go`, `cmd/evener/run_test.go:1567+`. Delete the cases that only pin retention. Keep the cases that pin preservation of transcripts, jobs or worktrees, editing out their scratch-manifest assertions.

- [ ] **Step 1: Delete the files and build**

```bash
git rm agent/session_scratch_retention.go agent/sandbox/scratch_retention.go agent/execenv/scratch_retention.go \
  agent/session_scratch_refresh_test.go agent/sandbox/scratch_retention_test.go agent/session_scratch_retention_test.go \
  agent/session_scratch_restore_failure_test.go agent/execenv/scratch_retention_test.go \
  agent/session_terminal_scratch_release_test.go agent/session_scratch_prune_test.go \
  agent/session_scratch_pool_adopt_test.go cmd/evener/root_scratch_failure_test.go \
  cmd/evener-hub/scratch_retention_delete_test.go
go build ./... 2>&1 | head -60
```

Expected: undefined-symbol errors at the call sites listed under Files.

- [ ] **Step 2: Remove each call site**

Work through the build errors. Each listed call either disappears, or becomes `DisposeSessionScratch` / `disposeUnadoptedScratch` as listed. Delete any function left with no caller (`go vet` and golangci-lint `unused` will name them). Repeat `go build ./... && go vet ./...` in each module (root, `cmd/evener`, `cmd/evener-hub`, `tools/tool-fluency`) until clean.

- [ ] **Step 3: Make the tests compile and pass**

Run: `TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/... -count=1 2>&1 | tail -40`, then the same for `cmd/evener`, `cmd/evener-hub` and `tools/tool-fluency`.

For each failure:
- a test that pins retention (a pin, a manifest, a binding, adoption of a retained dir, or a dir that survives an end) is deleted;
- a test whose subject is something else is fixed to the new behaviour, with a ledger line.

Add one sweep test to `agent/sandbox/session_scratch_test.go`:

```go
func TestSweepReclaimsAnOldDirectoryThatStillCarriesARetentionPin(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, sessionScratchPrefix+"123")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".evener-retained-session.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepCrashedSessionScratch(base); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("an old pinned directory survived the sweep: %v", err)
	}
}
```

Write it before deleting the sweep's pin gate, and watch it fail first. It also needs the directory to pass `scratchEntryOwnedByProcess`; read that check and set up the fixture to satisfy it.

- [ ] **Step 4: Full gates**

Run the full `go test` per touched module, `make vet`, `make lint`, and `go test -short -count=1 .` at the root. Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add -A agent cmd tools
git diff --cached --stat | tail -3
git commit -m "refactor: remove scratch retention"
```

Check the staged stat first: only deletions and the call-site edits should appear.

---

## PR 3: sweep at root session end

### Task 5: Run the sweep when a root session ends

**Files:**
- Modify: `agent/session_lifecycle.go` (after `disposeOwnedCurrentScratch` in close, root only)
- Test: `agent/session_scratch_end_test.go`

**Interfaces:**
- Consumes: `sandbox.SweepCrashedSessionScratch(workspaceRoot string) error` (existing).

- [ ] **Step 1: Write the failing test**

```go
// TestRootCloseSweepsOldScratch: a long-running daemon reclaims old scratch as
// root sessions end, without waiting for a restart.
func TestRootCloseSweepsOldScratch(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	old := filepath.Join(base, "evener-sandbox-424242")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	root.Close()
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("root close did not sweep %s: %v", old, err)
	}
}
```

Run it with `-run TestRootCloseSweepsOldScratch` and watch it fail.

- [ ] **Step 2: Implement**

After the `disposeOwnedCurrentScratch` block in close:

```go
		// A daemon runs for weeks; sweeping as each root ends reclaims crash
		// leftovers and detached commands' finished TMPDIRs without a timer.
		if s.parent == nil && cleanupEnv {
			if err := sandbox.SweepCrashedSessionScratch(s.workingDir()); err != nil {
				s.emit(events.EventWarning, events.WarningData{Message: "scratch sweep incomplete: " + err.Error()})
			}
		}
```

Check the session's root-identity predicate (`s.parent == nil` or its equivalent) and its working-dir accessor in `agent/session.go`, and use those.

- [ ] **Step 3: Run, gate, commit**

Run the test, then the full `./agent/` package. Then:

```bash
git add agent/session_lifecycle.go agent/session_scratch_end_test.go
git commit -m "feat(agent): sweep stale scratch when a root session ends"
```

---

## PR 4: surfaces

### Task 6: Prompts, packet note, docs and skills

**Files:**
- Modify: `agent/subagents.go` (the `ScratchPath` doc comment); `docs/job-control.md` (~1115-1127).
- Modify: `agent/session_prompts.go` `sandboxPromptLine`.
- Modify: `agent/prompts/system.md.tmpl` (Finishing scratch paragraph and the two table rows).
- Modify docs:
  - `docs/daemon-idle-retirement.md`, `docs/product/subsystems.md`, `docs/product/friction.md`;
  - `docs/subagent-management/11-delegate-resource-model.md`, `docs/developing-evener/testing.md`, `docs/developing-evener/environment.md`;
  - `docs/superpowers/specs/2026-07-15-session-scratch-and-orchestration-posture-design.md` (a superseded note at the top);
  - `internal/bundled/skills/doctoring-evener/SKILL.md`, `internal/bundled/skills/doctoring-evener/references/session-interrogation.md`.

- [ ] **Step 1: Packet note**

Change "retained on disk regardless of outcome" to: "it exists until the delegate's runtime is torn down, after which it is deleted". Make the same edit in `docs/job-control.md`.

- [ ] **Step 2: Sandbox line**

Replace `"; cleanup is manual."` with `". It is deleted when this session's environment ends; return what your parent needs in your result."`. Existing golden tests for the prompt line regenerate with the package's update flag. Find it with `grep -rn 'update' agent/*golden*_test.go | head`.

- [ ] **Step 3: System prompt paragraph**

Replace the Finishing scratch paragraph and its two table rows with:

```
When you have a scratch directory, named in the environment block or in `$EVENER_SCRATCH_DIR`, make scratch there, and remove scratch you made anywhere else. Delete each scratch file once you are done with it. Scratch can be cleared when the session goes idle or ends, so put anything you are keeping outside it, in the workspace or where the task names, and say where. Leave files someone else made: another agent may share the directory.

| When you think | Do this instead |
|---|---|
| "I'll keep this copy or log as evidence." | Put the evidence in your report and delete the file. |
| "The person wants this file, so I'll leave it in scratch." | Write it where the task names, or in the workspace, and give its path in your report. |
```

Measure it before landing. Rerun the scratch-cleanup eval tasks (`sc-tasks`, `run_sc1.sh` pattern in the lab), main against this branch, with luna and vision, 3 reps each. On main, the one-off CSV is kept in scratch and named. On the branch it must land outside scratch and still be named, with leftover counts no worse than main. If it regresses, iterate on the wording before Step 4.

- [ ] **Step 4: Docs and skills**

Rewrite each listed doc's retention passage to say that scratch is deleted when its environment ends, that a resumed session gets a fresh scratch, and that the sweep runs at startup and at root session end. Remove the doctoring-evener skill's manifest-refusal guidance. Add a "Superseded 2026-10-03 by 2026-10-03-remove-scratch-retention-design.md" line to the 2026-07-15 spec. Close #3667 and #3681 from the PR body.

- [ ] **Step 5: Gates and commit**

Run the prompt golden tests and `go test ./agent/ -count=1`, then `make lint`. Then:

```bash
git add agent/subagents.go agent/session_prompts.go agent/prompts/system.md.tmpl docs internal/bundled/skills/doctoring-evener
git commit -m "docs: scratch is deleted when its environment ends"
```

---

## Completion

After Task 6, run the final whole-branch review (superpowers:executing-plans, "Final Review"). Then open the four PRs in order, each based on main as the previous one merges, following the merge gate in Global Constraints.
