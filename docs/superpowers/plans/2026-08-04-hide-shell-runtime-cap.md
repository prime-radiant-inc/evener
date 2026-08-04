# Hide Shell Runtime Cap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `max_runtime_ms` from the model-facing shell schema while preserving the existing internal parser and process-runtime deadline implementation.

**Architecture:** Change only `DefShell` and its schema contract test. The internal `shellToolArgs.MaxRuntimeMS`, validation, buffered timeout, streaming timer, signaling, and runtime-timeout tests remain intact for compatibility with host/internal callers. Document the compatibility change in the repository’s existing release-note surface if one exists.

**Tech Stack:** Go, `llm.ToolDefinition`, standard Go tests.

## Global Constraints

- The model-facing shell properties are exactly `command`, `description`, `background`, and `purpose` after shared parameter decoration.
- No `max_runtime_ms` name or runtime-deadline guidance appears in `DefShell` prose.
- Internal argument parsing and runtime enforcement remain unchanged.
- `job_stop` remains the model-facing cancellation mechanism.
- Tests are deterministic and require no network or provider credentials.
- Keep the implementation focused; do not alter job lifecycle semantics, reminders, or the fluency harness.

---

### Task 1: Hide the Runtime Cap from the Shell Definition

**Files:**
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/internal/tool/definitions.go`

**Interfaces:**
- Consumes: `DefShell() llm.ToolDefinition`
- Produces: a model-facing shell schema without `max_runtime_ms`; internal shell argument types are unchanged.

- [ ] **Step 1: Write the failing schema contract test**

Update `TestDefShellHasJobParams` to reject `max_runtime_ms`, require the concise evaluated tool description, and require the evaluated `background` description:

```go
if _, ok := props["max_runtime_ms"]; ok {
    t.Fatal("DefShell must not expose max_runtime_ms")
}
if got := DefShell().Description; got != "Run a shell command and report stdout, stderr, and exit status." {
    t.Fatalf("DefShell description mismatch:\n%q", got)
}
if got := props["background"].(map[string]any)["description"]; got != "Choose foreground execution (false, default) for inline results, or background execution (true) for an immediate job_id. Foreground commands still running at ~120s continue as background jobs." {
    t.Fatalf("background description mismatch:\n%q", got)
}
```

Remove the assertion that expects a `max_runtime_ms` description.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./agent/internal/tool -run '^TestDefShellHasJobParams$' -count=1
```

Expected: FAIL because `DefShell` still exposes `max_runtime_ms` and uses runtime-cap prose.

- [ ] **Step 3: Implement the minimal schema change**

In `DefShell`, set:

```go
Description: "Run a shell command and report stdout, stderr, and exit status.",
```

Set the `background` description to the exact string asserted above and delete only the `max_runtime_ms` property. Do not modify `agent/session_tools_shell.go` or `agent/job_shell.go`.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
go test ./agent/internal/tool -run '^TestDefShellHasJobParams$' -count=1
go test ./agent/internal/tool ./agent -run 'TestDefShellHasJobParams|TestParseShellToolArgs|TestBufferedShellHonorsMaxRuntime|TestShell.*MaxRuntime|TestShell.*Runtime' -count=1
```

Expected: PASS. The second command confirms the model schema changed while internal compatibility tests still pass.

- [ ] **Step 5: Commit the focused implementation**

```bash
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "fix: hide shell runtime cap from models"
```

### Task 2: Document and Verify the Compatibility Change

**Files:**
- Modify: the existing release-note/changelog file found in the repository, if present
- Test: repository verification commands

**Interfaces:**
- Consumes: the hidden model-facing schema from Task 1
- Produces: release documentation and merge-ready verification evidence.

- [ ] **Step 1: Add a concise release note if the repository has an established release-note surface**

Use this content, adapted only to the existing format:

```text
The shell tool no longer exposes max_runtime_ms to models. Normal foreground commands still promote to durable background jobs at the wait bound, and job_stop remains available for explicit cancellation. Internal runtime-deadline compatibility is retained.
```

If the repository has no release-note/changelog surface, record that fact in the verification report and do not invent one.

- [ ] **Step 2: Verify the schema and retained internal path**

Run:

```bash
rg -n 'max_runtime_ms' agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
rg -n 'max_runtime_ms|MaxRuntimeMS' agent/session_tools_shell.go agent/session_tools_shell_test.go agent/job_shell.go agent/job_shell_test.go
git diff --check
```

Expected: the first command finds no model-facing occurrence; the second still finds internal parser/runtime compatibility; `git diff --check` passes.

- [ ] **Step 3: Run project gates**

Run:

```bash
go test ./agent/internal/tool ./agent -count=1
make test-short
```

Expected: PASS with no ignored failures.

- [ ] **Step 4: Review the final diff**

Run:

```bash
git status --short
git diff HEAD~1 --check
git diff HEAD~1 -- agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
```

Expected: only the planned schema/test change plus any established release note and this plan are present.

- [ ] **Step 5: Commit documentation, if changed**

```bash
git add docs/superpowers/plans/2026-08-04-hide-shell-runtime-cap.md
# Add the established release-note file here only if one was changed.
git commit -m "docs: record hidden shell runtime cap"
```
