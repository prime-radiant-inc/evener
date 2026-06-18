# Observer Watch Origin and Loop Suppression Design

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

> Every event has an optional causal origin chain. Watch deliveries append their
> `watch_id` and `delivery_id` to that chain. Any tool call, steering message,
> model turn, job, or emitted event caused by that delivery inherits the chain.
> A watch must not fire on an event whose origin chain already contains that
> watch's active generation.

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
- Do not require every event payload struct to grow bespoke origin fields.
- Do not suppress unrelated human/user turns merely because they happen after an
  observer injected steering.

## Vocabulary

| Term | Meaning |
| --- | --- |
| Watch delivery | One fired watch frame/message, identified by `watch_id`, watch generation, and `delivery_id`. |
| Origin chain | Ordered causal provenance attached to events and queued work. |
| Watch origin | One origin-chain element representing a watch delivery. |
| Root cause event | The event that caused a watch delivery, before the watch origin was appended. |
| Observer effect | Any action taken by an observer while handling a watch-originated frame. |
| Same-watch echo | An event whose origin chain contains the watch currently considering firing. |

## Origin Model

Add event-level provenance to the `events.SessionEvent` envelope, not to each
payload:

```go
type EventOrigin struct {
    Kind       string `json:"kind"` // "watch", future: "hook", "goal", ...
    WatchID    string `json:"watch_id,omitempty"`
    Generation string `json:"generation,omitempty"`
    DeliveryID string `json:"delivery_id,omitempty"`
    SessionID  string `json:"session_id,omitempty"`
    JobID      string `json:"job_id,omitempty"`
}

type SessionEvent struct {
    Kind      EventKind     `json:"kind"`
    Timestamp time.Time     `json:"timestamp"`
    SessionID string        `json:"session_id"`
    Data      EventData     `json:"data,omitempty"`
    Origin    []EventOrigin `json:"origin,omitempty"`
}
```

The origin chain is ordered from oldest to newest. The immediate cause is the
last element. Watch loop suppression scans the full chain, not just the last
element, because an observer can cause another delegate turn that causes another
event.

Implementation may initially keep this metadata internal if exposing it on SSE
is too large a compatibility step, but the event envelope is the right semantic
home. Putting origin on individual payloads will miss events and produce drift.

## Watch Delivery Origin

When `job_watch` fires, Serf creates a `watchDeliveryContext`:

```go
type watchDeliveryContext struct {
    WatchID    string
    Generation string
    DeliveryID string
    OwnerID    string
    Target     string
    Trigger    string
    Origin     []events.EventOrigin
}
```

`Origin` starts as the root event's origin chain plus one new watch element:

```json
{
  "kind": "watch",
  "watch_id": "watch_...",
  "generation": "wg_...",
  "delivery_id": "del_...",
  "session_id": "01K...",
  "job_id": "job_..."
}
```

The generation is part of the identity. If a watch is cleared and later
re-created with the same normalized config, the new generation is a different
loop boundary. Old pending deliveries from the cleared generation are dropped.

## Propagation Rules

The origin chain must travel through every async boundary that can emit later
events:

1. Watch-send pending records store `origin`.
2. Delivery to an observer delegate attaches `origin` to the started/resumed job
   and to the observer session's current input origin.
3. `delegate_send(to="caller")` from an observer inherits the observer's current
   input origin and stores it on the caller steering entry.
4. When the caller processes that steering entry, the session sets its current
   input origin to the steering entry's origin for the duration of the driven
   turn.
5. Every event emitted during that driven turn carries the current input origin:
   `STEERING_INJECTED`, assistant text events, tool-call events, `COMMUNICATE`,
   job lifecycle events, warnings, errors, and nested watch deliveries.
6. Jobs created during that turn store the origin on their runtime/job record so
   detached completion events carry it after the turn ends.

Origin is therefore part of the work item, not a goroutine-local side effect.
The safe implementation shape is explicit fields on queue entries, running jobs,
delegate restore descriptors, watch-send pending rows, and the session's
currently-processed input.

## Suppression Rule

Before a watch records a notification or a watch send, it evaluates:

```go
func shouldSuppressWatch(cfg *watchConfig, event events.SessionEvent) bool {
    for _, origin := range event.Origin {
        if origin.Kind == "watch" &&
           origin.WatchID == cfg.watchID &&
           origin.Generation == cfg.generation {
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
appears in the origin chain. This preserves useful chains such as a cost sentinel
watching an observer's own expensive behavior.

## Steering Scenario

Monty Python observer:

1. Caller says `actually`.
2. Watch `W@G1` fires delivery `D1` to observer.
3. Observer receives a frame with origin `[W@G1/D1]`.
4. Observer calls `delegate_send(to="caller", message="PYTHON_QUOTE ...")`.
5. The caller steering entry stores origin `[W@G1/D1]`.
6. Caller processes the steering turn. Its `STEERING_INJECTED`,
   `ASSISTANT_TEXT_*`, `TOOL_CALL_*`, and `COMMUNICATE` events carry
   `[W@G1/D1]`.
7. Watch `W@G1` ignores those events because its own origin is present.

If the human later says `actually` again, that is a new `USER_INPUT` with no
watch origin and `W@G1` may fire normally.

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
origin: user
event:
  kind: communicate
  message: actually alpha marker
  await_reply: false
  truncated: false
```

For watch-originated events, include origin summary:

```text
origin:
  kind: watch
  watch_id: watch_...
  delivery_id: del_...
  suppressed_by: same_watch_origin
```

Frames delivered to observers normally will not include suppressed events
because no delivery occurs. The origin summary is still useful for cross-watch
deliveries and debugging.

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
| `communicate` | `message`, `await_reply`, `truncated` |
| `assistant.message` | final text excerpt, model, finish reason, truncation |
| `assistant.tool` | tool name, call id, argument excerpt/hash, result status |
| `job.notification` | job id, status, reason, excerpt if available |

## Runtime Alias Rules

`delegate_send(to="caller")` is still the right model-facing action for observer
injection. Do not add a separate injection primitive.

The route must inherit current input origin. This applies only when the caller
alias exists, which is already restricted to delegate/watch-delivered contexts.
Top-level `caller` remains invalid.

Watch sends to `send.to="caller"` are internal notification-rail deliveries, not
ordinary `delegate_send(to="caller")` calls. They still create a watch origin for
the notification turn so the owner's response to that notification cannot echo
back into the same watch.

## Data-Carrying Sites

Minimum implementation sites:

- `events.SessionEvent`: add origin chain field or equivalent internal metadata.
- `Session.emit` / `sendEvent`: attach current input origin to emitted events.
- `Session` processing state: hold current input origin while processing user,
  notification, continuation, and steering inputs.
- Steering queue entries: store origin.
- Job records / running jobs: store origin for detached lifecycle events.
- Watch-send pending records: store origin with `watch_id`, generation, and
  `delivery_id`.
- Delegate resume descriptors: store origin for watch-started observer jobs.
- `delegate_send(to="caller")`: copy the sender's current input origin into the
  caller steering entry.
- `job_watch.onSessionEvent`: suppress same-watch echoes before any delivery
  accounting or pending-frame mutation.

The implementation should avoid context globals for provenance. Explicit fields
are harder to forget and easier to test.

## Durable State

Pending watch sends must persist enough origin to survive process restart:

```json
{
  "watch_id": "watch_...",
  "generation": "wg_...",
  "delivery_id": "del_...",
  "target": "dlg_...",
  "message": "...",
  "frame": "...",
  "origin": [
    {
      "kind": "watch",
      "watch_id": "watch_...",
      "generation": "wg_...",
      "delivery_id": "del_..."
    }
  ]
}
```

If Serf restores a pending watch delivery and starts an observer job after
restart, that observer job still carries the same origin. If Serf restores a
running job as `stopped/runtime_lost`, the terminal lifecycle event should carry
the job's stored origin if known.

## Interaction With Existing Guards

Current `FromWatch` suppression for watch-originated delegate lifecycle events is
a special case of this design. It should either be replaced by origin-chain
suppression or kept temporarily as a belt-and-suspenders guard while origin is
rolled out.

The existing validation that rejects direct feedback shapes such as
`target="caller", events=["communicate"], send.to="caller"` should remain. Origin
suppression prevents accidental causal loops; it should not legalize obviously
self-delivering configurations that teach the model the wrong composition.

Delivery budgets still matter. Origin suppression prevents same-watch cycles,
but a broad observer can still cause useful-but-expensive cross-watch traffic.
The existing watch delivery cap remains the circuit breaker.

## Edge Cases

### Observer sends a message containing the trigger word

Still safe. The observer's injected steering and downstream model turn inherit
the watch origin, so the same watch suppresses those events even if the message
contains the trigger word.

### Observer starts another delegate

If delegation allowance permits it, the child delegate job inherits the origin.
Lifecycle and final-result events from that child also carry the origin.

### Observer queues multiple messages while caller is busy

Each steering entry carries its own origin. If the queue coalesces messages in a
future implementation, the merged entry's origin is the union of all source
origins, preserving suppression for every contributing watch.

### Watch fires because of a human follow-up after observer injection

A later human `USER_INPUT` has no watch origin, so the watch may fire. This is
not a loop; it is a new external cause.

### Watch A causes Watch B causes Watch A

`A -> B -> A` is suppressed when A sees its own origin in the chain. `B` may fire
once on A's effect unless B is also already in the chain. This allows useful
pipelines while breaking cycles.

### Watch is cleared and recreated

Suppression keys include generation. Old-origin events suppress only the old
generation. Pending deliveries from cleared generations are dropped, so stale
events do not affect the replacement watch except as ordinary new inputs if they
are truly re-emitted after the new watch exists.

### Origin chain gets long

Set a small internal max depth, for example 16. On overflow, retain the oldest
and newest watch origins and set `truncated=true` in diagnostic rendering. Do
not drop all origins, because that reopens loops.

## Testing Requirements

Unit tests:

- `job_watch` suppresses a same-watch-origin `communicate` event before creating
  a pending watch send.
- Suppression checks generation; same `watch_id` with old generation does not
  suppress the new watch.
- Watch-send pending records persist and restore origin.
- `delegate_send(to="caller")` from a watch-delivered observer stores origin on
  the caller steering entry.
- Events emitted during a steering-driven turn carry the steering origin.
- Detached delegate/job completion events inherit the origin stored on the job.

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
  - A suppresses re-entry when B's effect carries A in the origin chain.

Live manual e2e:

- Run the Monty Python scenario with `kimi/kimi-for-coding`.
- Read parent and observer transcripts after the run.
- Verify there are exactly two `PYTHON_QUOTE` injections for two external trigger
  inputs and zero extra observer jobs from injected/acknowledgement traffic.

## Implementation Sequence

1. Add event-origin types and attach current input origin in `Session.emit`.
2. Add origin fields to steering queue entries, running jobs, delegate restore,
   and watch-send pending rows.
3. Thread origin through watch delivery, observer delegate start/resume, and
   `delegate_send(to="caller")`.
4. Implement same-watch suppression in `job_watch.onSessionEvent` before
   delivery accounting.
5. Add content-bearing `communicate` event frames.
6. Add unit tests for propagation and suppression.
7. Update `job-watch-actually-monty-python-injection.md` from an aspirational
   failure card to the expected pass contract.
8. Run live Kimi e2e and inspect transcripts.

## Open Questions

- Should event origin be exposed on the public SSE/appwire stream immediately, or
  kept internal until the hub has rendering affordances? Semantically it belongs
  on the event envelope either way.
- Should watch frames render the full origin chain or only a compact summary?
  Compact summary is probably enough for models; full chain can remain in logs.
- Should there be future opt-in cross-watch policies, such as "suppress all
  watch-originated events"? Not for v1; same-watch suppression is the smallest
  safety boundary.
