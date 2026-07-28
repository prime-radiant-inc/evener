# Compaction Hook Tool-Result Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep context compaction from orphaning tool results when a persisted hook-completion marker separates the result from its assistant tool call.

**Architecture:** Preserve the valid durable turn sequence at the compaction boundary. Teach the existing `safeCutoff` scan to cross `HOOK_COMPLETED` only while tracing a provider-visible tool result back to its assistant call. Prove both positive cutoff positions through the deterministic checkpoint and the real provider-message projection, while proving standalone hook markers remain valid cutoffs.

**Tech Stack:** Go, Serf `schema.Turn`, context-manager checkpoint tests, agent provider-history projection tests

## Global Constraints

- Track the work in kata `rb86`; do not use Linear.
- Make the smallest reasonable production change.
- Default tests must be deterministic and must not make live provider requests.
- The regressions must exercise real checkpoint behavior and the production `expandHistory` provider projection.
- Do not sanitize or discard malformed history in provider projection.
- Do not broaden retention to presentational marker kinds not required by the reproduced hook sequence.

---

### Task 1: Preserve tool exchanges across hook-completion markers

**Files:**
- Modify: `agent/internal/contextmgr/context_manager.go:1335`
- Test: `agent/internal/contextmgr/context_manager_test.go:1643`
- Test: `agent/session_hook_turn_test.go`

**Interfaces:**
- Consumes: `checkpoint(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []schema.Turn` and `safeCutoff(history []schema.Turn, cutoff int) int`
- Produces: checkpoint output whose preserved tail includes the full assistant-tool-call, hook-completion, tool-results sequence without moving standalone hook cutoffs
- Verifies: `expandHistory(historyTurns []schema.Turn, scope replayScope) []llm.Message` projects the assistant tool call before its matching tool result

- [ ] **Step 1: Write the failing checkpoint regression**

Add `TestSafeCutoff_DoesNotSkipStandaloneHookCompleted` with a cutoff on a
SessionStart-style hook marker followed by an ordinary assistant turn. Assert
that `safeCutoff` leaves the cutoff unchanged.

Extend `TestCheckpoint_PreservesToolCallAcrossHookCompleted` around:

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

Call `checkpoint` with `preserveRecent` values that initially place the cutoff
on `TOOL_RESULTS` and on the preceding `HOOK_COMPLETED`. Assert that both
returned kind sequences are
`CHECKPOINT,ASSISTANT,HOOK_COMPLETED,TOOL_RESULTS,ASSISTANT`, and assert that
the preserved assistant content contains the tool call with `callID` while the
preserved result contains the same ID. Also cover adjacent hook markers.

Through the real `expandHistory` seam, assert that the assistant tool call
precedes the matching projected tool-result message for both cutoff positions.
The assertion must identify an absent call as malformed output, not merely
count projected messages.

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./agent/internal/contextmgr -run '^TestSafeCutoff_DoesNotSkipStandaloneHookCompleted$' -count=1
```

Expected: FAIL because unconditional hook traversal moves the standalone cutoff
back to the preceding turn.

- [ ] **Step 3: Implement the minimal boundary fix**

In `safeCutoff`, track whether the boundary is being traced from a
`TurnTool`/`TurnToolResults`. If the initial cutoff is on one or more hook
markers, seed that state only when the next non-hook turn is a tool result.
Walk backward across hook markers only while that state is active. Preserve the
existing unconditional handling for tool-result and steering turns.

Update the function comment to distinguish hook markers inside a tool exchange
from standalone hook markers, which remain valid cutoff positions.

- [ ] **Step 4: Run focused verification and mutation proof**

Run:

```bash
go test ./agent/internal/contextmgr ./agent -run 'Test(SafeCutoff|Checkpoint_PreservesToolCallAcrossHookCompleted|CompactedHookToolExchangeProjectsValidProviderMessages)' -count=1
```

Expected: PASS.

Mutation-prove both branches:

- Restore the original behavior that never crosses hook markers. The positive
  checkpoint and provider-projection tests must fail because the assistant call
  is absent.
- Restore the rejected unconditional hook traversal. The standalone-hook test
  must fail because its cutoff moves.

Restore the exchange-aware implementation and rerun the focused command; it
must pass.

- [ ] **Step 5: Run package and repository verification**

Run:

```bash
go test ./agent/internal/contextmgr -count=1
go test ./agent -count=1
make test
make build
git diff --check ca0716f12..HEAD
```

Expected: all commands exit 0 with no test failures.

- [ ] **Step 6: Commit the reviewed fix wave**

Stage the implementation, regressions, and corrected design/plan text, then
commit with a detailed message describing the exchange-aware boundary and
strict-provider failure. Record verification in the task report without
mutating kata or Linear state.
