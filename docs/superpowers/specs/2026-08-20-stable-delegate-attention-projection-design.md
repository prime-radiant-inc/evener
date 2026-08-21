# Stable Delegate Attention Projection

- **Issue:** [#307](https://github.com/prime-radiant-inc/evener/issues/307)
- **Status:** Approved design, revised after adversarial review
- **Date:** 2026-08-20

## Problem

Delegate cards combine stable delegate lifecycle with generic child-thread status. These streams describe different domains and have no shared revision: a completed delegate may have an idle, resumable child session, and reconnect may deliver stale child status.

The stable delegate projection already owns lifecycle and `ProjectionRevision`. It lacks one fact: whether unresolved transcript attention can wake the delegate.

## Design

The child transcript remains the durable authority. The delegate journal materializes one derived field:

```go
NeedsAttention bool
```

Add one delegate event carrying that boolean. Append it only when the value changes; folding it updates `NeedsAttention` and advances the existing `ProjectionRevision`. Do not add attention IDs, another revision, or an unknown state.

A consumed attention resolution also records an optional `ResumeGeneration`. This is crash-recovery evidence, not another state authority: zero means a historical resolution created before this design; nonzero means that exact generation must exist in the delegate journal. No new acceptance-intent event is added.

Carry the required boolean through this closed projection list:

- delegate aggregate, snapshot, and status;
- warm and cold delegate status;
- delegate update event;
- Appwire and generated TypeScript;
- `ThreadModel.delegates`;
- `job_status`;
- Hub delegate cards.

`job_list` remains unchanged. Generic child-thread status remains unchanged.

## Attention ownership

The controller keeps one process-local set that exactly mirrors all transcript-unresolved attention IDs, including claimed or in-flight IDs. Reservation and settlement claims remain separate from this set. An ID leaves the set only after its transcript resolution and any required delegate event commit.

A resumed generation claims one attention ID. Other unresolved IDs remain in the set and keep `NeedsAttention=true`.

`NeedsAttention` is true when at least one unresolved ID is eligible to wake or resume the delegate.

- The first unresolved ID changes false to true.
- Additional IDs and nonfinal consumption do not change the boolean or revision.
- Durable acceptance of the final answer changes true to false.
- `PhaseStopping`, a pending stop, permanent closure, non-resumability, or a permanent ancestor fence forces false.
- A completed generation's `Terminal` bit or `OutcomeStopped` does not itself force false. An idle, resumable delegate may need attention between generations.
- A resumed generation can become true again.

Existing lifecycle events normalize `NeedsAttention=false` in the same fold whenever they make an aggregate stopping or permanently ineligible. Subtree lifecycle events normalize every affected aggregate. Do not append separate attention events for lifecycle-caused clears. The frontend uses `NeedsAttention` directly; it does not add a second terminal gate.

## Serialized transitions

Attention transitions use the existing controller claim pattern. A per-child claim spans transcript I/O and the delegate-journal commit so two transcript-derived proposals cannot publish out of order. It is not a generalized transaction framework.

### Opening attention through delivery

1. Append and verify the attention turn in the receiver transcript.
2. Under the claim, derive the complete unresolved-ID set.
3. In one controller-locked `AppendBatch`, append the owner's attention event when false becomes true and acknowledge the sender's delivery.
4. After the batch succeeds, publish the local set and revisioned updates for affected delegates.

Root-owned deliveries have no owner delegate event. If the batch fails, the delivery remains replayable and local attention state does not advance.

### Accepting an answer and resuming

1. Acquire an acceptance claim and reserve the next generation. If stop already won, reject before transcript consumption.
2. Durably record consumption in the child transcript with that `ResumeGeneration`.
3. Derive the complete unresolved-ID set.
4. In one `AppendBatch`, append the final false event when needed and append `RunStarted` for the reserved generation.
5. After the batch succeeds, publish the local removal and start the generation.

Once consumption is admitted, the claim linearizes against stop: stop wins before transcript consumption or waits until the attention-clear/`RunStarted` batch commits. If the batch fails after transcript consumption, retain the desired boolean and owed generation as process-local repair state; retry completes the same batch without duplicating transcript records.

## Projection append failure

A failed attention append does not publish local state, emit a delegate update, acknowledge a delivery, or admit a generation. Existing retry paths complete persistence before those operations continue.

During retry, every live projection surface continues to serve the last journal-committed value and the operation reports its persistence error. Do not overlay an uncommitted value on selected reads. Wake, resume, and start remain blocked until persistence succeeds. A crash discards process-local retry state, but bootstrap reconstructs it from transcripts as described below.

The transcript drives repair; the delegate journal remains the one client-visible live projection. Do not make general snapshot/status APIs error-capable and do not add a background repair service.

## Restore and cold reads

Bootstrap performs existing stop reconciliation, missing-input cleanup, and unreachable-attention transfer first. Immediately before exposing the controller, it scans eligible delegate transcripts.

For each consumed resolution with nonzero `ResumeGeneration`, bootstrap verifies that the matching `RunStarted` exists. If it is missing, a dedicated accepted-attention admission step reconstructs the child runtime from its persisted transcript/descriptor, appends the owed `RunStarted` together with any required boolean change, attaches the runtime, and launches that generation. It reuses existing runtime construction and committed-start failure handling; it does not route the generation through lost-runtime reconciliation. Historical resolutions with zero carry no owed-start intent. Recovery works even when other attention IDs remain unresolved and the boolean is already true.

After recovering owed starts, bootstrap compares transcript-derived booleans with journal aggregates and appends the same attention-change event for remaining mismatches through the existing batch path. It then publishes the rebuilt local sets.

Read transcripts only for delegates that can still wake. Closed, non-resumable, stopping, pending-stop, and permanently ancestor-fenced delegates normalize to false without requiring a transcript read. A missing or unreadable transcript for an eligible existing delegate is an error; do not reuse missing-as-empty behavior.

A cold `LoadSessionDelegateStatus` read applies the same eligibility rules and overlays transcript-derived attention without writing the journal. A cold snapshot replaces client state rather than joining a live revision stream. If its controller later starts, bootstrap persists any mismatch before live updates begin.

Existing journals require no migration. Their absent field folds as false and is corrected by cold overlay or startup repair. No cache, poller, TTL, janitor, migration command, second repair event type, or downgrade mechanism is added.

## Frontend ownership and deletion

After hydration, the owning `ThreadModel.delegates` entry solely determines lifecycle, outcome, reason, timing, exhaustion, resumability, and attention. Frozen delegate tool output is the pre-hydration fallback.

Child watches remain only where expanded cards need transcript content, quotes, turns, calls, counts, or latest-quote activity. Delete rather than layer over:

- `WatchedChildIndicator` and its lean collapsed-row mount;
- child-status lifecycle mapping and write-back;
- `liveKind`, lifecycle no-resurrection helpers, and lifecycle shadow fields;
- follow-up-tool mutation of spawn-row lifecycle/reason/resumability/exhaustion;
- child-derived lifecycle, attention, and run-window fallbacks;
- obsolete tests for those paths.

Stable `NeedsAttention` controls the card's hidden status and glyph even when a content watch reports child `active`, `idle`, or `awaiting`.

## Tests

Use existing suites and fault seams. Add no test infrastructure, meta-tests, source-string assertions, compiler/browser/process mocks, mutation scaffolding, or one-test-per-function coverage work.

Keep three behavior groups:

1. **Controller/store transition:** first open, additional IDs, nonfinal/final consumption, delivery-ack batching, acceptance-vs-stop precedence, lifecycle-folded false, same-value no-op, revision changes, append-failure retry state, and crash recovery of a consumed resolution's owed generation. Extend existing controller state-machine tests; do not create one test per case.
2. **Restore and cold read:** stale values in both directions, eligibility filtering, final bootstrap placement, and eligible missing-transcript error in one table-driven group.
3. **Projection and card:** extend existing event/Appwire fixtures, then use one real card test to prove stable attention authority over child status. Delete obsolete reconciliation tests.

Add another test only for a distinct failure mode these groups cannot express.

## Acceptance criteria

- Every surface in the closed projection list carries required `NeedsAttention` and agrees.
- Attention handling advances `ProjectionRevision` only when the boolean changes.
- Delivery acknowledgment and first-open materialization commit atomically in the delegate journal.
- Final answer consumption records an owed generation; attention clear and `RunStarted` commit atomically and are linearized against stop.
- Append failure leaves every live surface on the last committed revision, reports the operation error, and blocks admission/live emission until retry persists.
- Bootstrap recovers a consumed-but-not-started generation after a crash.
- Lifecycle events normalize attention without separate attention-event records.
- Bootstrap and cold reads derive attention after attention-mutating reconciliation and apply eligibility filtering.
- Frontend lifecycle and attention come only from the stable delegate projection after hydration.
- Obsolete child-status reconciliation production code and tests are deleted.
- The implementation adds only the materialized boolean, its delegate event, the optional owed-generation transcript marker, and the narrow claims/retry state required for ordering—no generalized attention framework or background machinery.
