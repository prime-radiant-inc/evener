# Pending Confirmation After RPC Design

## Problem

The WebUI can report `Send failed: The server didn't confirm this message in
time.` while a cold-session `turn/start` request is still unresolved.

`submitWithPendingTracking` currently starts its ten-second confirmation timer
before invoking the mutation RPC. The AppWire request has its own deadline,
and `turn/start` may need to launch a stopped daemon, wait for its rendezvous
entry, attach the relay, and retry the turn before it can return.

The optimistic timer can therefore fail a message before the authoritative RPC
settles. That failure also removes the callback that would report the RPC's
eventual rejection, replacing a specific resume, transport, or server error
with the synthetic confirmation timeout.

A full page reload later allowed the reported session to restart. That
observation does not establish whether the original RPC was still in flight,
failed before reaching the daemon, or ran against stale page state. This
design therefore does not assume that the server accepted the original
message, and it does not add a speculative snapshot-refresh fallback.

## Goals

Keep optimistic composer feedback while a mutation is in flight, but assign
each deadline to one lifecycle phase:

- the AppWire request owns transport and cold-resume latency;
- the pending confirmation timeout owns only the delay between a successful
  mutation response and its authoritative wire echo;
- a real RPC rejection is never hidden by optimistic bookkeeping;
- pending tracking runs only while its thread model is present and able to
  reconcile notifications;
- user-facing feedback distinguishes a rejected mutation from an accepted
  mutation whose echo did not reach the view.

## Design

### RPC and confirmation phases

Register the pending entry and render its optimistic chip before invoking the
mutation, as today. Do not arm the pending confirmation timer yet.

Await `perform()`:

- If it rejects, fail the pending lifecycle immediately and report that exact
  error through `onFailure`.
- If it succeeds and the pending entry still exists, arm a fresh ten-second
  confirmation timer.
- If an authoritative notification already reconciled the optimistic entry
  while the RPC was in flight, do not arm a confirmation timer.

An echo that arrives before the RPC settles removes only the optimistic entry.
It retains the failure callback and, for a send, its `awaitingFirstFrame`
state. A later RPC rejection clears all remaining lifecycle state and invokes
`onFailure` exactly once with the original rejection. A later RPC success
retires the now-unneeded rejection callback.

The ten-second duration and the content-matching reconciliation predicates do
not change.

### First-frame lifecycle

For a send, `awaitingFirstFrame` remains visible after the user-message echo
until the first authoritative assistant/tool frame, a terminal turn or thread
state, model release, or RPC rejection.

If the user echo precedes a successful RPC response, no confirmation timer is
needed because the echo already confirmed the mutation. The first-frame state
continues waiting for its own authoritative transition. This matches the
current successful-send behavior: after `perform()` succeeds, the existing
implementation also clears its timer while leaving `awaitingFirstFrame` to the
frame or terminal-state reconciliation.

If the RPC never settles, its AppWire deadline eventually rejects it and
clears the first-frame state. The pending confirmation timer does not serve as
a second RPC deadline.

### Model release

The pending store can reconcile an entry only while `threadsStore` retains the
entry's ref. Model release is therefore a terminal pending-tracking
transition.

When a ref disappears from the thread map, retire every pending entry,
callback, timer, and `awaitingFirstFrame` record for that ref without reporting
failure. A released pane has no consumer for failure feedback and no
authoritative notification source with which to judge confirmation.

Pending entries do not survive release, so a later remount and hydration
cannot reconcile them against historical same-text items after
`lastSeenModels` has reset.

### Phase-specific feedback

Introduce a typed pending-confirmation timeout error. It is created only by a
timer armed after `perform()` succeeds.

Composer and queue-drain callers render that error as a warning, not an action
failure:

`<Action> was accepted, but this view didn't update. Reload before retrying.`

`<Action>` is `Send`, `Queue`, `Steer`, or `Drain` according to the submitted
method. The wording records that the RPC succeeded while explicitly avoiding
an unsafe duplicate retry.

All RPC rejections retain their existing error severity and method-specific
wording. A queued-drain partial error remains a real partial failure and keeps
its existing special case.

## State Transitions

A tracked entry follows one of these paths:

1. Register -> RPC rejects -> remove all lifecycle state and report the exact
   RPC error once.
2. Register -> wire echo removes the optimistic entry -> RPC succeeds ->
   retire the rejection callback; no confirmation timer.
3. Register -> wire echo removes the optimistic entry -> RPC rejects -> remove
   remaining lifecycle state and report the exact RPC error once.
4. Register -> RPC succeeds -> arm confirmation timer -> wire echo arrives ->
   remove the entry and cancel the timer.
5. Register -> RPC succeeds -> arm confirmation timer -> no wire echo for ten
   seconds -> remove lifecycle state and report the typed accepted-but-stale
   warning.
6. Register -> model releases at any point -> retire lifecycle state silently;
   later RPC settlement cannot resurrect it or report through the released
   pane.

An unresolved RPC never enters the confirmation-timeout phase.

## Alternatives Rejected

Increasing the pending timeout beyond the AppWire deadline would leave two
competing clocks for one operation and allow their ordering to drift again if
either timeout changes.

Treating a successful RPC response as the wire echo would remove the pending
chip before the thread or queue model reflects the mutation. That is a broader
change to the optimistic-rendering contract and is unnecessary for this bug.

Performing a targeted `thread/read` after a confirmation timeout is not
included. The corrected reload observation does not prove that the original
RPC succeeded and then lost its echo. Snapshot recovery should be added only
with evidence of that separate failure mode.

Keeping entries across model release would require preserving their
pre-release reconciliation baseline and a notification subscription solely
for an unmounted UI action. Retiring them is smaller and matches the absence
of a feedback consumer.

Removing the confirmation timeout entirely would prevent detection when a
successful mutation response is not followed by the notification needed to
converge the WebUI model.

## Testing

Add deterministic fake-timer coverage around the real
`submitWithPendingTracking` lifecycle:

- a deferred `perform()` remains pending beyond ten seconds without firing
  `onFailure` or removing the optimistic entry;
- rejecting that deferred operation afterward reports the exact rejection;
- resolving a deferred operation arms a fresh ten-second confirmation window;
- an echo followed by a later RPC rejection reports the exact rejection once
  and clears send first-frame state;
- cover that late-rejection transition for send and one non-send method;
- an echo followed by a later RPC success creates no timer, while send
  first-frame state remains until an authoritative frame clears it;
- a successful operation with no echo reports the typed confirmation warning
  after ten seconds;
- releasing the model before or after RPC success retires pending state
  silently, and later settlement cannot report or resurrect it;
- release followed by remount cannot reconcile the retired entry against
  historical same-text content.

Add one Composer integration test through `threadsStore.send` and the fake
AppWire client. Hold the scripted `turn/start` response beyond ten seconds and
assert that the optimistic state remains and no toast fires before the request
settles. Then reject it and assert that the exact request error is reported.

Add caller tests proving that the typed post-success confirmation timeout is a
warning with accepted-but-stale wording for Composer and queue drain, while
ordinary rejections and queued-drain partial errors retain their existing
messages.

Prove the unresolved-RPC test is red against the current implementation before
changing production code. Mutation-check the echo-then-rejection callback
retention and model-release cleanup as well. Use fake timers, deferred
promises, and the fake AppWire boundary; do not use sleeps, network access, or
a live provider.

The implementation must not couple these tests to AppWire's current numeric
default timeout. The contract is that no pending-confirmation timer runs while
`perform()` is unresolved, regardless of the request's configured deadline.

## Scope

The implementation is limited to the pending-turn store, Composer and queue
drain feedback, and their focused tests. AppWire request deadlines, hub resume
behavior, and notification reconciliation predicates remain unchanged.
