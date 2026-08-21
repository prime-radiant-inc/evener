# Stable Delegate Attention Projection

- **Issue:** [#307](https://github.com/prime-radiant-inc/evener/issues/307)
- **Status:** Approved design
- **Date:** 2026-08-20

## Problem

Delegate cards currently combine stable delegate lifecycle with child-thread status. These streams describe different domains and have no shared revision: a completed delegate may have an idle, resumable child session, and reconnect may deliver stale child status. Neither backend nor frontend should infer delegate lifecycle or attention from generic child-thread status.

The stable delegate projection already owns lifecycle and has `ProjectionRevision`. It needs one additional fact: whether unresolved transcript attention can wake the delegate.

## Design

The child transcript is the durable authority for unresolved attention. The delegate journal materializes only its derived boolean:

```go
NeedsAttention bool
```

Add a delegate event containing that boolean. Append it only when the value changes; folding it updates `NeedsAttention` and advances the existing `ProjectionRevision`. Do not add attention IDs, another revision, or an unknown attention state.

The boolean is required on every stable delegate projection surface, including delegate updates, warm and cold status reads, `job_status`, Appwire, generated TypeScript, `ThreadModel.delegates`, and Hub cards. Missing historical journal data has the zero value and is corrected as described below, never by falling back to child-thread status.

The process-local pending-ID set remains an internal wake-routing aid. It is rebuilt from transcripts and is not persisted in the delegate journal or exposed to clients.

## Invariants

`NeedsAttention` is true exactly when the transcript contains at least one unresolved attention item eligible to wake or resume the current delegate.

- The first unresolved item changes false to true.
- Additional unresolved items do not change the boolean or revision.
- Consuming an item changes nothing while another remains unresolved.
- Durable acceptance of the final answer changes true to false.
- Closed, stopped, and otherwise non-resumable delegates project false, including when malformed historical state says true.
- A resumed generation can become true again.

Stable projection data solely determines delegate lifecycle, terminal outcome, resumability, and attention. Child transcripts remain authoritative for attention content; generic child-thread lifecycle status determines none of those delegate fields.

## Write ordering

The controller serializes comparison with the folded aggregate, journal append, and publication of the process-local pending set. It emits the existing revisioned delegate update afterward.

When opening attention:

1. Durably append and verify the attention turn in the child transcript.
2. Derive the proposed pending set.
3. If its boolean differs, append the delegate event.
4. Publish the local set and delegate update only after any required journal append succeeds.

When accepting an answer:

1. Durably record its consumption in the child transcript.
2. Derive the proposed pending set.
3. If the final unresolved item was consumed, append `NeedsAttention=false`.
4. Publish the local removal and delegate update only after any required journal append succeeds.
5. Admit the resumed generation only after the clear succeeds.

A journal failure leaves transcript truth durable but does not publish the proposed local state or admit a resumed generation. Retry derives state from the transcript, completes the projection change without duplicating transcript records, and then resumes. Duplicate booleans are no-ops and do not advance `ProjectionRevision`.

## Restore, cold reads, and compatibility

Before exposing a controller, startup rebuilds pending IDs from each child transcript and compares the derived boolean with the journal aggregate. It appends one repair event when they differ, then publishes the rebuilt local sets. Transcript read or repair failure fails startup rather than serving known-stale state.

A cold `LoadSessionDelegateStatus` read overlays the transcript-derived boolean on its returned snapshot without modifying the journal. A cold snapshot replaces client state rather than joining a live revision stream. If that controller later starts, startup repair persists any difference before live updates begin.

Existing journals require no migration. Their absent field reads as false and is corrected by cold overlay or startup repair. No background repair, cache, poller, TTL, janitor, migration command, or downgrade compatibility mechanism is added.

## Frontend ownership

After stable delegate hydration, `ThreadModel.delegates` is the sole source of card lifecycle and attention. Cards derive lifecycle, terminal outcome, attention, reason, usage, timing, exhaustion, and resumability from that projection. Attention is shown only on a nonterminal projection with `NeedsAttention=true`.

Before hydration, frozen delegate tool output remains the fallback. Child watches may provide transcript content, quotes, turns, calls, and counts, but never card lifecycle or attention. Remove the child-status reconciliation path and its obsolete tests when no content behavior needs them.

## Failure behavior

- Never emit or return a projection transition whose journal append failed.
- Treat transcript read failure during startup or cold loading as an error, not false attention.
- Preserve existing revision fencing and latest-activity merge behavior for delegate updates.

## Test strategy

Use the smallest existing behavior-level tests that prove:

1. Opening the first of multiple attention items and durably accepting the last answer produce the only boolean and revision changes; journal failure prevents visibility and resume until retry.
2. Startup repair and cold reads correct stale values in both directions from transcript truth, and transcript read failure is returned.
3. One frontend card test shows that stable lifecycle and attention control the card even when child status is `active` or `awaiting`.

Update existing event/Appwire fixtures for the required field. Delete obsolete child-lifecycle tests. Add no test infrastructure, meta-tests, source-string assertions, mocks of compilers/browsers/processes, matrices, or one-test-per-function coverage scaffolding.

## Acceptance criteria

- Every stable delegate projection surface carries required `NeedsAttention`.
- Attention handling advances the existing `ProjectionRevision` only when the boolean changes.
- Live updates, warm reads, cold reads, `job_status`, Appwire, and Hub cards agree.
- Durable acceptance of the final answer clears attention before resume begins.
- Startup repairs stale journal state from transcript authority; cold reads overlay the same truth without writing.
- Stable projection data solely owns frontend lifecycle and attention; child-thread status cannot override it.
- Obsolete frontend reconciliation code and tests are removed.
- The design adds only the materialized boolean and its journal event, with no generalized attention framework or background machinery.
