# Turn Start Auto-Resume Failure Hardening Design

**Date:** 2026-07-29
**Status:** Approved

## Problem

`turn/start` retries a stale session by asking the Hub to resume its daemon.
There are two paths into that auto-resume:

1. the Hub cannot resolve a live source before attempting the turn; or
2. the resolved source becomes unavailable while the Hub starts the turn.

If resume itself fails, both paths currently return the raw resume error. That
error has no `clientMutationId`, `mutationOutcome`, or `retryDisposition`.
The browser therefore cannot associate the response with its durable outbox
record. It leaves the record in `submitting`, and the ready-state liveness scan
resends it every two seconds.

An incompatible persisted mutation snapshot exposed this gap. Strict snapshot
decoding correctly rejected the old data, but every browser retry launched
another daemon that failed on the same snapshot.

## Goals

- Stop automatic retry storms after a `turn/start` auto-resume failure.
- Preserve the original browser outbox payload for manual recovery.
- Preserve the underlying resume failure message for diagnosis.
- Apply the same outcome contract to both auto-resume failure paths.
- Keep the change at the Hub boundary where mutation identity is still known.

## Non-goals

- Migrating or decoding legacy mutation snapshots.
- Adding field aliases, format fallbacks, or backward compatibility.
- Making an incompatible old session resumable.
- Discarding blocked input automatically.
- Classifying a failed resume as authoritative session deletion.
- Adding cooldowns, retry caches, or new frontend recovery states.
- Optimizing session startup latency before measuring it without the retry
  storm.

## Design

When `turn/start` attempts auto-resume and resume returns an error, the Hub
returns a structured `appwire.WireError` with:

- the original resume error text as `message`;
- `code: CodeInternalError`;
- `serfErrorInfo: mutationOutcomeUnknown`;
- the request's original `clientMutationId`;
- `mutationOutcome: unknown`;
- `retryDisposition: blocked`; and
- `cause: persistenceUnavailable`.

The Hub applies this normalization only after an attempted auto-resume fails.
Validation errors, source errors for refs the Hub does not own, live
non-session-unavailable errors, successful resume-and-retry, and daemon
mutation errors retain their existing behavior.

A small Hub-local helper constructs the error so the two branches cannot drift.
No persistence or frontend production code changes are required.

## Browser Recovery

The existing mutation dispatcher recognizes a matching
`unknown + blocked` result and atomically changes the outbox record from
`submitting` to `blockedUnknown`. The record remains durable and visible in the
recovery UI. Automatic dispatch stops, and later mutation sequence numbers for
the same target remain blocked to preserve ordering.

The user may retry the same mutation ID manually after repairing storage, or
copy/export the payload. The Hub does not claim the mutation was rejected and
does not discard it.

## Persistence Compatibility

Snapshot decoding remains strict. Incompatible snapshots continue to fail
resume rather than being interpreted under an uncertain schema. This change
contains that failure by returning a terminal automatic-retry disposition; it
does not introduce any compatibility path.

## Testing

Add deterministic AppWire handler regressions for both control-flow branches:

1. initial source resolution fails and the subsequent auto-resume fails; and
2. a resolved source reports `sessionUnavailable` during `turn/start`, then the
   subsequent auto-resume fails.

Each case must prove:

- the response is a `WireError` with the original mutation ID;
- outcome is `unknown`;
- retry disposition is `blocked`;
- cause is `persistenceUnavailable`;
- the resume failure message is preserved; and
- auto-resume is attempted exactly once.

The second case also proves the original turn start is attempted exactly once.
The tests exercise the real AppWire router and Hub handler, replacing only the
source-resolution and resume boundaries with deterministic seams.

Existing frontend dispatcher coverage already proves that this wire shape
produces `blockedUnknown`, stops later dispatch, and retains the outbox record.
No duplicate frontend test is required.

## Acceptance Criteria

- A failed `turn/start` auto-resume returns a mutation-correlated blocked
  unknown outcome on both Hub paths.
- The browser can stop automatic retries without losing the submitted payload.
- Successful auto-resume behavior remains unchanged.
- Strict persisted-state decoding remains unchanged.
- No legacy migration or compatibility behavior is added.
