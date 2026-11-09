# Parent visibility for dirty delegate worktrees

## Intent

When a worktree-isolated delegate finishes, its parent needs an explicit cue if
the lane still has uncommitted changes. The parent can then inspect or resume
the child, preserve useful changes, and dispose the lane after it is clean.
Evener must never discard those changes automatically.

## Current behavior

Terminal packet metadata already records a delegate worktree's path, branch,
head, ahead count, and dirty bit. The parent-owned inline delegate result also
gets `disposal_hint`, but terminal packet construction drops that field. A
background completion therefore reaches the parent without the same explicit
cleanup guidance. The parent already has the ownership-checked
`manage_worktree dispose` operation; it refuses dirty lanes unless the caller
explicitly passes `force_dirty`.

## Goals

- Carry the existing parent-only disposal guidance through terminal packets
  and their notification/replay paths.
- Make the hint distinguish a dirty lane that needs review from a clean lane
  ready for ordinary disposal.
- Preserve parent authority checks and existing disposal guards.
- Keep dirty changes until the parent or child explicitly cleans, salvages, or
  discards them.

## Non-goals

- Automatic deletion or archival of dirty worktrees.
- A new worktree inspection or cleanup operation.
- Changes to child resumability, session retirement, or worktree collection
  policy.

## Approaches

1. **Propagate the existing parent-only hint (selected).** Terminal metadata
   already has the worktree snapshot and parent ownership context. Carry the
   hint into that metadata and make it tell the parent to inspect or resume a
   dirty lane before disposal. This uses the existing parent-owned `dispose`
   operation and `force_dirty` guard.
2. **Add a separate cleanup operation or UI.** This would duplicate the
   existing disposal API and is unnecessary for a parent that already receives
   the lane path and can inspect it or resume its child.

## Behavior

The runtime builds a terminal worktree report from the same descriptor and
post-run worktree snapshot used by inline results. It includes the disposal hint
only when the receiving parent owns the delegate and has the worktree tool.
For a dirty lane, the hint directs the parent to inspect or resume the child
before disposal and explains that `force_dirty` discards uncommitted changes.
For a clean lane, the ordinary disposal instruction remains. The immutable
terminal packet carries this field through delivery and replay, so inline and
background completion give the owning parent the same actionable state.

No code path automatically invokes dirty-forced disposal. The existing
`manage_worktree dispose` refusal remains the final protection if a lane is
still dirty when the parent attempts ordinary disposal.

## Verification

Behavior tests will assert that a dirty terminal lane's parent-facing packet
contains the cleanup guidance, that the owning parent's inline result preserves
it, and that a non-owner or a parent without `manage_worktree` receives no
hint. Existing disposal tests continue to prove dirty state survives ordinary
disposal and is discarded only with explicit `force_dirty`.

The S12 subsystem guide and session-activity guide will document the ownership,
notification, and explicit-disposal behavior.
