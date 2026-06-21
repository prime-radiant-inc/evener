# Observer Watch Causal Provenance and Loop Suppression Design

Date: 2026-06-18
Status: draft for Jesse review
Builds on: `docs/job-control.md`, `docs/superpowers/specs/2026-06-18-job-control-handle-split-design.md`, `docs/superpowers/research/2026-06-18-observer-sidecar-use-cases.md`

## Problem

Observer sidecars are ordinary Serf composition: start a delegate, then create a
`job_watch` that sends frames to that delegate. The current composition has two
gaps:

1. Caller-event frames do not include enough event content for useful observers.
   A `communicate` watch can tell an observer that `COMMUNICATE` happened, but
   not what message was communicated.
2. If an observer reacts by steering the observed session, the platform must not
   let that steering turn retrigger the same observer indefinitely.

The second gap is the safety boundary. Prompting an observer to avoid loops is
not enough. The runtime must attach causal provenance to watch deliveries and to
the effects caused by those deliveries, then suppress same-watch echoes.

## Design Thesis

Keep the public API small. Do not add an `observer` primitive.

The platform needs one internal mechanism:

> Every event has optional causal provenance. The safety-critical part is a
> deduped set of `(watch_id, watch_generation)` keys inherited by every tool
> call, steering message, model turn, job, and emitted event caused by a watch
> delivery. A watch must not fire on an event whose provenance set already
> contains that watch's active key.

This makes loop prevention a runtime invariant rather than a model convention.

## Goals

- Prevent observer self-loops when an observer injects content into the watched
  session.
- Preserve the simple `delegate` + `job_watch` + `delegate_send` composition.
- Make watch frames content-bearing enough for observer decisions.
- Keep model-facing knobs minimal; loop suppression is always on.
- Preserve useful cross-watch behavior unless it is the same causal cycle.
- Make provenance visible enough for debugging without exposing new authority.

## Non-Goals

- Do not add `observe`, `observer_send`, `memory_inject`, or a separate sidecar
  tool.
- Do not add a user-configurable "allow self loops" option in v1.
- Do not rely on observer prompts such as "do not include the trigger word".
- Do not make `delivery_id` or `watch_id` a bearer capability.
- Do not require every event payload struct to grow bespoke provenance fields.
- Do not suppress unrelated human/user turns merely because they happen after an
  observer injected steering.

## Vocabulary

| Term | Meaning |
| --- | --- |
| Watch delivery | One fired watch frame/message, identified by `watch_id`, watch generation, and `delivery_id`. |
| Watch provenance key | The safety-critical `(watch_id, watch_generation)` pair. Suppression uses only this pair. |
| Provenance set | Deduped set of watch provenance keys attached to events and queued work. |
| Diagnostic chain | Ordered provenance breadcrumbs for logs/frames. This is not used for suppression and may be truncated. |
| Root cause event | The event that caused a watch delivery, before the watch key was added. |
| Observer effect | Any action taken by an observer while handling a watch-delivered frame. |
| Same-watch echo | An event whose provenance set contains the watch currently considering firing. |

## Causal Provenance Model

Add event-level provenance to the `events.SessionEvent` envelope, not to each
payload:

```go
type WatchProvenanceKey struct {
    WatchID         string `json:"watch_id"`
    WatchGeneration string `json:"watch_generation"`
}

type CausalProvenance struct {
    WatchKeys      []WatchProvenanceKey `json:"watch_keys,omitempty"`
    Chain          []ProvenanceEntry    `json:"chain,omitempty"`
    ChainTruncated bool                 `json:"chain_truncated,omitempty"`
}

type ProvenanceEntry struct {
    Kind            string `json:"kind"` // "watch", future: "hook", "goal", ...
    WatchID         string `json:"watch_id,omitempty"`
    WatchGeneration string `json:"watch_generation,omitempty"`
    DeliveryID      string `json:"delivery_id,omitempty"`
    SessionID       string `json:"session_id,omitempty"`
    JobID           string `json:"job_id,omitempty"`
}

type SessionEvent struct {
    Kind       EventKind         `json:"kind"`
    Timestamp  time.Time         `json:"timestamp"`
    SessionID  string            `json:"session_id"`
    Data       EventData         `json:"data,omitempty"`
    Provenance *CausalProvenance `json:"provenance,omitempty"`
}
```

`WatchKeys` is a mathematical set represented in stable order for JSON
determinism. Combining provenance means set union. Suppression membership tests
only `(watch_id, watch_generation)`. `SessionID`, `JobID`, and `DeliveryID` are
diagnostic and must not participate in the suppression key.

The diagnostic chain is ordered from oldest to newest. The immediate cause is
the last entry. It exists for debugging and frame rendering only. It may be
bounded or truncated; truncating it must never remove a key from `WatchKeys`.

Implementation may initially keep this metadata internal if exposing it on SSE
is too large a compatibility step, but the event envelope is the right semantic
home. Putting provenance on individual payloads will miss events and produce
drift.

Use `watch_generation` for the watch-suppression generation to avoid confusing
it with delegate generation, such as the stop-gating `DelegateGeneration` field
on watch-send state.

## Watch Delivery Provenance

When `job_watch` fires, Serf creates a `watchDeliveryContext`:

```go
type watchDeliveryContext struct {
    WatchID         string
    WatchGeneration string
    DeliveryID      string
    OwnerID         string
    Target          string
    Trigger         string
    Provenance      events.CausalProvenance
}
```

`Provenance` starts as the root event's provenance plus one new watch key and
one diagnostic chain entry:

```json
{
  "watch_keys": [
    {
      "watch_id": "watch_...",
      "watch_generation": "wg_..."
    }
  ],
  "chain": [
    {
      "kind": "watch",
      "watch_id": "watch_...",
      "watch_generation": "wg_...",
      "delivery_id": "del_...",
      "session_id": "01K...",
      "job_id": "job_..."
    }
  ]
}
```

The watch generation is part of the suppression identity. If a watch is cleared
and later re-created with the same normalized config, the new watch is a
different loop boundary. Upsert is active-watch-only: duplicate create returns
the existing active watch and generation, but recreate-after-clear must never be
a generation-preserving no-op. If the visible `watch_id` encodes generation,
mint a new `watch_id`; if the implementation keeps a stable visible `watch_id`,
it must still mint a new `watch_generation`. Old pending deliveries from the
cleared generation are dropped.

## Propagation Rules

The provenance set must travel through every async boundary that can emit later
events:

1. Watch-send pending records store `provenance`.
2. Delivery to an observer delegate attaches `provenance` to the started/resumed
   job and to the observer session's active provenance.
3. `delegate_send(to="caller")` from an observer inherits the observer's active
   provenance and stores it on the caller steering entry.
4. When the caller processes that steering entry, the session sets its current
   active provenance to include the steering entry's provenance for the duration
   of the driven turn.
5. At the start of each new top-level input turn, active provenance is replaced
   with that input's provenance set; for an ordinary external `USER_INPUT`, that
   set is empty. Provenance is unioned only within the currently driven turn.
6. If steering is drained mid-turn between tool rounds, Serf unions each drained
   steering entry's provenance into the turn's active provenance immediately and
   leaves it active until the turn ends. This intentionally over-suppresses the
   rest of that turn if necessary; it fails safe toward no loop.
7. If multiple steering messages are drained together, the active provenance is
   the union of their provenance sets. A scalar "current input provenance" is not
   sufficient.
8. Every event emitted during that driven turn carries the active provenance:
   `STEERING_INJECTED`, assistant text events, tool-call events, `COMMUNICATE`,
   job lifecycle events, warnings, errors, and nested watch deliveries.
9. Jobs created during that turn store the provenance on their runtime/job record
   so detached completion events carry it after the turn ends.
10. Job notifications created from watch-delivered observer jobs store provenance
   in both the in-memory notification carrier and the durable pending
   notification record, so notification-driven parent acknowledgements inherit
   it after restart.

Provenance is therefore part of the work item, not a goroutine-local side effect.
The safe implementation shape is explicit fields on queue entries, running jobs,
delegate restore descriptors, watch-send pending rows, and the session's
currently-processed input.

## Suppression Rule

Before a watch records a notification or a watch send, it evaluates:

```go
func shouldSuppressWatch(cfg *watchConfig, event events.SessionEvent) bool {
    if event.Provenance == nil {
        return false
    }
    for _, key := range event.Provenance.WatchKeys {
        if key.WatchID == cfg.watchID &&
           key.WatchGeneration == cfg.watchGeneration {
            return true
        }
    }
    return false
}
```

If suppressed:

- no caller notification is enqueued;
- no observer frame is sent;
- no delivery counter is incremented;
- no pending latest-frame entry is replaced;
- a debug counter may increment for inspectability, but not a model-facing
  delivery counter.

Suppression is same-watch only. Watch `A` may trigger watch `B` unless `B` also
appears in the provenance set. This preserves useful chains such as a cost
sentinel watching an observer's own expensive behavior.

## Steering Scenario

Monty Python observer:

1. Caller says `actually`.
2. Watch `W@G1` fires delivery `D1` to observer.
3. Observer receives a frame with provenance set `{W@G1}` and diagnostic
   delivery `D1`.
4. Observer calls `delegate_send(to="caller", message="PYTHON_QUOTE ...")`.
5. The caller steering entry stores provenance `{W@G1}`.
6. Caller processes the steering turn. Its `STEERING_INJECTED`,
   `ASSISTANT_TEXT_*`, `TOOL_CALL_*`, and `COMMUNICATE` events carry
   `{W@G1}`.
7. Watch `W@G1` ignores those events because its own key is present.

If the human later says `actually` again, that is a new `USER_INPUT` with no
watch provenance and `W@G1` may fire normally.

## Notification Acknowledgement Scenario

The live failure that motivated this design was not only direct observer
injection. The parent also acknowledged observer terminal notifications with
`communicate`, and those acknowledgements were themselves watched caller events.

Required flow:

1. Watch `W@G1` delivers frame `D1` to observer with provenance `{W@G1}`.
2. The observer job completes and emits a terminal notification for the parent.
3. That notification carries provenance `{W@G1}` in memory and durably.
4. `acceptNotificationInput` adopts or unions `{W@G1}` into the parent turn's
   active provenance when it renders the notification as steering.
5. If the parent responds by calling `communicate("acknowledged ...")`, the
   `COMMUNICATE` event carries `{W@G1}`.
6. Watch `W@G1` suppresses that acknowledgement before creating a new observer
   delivery.

## Content-Bearing Event Frames

Loop suppression is not enough; observers still need the observed content.

For event watches, sent frames should include a bounded typed event block. The
preferred human/model-readable shape is:

```text
Watch frame
watch_id: watch_...
delivery_id: del_...
job_id: caller
trigger: event: COMMUNICATE
provenance: external
event:
  kind: communicate
  message: actually alpha marker
  end_turn: false
  truncated: false
```

`provenance: external` is display shorthand for an empty watch provenance set:
the event was caused by a human/external input rather than by prior watch
delivery.

For watch-caused events delivered to a different watch, include a compact
provenance summary:

```text
provenance:
  watch_keys:
    - watch_id: watch_...
      watch_generation: wg_...
  latest_delivery_id: del_...
```

Frames delivered to observers normally will not include suppressed events
because no delivery occurs. The provenance summary is still useful for
cross-watch deliveries and debugging.

Event payloads must be bounded independently from the full frame. Suggested
initial bounds:

- `message`: reuse the existing watch text limit discipline;
- `truncated`: true when the original payload was larger than the rendered
  field;
- no transcript dump in the frame;
- no raw secrets beyond whatever the source event was already allowed to expose.

`communicate` is the first payload to support because it is the scenario-driving
gap. Later event blocks can be added one kind at a time:

| Event kind | Frame fields |
| --- | --- |
| `communicate` | `message`, `end_turn`, `truncated` |
| `assistant.message` | final text excerpt, model, finish reason, truncation |
| `assistant.tool` | tool name, call id, argument excerpt/hash, result status |
| `job.notification` | job id, status, reason, excerpt if available |

Implementation note: the frame builder must receive the triggering
`events.EventData`. The message is available in `onSessionEvent(kind, data)`;
the current gap exists because the send snapshot and `buildWatchFrame` path carry
only a trigger string.

## Runtime Alias Rules

For v1, `delegate_send(to="caller")` remains the model-facing action for
observer injection. Do not add a separate injection primitive in this design.

The observer research note recommends a future typed `communicate(route="caller",
kind=..., dedupe_key=...)` style route instead of using generic delegate control
for sidecar output. This spec deliberately defers that API change for v1: the
handle-split design already establishes `delegate_send(to="caller")`, and loop
suppression can be fixed without adding a primitive. A future typed route must
inherit the same provenance set and may add the dedupe/report semantics described
in the research doc.

The route must inherit active provenance. This applies only when the caller alias
exists, which is already restricted to delegate/watch-delivered contexts. Top
level `caller` remains invalid.

Watch sends to `send.to="caller"` are internal notification-rail deliveries, not
ordinary `delegate_send(to="caller")` calls. They still create watch provenance
for the notification turn so the owner's response to that notification cannot
echo back into the same watch.

## Data-Carrying Sites

Minimum implementation sites:

- `events.SessionEvent`: add causal provenance or equivalent internal metadata.
- `Session.emit` / `sendEvent`: attach active provenance to emitted events.
- `Session` processing state: hold active provenance while processing user,
  notification, continuation, and steering inputs; union mid-turn steering
  provenance when the tool loop drains steering between rounds.
- Steering queue entries: store provenance.
- Job records / running jobs: store provenance for detached lifecycle events.
- Watch-to-observer send arguments and observer session seed input: store
  provenance when a watch starts or steers the observer.
- In-memory job notifications and durable pending notification records
  (`EventJobNotificationPending`): store provenance so notification
  acknowledgements inherit it.
- Watch-send pending records: store provenance with `watch_id`,
  `watch_generation`, and `delivery_id`.
- Delegate resume descriptors: store provenance for watch-started observer jobs.
- `delegate_send(to="caller")`: copy the sender's active provenance into the
  caller steering entry.
- `job_watch.onSessionEvent`: suppress same-watch echoes before any delivery
  accounting or pending-frame mutation.

The implementation should avoid context globals for provenance. Existing
`context.WithValue` patterns are useful for call-local metadata, but they do not
survive steering queue entries or durable persistence. Provenance has to cross
exactly those boundaries, so explicit fields are harder to forget and easier to
test.

## Durable State

Pending watch sends must persist enough provenance to survive process restart:

```json
{
  "watch_id": "watch_...",
  "watch_generation": "wg_...",
  "delivery_id": "del_...",
  "target": "dlg_...",
  "message": "...",
  "frame": "...",
  "provenance": {
    "watch_keys": [
      {
        "watch_id": "watch_...",
        "watch_generation": "wg_..."
      }
    ],
    "chain": [
      {
        "kind": "watch",
        "watch_id": "watch_...",
        "watch_generation": "wg_...",
        "delivery_id": "del_..."
      }
    ]
  }
}
```

If Serf restores a pending watch delivery and starts an observer job after
restart, that observer job still carries the same provenance. If Serf restores a
running job as `stopped/runtime_lost`, the terminal lifecycle event should carry
the job's stored provenance if known.

## Interaction With Existing Guards

Current `FromWatch` suppression for watch-originated delegate lifecycle events is
a global early return: any watch-originated `JOB_STARTED` or `JOB_FINISHED` event
is hidden from every watch. That conflicts with this design, because cross-watch
observation is allowed. The provenance implementation replaces that guard; it
must not remain as an authoritative suppression path. A legacy `FromWatch` field
may survive temporarily for migration diagnostics, but `job_watch` suppression
must be keyed by the active watch's provenance membership.

The existing validation that rejects direct feedback shapes such as
`target="caller", events=["communicate"], send.to="caller"` should remain.
Provenance suppression prevents accidental causal loops; it should not legalize
obviously self-delivering configurations that teach the model the wrong
composition.

Delivery budgets still matter. Provenance suppression prevents same-watch cycles,
but a broad observer can still cause useful-but-expensive cross-watch traffic.
The existing watch delivery cap remains the circuit breaker.

## Edge Cases

### Observer sends a message containing the trigger word

Still safe. The observer's injected steering and downstream model turn inherit
the watch provenance key, so the same watch suppresses those events even if the
message contains the trigger word.

### Observer starts another delegate

If delegation allowance permits it, the child delegate job inherits the
provenance set. Lifecycle and final-result events from that child also carry the
same set.

### Observer queues multiple messages while caller is busy

Each steering entry carries its own provenance. If the queue coalesces messages
in a future implementation, the merged entry's provenance set is the union of
all source provenance sets, preserving suppression for every contributing watch.

### Watch fires because of a human follow-up after observer injection

A later human `USER_INPUT` has no watch provenance key, so the watch may fire.
This is not a loop; it is a new external cause.

### Watch A causes Watch B causes Watch A

`A -> B -> A` is suppressed when A sees its own key in the provenance set. `B`
may fire once on A's effect unless B is also already in the set. This allows
useful pipelines while breaking cycles.

### Watch is cleared and recreated

Suppression keys include watch generation. Old-provenance events suppress only
the old generation. Pending deliveries from cleared generations are dropped, so
stale events do not affect the replacement watch except as ordinary new inputs
if they are truly re-emitted after the new watch exists.

### Diagnostic chain gets long

Set a small internal max depth for the diagnostic chain, for example 16. On
overflow, retain the oldest and newest useful breadcrumbs and set
`chain_truncated=true` in diagnostic rendering. Never truncate `watch_keys`;
dropping a watch key reopens that watch's loop.

## Testing Requirements

Unit tests:

- `job_watch` suppresses a same-watch-provenance `communicate` event before
  creating a pending watch send.
- Suppression checks watch generation; same `watch_id` with old generation does
  not suppress the new watch.
- Watch-send pending records persist and restore provenance.
- `delegate_send(to="caller")` from a watch-delivered observer stores
  provenance on the caller steering entry.
- Events emitted during a steering-driven turn carry the steering provenance.
- Mid-turn steering between tool rounds unions steering provenance into the
  active turn provenance and leaves it active until the turn ends.
- A fresh external `USER_INPUT` replaces active provenance with an empty set, so
  watch-caused provenance from the prior turn cannot suppress legitimate later
  triggers.
- Multiple drained steering entries union their provenance sets.
- In-memory and durable job notifications carry provenance, and notification
  acknowledgement turns adopt it.
- Detached delegate/job completion events inherit the provenance stored on the
  job.

Scenario tests:

- Monty Python observer:
  - initial human `communicate("actually ...")` triggers observer;
  - observer injects `PYTHON_QUOTE`;
  - injected caller turn and any `communicate` acknowledgement do not retrigger
    the same watch;
  - a later human `communicate("Actually ...")` triggers a second legitimate
    observer injection.
- Snide observer:
  - observer that only calls `communicate` in its own thread does not inject into
    caller;
  - observer's own lifecycle/communicate events do not retrigger the caller watch
    through same-watch provenance.
- Cross-watch chain:
  - watch A can cause watch B once;
  - A suppresses re-entry when B's effect carries A in the provenance set.

Live manual e2e:

- Run the Monty Python scenario with `kimi/kimi-for-coding`.
- Read parent and observer transcripts after the run.
- Verify there are exactly two `PYTHON_QUOTE` injections for two external trigger
  inputs and zero extra observer jobs from injected/acknowledgement traffic.

## Definition of Done

The design is implemented only when all of these are true:

- Event-watch frames for `communicate` include `watch_id`, `delivery_id`,
  trigger metadata, and a bounded `event:` block containing the communicated
  `message`, `end_turn`, and `truncated` flag.
- Every event emitted from a watch-delivered observer turn, and every event
  emitted from downstream work caused by that turn, carries the watch provenance
  set or equivalent internal metadata.
- `delegate_send(to="caller")` from a watch-delivered observer stores the
  current watch provenance on the caller steering entry before the caller is
  driven.
- Events emitted while processing that caller steering entry inherit the same
  provenance, including assistant text, tool calls, `communicate`, job
  lifecycle events, warnings, and errors.
- Steering drained mid-turn unions provenance into the active turn before any
  downstream model reaction can emit events.
- Each new top-level input replaces active provenance with that input's set; an
  external human/user input starts empty and can legitimately retrigger the
  watch.
- Observer terminal notifications and notification acknowledgement turns carry
  the same provenance.
- `job_watch` suppresses same-watch echoes before notification enqueue,
  watch-send recording, pending-frame replacement, and delivery accounting.
- Suppression keys include watch generation, so clearing and recreating a watch
  does not let stale pending deliveries affect the replacement watch.
- Existing invalid direct-feedback watch configurations remain rejected;
  provenance suppression does not legalize self-delivery shapes.
- Unit tests cover propagation through watch delivery, observer
  `delegate_send(to="caller")`, caller steering, mid-turn steering, job
  notifications, detached job completion, and same-watch-generation versus
  new-watch-generation suppression.
- The Monty Python scenario passes against real Kimi with transcript inspection:
  two external trigger messages produce exactly two `PYTHON_QUOTE` injections,
  non-trigger messages produce none, and observer-injected or parent
  acknowledgement traffic does not create extra observer jobs.
- The snide observer scenario still passes: observer commentary stays in the
  observer thread and does not inject into the caller.
- The implementation leaves no new public observer primitive and no
  user-configurable loop-escape knob in v1.

## Implementation Sequence

1. Add causal-provenance types and attach active provenance in `Session.emit`.
2. Add provenance fields to steering queue entries, running jobs, delegate
   restore, job notifications, and watch-send pending rows.
3. Thread provenance through watch delivery, observer delegate start/resume, and
   `delegate_send(to="caller")`.
4. Implement same-watch suppression in `job_watch.onSessionEvent` before
   delivery accounting.
5. Add content-bearing `communicate` event frames.
6. Add unit tests for propagation and suppression.
7. Update `job-watch-actually-monty-python-injection.md` from an aspirational
   failure card to the expected pass contract.
8. Run live Kimi e2e and inspect transcripts.

## Open Questions

- Should causal provenance be exposed on the public SSE/appwire stream
  immediately, or kept internal until the hub has rendering affordances?
  Semantically it belongs on the event envelope either way.
- Should watch frames render the full diagnostic chain or only a compact
  summary? Compact summary is probably enough for models; the full chain can
  remain in logs.
- Should there be future opt-in cross-watch policies, such as "suppress all
  watch-caused events"? Not for v1; same-watch suppression is the smallest
  safety boundary.
