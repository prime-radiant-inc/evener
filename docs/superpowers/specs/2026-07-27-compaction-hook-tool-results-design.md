# Compaction Hook Tool-Result Integrity Design

**Kata:** rb86

## Problem

Context compaction may choose a cutoff at a `TOOL_RESULTS` turn. `safeCutoff`
walks backward across that result, but currently stops at an intervening
`HOOK_COMPLETED` turn. Because `expandHistory` omits hook-completion markers
from provider history, the preserved tail then contains a tool result without
the assistant tool call that introduced its ID. Strict providers reject that
request.

The durable transcript is valid. Compaction creates the invalid provider
projection.

## Considered Approaches

1. Make `safeCutoff` cross `HOOK_COMPLETED` while finding the start of a tool
   exchange. This retains the complete assistant-call, hook-marker, result
   sequence. It fixes the source of the malformed projection without changing
   repair or provider behavior.
2. Run orphan repair again after context management. This would mask the
   compaction defect by changing valid durable history after compaction and
   could replace a real result with recovery output.
3. Sanitize orphan results during provider projection. This would silently
   discard context and leave stored history malformed.

Approach 1 is the smallest source-level fix and preserves all durable data.

## Design

Add `schema.TurnHookCompleted` to the kinds that `safeCutoff` walks backward
over. Do not generalize the change to every presentational marker: hook
completion is the marker proven to occur inside the tool-call/result exchange,
while broader retention changes are not required by this defect.

Add a deterministic checkpoint regression with this boundary:

`ASSISTANT(tool call) -> HOOK_COMPLETED -> TOOL_RESULTS`

Choose `preserveRecent` so the initial cutoff lands on `TOOL_RESULTS`. Assert
that the checkpoint's preserved tail still contains the assistant tool call,
hook marker, and matching result in order. Reverting the production condition
to its prior form must make this test fail by dropping the assistant call.

No live provider, timing, credential, or network dependency is involved.

## Success Criteria

- Compaction never preserves the reproduced tool result without its assistant
  tool call.
- Existing steering and tool-boundary behavior remains unchanged.
- The focused context-manager tests and the repository test/build checks pass.

