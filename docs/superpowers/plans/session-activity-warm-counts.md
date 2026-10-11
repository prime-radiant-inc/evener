# Recover established session activity counts

## Reviewed design

A watch mutation currently advances one root-wide freshness revision. Summary
counts lose trust until a collection read revalidates the physical journals.
Users who stay on Tasks or Jobs can therefore see count placeholders indefinitely.

Keep the existing index and shared store as the recovery owners. A physical
source becomes established only after a complete fold in its current incarnation.
Summary may advance already established required sources through `advanceJobs`,
using its existing shared work and byte budget. It must not create or fold cold
sources. Complete unchanged sources revalidate their existing file evidence;
current sources need no additional probe. Incarnation replacement resets the
index and eligibility, preventing a badge from rebuilding replacement history.
An established empty source may catch up from its proven zero baseline when
its first journal appears. A never-observed source has no such eligibility.
The existing index remembers the last admitted summary source and rotates the
next read's start, so sustained growth cannot starve another warm source.

Expose optional summary `refreshPending` only for bounded recovery of established
sources. Unknown cold counts do not set it. The shared store schedules its
existing 100ms summary timer while this field is true. Existing observer release,
offline, disposal and automatic error retry retain their lifetimes. Genuine warm
source failures revoke stale summary trust while preserving context and rows.
Keep root-wide revision and all owner/receiver filtering initially. Add no
background runner, collection demand, or compatibility shim.

## Implementation sequence

1. Add meaningful red producer tests for summary-only recovery, cold reads,
   bounded suffix progress across owners, and source replacement. Add red shared
   store tests for typed polling, release/offline/disposal and stale error trust.
2. Add the current-incarnation establishment latch and bounded warm-source
   summary refresh. Reuse source checks, suffix folding and automatic retry.
3. Add the typed optional `refreshPending` summary field and store scheduling.
   Preserve context and collection rows while clearing stale summary on errors.
4. Generate contracts, run focused Go/race and shared-store tests, then web/API
   gates. Format with the correct frontend Biome and normal commit hooks.
5. Update the owning activity guide/subsystem map with verified cold versus warm
   behavior and recovery ownership. Record exact evidence and limits in the
   ignored integration ledger. Commit the bounded implementation; do not push.

## Acceptance checks

- Real root and descendant receiver-watch settlement updates summary-only reads
  without opening collections. Shell counts and watch totals retain exact scope.
- A suffix larger than one fold budget makes bounded progress and eventually
  recovers all established sources without allowing cold source scans.
- Repeated cold summary reads create no indexes and advance no journal offsets.
- Replacement loses establishment; summary cannot replay replacement history.
- Summary-only observers issue no Jobs/Watches RPC, poll only typed warm demand,
  and stop on release/offline/disposal. Automatic source retry must not display
  old known counts as current and must preserve healthy collection rows/context.
