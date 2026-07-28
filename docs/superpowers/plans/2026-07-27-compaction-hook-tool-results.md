# Compaction Hook Tool-Result Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep context compaction from orphaning tool results when a persisted hook-completion marker separates the result from its assistant tool call.

**Architecture:** Preserve the valid durable turn sequence at the compaction boundary. Teach the existing `safeCutoff` boundary scan that `HOOK_COMPLETED` is transparent inside the provider-visible tool exchange, and prove the behavior through the real deterministic checkpoint function.

**Tech Stack:** Go, Serf `schema.Turn`, context-manager checkpoint tests

## Global Constraints

- Track the work in kata `rb86`; do not use Linear.
- Make the smallest reasonable production change.
- Default tests must be deterministic and must not make live provider requests.
- The regression must exercise real checkpoint behavior and fail when the prior `safeCutoff` condition is restored.
- Do not sanitize or discard malformed history in provider projection.
- Do not broaden retention to presentational marker kinds not required by the reproduced hook sequence.

---

### Task 1: Preserve tool exchanges across hook-completion markers

**Files:**
- Modify: `agent/internal/contextmgr/context_manager.go:1335`
- Test: `agent/internal/contextmgr/context_manager_test.go:1643`

**Interfaces:**
- Consumes: `checkpoint(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []schema.Turn` and `safeCutoff(history []schema.Turn, cutoff int) int`
- Produces: checkpoint output whose preserved tail includes the full assistant-tool-call, hook-completion, tool-results sequence

- [ ] **Step 1: Write the failing checkpoint regression**

Add a test named `TestCheckpoint_PreservesToolCallAcrossHookCompleted` that
constructs:

```go
const callID = "call_with_hook"
history := []schema.Turn{
    {Kind: schema.TurnUserInput, Message: llm.User("task")},
    {Kind: schema.TurnAssistant, Message: llm.Assistant("earlier answer")},
    {Kind: schema.TurnAssistant, Message: assistantWithToolCall(callID, "probe", `{}`)},
    schema.NewTurn(schema.TurnHookCompleted, llm.System("PreToolUse hook exit 0")),
    schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "ok", false)),
    {Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
}
```

Call `checkpoint(history, 2, nil, "communicate")`, which initially places the
cutoff on the `TOOL_RESULTS` turn. Assert that the returned kinds are
`CHECKPOINT,ASSISTANT,HOOK_COMPLETED,TOOL_RESULTS,ASSISTANT`, and assert that
the preserved assistant content contains the tool call with `callID` while the
preserved result contains the same ID.

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./agent/internal/contextmgr -run '^TestCheckpoint_PreservesToolCallAcrossHookCompleted$' -count=1
```

Expected: FAIL because the old cutoff stops on `HOOK_COMPLETED`, so the
assistant tool call is absent from the preserved tail.

- [ ] **Step 3: Implement the minimal boundary fix**

In `safeCutoff`, include `schema.TurnHookCompleted` in the existing backward
walk:

```go
if k == schema.TurnTool ||
    k == schema.TurnToolResults ||
    k == schema.TurnSteering ||
    k == schema.TurnHookCompleted {
    cutoff--
    continue
}
```

Update the function comment so it says the scan also crosses hook markers that
may be persisted between an assistant tool call and its result.

- [ ] **Step 4: Run focused verification and mutation proof**

Run:

```bash
go test ./agent/internal/contextmgr -run 'Test(SafeCutoff|Checkpoint_PreservesToolCallAcrossHookCompleted)' -count=1
```

Expected: PASS.

Temporarily remove the `TurnHookCompleted` branch and rerun
`TestCheckpoint_PreservesToolCallAcrossHookCompleted`; it must fail for the
missing assistant call. Restore the branch and rerun the focused command; it
must pass.

- [ ] **Step 5: Run package and repository verification**

Run:

```bash
go test ./agent/internal/contextmgr -count=1
make test
make build
```

Expected: all commands exit 0 with no test failures.

- [ ] **Step 6: Commit and update the kata**

Stage only the context-manager implementation and regression test, then commit
with a detailed message describing the compaction boundary and strict-provider
failure. Add the verification evidence to `rb86` and close it only after review
and final verification are complete.

