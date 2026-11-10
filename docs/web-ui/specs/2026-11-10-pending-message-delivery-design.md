# Pending message delivery — design

Status: accepted 2026-11-10.

## Problem

One user message can be represented by four independent projections: the local
IndexedDB mutation rows, the daemon's input queue, the daemon's pending
executions, and the recorded transcript. Four surfaces rendered those
projections with different filters and different shapes: compact "Sending"
chips, dashed steering bubbles, queue-strip rows, and delivered transcript
rows. Nothing owned the message across its life, so the same identity could
render twice and could disappear during a handoff.

## Decision

Every undelivered user message renders in one stack at the very bottom of the
transcript, below every server-sourced turn. The stack scrolls with the
transcript; it is not a fixed overlay. Each row is the message it will become:
the same `UserMessageView` shape as a delivered row, with a status caption in
the timestamp slot and a persistent action footer.

A pending row leaves the stack only when a recorded transcript item with the
same `clientMutationId` is visible, or when the user explicitly dismisses a
terminal row. Queue consumption and the disappearance of a pending execution
update the row; they do not delete it. That is the anti-vanish rule and the
reason a queue row cannot render beside its delivered transcript item.

Identical text with different identities is two messages. The stack never
dedupes by text.

## Projection

`appwire-client/typescript/state/mutation/pendingMessages.ts` exports one pure
`projectPendingMessages` function. Inputs:

- local outbox, optimistic and recovery records;
- `ThreadModel.queue`, including entry ids and client mutation ids;
- `ThreadModel.pendingMutations`, including execution order and queue entry ids;
- the visible transcript item identities;
- a page-lifetime set of identities already seen as delivered;
- the thread model's status for the Stop-parked queue state.

The projection keys rows by the effective wire identity (`clientMutationId`).
A legacy queue row with no identity is keyed `queue:<entryId>` and merged in
queue order with an unmatched local queue row whose complete content matches
it. A preview-only legacy row stays visible on its own rather than merging on
truncated text. A drain or promote row owns the queue entries named by
`queueEntryIds`, so those rows do not render beside it. A queue row whose
identity is visible as a transcript item is suppressed.

Row states:

| State | Source | Actions |
|---|---|---|
| `waiting` | local row, never attempted | Edit, Remove |
| `sending` | local row attempted, or accepted send | none |
| `queued` | daemon queue entry | Steer now, Edit, Remove |
| `held` | queue parked by Stop | Steer now, Edit, Remove |
| `steering` | accepted steer, drain or promote | none |
| `delivering` | reflected but not yet visible | none |
| `uncertain` | delivery unknown | Edit, Remove, Retry |
| `canceled` | Stop canceled | Edit, Remove, Retry |
| `refused` | rejected recovery or refused Stop | Edit, or Dismiss for a Stop |
| `orphaned` | destination deleted | Copy |

The stack header keeps the existing "Steer queue now" drain affordance when a
queued row exists and the session's controls allow a drain.

## Settlement

Transport settlement stays as strict as it is today: a receipt or an
authoritative identity settles the outbox row so it cannot be dispatched again.
The accepted optimistic copy is retained for display.

An explicit queue Edit, Remove or Steer now ends that queue row's display copy
as part of the action; the row does not remain as a permanent `delivering`
entry after its queue entry is gone.

`handleNotification` settles an identity only when the accepted model makes its
message visible: a recorded transcript item for history frames, or a queue row
for queue frames. A history frame the client ignores or invalidates must not
delete the only pending row. Consumed queue ids are the one positive-evidence
exception: a drain's own row carries them.

An accepted history frame settles the identities it names even when the
projected model has not exposed the item yet; the frame itself proves the
recording. Only an ignored or invalidated frame leaves the pending copy alone.

A session-scoped `deliveredHere` set remembers identities already seen as
delivered so a later bounded read cannot resurrect a row.

## Presentation boundary

`ConversationBody` owns one `useBuiltinSubmission` instance and passes it to
the composer and the stack. The stack mounts in the existing `live-edge`
trailing virtual row, after the AskDock. `renderedRowCount` counts that row as
one. The arrival epoch covers every pending row, not only held steers, and the
new-content pill tells a reader who is scrolled away that rows appeared.

The stack stays visible on dormant, fenced and `notLoaded` surfaces so the
messages never lose their one rendering. The action gates, not the row's
existence, carry the fence.

## Non-goals

- No wire change in this implementation. A daemon-assigned sequence for exact
  cross-client ordering is a follow-up; the current order uses known creation
  time, durable intent sequence, queue position and identity.
- No fake transcript items and no in-place replacement inside recorded history.
- No native client change in this implementation; `mobile-native`'s ghost
  projection can adopt the shared rows later.
- No text deduplication.

## Tests

The projection suite covers identity merge, queue and pending-execution merge,
drain and promote suppression, visible-delivery retirement, identical text,
images and selections, legacy queue rows, state mapping, ordering and the
delivered-identity guard. The renderer suite covers one row per message, the
status slot, active-recovery exclusion, action visibility and image rendering.
The composer integration, queued-message and submit suites cover queue edit,
cancel, promote, drain, recovery consolidation, retry and the shared busy gate. The
session transcript suite covers the trailing row, fenced and dormant surfaces,
the arrival epoch and the new-content pill. A store test proves an ignored
history frame does not settle the accepted copy while a visible item does.
