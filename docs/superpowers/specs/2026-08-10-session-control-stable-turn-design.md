# Stable Session-Control Turn Identity Design

**Date:** 2026-08-10
**Kata:** c2ty
**Incident session:** `0343wE3LB14m5xoC2CBMiD`

## Problem

Retry-safe `turn/start` accepts a durable user turn under a stable client-mutation
identity such as `turn_m70`. Before the corresponding user-input event reaches
the AppWire projector, the serve loop marks the daemon processing through the
generic `SetProcessing(true)` path. That path independently reserves a numeric
projector identity such as `turn_8513`.

The normal user-input event replaces the temporary projector reservation with
the durable `StableTurnID`. Any delay before that event, however, exposes the
numeric identity through `thread/read` and Hub APIs while durable Steer and Stop
preconditions still require the stable identity. Controls then reach the daemon
but fail with `turn is not active`.

The affected persisted snapshot proves this state:

- durable `ActiveTurnID`: `turn_m70`;
- pending `turn/start`: operation `applied`, execution `claimed`, projection
  `pending`;
- projected active turn observed before shutdown: `turn_8513`;
- thirteen durable interrupt attempts targeting `turn_8513`, all rejected with
  `-32013 turn is not active`.

The `turn_mN` and `turn_N` namespaces are intentionally distinct. Resume must
not copy a projected numeric ID into the durable mutation store.

## Design

Carry the durable stable turn identity across the existing runnable-start
callback and use it when the server becomes processing.

1. `Session.ProcessClientMutationStart` identifies the runnable durable turn
   before invoking its existing pre-claim callback and passes that turn's
   `StableTurnID` to the callback.
2. The serve loop installs its cancellation runner as it does today, then calls
   a server operation that marks processing and reserves the supplied stable
   identity under one `Server.mu` hold.
3. The AppWire projector stores that supplied identity as its outstanding
   reservation. A warning, hook notification, or other event projected before
   `EventUserInput` therefore cannot overwrite the server's active identity
   with an empty or independently minted numeric ID.
4. `EventUserInput` consumes the same reservation through the existing
   `StableTurnID` behavior. No translation table or second durable identity is
   introduced.
5. Generic direct inputs, queued auto-continuations, notifications, and other
   processing paths continue to use `SetProcessing(true)` and numeric projector
   reservations.

The new processing operation requires a non-empty durable turn ID. An empty ID
is a programming error at the Session-to-server boundary rather than a reason
to silently fall back to minting a different identity.

## Rejected Alternatives

### Translate projected IDs in every control handler

Mapping `turn_N` back to the current durable `turn_mN` would make controls
appear to work while the public active-turn identity remained false. It would
also require ambiguous mapping state across resume and completion.

### Synchronize durable `ActiveTurnID` from AppWire during resume

This reverses authority and crosses intentionally disjoint namespaces. It could
replace stable retry identities with projector-local identities.

### Publish the accepted start response ID directly from `turn/start`

A replayed terminal start can return its historical stable ID. Publishing every
successful response would risk resurrecting a completed turn. The runnable
execution callback is the boundary where the daemon knows the durable turn is
actually about to run.

## Behavioral Verification

Use a real `runServeWithDeps` daemon with:

- a temporary state directory;
- a real Session mutation store and transcript;
- the real AppWire WebSocket server and event bridge;
- a scripted provider at the LLM boundary;
- channel handshakes, not sleeps, to hold `SetState(processing)` after the
  processing identity has been published but before the durable claim and
  `EventUserInput`.

The regression test performs two turns:

1. Start a turn, hold the pre-claim window, project an intervening warning, and
   assert `thread/read.activeTurnId` still equals the `turn/start` response ID.
   Send Steer with that ID, release execution, and assert the original pending
   user input appears in the projected transcript with its client mutation ID.
2. Start another turn, hold the same window, read the published active ID, and
   send Stop (`turn/interrupt`) with that ID. Observe the real interrupt handler
   entry before releasing execution, then assert Stop succeeds and the daemon
   returns idle.

The current implementation must fail behaviorally at the first Steer: AppWire
publishes a numeric `turn_N`, while the durable store owns `turn_mN`. The test
must not depend on compilation failure, source-text matching, wall-clock sleeps,
provider credentials, network services, or the stopped incident session.

## Scope

This change repairs turn identity consistency only. It does not determine the
specific operation that held incident turn `turn_m70` before transcript
incorporation, change ENOSPC recovery, or alter durable resume reconciliation.
ENOSPC widened the vulnerable interval but is not required for the mismatch.
