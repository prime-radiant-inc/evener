# Pending Confirmation After RPC Design

## Problem

The WebUI reports `Send failed: The server didn't confirm this message in
time.` when a cold session takes more than ten seconds to resume.

`submitWithPendingTracking` currently starts its ten-second confirmation timer
before invoking the mutation RPC. The AppWire client independently permits the
RPC to run for thirty seconds, and `turn/start` may use that time to launch a
stopped daemon, wait for its rendezvous entry, attach the relay, and retry the
turn.

The optimistic timer can therefore fail a message while the authoritative RPC
is still in flight. That failure also removes the callback that would report
the RPC's eventual rejection, replacing a specific resume, transport, or
server error with the synthetic confirmation timeout.

## Goal

Keep optimistic composer feedback while a mutation is in flight, but assign
each deadline to one lifecycle phase:

- the AppWire request timeout owns transport and cold-resume latency;
- the pending confirmation timeout owns only the delay between a successful
  mutation response and its authoritative wire echo.

## Design

Register the pending entry and render its optimistic chip before invoking the
mutation, as today. Do not arm the pending confirmation timer yet.

Await `perform()`:

- If it rejects, fail the pending entry immediately and report that exact
  error through `onFailure`.
- If it succeeds and the pending entry still exists, arm the existing
  ten-second confirmation timer.
- If an authoritative notification already reconciled the pending entry while
  the RPC was in flight, perform no further confirmation work.

For a send whose user-message echo arrived while the RPC was unresolved, keep
the existing `awaitingFirstFrame` lifecycle. A successful RPC does not restart
or extend that visual lifecycle; the first assistant/tool frame or a terminal
turn state still removes it.

The ten-second duration and the reconciliation predicates do not change. The
change is solely which phase owns the timer.

## State Transitions

An entry follows one of these paths:

1. Register -> RPC rejects -> remove entry and report the RPC error.
2. Register -> wire echo arrives -> remove optimistic entry -> RPC succeeds ->
   no timer.
3. Register -> RPC succeeds -> arm confirmation timer -> wire echo arrives ->
   remove entry and cancel timer.
4. Register -> RPC succeeds -> arm confirmation timer -> no wire echo for ten
   seconds -> remove entry and report the confirmation error.

An unresolved RPC never enters the confirmation-timeout phase.

## Alternatives Rejected

Increasing the pending timeout beyond thirty seconds would leave two competing
clocks for one operation and allow their ordering to drift again if either
timeout changes.

Treating a successful RPC response as the wire echo would remove the pending
chip before the thread or queue model reflects the mutation. That is a broader
change to the optimistic-rendering contract and is unnecessary for this bug.

Removing the confirmation timeout entirely would prevent detection when a
successful mutation response is not followed by the notification needed to
converge the WebUI model.

## Error Handling

The real RPC error remains the only failure while `perform()` is unresolved.
This includes AppWire request timeout, disconnect, resume failure, conflict,
and provider/session errors.

The existing synthesized `The server didn't confirm this message in time.`
error remains valid only after the server has accepted the mutation and no
matching wire echo arrives within ten seconds.

## Testing

Add deterministic fake-timer coverage around the real
`submitWithPendingTracking` lifecycle:

- a deferred `perform()` remains pending beyond ten seconds without firing
  `onFailure` or removing the optimistic entry;
- rejecting that deferred operation afterward reports the exact rejection;
- resolving a deferred operation arms a fresh ten-second confirmation window;
- an echo received while the operation is unresolved removes the optimistic
  entry and a later successful resolution does not create a timer;
- a successful operation with no echo still reports the existing confirmation
  timeout after ten seconds.

Prove the first test is red against the current implementation before changing
production code. Use fake timers and deferred promises; do not use sleeps,
network access, or a live provider.

## Scope

The implementation is limited to the pending-turn store and its focused tests.
AppWire request deadlines, hub resume behavior, notification reconciliation,
and composer wording remain unchanged.
