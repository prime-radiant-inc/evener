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

The cutoff may also land directly on the hook marker. Hook completion turns are
persisted for unrelated lifecycle hooks such as SessionStart and SessionStop,
so treating every hook marker as transparent would retain unrelated preceding
turns and can suppress compaction when the scan reaches zero.

## Considered Approaches

1. Make `safeCutoff` cross `HOOK_COMPLETED` only while finding the start of a
   provider-visible tool exchange. This retains the complete assistant-call,
   hook-marker, result sequence without moving standalone lifecycle-hook
   cutoffs. It fixes the source of the malformed projection without changing
   repair or provider behavior.
2. Run orphan repair again after context management. This would mask the
   compaction defect by changing valid durable history after compaction and
   could replace a real result with recovery output.
3. Sanitize orphan results during provider projection. This would silently
   discard context and leave stored history malformed.

Approach 1 is the smallest source-level fix and preserves all durable data.

## Design

Track whether `safeCutoff` is tracing a `TurnTool`/`TurnToolResults` back to its
assistant call. If the initial cutoff is a hook marker, scan forward across
adjacent hook markers and activate that state only when the next turn is a tool
result. Cross hook markers only while the state is active. A standalone hook
marker followed by ordinary conversation remains a valid cutoff.

Do not generalize the forward look or backward traversal to every presentational
marker: hook completion is the marker proven to occur inside the
tool-call/result exchange, while broader retention changes are not required by
this defect.

Add a deterministic checkpoint regression with this boundary:

`ASSISTANT(tool call) -> HOOK_COMPLETED -> TOOL_RESULTS`

Choose `preserveRecent` values so the initial cutoff lands on `TOOL_RESULTS`
and directly on `HOOK_COMPLETED`. Assert that the checkpoint's preserved tail
still contains the assistant tool call, hook marker, and matching result in
order. Cover adjacent hook markers and add a negative test proving a standalone
hook marker does not move the cutoff.

Project each positive checkpoint through the real `expandHistory` seam and
assert that the matching assistant tool call precedes the projected tool-result
message. Reverting to the original behavior must produce an orphan result;
reverting to unconditional marker traversal must fail the standalone negative.
No live provider, timing, credential, or network dependency is involved.

## Success Criteria

- Compaction preserves the reproduced tool result only with its preceding
  assistant tool call for both cutoff positions.
- Standalone hook markers do not move the cutoff.
- The provider-message projection orders the matching assistant call before the
  tool result.
- Existing steering and tool-boundary behavior remains unchanged.
- The focused context-manager and agent tests and the repository test/build
  checks pass.
