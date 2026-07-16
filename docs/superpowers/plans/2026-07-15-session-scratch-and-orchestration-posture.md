# Session Scratch and Orchestration Posture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every live root and child session one private temporary directory with truthful lifecycle exposure, and add concise Serf-owned guidance for shared-workspace isolation, verification, compaction, and repeated review loops.

**Architecture:** Promote the existing sandbox `SessionTmp` primitive into a universal `SessionScratch` owned by each local session execution environment. A session-preserving `WithWorkingDirectory` view shares the path and process tracker without gaining cleanup ownership, while a new child-session constructor gets a distinct scratch and process tracker; sandbox wrappers, shells, hooks, and stdio MCP servers all receive the same `TMPDIR` and `SERF_SCRATCH_DIR`. Expose the live path through a non-serialized `EnvironmentInfo` field and one focused prompt component, keep orchestration posture in focused prompt sections, and return a non-blocking warning on a second concurrently running shared-workspace delegate.

**Tech Stack:** Go 1.x, local execution environments, Serf sandbox wrappers, embedded Go templates, JSON tool results, deterministic scripted-provider tests.

## Global Constraints

Implementation must not:

- modify Superpowers code, skills, plans, or prompts;
- automatically select worktree isolation;
- block shared delegates;
- add a protected-path mechanism or gate service;
- persist scratch across restart;
- make scratch a handoff/artifact store;
- automatically compact context or terminate semantic review loops;
- redirect `HOME` or durable build caches as part of this feature.

- Treat every requirement outside `docs/superpowers/specs/2026-07-15-session-scratch-and-orchestration-posture-design.md` as a defect. Stop and ask Jesse instead of expanding the implementation scope.

## Prerequisite and Preservation Note

This is project 5 and assumes the delegate-budget truthfulness, transcript/API-log separation, job-supervision surface cleanup, and agent-model selection correctness projects have already landed. Where this plan overlaps `subagents.go`, `job_delegate.go`, `session_tools_jobs.go`, prompt/template files, or environment wiring, preserve those earlier contracts and their focused tests. Do not recreate removed transcript/API or job-read surfaces, alter prior-project result shapes beyond the optional advisory defined here, or add implementation work for those projects.

## Testing Discipline

- Read and follow `docs/testing.md` before changing tests.
- Keep every default test deterministic: no provider credentials, network access, quota, ambient daemon, or current-model behavior.
- Put scripted providers at the LLM boundary and exercise real Serf session, delegation, prompt, environment, process, and cleanup behavior beneath them.
- Assert structured environment values, filesystem outcomes, tool-result fields, and focused prompt sections; do not snapshot or regex-match a large rendered system prompt.
- Do not use sleeps or wall-clock races. Use channels for process/delegate ordering and injected timestamps for stale-directory sweep tests.
- Preserve unrelated uncommitted and untracked files. Run `git status --short` before every staging step, and never use `git add -A`.

---

## File Structure

- Rename `agent/sandbox/session_tmp.go` to `agent/sandbox/session_scratch.go`: retain the one Serf-owned temporary-directory allocator/sweeper, add workspace-aware base selection, and hold an OS-released liveness lease for each live scratch.
- Rename `agent/sandbox/session_tmp_test.go` to `agent/sandbox/session_scratch_test.go`: prove permissions, cleanup, outside-worktree fallback, and the 24-hour prefix-and-liveness-limited sweep.
- Create `agent/sandbox/session_scratch_lock_unix.go`: acquire/release a non-blocking exclusive lease with `golang.org/x/sys/unix` on supported Unix platforms.
- Create `agent/sandbox/session_scratch_lock_windows.go`: acquire/release the same lease with `golang.org/x/sys/windows`.
- Modify `agent/sandbox/fuzz_contract_structural_test.go`: mechanically follow the allocator's renamed test seams and sweep helper.
- Modify `agent/sandbox/fuzz_policy_assembly_test.go`: mechanically follow the renamed allocator/type/sweep helper.
- Modify `agent/sandbox/env_floor.go`: add the shared environment override that sets `TMPDIR` and `SERF_SCRATCH_DIR` to one path without changing cache policy.
- Modify `agent/sandbox/env_floor_test.go`: prove both scratch variables agree, all current security filters remain enforced, and `HOME`/durable caches remain unchanged except under the pre-existing session-private sandbox cache strategy.
- Modify `envvars/envvars.go`: register `SERF_SCRATCH_DIR` and use its `envvars.Var` at call sites.
- Modify `envvars/envvars_test.go`: keep the supported-variable registry and ordering audit green.
- Modify `docs/environment.md`: document `SERF_SCRATCH_DIR` as Serf-provided, session-scoped, private, and non-durable.
- Modify `agent/execenv/execenv.go`: define the optional scratch capability and its nil-safe path helper.
- Modify `agent/execenv/local.go`: provision scratch at environment initialization, expose it, preserve it across worktree re-rooting, create distinct child-session environments, inject both variables into commands, and remove scratch only after tracked processes stop.
- Modify `agent/execenv/local_test.go`: cover universal unsandboxed provisioning, workspace-aware allocation, environment values, unchanged `HOME`/caches, and close cleanup.
- Modify `agent/execenv/sandbox_tmpbase_test.go`: follow the renamed ownership field and retain the configured-base assertion.
- Modify `agent/execenv/sandbox_reroot_test.go`: prove one live session keeps the same scratch across re-root and invocation-grant clones while a child-session environment gets another.
- Modify `agent/execenv/sandbox_lifecycle_test.go`: preserve the process-before-scratch teardown contract.
- Modify `agent/execenv/sandbox_lifecycle_program_fuzz_test.go`: rename the owned scratch fields/methods while retaining the deterministic lifecycle program.
- Modify `agent/execenv/process_runtime_program_fuzz_test.go`: expect universal scratch environment values and the renamed narrow disposal method.
- Modify `agent/internal/hooks/hooks.go`: let a hook runner carry the session scratch path.
- Modify `agent/internal/hooks/command_runtime.go`: apply the universal scratch environment before optional sandbox confinement.
- Modify `agent/internal/hooks/sandbox_test.go`: prove sandboxed and unsandboxed command hooks receive the same two variables.
- Modify `agent/internal/mcp/manager.go`: add a manager option for the session scratch path and apply it to stdio server process environments before optional sandbox confinement.
- Modify `agent/internal/mcp/sandbox_test.go`: prove sandboxed and unsandboxed stdio MCP commands receive the same two variables.
- Modify `agent/session_init.go`: make fresh and restored sessions clean scratch on construction failure, provision restore scratch before sandbox attachment, and wire scratch into hooks/MCP.
- Modify `agent/session.go`: retain the one scratch-owning environment handle across non-owning working-directory views.
- Modify `agent/session_lifecycle.go`: close SessionEnd hooks and stdio MCP transports before process/scratch cleanup, and clean both ordinary and discarded restored sessions through the full ownership path.
- Modify `agent/session_lifecycle_tail_coverage_fuzz_test.go`: pass explicit environment ownership to restored-candidate discard coverage.
- Modify `agent/session_lifecycle_slots_fuzz_test.go`: pass explicit environment ownership to restored-candidate discard program cases.
- Modify `agent/subagents.go`: always give a local child a fresh session environment, including shared-workspace delegates, and clean it on every spawn failure.
- Modify `agent/job_delegate.go`: restore delegates into fresh session environments and return the shared-workspace advisory on successful second concurrent launches.
- Create `agent/job_delegate_restore_scratch_test.go`: prove every unadopted restored-local-environment exit cleans scratch, adopted children record ownership, and candidate discard cleans the environment.
- Modify `agent/session_tools_worktree_dispose.go`: use full child environment cleanup and the renamed scratch ownership method where the lifecycle already performs narrow disposal.
- Modify `agent/sandbox_delegate_create_test.go`: replace sandbox-only scratch expectations with universal parent/child/sibling/spawn-failure lifecycle assertions.
- Create `agent/session_scratch_lifecycle_test.go`: cover root, child, sibling, fork restore, ordinary restore, environment/prompt parity, and teardown outcomes through real sessions with scripted providers.
- Modify `agent/schema/env_info.go`: carry runtime-only `ScratchDir` in dynamic environment information without serializing it into session metadata.
- Modify `agent/env_info.go`: source `ScratchDir` from the execution environment.
- Modify `agent/serialization_test.go`: prove `EnvironmentInfo.ScratchDir` is omitted from persisted JSON and is empty after a JSON round-trip.
- Modify `agent/prompt_data.go`: carry the scratch path into prompt templates.
- Modify `agent/session_prompts.go`: populate prompt data from the dynamic environment snapshot.
- Modify `agent/prompts/sections/environment.md.tmpl`: show the exact scratch path in the environment block.
- Create `agent/prompts/sections/session-scratch.md.tmpl`: own the normative scratch usage, privacy, cleanup, durability, and ignored-report guidance.
- Create `agent/prompts/sections/verification.md`: own required-gate truthfulness and fixture-versus-product guidance.
- Create `agent/prompts/sections/context-management.md`: own advisory compaction and two-cycle stop/reslice guidance.
- Modify `agent/prompts/sections/delegation.md`: own worktree-versus-shared-workspace posture and existing sandbox deny-path guidance.
- Modify `agent/prompts/templates/system.md.tmpl`: include scratch, verification, and context-management sections for roots.
- Modify `agent/prompts/templates/subagent.md.tmpl`: include scratch, verification, and context-management sections for children; delegation posture remains capability-gated.
- Modify `agent/section_resolver_test.go`: test each focused component semantically and test root/child inclusion without a large rendered-prompt snapshot.
- Create `agent/job_delegate_shared_workspace_test.go`: prove the advisory is returned once, does not block launch, and is omitted for first, isolated, non-overlapping, and no-longer-concurrent delegates.
- Modify `agent/session_tools_jobs.go`: add the advisory to the bounded delegate tool-result shape without changing the job schema.
- Modify `agent/session_tools_jobs_seed100_range_c_test.go`: keep bounded delegate-result projection coverage type-consistent after the optional warning field is added.

---

### Task 1: Promote the Temporary-Directory Primitive and Environment Contract

**Files:**
- Rename: `agent/sandbox/session_tmp.go` -> `agent/sandbox/session_scratch.go`
- Rename: `agent/sandbox/session_tmp_test.go` -> `agent/sandbox/session_scratch_test.go`
- Create: `agent/sandbox/session_scratch_lock_unix.go`
- Create: `agent/sandbox/session_scratch_lock_windows.go`
- Modify: `agent/sandbox/fuzz_contract_structural_test.go`
- Modify: `agent/sandbox/fuzz_policy_assembly_test.go`
- Modify: `agent/sandbox/env_floor.go`
- Modify: `agent/sandbox/env_floor_test.go`
- Modify: `agent/execenv/local.go`
- Modify: `agent/execenv/sandbox_lifecycle_test.go`
- Modify: `envvars/envvars.go`
- Modify: `envvars/envvars_test.go`
- Modify: `docs/environment.md`

- [ ] **Step 0: Capture the pre-project base commit**

```bash
base_ref=refs/serf-plan-bases/session-scratch-orchestration-posture
if git show-ref --verify --quiet "$base_ref"; then
  echo "ref already exists; inspect it before resuming: $base_ref" >&2
  exit 1
fi
git update-ref "$base_ref" HEAD
git rev-parse "$base_ref"
```

Use this ref for every whole-project diff and review range. Do not replace it with `HEAD~N`; task review fixes may add commits.

**Interfaces:**
- Consumes: `os.TempDir`, the existing `serf-sandbox-` reserved prefix, and the existing 24-hour best-effort sweep.
- Produces:

```go
// agent/sandbox/session_scratch.go
type SessionScratch struct {
    Dir string
    base string
    lease scratchLease
}

type scratchLease interface {
    Release() error
}

func acquireScratchLease(path string) (lease scratchLease, contended bool, err error)
func NewSessionScratch(base, workspaceRoot string) (*SessionScratch, error)
func (s *SessionScratch) Cleanup() error

// agent/sandbox/env_floor.go
func ApplySessionScratchEnv(env []string, scratchDir string) []string

// envvars/envvars.go
var SERFScratchDir = Var{
    Name:       "SERF_SCRATCH_DIR",
    Summary:    "Session-scoped private scratch directory provided to agent subprocesses.",
    Visibility: Internal,
}
```

Keep the existing `serf-sandbox-` prefix rather than adding a second prefix or compatibility sweep. It is already the reserved Serf-owned namespace, and retaining it lets the renamed implementation continue sweeping existing crash leftovers without a backward-compatibility alias.

- [ ] **Step 1: Rename the primitive and write failing lifecycle/environment tests**

Use `git mv` for the two files, then change tests to call `NewSessionScratch(base, workspaceRoot)`. Mechanically update `agent/execenv/local.go`, `agent/execenv/sandbox_lifecycle_test.go`, `agent/sandbox/fuzz_contract_structural_test.go`, and `agent/sandbox/fuzz_policy_assembly_test.go` to use the renamed `SessionScratch`, two-argument `NewSessionScratch`, allocator seams, prefix, and sweep helper. At the existing `EnableSandbox` allocation site, pass the canonical `GitRootOrEmpty(e, e.RootDir)` result, falling back to `e.RootDir` for a non-Git workspace. Do not add a `SessionTmp`/`NewSessionTmp` compatibility alias. Add explicit permission and environment assertions:

```go
func TestSessionScratchLifecycle(t *testing.T) {
    base := t.TempDir()
    scratch, err := NewSessionScratch(base, t.TempDir())
    if err != nil {
        t.Fatalf("NewSessionScratch: %v", err)
    }
    if fi, err := os.Stat(scratch.Dir); err != nil || !fi.IsDir() {
        t.Fatalf("scratch must exist as a directory: %v", err)
    } else if got := fi.Mode().Perm(); got != 0o700 {
        t.Fatalf("scratch mode = %04o, want 0700", got)
    }
    if err := scratch.Cleanup(); err != nil {
        t.Fatalf("Cleanup: %v", err)
    }
    if _, err := os.Stat(scratch.Dir); !os.IsNotExist(err) {
        t.Fatalf("scratch remains after cleanup: %v", err)
    }
}

func TestSessionScratchCleanupRefusesUnownedPath(t *testing.T) {
    base := t.TempDir()
    unrelated := filepath.Join(base, "ordinary-temp")
    if err := os.Mkdir(unrelated, 0o700); err != nil {
        t.Fatal(err)
    }
    scratch := &SessionScratch{Dir: unrelated, base: base}
    if err := scratch.Cleanup(); err == nil {
        t.Fatal("Cleanup accepted a directory outside its allocated prefix/base")
    }
    if _, err := os.Stat(unrelated); err != nil {
        t.Fatalf("Cleanup touched unrelated directory: %v", err)
    }
}

func TestApplySessionScratchEnvReplacesBothVariablesOnly(t *testing.T) {
    in := []string{
        "TMPDIR=/ambient/tmp",
        "SERF_SCRATCH_DIR=/ambient/serf",
        "HOME=/home/jesse",
        "GOCACHE=/cache/go",
        "npm_config_cache=/cache/npm",
        "CARGO_HOME=/cache/cargo",
    }
    out := ApplySessionScratchEnv(in, "/tmp/serf-sandbox-owned")
    for _, name := range []string{"TMPDIR", "SERF_SCRATCH_DIR"} {
        if got, _ := envValue(out, name); got != "/tmp/serf-sandbox-owned" {
            t.Fatalf("%s = %q, want session scratch", name, got)
        }
    }
    for name, want := range map[string]string{
        "HOME": "/home/jesse", "GOCACHE": "/cache/go",
        "npm_config_cache": "/cache/npm", "CARGO_HOME": "/cache/cargo",
    } {
        if got, _ := envValue(out, name); got != want {
            t.Fatalf("%s = %q, want unchanged %q", name, got, want)
        }
    }
}
```

Retain and rename the stale-sweep test. Set one prefixed directory and one unrelated directory to 48 hours old, leave a fresh prefixed directory, call `NewSessionScratch`, and assert only the stale prefixed directory was removed.

Add three deterministic allocator tests:

```go
func TestSessionScratchSweepSkipsOldLiveLease(t *testing.T)
func TestSessionScratchSweepRemovesOldReleasedLease(t *testing.T)
func TestSessionScratchFallsBackOutsideWorkspace(t *testing.T)
func TestSessionScratchDoesNotChmodCandidateBase(t *testing.T)
```

For the live test, create a real `SessionScratch`, age its directory to 48 hours while its lease remains held, allocate a second scratch under the same base, and assert the old live directory remains. For the released/crashed test, create an old prefixed directory with an unlocked lease file and assert the next allocation removes it. For the fallback test, inject `sessionScratchTempDir` inside `workspaceRoot` and `sessionScratchUserCacheDir` as an existing directory outside it; allocate with an empty base and assert the resulting path is a newly created child of the cache fallback and `pathWithin(result, workspaceRoot)` is false. For the mode test, chmod a caller-owned candidate base to `0750`, allocate and clean scratch below it, and assert the base remains `0750` throughout while the created scratch itself is `0700`. The seam-mutating fallback test must not call `t.Parallel` and must restore both function variables with `t.Cleanup`. These tests use real non-blocking OS locks and injected paths/timestamps, not sleeps or daemon state.

Add `TestEnvFloorScratchPreservesSecurityFilters` before implementation. Its input contains `SSH_AUTH_SOCK`, AWS/Google/GCLOUD/VAULT variables, an external `KUBECONFIG`, old scratch values, `HOME`, and durable cache values. Require every security-sensitive value to remain absent, both scratch variables to equal the new path, and `HOME`/caches to remain unchanged under `CacheNone`.

- [ ] **Step 2: Run the focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/sandbox ./envvars \
  -run 'TestSessionScratch|TestApplySessionScratchEnv|TestAll' -count=1 -v
```

Expected: compile failure because `SessionScratch`, the workspace-aware allocator/lease helpers, `ApplySessionScratchEnv`, and `envvars.SERFScratchDir` do not exist after the test rename.

- [ ] **Step 3: Implement the renamed allocator and shared environment override**

Rename `SessionTmp`/`NewSessionTmp` to `SessionScratch`/`NewSessionScratch`; rename `sessionTempDir`, `sessionReadDir`, `sessionTmpPrefix`, `crashedSessionMaxAge`, and `sweepCrashedSessions` to `sessionScratchTempDir`, `sessionScratchReadDir`, `sessionScratchPrefix`, `crashedSessionScratchMaxAge`, and `sweepCrashedSessionScratch`. Add `sessionScratchUserCacheDir = os.UserCacheDir` as a deterministic base-selection seam. Keep the scratch-specific lock interface in `agent/sandbox`; this is an OS-released live-process lease, not persisted daemon ownership.

In `session_scratch_lock_unix.go`, build for Darwin, DragonFly, FreeBSD, Linux, NetBSD, OpenBSD, and Solaris. Open the lease with `unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW` at `0600`, require a regular file, chmod it to `0600`, and call `unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)`. Classify only `EWOULDBLOCK`/`EAGAIN` as `contended=true`; every error path closes the descriptor. `Release` calls `LOCK_UN` and closes, joining both errors. In `session_scratch_lock_windows.go`, use `CreateFile` with read/write access, shared read/write/delete, `OPEN_ALWAYS`, and `FILE_FLAG_OPEN_REPARSE_POINT`; reject directories and reparse points; call `LockFileEx` with exclusive/immediate flags over the full range; classify only `ERROR_LOCK_VIOLATION` as contention; and release with `UnlockFileEx` followed by `CloseHandle`.

Select the base before sweeping or creating:

```go
func pathWithin(path, root string) bool {
    if strings.TrimSpace(root) == "" {
        return false
    }
    path = filepath.Clean(path)
    root = filepath.Clean(root)
    rel, err := filepath.Rel(root, path)
    if err != nil || filepath.IsAbs(rel) {
        return false
    }
    return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func sessionScratchBase(requested, workspaceRoot string) (string, error) {
    candidates := []string{requested}
    if strings.TrimSpace(requested) == "" {
        candidates[0] = sessionScratchTempDir()
    }
    if cache, err := sessionScratchUserCacheDir(); err == nil && cache != "" {
        candidates = append(candidates, cache)
    }
    for _, candidate := range candidates {
        absolute, err := filepath.Abs(candidate)
        if err != nil || pathWithin(absolute, workspaceRoot) {
            continue
        }
        info, err := os.Stat(absolute)
        if err != nil || !info.IsDir() {
            continue
        }
        canonical, err := filepath.EvalSymlinks(absolute)
        if err != nil || pathWithin(canonical, workspaceRoot) {
            continue
        }
        return canonical, nil
    }
    return "", fmt.Errorf("sandbox: no session scratch base outside workspace %q", workspaceRoot)
}
```

Canonicalize `workspaceRoot` with `filepath.Abs` plus `filepath.EvalSymlinks` before calling this helper. Never fall back to a lexical or symlink-resolved candidate that is inside that root; a safe failure is preferable to putting scratch in the Git worktree. Candidate bases must already exist and are never chmodded or otherwise mutated. `os.MkdirTemp(cleanBase, sessionScratchPrefix+"*")` atomically creates the Serf-owned private subdirectory beneath the selected base; chmod only that returned directory. Acquire and retain its lease, and sweep only stale prefixed sibling directories whose lease can be acquired:

```go
func NewSessionScratch(base, workspaceRoot string) (*SessionScratch, error) {
    cleanBase, err := sessionScratchBase(base, workspaceRoot)
    if err != nil {
        return nil, err
    }
    sweepCrashedSessionScratch(cleanBase)
    dir, err := os.MkdirTemp(cleanBase, sessionScratchPrefix+"*")
    if err != nil {
        return nil, fmt.Errorf("sandbox: create session scratch: %w", err)
    }
    if err := os.Chmod(dir, 0o700); err != nil {
        _ = os.RemoveAll(dir)
        return nil, fmt.Errorf("sandbox: secure session scratch: %w", err)
    }
    lease, contended, err := acquireScratchLease(filepath.Join(dir, ".serf-session.lock"))
    if err != nil {
        _ = os.RemoveAll(dir)
        return nil, fmt.Errorf("sandbox: acquire session scratch lease: %w", err)
    }
    if contended {
        _ = os.RemoveAll(dir)
        return nil, errors.New("sandbox: new session scratch lease is already held")
    }
    return &SessionScratch{Dir: dir, base: cleanBase, lease: lease}, nil
}

func (s *SessionScratch) Cleanup() error {
    if s == nil || s.Dir == "" {
        return nil
    }
    dir := filepath.Clean(s.Dir)
    base := filepath.Clean(s.base)
    if s.base == "" || filepath.Dir(dir) != base ||
        !strings.HasPrefix(filepath.Base(dir), sessionScratchPrefix) {
        return fmt.Errorf("sandbox: refuse cleanup outside session scratch namespace: %q", s.Dir)
    }
    var releaseErr error
    if s.lease != nil {
        releaseErr = s.lease.Release()
    }
    s.lease = nil
    return errors.Join(releaseErr, os.RemoveAll(dir))
}

func ApplySessionScratchEnv(env []string, scratchDir string) []string {
    out := make([]string, 0, len(env)+2)
    for _, kv := range env {
        name, _, ok := strings.Cut(kv, "=")
        if ok && (name == envvars.TmpDir.Name || name == envvars.SERFScratchDir.Name) {
            continue
        }
        out = append(out, kv)
    }
    if scratchDir != "" {
        out = append(out,
            envvars.TmpDir.Assignment(scratchDir),
            envvars.SERFScratchDir.Assignment(scratchDir),
        )
    }
    return out
}
```

In `sweepCrashedSessionScratch`, keep the age/prefix/directory checks, then call `acquireScratchLease` on the candidate's lease file. A contended lease means the scratch is live even when older than 24 hours: close nothing and skip it. On an acquired lease, release it and remove the stale directory. Any open/stat/lock/release error is best-effort and skips deletion. The lease is process-held only; add no daemon record or restart persistence.

Preserve `ApplyEnvFloor`'s current `floorDrops`, external-`KUBECONFIG`, and cache-strategy filtering exactly. After that existing filtering loop, call `ApplySessionScratchEnv(out, sessionScratch)` so both reserved scratch variables are replaced together; then append the same pre-existing cache redirects only for `CacheSessionPrivate`. Do not redirect `HOME`, `GOCACHE`, npm, or Cargo merely because scratch exists. Make the previously written `TestEnvFloorScratchPreservesSecurityFilters` pass without weakening or duplicating any current filter.

- [ ] **Step 4: Register and document `SERF_SCRATCH_DIR`**

Add `SERFScratchDir` to `allVars` beside the other internal Serf variables. Add this row to the internal/provided-variable table in `docs/environment.md`:

```markdown
| `SERF_SCRATCH_DIR` | Serf-provided private scratch directory for one live session. It may be deleted when the session closes or Serf restarts; move durable artifacts into the workspace or another durable location. |
```

Do not describe it as a user configuration input, persistence location, handoff store, `HOME`, or build-cache override.

- [ ] **Step 5: Run focused tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/sandbox ./envvars -count=1
```

Expected: PASS. The sweep removes only stale prefixed directories whose liveness lease is acquirable, skips an old live lease, direct cleanup refuses paths outside the allocator's recorded base/prefix, allocation falls outside an adversarial workspace-local temp base without changing either candidate base's mode, scratch mode is `0700`, security filters remain active, both environment variables agree, and ordinary `HOME`/caches are unchanged.

- [ ] **Step 6: Commit the primitive**

```bash
git status --short
git add agent/sandbox/session_tmp.go agent/sandbox/session_scratch.go \
  agent/sandbox/session_tmp_test.go agent/sandbox/session_scratch_test.go \
  agent/sandbox/session_scratch_lock_unix.go agent/sandbox/session_scratch_lock_windows.go \
  agent/sandbox/fuzz_contract_structural_test.go agent/sandbox/fuzz_policy_assembly_test.go \
  agent/sandbox/env_floor.go agent/sandbox/env_floor_test.go \
  agent/execenv/local.go agent/execenv/sandbox_lifecycle_test.go \
  envvars/envvars.go envvars/envvars_test.go docs/environment.md
git commit -m "feat(execenv): promote session scratch primitive

Rename the existing sandbox-owned temporary directory into the universal
session scratch lifecycle. Set TMPDIR and SERF_SCRATCH_DIR together while
leaving HOME and durable build-cache policy unchanged, and retain the
prefix-limited 24-hour crash sweep."
```

---

### Task 2: Make Scratch Universal Per Live Session

**Files:**
- Modify: `agent/execenv/execenv.go`
- Modify: `agent/execenv/local.go`
- Modify: `agent/execenv/local_test.go`
- Modify: `agent/execenv/sandbox_tmpbase_test.go`
- Modify: `agent/execenv/sandbox_reroot_test.go`
- Modify: `agent/execenv/sandbox_lifecycle_test.go`
- Modify: `agent/execenv/sandbox_lifecycle_program_fuzz_test.go`
- Modify: `agent/execenv/process_runtime_program_fuzz_test.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_lifecycle_tail_coverage_fuzz_test.go`
- Modify: `agent/session_lifecycle_slots_fuzz_test.go`
- Modify: `agent/subagents.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_worktree_dispose.go`
- Modify: `agent/sandbox_delegate_create_test.go`
- Create: `agent/session_scratch_lifecycle_test.go`
- Create: `agent/job_delegate_restore_scratch_test.go`

**Interfaces:**
- Consumes: `sandbox.NewSessionScratch`, `LocalExecutionEnvironment.WithWorkingDirectory`, `LocalExecutionEnvironment.EnableSandbox`, `Session.Close`, Project 4's construction-layer `prepareSubagentRunWithModelSelection`, and restore paths.
- Produces:

```go
// agent/execenv/local.go
func (e *LocalExecutionEnvironment) SessionScratchDir() string
func (e *LocalExecutionEnvironment) NewSessionEnvironment(dir string) (*LocalExecutionEnvironment, error)
func (e *LocalExecutionEnvironment) DisposeSessionScratch()

// agent/execenv/execenv.go
type SessionScratchProvider interface {
    SessionScratchDir() string
    DisposeSessionScratch()
}

func SessionScratchDir(env ExecutionEnvironment) string
func DisposeSessionScratch(owner SessionScratchProvider)
```

`SessionScratchProvider` remains optional. `SessionScratchDir` returns `""` for nil/non-providers, and `DisposeSessionScratch` accepts an optional provider and returns without action when it is nil.

```go
func DisposeSessionScratch(owner SessionScratchProvider) {
    if owner != nil {
        owner.DisposeSessionScratch()
    }
}
```

`NewSessionEnvironment` is the only child-session clone. It gets a fresh `runningPIDs` tracker and scratch, but retains the parent's filesystem/test seams, environment policy, and re-rooted sandbox policy. `WithWorkingDirectory` remains a non-owning within-session/control view: it shares the process tracker and copies the scratch path for command/prompt parity, but cannot delete the owner's directory. `Session` retains the original scratch-owning environment capability so closing a re-rooted session removes scratch only after the current view has stopped the shared tracked processes.

- [ ] **Step 1: Write failing execution-environment lifecycle tests**

Add tests with a private scratch base via `env.sandboxTmpBase`:

```go
func TestLocalExecutionEnvironmentInitializesUniversalScratch(t *testing.T) {
    env := NewLocalExecutionEnvironment(t.TempDir())
    env.sandboxTmpBase = t.TempDir()
    if err := env.Initialize(); err != nil {
        t.Fatalf("Initialize: %v", err)
    }
    scratch := env.SessionScratchDir()
    if scratch == "" {
        t.Fatal("Initialize did not provision scratch")
    }
    res, err := env.ExecCommand(context.Background(),
        `printf '%s\n%s\n%s\n%s\n' "$TMPDIR" "$SERF_SCRATCH_DIR" "$HOME" "$GOCACHE"`,
        5000, "", nil)
    if err != nil {
        t.Fatalf("ExecCommand: %v", err)
    }
    lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
    if len(lines) != 4 || lines[0] != scratch || lines[1] != scratch {
        t.Fatalf("scratch environment = %#v, want %q twice", lines, scratch)
    }
    env.Cleanup()
    if _, err := os.Stat(scratch); !os.IsNotExist(err) {
        t.Fatalf("scratch remains after Cleanup: %v", err)
    }
}

func TestSessionEnvironmentScratchOwnership(t *testing.T) {
    root := NewLocalExecutionEnvironment(t.TempDir())
    root.sandboxTmpBase = t.TempDir()
    if err := root.Initialize(); err != nil { t.Fatal(err) }

    reroot := root.WithWorkingDirectory(t.TempDir())
    if got, want := reroot.SessionScratchDir(), root.SessionScratchDir(); got != want {
        t.Fatalf("re-root scratch = %q, want same live-session scratch %q", got, want)
    }

    child, err := root.NewSessionEnvironment(root.WorkingDirectory())
    if err != nil { t.Fatal(err) }
    if child.SessionScratchDir() == root.SessionScratchDir() {
        t.Fatal("child session reused parent scratch")
    }
}
```

Update the existing teardown-order test so a tracked process writes a sentinel in `SERF_SCRATCH_DIR` from its TERM handler. Assert the sentinel exists when TERM runs and the directory is gone only after `Cleanup` returns.

- [ ] **Step 2: Write failing real-session uniqueness/restore tests**

In `agent/session_scratch_lifecycle_test.go`, use a scripted provider and real `LocalExecutionEnvironment` values. Read the allocated paths through the scratch capability rather than adding an exported test-only base-directory option. Add these behavior tests:

```go
func TestSessionScratchUniqueAcrossRootChildrenAndSiblings(t *testing.T)
func TestSessionScratchForkRestoreGetsNewEmptyDirectory(t *testing.T)
func TestSessionScratchRestoreGetsNewEmptyDirectory(t *testing.T)
func TestSessionScratchSpawnFailureAndParentTeardownCleanOwnedDirectories(t *testing.T)
func TestSessionCloseKeepsScratchThroughHooksAndMCPShutdown(t *testing.T)
```

The first test creates a root plus two blocked background delegates. Read each retained child environment through its transcript ref and assert three facts: root/child/sibling paths are non-empty and pairwise distinct, each path is outside the Git worktree, and each child shell sees its own `TMPDIR` and `SERF_SCRATCH_DIR`. In a separate deterministic sandbox-wrapper test, construct two enforced child environments with the existing policy fixture and assert each wrapper exposes its own scratch path and has no bind or writable-path entry for its sibling's scratch. Do not require a platform sandbox backend in the default test suite.

The fork test uses the existing `ForkSession` fixture path to create fork metadata, writes a marker in the parent scratch, restores the fork into a fresh local environment, and asserts the fork path differs and contains no marker. The ordinary restore test closes a session after writing a marker, restores its metadata into a fresh local environment, and makes the same new-empty-path assertion.

Write `TestSandboxInvocationGrantPreservesSessionScratch` in `agent/execenv/sandbox_reroot_test.go`: initialize a sandboxed owner, create a one-invocation grant, and assert its `SessionScratchDir`, `TMPDIR`, and `SERF_SCRATCH_DIR` equal the owner; cleaning/discarding the grant must leave the owner directory present.

For `TestSessionCloseKeepsScratchThroughHooksAndMCPShutdown`, use a real scratch-owning local environment, a deterministic recording command-hook runtime, and a fake stdio MCP transport. The hook and transport `Close` each `os.Stat` the exact scratch path and append to a channel-backed order log; the environment cleanup wrapper appends after process cleanup. Require `hook < mcp-close < env-cleanup`, both stat checks to succeed, and the directory to be absent only after `Session.Close` returns. Do not use sleeps.

In `agent/job_delegate_restore_scratch_test.go`, write table-driven tests for every exit after restored local environment allocation: frozen-skill restoration, session restoration, required-tool validation, track collision/error, and side-effect admission. Also write `TestDiscardRestoredCandidateCleansEnvironmentScratch` and `TestRestoredLocalSubagentOwnsEnvironment`. Each failure must remove the allocated child scratch while leaving the parent scratch present; the successful restored local child must have `ownsEnv == true` and `isolation == desc.Isolation`, including the persisted `"worktree"` case.

Add `TestSessionCloseWithoutScratchProvider` and `TestDiscardRestoredCandidateWithoutScratchProvider` using an existing deterministic `ExecutionEnvironment` fake that implements `Cleanup` but not `SessionScratchProvider`. Invoke candidate discard with `cleanupEnv=true`; assert cleanup occurs exactly once and neither path panics or attempts scratch disposal. Add a `cleanupEnv=false` case proving a shared non-provider environment is not cleaned.

- [ ] **Step 3: Run focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/execenv ./agent \
  -run 'TestLocalExecutionEnvironmentInitializesUniversalScratch|TestSessionEnvironmentScratchOwnership|TestSessionScratch|TestSandboxInvocationGrantPreservesSessionScratch|Test.*Restore.*Scratch|TestDiscardRestoredCandidateCleansEnvironmentScratch|Test.*WithoutScratchProvider' \
  -count=1 -v
```

Expected: compile failure because the new scratch accessors, optional-capability helper, and child-session constructor do not exist; current unsandboxed environments have no owned scratch, invocation grants drop the path, restored-local exits leak fresh environments and isolation metadata, candidate discard does not clean the environment, and close deletes sandbox scratch before hooks/MCP shutdown.

- [ ] **Step 4: Implement universal local-environment ownership**

Replace the mechanically updated `ownedSessionTmp *sandbox.SessionScratch` with an owning pointer plus a non-owning path view:

```go
ownedSessionScratch *sandbox.SessionScratch
sessionScratchDir   string

func (e *LocalExecutionEnvironment) ensureSessionScratch() error {
    if e.ownedSessionScratch != nil && e.ownedSessionScratch.Dir != "" {
        return nil
    }
    workspaceRoot := GitRootOrEmpty(e, e.RootDir)
    if workspaceRoot == "" {
        workspaceRoot = e.RootDir
    }
    scratch, err := sandbox.NewSessionScratch(e.sandboxTmpBase, workspaceRoot)
    if err != nil {
        return err
    }
    e.ownedSessionScratch = scratch
    e.sessionScratchDir = scratch.Dir
    return nil
}

func (e *LocalExecutionEnvironment) SessionScratchDir() string {
    if e == nil {
        return ""
    }
    if e.ownedSessionScratch != nil {
        return e.ownedSessionScratch.Dir
    }
    return e.sessionScratchDir
}

func (e *LocalExecutionEnvironment) DisposeSessionScratch() {
    if scratch := e.ownedSessionScratch; scratch != nil {
        e.ownedSessionScratch = nil
        e.sessionScratchDir = ""
        _ = scratch.Cleanup()
    }
}
```

Call `ensureSessionScratch` from `Initialize` for sandbox-off sessions. In `EnableSandbox`, reuse an existing scratch or provision one before constructing the wrapper; pass `SessionScratchDir()` to `sandbox.NewWrapper`. On every failure after provisioning, clear policy/wrapper and dispose only the fresh environment's scratch.

Change `commandEnvironment` to finish with:

```go
return sandbox.ApplySessionScratchEnv(
    filteredEnvWithSource(e.EnvPolicy, extra, inherited),
    e.SessionScratchDir(),
)
```

In both `WithWorkingDirectory` and `WithSandboxInvocationGrant`, set `sessionScratchDir: e.SessionScratchDir()` but do not copy `ownedSessionScratch`. This preserves the exact path for ephemeral control and one-invocation grant views while ensuring clone cleanup cannot delete a live owner's scratch; make the Step 2 grant test pass. Add `NewSessionEnvironment` by re-rooting first, then clearing the inherited path view and provisioning distinct ownership:

```go
func (e *LocalExecutionEnvironment) NewSessionEnvironment(dir string) (*LocalExecutionEnvironment, error) {
    child := e.WithWorkingDirectory(dir)
    if err := child.SandboxReRootError(); err != nil {
        return nil, err
    }
    child.runningPIDs = &sync.Map{}
    child.ownedSessionScratch = nil
    child.sessionScratchDir = ""
    child.Wrapper = nil
    if err := child.ensureSessionScratch(); err != nil {
        return nil, err
    }
    if child.Sandbox != nil && child.Sandbox.Enforced() {
        wrapper, err := sandbox.NewWrapper(*child.Sandbox, child.Sandbox.HostBinaryPath(), child.SessionScratchDir())
        if err != nil {
            child.DisposeSessionScratch()
            child.Sandbox = nil
            return nil, err
        }
        child.Wrapper = wrapper
    }
    return child, nil
}
```

In `Cleanup`, retain the existing order but defer owned `SessionScratch.Cleanup` until after TERM/grace/KILL. Rename every `DisposeSandboxScratch` use to `DisposeSessionScratch`; do not retain a compatibility alias.

- [ ] **Step 5: Give every local child a fresh session environment**

In `prepareSubagentRunWithModelSelection`, replace the initial shared local environment with `NewSessionEnvironment`, using the requested `workingDir` or the parent's current directory. Keep `prepareSubagentRun` as Project 4's selection-only wrapper so model preflight still completes before IDs, worktrees, scratch, transcripts, metadata, or watches are created:

```go
parentEnv := s.currentEnv()
subEnv := parentEnv
ownsEnv := false
if local, ok := parentEnv.(*execenv.LocalExecutionEnvironment); ok {
    childDir := strings.TrimSpace(workingDir)
    if childDir == "" {
        childDir = local.WorkingDirectory()
    }
    child, err := local.NewSessionEnvironment(childDir)
    if err != nil {
        return nil, fmt.Errorf("create child session environment: %w", err)
    }
    subEnv = child
    ownsEnv = true
} else if strings.TrimSpace(workingDir) != "" {
    return nil, errors.New("execution environment does not support working_dir override")
}
```

Apply an explicit delegate sandbox to that already-private child environment; do not clone it a second time. On every error after `ownsEnv` becomes true, call the child environment's full `Cleanup` method so any process is stopped before scratch is removed. Store `ownsEnv` on `subagent` instead of deriving it from isolation/sandbox arguments. Add immutable `isolation string` to `subagent` here and set it from `subCfg.spawn.isolation` for fresh children; Task 5 will consume the same field for advisory classification.

Change `restoreDelegateChildEnvironment` to return `(execenv.ExecutionEnvironment, bool, error)`, where the bool is true exactly when it allocated a fresh local child environment. Call `local.NewSessionEnvironment(workDir)`, immediately install a deferred full-cleanup guard, set the restored `EnvPolicy`, and then apply the persisted sandbox. Mark the guard adopted only on the successful `(child, true, nil)` return. Every sandbox-resolution, sandbox-enable, or validation exit after allocation therefore cleans the fresh scratch. A non-local environment returns `(env, false, nil)` only when its working directory is already correct.

- [ ] **Step 6: Make fresh/restore failures and parent teardown own cleanup**

In both `NewSession` and `RestoreSessionFromMetaWithConfig`, after successful `env.Initialize`, install a failure guard:

```go
sessionConstructed := false
defer func() {
    if !sessionConstructed {
        env.Cleanup()
    }
}()
```

Set `sessionConstructed = true` only immediately before the successful return. Register this environment guard immediately after initialization, before the existing later MCP-manager error guard; Go's LIFO defer order then closes any created MCP manager before environment/scratch cleanup. The full cleanup preserves the required tracked-process-before-scratch order even if construction started another subprocess before a later error. In restore, call `env.Initialize()` before `provisionRestoredSandbox` so sandbox attachment always receives the new restore scratch.

Add immutable optional `scratchOwner execenv.SessionScratchProvider` to `Session`, populated only when the successfully initialized fresh/restore environment satisfies that capability. Reorder the resource tail of `Session.close` to:

1. run the bounded SessionEnd hooks while scratch exists;
2. emit the existing SessionEnd event in its current semantic position;
3. call `s.mcpMgr.Close()` and wait for stdio MCP shutdown while scratch exists;
4. when `cleanupEnv` is true, call `s.currentEnv().Cleanup()` so tracked processes stop before owned scratch removal, then call the nil-safe `execenv.DisposeSessionScratch(s.scratchOwner)` helper for a re-rooted non-owning current view;
5. continue transcript/export/event-channel teardown unchanged.

Do not leave the current environment `Cleanup` block before SessionEnd hooks. Change the internal helper to `discardRestoredCandidate(cleanupEnv bool)`. It still omits SessionEnd hooks/events, closes each child with `sub.sess.discardRestoredCandidate(sub.ownsEnv)`, and closes MCP first. Only when `cleanupEnv` is true does it perform `currentEnv().Cleanup()` plus nil-safe `execenv.DisposeSessionScratch(s.scratchOwner)` before transcript/event teardown. Call it with the restored `ownsEnv` result at every candidate-discard site, so fresh local candidates clean while a shared non-local environment is never disposed. Update the two lifecycle fuzz files' direct calls with the ownership appropriate to their fixture.

Make the Step 2 `TestSessionCloseKeepsScratchThroughHooksAndMCPShutdown` pass with the stated ordering; do not replace its channels with sleeps.

In `restoreTerminalDelegateChildClaimed`, receive `childEnv, ownsEnv, err`; install another unadopted-environment cleanup guard immediately. Keep it armed across frozen-skill restoration and `restoreDelegateSession` failure. Once a `Session` successfully adopts the environment, disarm the raw guard because `discardRestoredCandidate` owns later failure cleanup. Set `subagent.ownsEnv = ownsEnv` and `subagent.isolation = desc.Isolation` when constructing every restored child so a resumed worktree delegate remains isolated for overlap-warning logic. Make all Step 2 restored-delegate ownership/failure cases pass; do not collapse distinct exits into one synthetic helper-only assertion.

In parent close, use the ownership bit to select full child cleanup:

```go
for _, sub := range subs {
    sub.sess.close(budgetCtx, sub.ownsEnv)
}
```

Remove the later child-only narrow scratch disposal block. A local child has its own PID tracker, so its full cleanup terminates its tracked processes before deleting its scratch. A non-local shared test environment keeps `ownsEnv == false` and retains the historical shared cleanup path.

In `disposeExecute`, make the same ownership decision at the existing child-eviction point:

```go
sub.sess.close(ctx, sub.ownsEnv)
s.subagents.remove(id)
```

Remove the following manual local-environment `Cleanup` block; the child session's full close now owns process shutdown and its retained scratch-owner disposal exactly once.

- [ ] **Step 7: Update deterministic lifecycle/fuzz expectations**

Mechanically rename `ownedSessionTmp` to `ownedSessionScratch` and `DisposeSandboxScratch` to `DisposeSessionScratch` in the focused lifecycle tests/fuzzer. Preserve the existing assertion that cleaning a non-owning `WithWorkingDirectory` view does not remove the owner's scratch, then prove closing the owning session does remove it. Keep the sandbox policy/wrapper's existing `SessionTmp` vocabulary; it still describes the sandbox's writable `/tmp` mapping and is not a second allocator. Change off-mode expectations: `EnableSandbox(nil)` leaves policy/wrapper off but retains universal scratch. Assert `HOME`, `GOCACHE`, `npm_config_cache`, and `CARGO_HOME` remain inherited unless the existing `CacheSessionPrivate` sandbox strategy redirects only the caches.

- [ ] **Step 8: Run focused lifecycle suites**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/execenv \
  -run 'TestLocalExecutionEnvironment|TestSessionEnvironment|TestCleanupDisposes|TestEnableSandbox|TestDisposeSessionScratch|Test.*InvocationGrant.*Scratch' \
  -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestSessionScratch|TestPrepareSubagentRun.*Scratch|TestParentClose.*Scratch|Test.*Restore.*Scratch|TestDiscardRestoredCandidate.*Scratch|TestSessionCloseWithoutScratchProvider|TestDiscardRestoredCandidateWithoutScratchProvider' \
  -count=1 -v
GOCACHE=/tmp/serf-gocache go test -tags=serffuzz ./agent/execenv \
  -run 'FuzzSandboxLifecycleProgram|FuzzProcessRuntimeProgram' -count=1
```

Expected: PASS. Root, child, sibling, and restored fork paths are distinct; re-root and invocation-grant views retain the exact path without cleanup ownership; restored paths are new/empty; every unadopted restore exit and candidate discard cleans its scratch; SessionEnd hooks and MCP close finish before process/scratch cleanup; spawn failure and ordinary close remove owned directories after process shutdown.

- [ ] **Step 9: Commit session ownership**

```bash
git status --short
git add agent/execenv/local.go agent/execenv/execenv.go \
  agent/execenv/local_test.go agent/execenv/sandbox_tmpbase_test.go \
  agent/execenv/sandbox_reroot_test.go \
  agent/execenv/sandbox_lifecycle_test.go agent/execenv/sandbox_lifecycle_program_fuzz_test.go \
  agent/execenv/process_runtime_program_fuzz_test.go agent/session_init.go \
  agent/session.go agent/session_lifecycle.go \
  agent/session_lifecycle_tail_coverage_fuzz_test.go agent/session_lifecycle_slots_fuzz_test.go \
  agent/subagents.go agent/job_delegate.go \
  agent/session_tools_worktree_dispose.go agent/sandbox_delegate_create_test.go \
  agent/session_scratch_lifecycle_test.go agent/job_delegate_restore_scratch_test.go
git commit -m "feat(agent): own scratch per live session

Provision scratch for sandboxed and unsandboxed sessions, preserve it across
worktree re-rooting, and give every local child and restore a fresh private
environment. Close child processes before removing their scratch and clean
construction failures without touching a parent environment."
```

---

### Task 3: Propagate Scratch to Hooks and Stdio MCP Processes

**Files:**
- Modify: `agent/internal/hooks/hooks.go`
- Modify: `agent/internal/hooks/command_runtime.go`
- Modify: `agent/internal/hooks/sandbox_test.go`
- Modify: `agent/internal/mcp/manager.go`
- Modify: `agent/internal/mcp/sandbox_test.go`
- Modify: `agent/session_init.go`

**Interfaces:**
- Consumes: `sandbox.ApplySessionScratchEnv` and `execenv.SessionScratchDir` from Tasks 1-2.
- Produces:

```go
// agent/internal/hooks
func (r *Runner) SetSessionScratchDir(path string)

// agent/internal/mcp
func WithSessionScratchDir(path string) Option
```

- [ ] **Step 1: Write failing hook and MCP construction tests**

For hooks, build a command invocation with a fixed inherited environment and no sandbox wrapper, capture `cmd.Env`, and assert both scratch variables equal the configured path. Repeat with a sandbox wrapper and assert the same path survives confinement.

For MCP, call a deterministic `productionDialWithScratchAndEnv` seam for a stdio server with and without a wrapper. Inspect the constructed command transport and assert:

```go
func commandEnvValue(env []string, name string) string {
    prefix := name + "="
    for _, entry := range env {
        if strings.HasPrefix(entry, prefix) {
            return strings.TrimPrefix(entry, prefix)
        }
    }
    return ""
}

for _, name := range []string{"TMPDIR", "SERF_SCRATCH_DIR"} {
    if got := commandEnvValue(cmd.Env, name); got != scratch {
        t.Fatalf("%s = %q, want %q", name, got, scratch)
    }
}
```

Also assert configured server env cannot override either reserved scratch variable.

- [ ] **Step 2: Run focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/hooks ./agent/internal/mcp \
  -run 'Test.*SessionScratch' -count=1 -v
```

Expected: compile failure because neither runner/manager accepts a scratch path; unsandboxed hook/MCP spawns currently use ambient `os.Environ`.

- [ ] **Step 3: Thread the path through hook invocation construction**

Add `sessionScratchDir string` to `hooks.Runner` and `SessionScratchDir string` to `commandHookInvocation`. Preserve the existing deterministic helper as an empty-scratch seam and add a scratch-aware helper for the runner:

```go
func (r *Runner) SetSessionScratchDir(path string) {
    r.sessionScratchDir = strings.TrimSpace(path)
}

func executeCommandHookWithRuntime(
    ctx context.Context,
    hook plugin.RegisteredHook,
    input Input,
    runtime commandHookRuntime,
    wrapper ...*sandbox.Wrapper,
) (hookResult, error) {
    return executeCommandHookWithRuntimeAndScratch(ctx, hook, input, runtime, "", wrapper...)
}

func executeCommandHookWithRuntimeAndScratch(
    ctx context.Context,
    hook plugin.RegisteredHook,
    input Input,
    runtime commandHookRuntime,
    scratchDir string,
    wrapper ...*sandbox.Wrapper,
) (hookResult, error)
```

Have `Runner.runHook` call `executeCommandHookWithRuntimeAndScratch` with `r.sessionScratchDir`; set `invocation.SessionScratchDir` after `prepareCommandHookInvocation` returns. Existing direct/fuzz callers of `executeCommandHookWithRuntime` continue to exercise the empty-scratch path without signature churn. In `systemCommandHookRuntime.Run`, apply the universal override before the optional sandbox floor:

```go
env := sandbox.ApplySessionScratchEnv(invocation.Env, invocation.SessionScratchDir)
if sbx := invocation.SandboxWrapper; sbx != nil {
    env = sandbox.ApplyEnvFloor(env, sbx.Policy(), invocation.SessionScratchDir)
    sbx.Confine(cmd, sbx.Policy().Git.WorktreeRoot)
    cmd.ExtraFiles = nil
}
cmd.Env = env
```

- [ ] **Step 4: Thread the path through MCP transport construction**

Add `scratchDir string` to the MCP options struct and:

```go
func WithSessionScratchDir(path string) Option {
    return func(o *managerOptions) { o.scratchDir = strings.TrimSpace(path) }
}
```

Keep the existing `productionDialWithEnv` test seam and add the scratch-aware seam used by `NewManager`:

```go
func productionDial(
    cfg mcpconfig.ServerConfig,
    wrapper *sandbox.Wrapper,
) func(context.Context) (mcpsdk.Transport, error) {
    return productionDialWithScratchAndEnv(cfg, wrapper, "", os.Environ)
}

func productionDialWithScratch(
    cfg mcpconfig.ServerConfig,
    wrapper *sandbox.Wrapper,
    scratchDir string,
) func(context.Context) (mcpsdk.Transport, error) {
    return productionDialWithScratchAndEnv(cfg, wrapper, scratchDir, os.Environ)
}

func productionDialWithEnv(
    cfg mcpconfig.ServerConfig,
    wrapper *sandbox.Wrapper,
    environ func() []string,
) func(context.Context) (mcpsdk.Transport, error) {
    return productionDialWithScratchAndEnv(cfg, wrapper, "", environ)
}

func productionDialWithScratchAndEnv(
    cfg mcpconfig.ServerConfig,
    wrapper *sandbox.Wrapper,
    scratchDir string,
    environ func() []string,
) func(context.Context) (mcpsdk.Transport, error)
```

Change `NewManager`'s existing construction call to `productionDialWithScratch(cfg, o.wrapper, o.scratchDir)`. Existing package tests and fuzz programs keep using the unchanged two-argument `productionDial` seam. After `transportForConfigWithEnv` creates a stdio `CommandTransport`, call `ApplySessionScratchEnv(ct.Command.Env, scratchDir)` so the reserved values override both inherited and configured server values. If a wrapper exists, pass the same path into `confineCommandUnderSandbox`; that existing path continues rebuilding the scrubbed inherited/configured environment, then calls `ApplyEnvFloor` with the owning scratch before confinement. Do not newly scrub the unsandboxed MCP environment or otherwise change its inherited/configured environment policy. Remote HTTP/SSE transports receive no process environment and remain unchanged.

In `initPlugins` and `initMCP`, compute the path once from the current environment and wire it:

```go
scratchDir := execenv.SessionScratchDir(s.currentEnv())
runner.SetSessionScratchDir(scratchDir)

mgr, outcomes := mcp.NewManager(ctx, configs, nil,
    mcp.WithSandboxWrapper(s.sandboxWrapper()),
    mcp.WithSessionScratchDir(scratchDir),
)
```

- [ ] **Step 5: Run focused tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/hooks ./agent/internal/mcp -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -run 'Test.*Plugin|Test.*MCP' -count=1
```

Expected: PASS. Shell, hook, and stdio MCP child processes agree on the owning session's two scratch variables in both sandbox-off and enforced modes.

- [ ] **Step 6: Commit subprocess propagation**

```bash
git status --short
git add agent/internal/hooks/hooks.go agent/internal/hooks/command_runtime.go \
  agent/internal/hooks/sandbox_test.go agent/internal/mcp/manager.go \
  agent/internal/mcp/sandbox_test.go agent/session_init.go
git commit -m "feat(agent): propagate session scratch to subprocesses

Apply the same TMPDIR and SERF_SCRATCH_DIR to shell-adjacent command hooks and
stdio MCP servers, before optional sandbox confinement, so every subprocess
uses its owning live session's scratch path."
```

---

### Task 4: Expose Exact Scratch Lifecycle in Environment Information and Prompts

**Files:**
- Modify: `agent/schema/env_info.go`
- Modify: `agent/env_info.go`
- Modify: `agent/serialization_test.go`
- Modify: `agent/prompt_data.go`
- Modify: `agent/session_prompts.go`
- Modify: `agent/prompts/sections/environment.md.tmpl`
- Create: `agent/prompts/sections/session-scratch.md.tmpl`
- Modify: `agent/prompts/templates/system.md.tmpl`
- Modify: `agent/prompts/templates/subagent.md.tmpl`
- Modify: `agent/section_resolver_test.go`
- Modify: `agent/session_scratch_lifecycle_test.go`

**Interfaces:**
- Consumes: `execenv.SessionScratchDir(env)` from Task 2.
- Produces:

```go
// Add to schema.EnvironmentInfo. Runtime-only: session metadata must not replay it.
ScratchDir string `json:"-"`

// Add to promptData.
ScratchDir string
```

Add one local helper in `agent/section_resolver_test.go` and reuse it for every focused component test in Tasks 4 and 6:

```go
func embeddedSectionResolverForTest(agentName string) *sectionResolver {
    return &sectionResolver{
        provider: "openai",
        agent:    agentName,
        agentFS:  bundled.Agents(),
        sources:  []sectionSource{
            embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
        },
    }
}
```

- [ ] **Step 1: Write failing structured environment/prompt tests**

Extend the environment JSON fixture with `ScratchDir: "/tmp/serf-sandbox-session"`. After marshal, assert the encoded object has no `scratch_dir` key. After unmarshal, assert `ScratchDir == ""`, then compare the remaining persisted fields with the fixture after clearing its `ScratchDir`. This makes restart behavior explicit: restore creates a fresh environment and `envInfoFromEnv` supplies its new path rather than replaying a stale one from metadata.

Add a focused section test that renders only `environment` and `session-scratch`:

```go
func TestSessionScratchPromptSectionsExposeLifecycle(t *testing.T) {
    resolver := embeddedSectionResolverForTest("coordinator")
    data := promptData{ScratchDir: "/tmp/serf-sandbox-session"}

    environment := resolver.Section("environment", data)
    if !strings.Contains(environment, "Scratch directory: /tmp/serf-sandbox-session") {
        t.Fatalf("environment section missing exact path: %s", environment)
    }

    guidance := resolver.Section("session-scratch", data)
    for _, want := range []string{
        "/tmp/serf-sandbox-session",
        "temporary files", "generated diagnostics", "intermediate reports",
        "private to this session", "session closes or Serf restarts",
        "workspace or another durable location", "Do not force-add ignored",
    } {
        if !strings.Contains(guidance, want) {
            t.Fatalf("session scratch section missing %q: %s", want, guidance)
        }
    }
}
```

In the real lifecycle test, assert `sess.envInfo.ScratchDir`, `execenv.SessionScratchDir(sess.currentEnv())`, the `TMPDIR`/`SERF_SCRATCH_DIR` values returned by a shell command, and the path in the focused prompt data are byte-for-byte equal.

- [ ] **Step 2: Run focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestEnvironmentInfo_JSONRoundTrip|TestSessionScratchPromptSectionsExposeLifecycle|TestSessionScratchEnvironmentPromptParity' \
  -count=1 -v
```

Expected: compile failure because `EnvironmentInfo.ScratchDir` and `promptData.ScratchDir` do not exist; the section is not embedded or included.

- [ ] **Step 3: Carry the dynamic path through environment information**

Add runtime-only `ScratchDir` with JSON tag `-` to `schema.EnvironmentInfo`. Populate it in `envInfoFromEnv`:

```go
return schema.EnvironmentInfo{
    WorkingDir:  wd,
    ScratchDir:  execenv.SessionScratchDir(env),
    Platform:    plat,
    OSVersion:   osv,
    Today:       clk.Now().UTC().Format("2006-01-02"),
    Workspace:   ScanWorkspace(wd),
}
```

Copy `s.envInfo.ScratchDir` into `promptData.ScratchDir`. Because `swapEnvAndRefresh` derives a new `EnvironmentInfo` from a `WithWorkingDirectory` clone that shares scratch, worktree re-root keeps the cached prompt path stable while still refreshing working-directory/git data.

- [ ] **Step 4: Add the focused scratch prompt component**

Add this line inside `<environment>`:

```gotemplate
Scratch directory: {{ .ScratchDir }}
```

Create `session-scratch.md.tmpl` with the normative contract:

```gotemplate
## Session scratch

Your session-scoped scratch directory is `{{ .ScratchDir }}`. Use it for temporary
files, generated diagnostics, intermediate reports, and disposable working data.
It is private to this session and may be deleted when the session closes or Serf
restarts. Move anything needed after handoff into the workspace or another durable
location.

Do not force-add ignored temporary reports. Put them in session scratch unless the
user explicitly requested a durable product artifact.
```

Include `{{ section "session-scratch" }}` immediately after `{{ section "environment" }}` in both master templates. Do not duplicate the prose into root/subagent role prompts.

- [ ] **Step 5: Prove prompt-cache stability across turns and re-root**

Extend the real lifecycle test: render once, execute another turn, and assert the cached prompt's scratch line is unchanged. Re-root the session with the existing `enterWorktree`/`swapEnvAndRefresh` test harness and assert the working directory changes but `ScratchDir` and the scratch guidance path do not.

- [ ] **Step 6: Run focused tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestEnvironmentInfo_JSONRoundTrip|TestSessionScratchPrompt|TestSessionScratchEnvironmentPromptParity|TestSessionScratchSurvivesWorktreeReroot' \
  -count=1 -v
```

Expected: PASS. Tests inspect structured fields and focused components; no test snapshots or regex-matches the full rendered system prompt.

- [ ] **Step 7: Commit prompt exposure**

```bash
git status --short
git add agent/schema/env_info.go agent/env_info.go agent/serialization_test.go \
  agent/prompt_data.go agent/session_prompts.go \
  agent/prompts/sections/environment.md.tmpl \
  agent/prompts/sections/session-scratch.md.tmpl \
  agent/prompts/templates/system.md.tmpl agent/prompts/templates/subagent.md.tmpl \
  agent/section_resolver_test.go agent/session_scratch_lifecycle_test.go
git commit -m "feat(prompts): expose exact session scratch lifecycle

Carry the live scratch path through EnvironmentInfo and focused root/child
prompt sections. State privacy, cleanup, restart, durability, and ignored-report
rules without turning scratch into a handoff store."
```

---

### Task 5: Return a Non-Blocking Shared-Workspace Delegate Advisory

**Files:**
- Modify: `agent/subagents.go`
- Modify: `agent/job_delegate.go`
- Modify: `agent/session_tools_jobs.go`
- Create: `agent/job_delegate_shared_workspace_test.go`
- Modify: `agent/session_tools_jobs_seed100_range_c_test.go`

**Interfaces:**
- Consumes: `Session.liveDirectSubagents`, each child's immutable isolation value, current working directories, and existing `delegateResult` projection.
- Produces:

```go
// Add to delegateResult.
Warning string

// Add to delegateToolResult.
Warning *string `json:"warning,omitempty"`

func (s *Session) sharedWorkspaceDelegateWarning(requestedIsolation, workingDir string) string
```

- [ ] **Step 1: Write failing advisory behavior tests**

Create a channel-blocked scripted adapter. Launch a first background shared delegate and wait on a channel emitted when its first model request starts. Launch a second shared delegate from the same parent working directory and assert:

```go
if second.Err != nil || second.JobID == "" {
    t.Fatalf("advisory blocked delegate launch: %+v", second)
}
for _, want := range []string{parent.currentEnv().WorkingDirectory(), `isolation="worktree"`, "shared workspace"} {
    if !strings.Contains(second.Warning, want) {
        t.Fatalf("warning missing %q: %q", want, second.Warning)
    }
}
```

Marshal the result, unmarshal into `map[string]any`, and assert one `warning` field exists. Release/stop both jobs through existing job controls.

Add table-driven helper cases:

```go
tests := []struct {
    name                 string
    requestedIsolation   string
    existingIsolation    string
    existingWorkingDir   string
    existingRunning      bool
    wantWarning          bool
}{
    {"first shared delegate", "", "", "", false, false},
    {"second shared same directory", "", "", "/work/project", true, true},
    {"new delegate isolated", "worktree", "", "/work/project", true, false},
    {"existing delegate isolated", "", "worktree", "/work/project", true, false},
    {"shared directories do not overlap", "", "", "/work/other", true, false},
    {"existing shared delegate finished", "", "", "/work/project", false, false},
}
```

Add a case where `workingDir == ""`, the parent current environment is `/work/project`, and a running shared child is also `/work/project`; require a warning containing that absolute path. Add a relative explicit-working-directory case and require it to resolve against the parent's absolute current directory before comparison.

- [ ] **Step 2: Run focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestCreateDelegateSharedWorkspaceAdvisory|TestSharedWorkspaceDelegateWarning' \
  -count=1 -v
```

Expected: compile failure because the warning field/helper do not exist; current delegate results cannot carry an advisory.

- [ ] **Step 3: Detect overlap using immutable child isolation**

Use the `subagent.isolation` field introduced in Task 2 and verify fresh construction still sets it from `subCfg.spawn.isolation`. Implement the read-only helper with the existing manager-then-child leaf-lock discipline:

```go
func (s *Session) sharedWorkspaceDelegateWarning(requestedIsolation, workingDir string) string {
    if strings.TrimSpace(requestedIsolation) != "" || s == nil || s.subagents == nil {
        return ""
    }
    parentEnv := s.currentEnv()
    if parentEnv == nil {
        return ""
    }
    parentDir := parentEnv.WorkingDirectory()
    targetDir := strings.TrimSpace(workingDir)
    if targetDir == "" {
        targetDir = parentDir
    } else if !filepath.IsAbs(targetDir) {
        targetDir = filepath.Join(parentDir, targetDir)
    }
    target := canonicalOrClean(targetDir)
    for _, sub := range s.liveDirectSubagents() {
        sub.mu.Lock()
        active := !sub.closed && (sub.running || sub.driving)
        isolation := sub.isolation
        sub.mu.Unlock()
        if !active || strings.TrimSpace(isolation) != "" {
            continue
        }
        env := sub.sess.currentEnv()
        if env != nil && canonicalOrClean(env.WorkingDirectory()) == target {
            return fmt.Sprintf(
                "shared workspace %q already has a running delegate; this delegate will still launch, but consider isolation=\"worktree\" to avoid file, report, branch, and Git-state collisions",
                target,
            )
        }
    }
    return ""
}
```

Do not inspect untracked files, add locks, create a worktree, or block the spawn.

- [ ] **Step 4: Attach one warning to the immediate delegate result**

In `createDelegate`, compute the warning immediately before preparing/tracking the new child, while the previous child is observably running. Set `res.Warning` on each successful immediate return path: background-running, foreground-completed, and foreground-timeout. Do not persist it in the job record or replay it on later `delegate_send` jobs.

Add `Warning` to `delegateToolResult`, map it in `marshalDelegateResult` with `stringPtrOrNil`, and leave bounded-output truncation unchanged. The advisory is metadata, not delegate output and not an `EventWarning`.

- [ ] **Step 5: Run focused tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestCreateDelegateSharedWorkspaceAdvisory|TestSharedWorkspaceDelegateWarning|TestMarshalDelegate' \
  -count=1 -v
```

Expected: PASS. The second same-directory shared delegate launches and returns one advisory; first, worktree-isolated, non-overlapping, and finished-delegate cases omit it.

- [ ] **Step 6: Commit the advisory**

```bash
git status --short
git add agent/subagents.go agent/job_delegate.go agent/session_tools_jobs.go \
  agent/job_delegate_shared_workspace_test.go agent/session_tools_jobs_seed100_range_c_test.go
git commit -m "feat(delegate): warn on concurrent shared workspace

Return one advisory when a second running shared delegate uses the same working
directory. Keep the caller's isolation choice authoritative and leave creation,
durable job state, and isolated/non-overlapping launches unchanged."
```

---

### Task 6: Add Focused Serf-Owned Orchestration Posture

**Files:**
- Modify: `agent/prompts/sections/delegation.md`
- Create: `agent/prompts/sections/verification.md`
- Create: `agent/prompts/sections/context-management.md`
- Modify: `agent/prompts/templates/system.md.tmpl`
- Modify: `agent/prompts/templates/subagent.md.tmpl`
- Modify: `agent/section_resolver_test.go`

**Interfaces:**
- Consumes: existing `isolation="worktree"`, sandbox deny-path configuration, shell/job exit and timeout evidence, and `compact_context` tool.
- Produces: three focused prompt sections with no runtime service/schema/automation changes.

- [ ] **Step 1: Write failing focused prompt-component tests**

Render sections directly with `sectionResolver.Section`, not the entire prompt. Require these semantic clauses:

```go
func TestDelegationSectionExplainsIsolationChoice(t *testing.T) {
    section := embeddedSectionResolverForTest("coordinator").Section("delegation", promptData{})
    for _, want := range []string{
        "independent writable tasks", "concurrent subagents", `isolation="worktree"`,
        "deliberate collaboration", "read-only work", "file, report, branch, and Git-state collisions",
        "sandbox deny paths", "does not add a protected_paths parameter",
    } {
        if !strings.Contains(section, want) { t.Fatalf("missing %q: %s", want, section) }
    }
}

func TestVerificationSectionDefinesIncompleteGates(t *testing.T) {
    section := embeddedSectionResolverForTest("coordinator").Section("verification", promptData{})
    for _, want := range []string{
        "actually ran and exited zero", "timeout", "launch failure", "sandbox denial",
        "environmental blockage", "verification incomplete", "fixture or environment failure",
        "rerun the decisive incomplete gate",
    } {
        if !strings.Contains(section, want) { t.Fatalf("missing %q: %s", want, section) }
    }
}

func TestContextManagementSectionIsAdvisoryAndBounded(t *testing.T) {
    section := embeddedSectionResolverForTest("coordinator").Section("context-management", promptData{})
    for _, want := range []string{
        "after completing and reporting a task", "compact_context", "before unrelated work",
        "two incomplete implement/review/fix cycles", "report the evidence", "reslice", "ask for direction",
    } {
        if !strings.Contains(section, want) { t.Fatalf("missing %q: %s", want, section) }
    }
}
```

Add small template-inclusion tests: root includes all three; a leaf child includes verification/context-management but not delegation; a delegating child includes delegation through the existing `.CanDelegate` gate. Assert markers only, not the full rendered prompt.

- [ ] **Step 2: Run focused tests and record the expected red failure**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestDelegationSectionExplainsIsolationChoice|TestVerificationSectionDefinesIncompleteGates|TestContextManagementSectionIsAdvisoryAndBounded|TestOrchestrationPostureTemplateInclusion' \
  -count=1 -v
```

Expected: missing-section/substring failures because the approved posture is not present.

- [ ] **Step 3: Extend only the delegation-owned isolation guidance**

Append to `delegation.md`:

```markdown
Isolation is the parent agent's choice. For independent writable tasks,
especially concurrent subagents, prefer `isolation="worktree"`. A shared
workspace is appropriate for deliberate collaboration on the same uncommitted
state or for read-only work. Before sharing a writable workspace, consider file,
report, branch, and Git-state collisions.

When existing sandbox deny paths are already configured, use them to protect
specific control artifacts from a shared delegate. This does not add a
`protected_paths` parameter, automatically choose a worktree, or block a shared
delegate.
```

Do not change the delegate schema or automatic isolation behavior.

- [ ] **Step 4: Add verification posture**

Create `verification.md`:

```markdown
## Verification

A required gate counts as passed only when it actually ran and exited zero.
Timeout, launch failure, sandbox denial, or environmental blockage leaves
verification incomplete. Report the exact condition and evidence instead of a
broad green status.

Before changing production behavior, prove whether a failure belongs to the
fixture or environment versus the product. When the parent has the environment a
child lacked, the parent reruns the decisive incomplete gate.
```

Include it in both master templates after workflow/delegation guidance and before submission/result guidance.

- [ ] **Step 5: Add advisory context-management posture**

Create `context-management.md`:

```markdown
## Context management

After completing and reporting a task, consider the existing `compact_context`
tool before starting unrelated work, especially after a large implementation or
review.

After two incomplete implement/review/fix cycles on the same task, stop repeating
the loop. Report the evidence, reslice the task, or ask for direction.
```

Include it in both master templates. Do not add automatic compaction calls, cycle counters, forced termination, or new context-manager state.

- [ ] **Step 6: Run prompt tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'Test.*Section|Test.*Template|TestSubagentPrompt' -count=1
```

Expected: PASS. Focused tests cover root/child capability ownership and all approved semantics without brittle full-prompt snapshots.

- [ ] **Step 7: Commit posture prompts**

```bash
git status --short
git add agent/prompts/sections/delegation.md \
  agent/prompts/sections/verification.md agent/prompts/sections/context-management.md \
  agent/prompts/templates/system.md.tmpl agent/prompts/templates/subagent.md.tmpl \
  agent/section_resolver_test.go
git commit -m "docs(prompts): state Serf orchestration posture

Teach parent and child agents truthful gate reporting, explicit worktree versus
shared-workspace judgment, advisory post-task compaction, and a two-cycle stop
rule using existing Serf tools only."
```

---

### Task 7: Verify the Complete Contract and Scope Lock

**Files:**
- Verify only: all files changed in Tasks 1-6

**Interfaces:**
- Consumes: all task-level tests and the repository's standard deterministic gate.
- Produces: evidence that scratch lifecycle, prompt posture, delegate advisory, and exclusions are all satisfied without hidden automation or schema expansion.

- [ ] **Step 1: Run the focused contract matrix**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/sandbox ./agent/execenv ./agent/internal/hooks ./agent/internal/mcp ./envvars -count=1
GOCACHE=/tmp/serf-gocache go test ./agent \
  -run 'TestSessionScratch|Test.*Restore.*Scratch|TestDiscardRestoredCandidate.*Scratch|Test.*WithoutScratchProvider|TestCreateDelegateSharedWorkspaceAdvisory|TestSharedWorkspaceDelegateWarning|TestDelegationSectionExplainsIsolationChoice|TestVerificationSectionDefinesIncompleteGates|TestContextManagementSectionIsAdvisoryAndBounded|TestOrchestrationPostureTemplateInclusion' \
  -count=1 -v
```

Expected: PASS with no live-provider or network calls.

- [ ] **Step 2: Run static scope-lock audits**

```bash
base_ref=refs/serf-plan-bases/session-scratch-orchestration-posture
IMPLEMENTATION_BASE="$(git rev-parse "$base_ref")"
git diff --unified=0 "$IMPLEMENTATION_BASE"..HEAD -- agent envvars docs/environment.md | \
  rg -n 'protected_paths|automatic.*worktree|auto.*compact|cycle detector|HOME=' || true
git diff --unified=0 "$IMPLEMENTATION_BASE"..HEAD -- agent envvars docs/environment.md | \
  rg -n 'SERF_SCRATCH_DIR|SessionScratch|NewSessionEnvironment|shared workspace'
git diff --stat "$IMPLEMENTATION_BASE"..HEAD
git status --short
```

Expected:

- `protected_paths` appears only in approved explanatory prompt/test text stating that no parameter is added.
- No production path automatically requests worktree isolation, blocks a shared delegate, persists scratch, redirects `HOME`, terminates cycles, or calls compaction at task boundaries.
- `SERF_SCRATCH_DIR` is sourced from `envvars.SERFScratchDir`, not duplicated raw throughout production Go code.
- The scratch sweep requires both age and an acquirable OS lease; no old live directory can be removed merely because its mtime crossed 24 hours.
- Base selection rejects lexical and canonical paths inside the active workspace and uses only an outside OS temp/cache candidate.
- Candidate OS temp/cache and caller-provided base directories retain their original modes; only the `MkdirTemp`-created Serf scratch child is chmodded to `0700`.
- Existing `ApplyEnvFloor` SSH-agent, cloud-variable, and external-`KUBECONFIG` drops remain in the implementation diff.
- No files under Superpowers code/skills/prompts changed; only this implementation plan lives under `docs/superpowers/plans`.
- Pre-existing untracked files remain unmodified and unstaged.

The private base ref is the authoritative commit immediately before this project. Do not derive the range from a task or commit count.

- [ ] **Step 3: Run the repository gate**

```bash
make test
```

Expected: PASS. A provider credential by itself causes no live request; all new behavior tests use filesystem/process fixtures and scripted providers.

- [ ] **Step 4: Inspect final diff for exact exclusions and accidental churn**

```bash
base_ref=refs/serf-plan-bases/session-scratch-orchestration-posture
IMPLEMENTATION_BASE="$(git rev-parse "$base_ref")"
git diff --check
git diff --check "$IMPLEMENTATION_BASE"..HEAD
git diff --name-only "$IMPLEMENTATION_BASE"..HEAD
git status --short
```

Expected: no whitespace errors; only the paths enumerated in this plan are changed; no Superpowers implementation, automatic worktree selection, shared-delegate block, `protected_paths` mechanism, gate service/result schema, scratch persistence/handoff store, automatic compaction/cycle termination, `HOME` redirect, or durable-cache redirect was added.

- [ ] **Step 5: Commit final test-only corrections if the full gate required any**

If and only if the full gate required a scoped correction, stage the exact already-listed files after reviewing `git status --short`, then commit:

```bash
git commit -m "test(agent): close session scratch contract gaps

Cover the final deterministic lifecycle or prompt regression exposed by the
repository gate without expanding the approved feature scope."
```

If the gate passes without corrections, make no empty commit.

---

## Acceptance Checklist

- [ ] Every live local root and child session owns one `0700` scratch directory outside the worktree.
- [ ] An ambient/explicit scratch base inside the workspace is rejected in favor of a canonical OS cache/temp base outside it; if no safe candidate exists, provisioning fails without creating scratch in the workspace.
- [ ] Allocation never chmods `os.TempDir()`, the OS cache directory, or a caller-provided base; it creates and chmods only the Serf-owned child directory.
- [ ] Parent, child, sibling, and restored fork sessions have distinct scratch paths.
- [ ] `TMPDIR`, `SERF_SCRATCH_DIR`, runtime-only `EnvironmentInfo.ScratchDir`, sandbox wrapper, subprocesses, and prompt data agree on the exact owning path.
- [ ] Worktree re-rooting and `WithSandboxInvocationGrant` within one live session preserve scratch and prompt-cache/environment stability without transferring cleanup ownership.
- [ ] SessionEnd hooks and stdio MCP shutdown complete while scratch still exists; tracked-process cleanup and scratch disposal happen afterward.
- [ ] Normal close, parent teardown, child dispose, spawn/construction failure, every unadopted restored-delegate exit, and `discardRestoredCandidate` remove owned scratch after process shutdown.
- [ ] Close and restored-candidate discard remain safe for execution environments that do not implement the optional `SessionScratchProvider` capability.
- [ ] Restore gets a new empty scratch; scratch is never persisted in session metadata as a reusable path.
- [ ] The 24-hour sweep removes only old directories with Serf's reserved prefix whose OS liveness lease is acquirable; an old live lease is never removed.
- [ ] `HOME` and durable caches are unchanged; only the pre-existing explicit session-private sandbox cache strategy redirects caches, and all existing SSH-agent/cloud/external-`KUBECONFIG` filters remain active.
- [ ] A second concurrently running shared delegate in the same directory launches and returns one advisory suggesting `isolation="worktree"`; an omitted working directory resolves to the parent's absolute current directory.
- [ ] First, isolated, non-overlapping, and finished-delegate launches do not warn.
- [ ] A restored delegate retains `desc.Isolation`; a resumed worktree delegate is never misclassified as shared by overlap-warning logic.
- [ ] Root and child prompts receive focused scratch, verification, and context-management posture; delegation posture is included only when delegation is available.
- [ ] Required-gate, fixture-versus-product, parent-rerun, post-task compaction, and two-cycle stop semantics match the approved design.
- [ ] No excluded automation, persistence, schema service, Superpowers modification, or brittle large rendered-prompt test was added.
