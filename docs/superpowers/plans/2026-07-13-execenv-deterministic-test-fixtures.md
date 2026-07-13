# Deterministic execenv Test Fixtures Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the macOS `agent/execenv` tests exercise their intended sandbox and cleanup contracts without changing production behavior.

**Architecture:** Keep both repairs in test code. Canonicalize only shared sandbox fixture roots before those paths enter the no-symlink walker, and keep the lifecycle fixture's Bash process alive so its TERM trap remains installed.

**Tech Stack:** Go testing, `filepath.EvalSymlinks`, Bash process traps, Git.

## Global Constraints

- Do not change production sandbox or lifecycle behavior.
- Do not weaken assertions or skip macOS coverage.
- Do not add sleeps, network access, provider credentials, or ambient machine dependencies.
- Preserve tests that intentionally construct symlinks.
- Keep each repair independently committed and verified.

---

### Task 1: Canonical Sandbox Fixture Roots

**Files:**
- Modify: `agent/execenv/securepath_test.go:43-55`
- Modify: `agent/execenv/sandbox_tools_test.go:19-50`

**Interfaces:**
- Consumes: existing `evalSym(t *testing.T, p string) string` test helper.
- Produces: `realTempDir(t *testing.T) string`, a symlink-resolved existing temporary directory for sandbox fixtures.

- [ ] **Step 1: Confirm the sandbox regression set is red**

Run:

```bash
go test ./agent/execenv -run 'Test(GrepNativeSkipsDenylist|GrepSandboxedIgnoresRipgrep|FileExistsGrantedRoot|GlobSkipsMaskedMatches|ListDirSkipsMaskedEntries|FileExistsDenylistedFalse|DenylistAllowsNonDenied|RestrictedConfinesReads|Escape_ReadWriteShapeMatchesContract)$' -count=1
```

Expected: FAIL on macOS with `refuses to traverse a symlinked or non-directory path component` for fixture paths beneath `/var`.

- [ ] **Step 2: Add the canonical temporary-directory fixture helper**

Add beside the shared secure-path fixture helpers in `securepath_test.go`:

```go
// realTempDir returns a fresh existing temp directory through its canonical
// path, so sandbox tests do not accidentally include a host-system symlink such
// as macOS /var in cases that are not testing symlink refusal.
func realTempDir(t *testing.T) string {
	t.Helper()
	return evalSym(t, t.TempDir())
}
```

Change `newSB` from:

```go
home := t.TempDir()
```

to:

```go
home := realTempDir(t)
```

Change both `sandboxedEnv` and `sandboxedEnvWithDenylist` from:

```go
home = t.TempDir()
```

to:

```go
home = realTempDir(t)
```

- [ ] **Step 3: Format and verify the sandbox regression set is green repeatedly**

Run:

```bash
gofmt -w agent/execenv/securepath_test.go agent/execenv/sandbox_tools_test.go
go test ./agent/execenv -run 'Test(GrepNativeSkipsDenylist|GrepSandboxedIgnoresRipgrep|FileExistsGrantedRoot|GlobSkipsMaskedMatches|ListDirSkipsMaskedEntries|FileExistsDenylistedFalse|DenylistAllowsNonDenied|RestrictedConfinesReads|Escape_ReadWriteShapeMatchesContract)$' -count=10
```

Expected: PASS for all ten repetitions.

- [ ] **Step 4: Commit the sandbox fixture repair**

Run `git status --short`, then:

```bash
git add agent/execenv/securepath_test.go agent/execenv/sandbox_tools_test.go
git commit -m "test(execenv): canonicalize sandbox fixture roots" -m "Resolve shared sandbox test roots before passing them through the no-symlink file-tool layer. This keeps macOS's /var system symlink from turning unrelated read, list, glob, grep, and existence contracts into symlink-refusal cases while preserving production enforcement and intentional symlink tests."
```

### Task 2: Keep the Cleanup TERM Observer Alive

**Files:**
- Modify: `agent/execenv/sandbox_lifecycle_test.go:191-192`

**Interfaces:**
- Consumes: existing `WATCH`, `SENTINEL`, and `READY` environment variables.
- Produces: a Bash process that retains its TERM trap while a background child waits.

- [ ] **Step 1: Confirm the lifecycle regression is red repeatedly**

Run:

```bash
go test ./agent/execenv -run '^TestCleanupDisposesOwnedTmpAfterChildGrace$' -count=10
```

Expected: FAIL ten times because Bash tail-executes the final `sleep`, discards its TERM trap, and never writes the sentinel.

- [ ] **Step 2: Prevent Bash from tail-executing the sleeper**

Change:

```go
sleep 300`
```

to:

```go
sleep 300 & wait`
```

- [ ] **Step 3: Format and verify the lifecycle regression is green repeatedly**

Run:

```bash
gofmt -w agent/execenv/sandbox_lifecycle_test.go
go test ./agent/execenv -run '^TestCleanupDisposesOwnedTmpAfterChildGrace$' -count=20
```

Expected: PASS for all twenty repetitions.

- [ ] **Step 4: Commit the lifecycle fixture repair**

Run `git status --short`, then:

```bash
git add agent/execenv/sandbox_lifecycle_test.go
git commit -m "test(execenv): keep cleanup trap process alive" -m "Background the lifecycle fixture's sleeper and wait in Bash so the shell is not tail-execed away after readiness. The tracked process now retains its TERM trap and the existing sentinel measures whether cleanup preserves the owned scratch directory through graceful shutdown."
```

### Task 3: Integrated Verification

**Files:**
- Verify: `agent/execenv`
- Verify: repository test modules

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: evidence that the repaired tests are deterministic and no other module regressed.

- [ ] **Step 1: Run the complete execenv package**

Run:

```bash
go test ./agent/execenv -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the repository gate**

Run:

```bash
make test
```

Expected: PASS. If the host process limit kills the uncapped run, retry once with `GOFLAGS='-p=2' make test` and report both results.

- [ ] **Step 3: Verify branch integrity**

Run:

```bash
git diff --check main...HEAD
git status --short
git log --oneline main..HEAD
```

Expected: no whitespace errors, a clean worktree, and the design, plan, and two test-repair commits only.
