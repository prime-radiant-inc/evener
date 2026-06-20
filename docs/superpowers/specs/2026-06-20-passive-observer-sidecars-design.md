# Passive Observer Sidecars Design

Date: 2026-06-20
Status: draft for Jesse review
Builds on: `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md`, `docs/superpowers/specs/2026-06-18-observer-watch-origin-loop-design.md`, `docs/superpowers/specs/2026-06-19-serf-doctor-unified-design.md`

## Problem

Observer sidecars are currently composed from existing primitives:

1. start a delegate,
2. create a `job_watch` that sends frames to that delegate,
3. let the delegate inspect the frame and optionally react with `delegate_send(to="caller")`.

That composition is safe enough for active observers, but it fails for passive observers that should ignore most frames. In a live test, a sidecar was asked to encourage successful `read_file` calls and stay silent for all other `assistant.tool` events. The watch itself behaved correctly — deliveries settled, no drops, no evictions, and no causal self-loop — but the observer delegate ran many unnecessary tools:

- `delegate_send`: 2 calls
- `job_list`: 30 calls
- `exec_command`: 24 calls
- `task_list`: 3 calls

The root cause was not watch delivery. The sidecar was a normal delegate being asked to act like a passive callback. A normal delegate turn must produce either a tool call or a result-tool response. If it produces no content, the session loop treats that as an empty model response and retries with steering such as “Your previous response was empty. Please continue working on the task.” The sidecar therefore used harmless tools (`job_list`, later `exec_command true`) as a substitute for a missing “ignore this frame” operation.

A second issue amplified the churn: `events: ["assistant.tool"]` delivered every parent tool event to the sidecar. The observer had to inspect and ignore `job_watch`, `job_read_output`, `use_skill`, `job_list`, `task_list`, `exec_command`, and transcript tools even though only successful `read_file` results were relevant.

## Design Thesis

Keep observer sidecars as composition: `delegate` + `job_watch` + `delegate_send`. Do not introduce a full observer-agent runtime in v1.

Add two narrowly scoped capabilities:

1. **Structured event predicates** at the watch boundary, so irrelevant events are never delivered.
2. **Watch-frame acknowledgement** for watch-originated observer turns, so a delegate can explicitly mark a frame handled or ignored without producing user-facing output and without triggering empty-response repair.

Together these make passive sidecars cheap and deterministic while preserving the existing watch mailbox, causal provenance, and same-watch loop suppression design.

## Goals

- Support passive observer sidecars that ignore non-matching frames without tool churn.
- Let `assistant.tool` watches filter on stable structured fields such as tool name and success/error status.
- Preserve existing watch delivery accounting: pending, delivered, dropped, evicted, coalesced, and self-loop diagnostics remain meaningful.
- Preserve existing same-watch causal suppression. Observer output caused by a watch delivery must not retrigger that same watch.
- Keep normal session empty-response and bare-text protections unchanged.
- Keep the public API small and implementable incrementally.

## Non-Goals

- Do not add a new top-level `observer` primitive or a new always-running sidecar runtime in v1.
- Do not add arbitrary expression evaluation to `job_watch` filters.
- Do not make prompts responsible for loop prevention or silent ignore semantics.
- Do not change `delegate_send(to="caller")` provenance behavior.
- Do not relax empty-response handling for ordinary user-driven or delegate-driven turns.
- Do not require observers to receive every broad event and filter in model space.

## Vocabulary

| Term | Meaning |
| --- | --- |
| Passive observer | A delegate whose common action is to ignore most watch frames and react only to selected frames. |
| Watch-originated turn | A delegate turn started or steered by `job_watch.send`, carrying watch provenance and `FromWatch`/equivalent runtime metadata. |
| Event predicate | A structured filter evaluated against an event payload before a watch delivery is recorded. |
| Watch ack | A small observer action that marks the current watch frame `handled` or `ignored` and terminates that watch-originated turn. |
| Qualifying frame | A watch frame that passes the watch condition and event predicate. |
| Ignored frame | A delivered frame that the observer explicitly acknowledges as requiring no side effect. |

## Design Overview

The design has two layers.

### Layer 1: Structured event predicates

`job_watch` gains an optional `event_filter` field for event watches:

```json
{
  "operation": "create",
  "target": "caller",
  "events": ["assistant.tool"],
  "event_filter": {
    "tool_name": "read_file",
    "status": "ok"
  },
  "send": {
    "to": "dlg_observer",
    "message": "Encourage successful file reads only."
  }
}
```

The first supported predicate shape is deliberately small:

```go
type watchEventFilter struct {
    ToolName string `json:"tool_name,omitempty"`
    Status   string `json:"status,omitempty"` // "ok" or "error"
}
```

In v1 these fields apply only to `assistant.tool` events. Supplying `tool_name` or `status` for unsupported event kinds returns `invalid_request` with a message naming the unsupported field/kind combination.

The matcher evaluates predicates after event-kind matching and same-watch suppression, but before recording a watch send/notification. A non-matching event is a non-event for this watch: no delivery id, no pending row, no notification, and no observer wake.

### Layer 2: Watch-frame acknowledgement

Observers need a way to consume a watch frame without producing user-facing output. Add a small tool available only in watch-originated delegate turns:

```go
watch_ack(action: "ignored" | "handled", note?: string)
```

Semantics:

- `ignored`: the observer intentionally took no external action for this frame.
- `handled`: the observer completed whatever internal handling was needed without sending a caller message.
- `note` is optional, short, and diagnostic only. It is not sent to the user.

A successful `watch_ack` terminates the current watch-originated delegate turn cleanly. It is neither a user-facing result nor an error. It does not call `communicate`, does not inject steering into the parent, and does not create a new watchable assistant output event beyond ordinary tool-call events emitted by the observer session itself.

The tool is only valid when the active input provenance or run metadata identifies the turn as watch-originated. Outside that context it returns `invalid_request: watch_ack is only available while handling a watch delivery`.

## Event Predicate Details

### Supported field mapping

For `assistant.tool`, the event payload already exposes or can expose stable fields equivalent to:

- `tool_name`
- `call_id`
- output/result status
- output truncation status

The v1 filter supports only `tool_name` and status:

- `tool_name` matches the normalized model/tool name in the event (`read_file`, `exec_command`, `delegate_send`, etc.).
- `status` is `ok` when the tool result is not an error, and `error` when it is an error.

The exact internal mapping should be implemented at the event payload boundary, not by parsing rendered watch-frame text.

### Predicate combination

All supplied predicate fields are ANDed. Empty `event_filter` is equivalent to no predicate. Unknown fields are rejected rather than ignored.

Examples:

- `{ "tool_name": "read_file" }` matches all `read_file` tool completions regardless of success.
- `{ "tool_name": "read_file", "status": "ok" }` matches only successful `read_file` completions.
- `{ "status": "error" }` matches all failed tool completions.

### Relationship to `output_match`

`event_filter` is for event payloads. It does not replace `output_match`, which continues to operate on job output. A watch may not combine `event_filter` with a watch that has no event condition; doing so returns `invalid_request`.

## Watch Ack Details

### Tool availability

`watch_ack` should be present in the observer's tool registry when processing a watch-originated delegate turn. It may be hidden or return an explicit error in other contexts. The stricter implementation is preferred: do not advertise the tool unless the turn can use it.

### Turn completion

A `watch_ack` result counts as an intentional terminal action for the current watch-originated input. The session loop must not inject empty-response steering after a successful ack.

This is intentionally narrower than changing `handleNoToolCalls`: ordinary empty model responses remain errors/retries. The special case is not “empty is okay”; the special case is “this watch-originated turn called the explicit ack tool.”

### Delivery accounting

A watch delivery is still considered delivered when the frame reaches the observer, matching current delivery semantics. `watch_ack` is not a fifth watch-send terminal. It is observer-turn state, not watch-send state.

Optional future diagnostics may count observer dispositions (`ignored`, `handled`, `responded`) in the delegate transcript or doctor tooling, but v1 does not need a new durable watch-send terminal.

### Provenance and loop suppression

`watch_ack` carries the active watch provenance like any other tool call in the observer session. It must not weaken `shouldSuppressWatch` or the existing provenance propagation rules.

If an observer calls `delegate_send(to="caller")`, the caller steering continues to inherit watch provenance. Parent events caused by acknowledging that steering continue to be suppressed by the same-watch key.

If an observer calls `watch_ack`, no parent steering is created, so there is no parent-side echo to suppress.

## Example Flow

### Successful file read

1. Parent runs `read_file` successfully.
2. Parent emits `assistant.tool` with `tool_name=read_file`, `status=ok`.
3. Watch event-kind and predicate both match.
4. Watch sends one frame to the sidecar delegate.
5. Sidecar decides to encourage and calls `delegate_send(to="caller", message="Nice read.")`.
6. Parent receives steering with watch provenance.
7. Same-watch suppression prevents the encouragement/acknowledgement path from retriggering the same watch.

### Non-read tool call

1. Parent runs `job_list` successfully.
2. Parent emits `assistant.tool` with `tool_name=job_list`, `status=ok`.
3. Watch event-kind matches but predicate fails.
4. No watch delivery is recorded and the sidecar is not woken.

### Delivered but ignored frame

Some observers will still subscribe to broader event sets. For a delivered frame they do not need to act on:

1. Watch sends frame to observer.
2. Observer calls `watch_ack(action="ignored")`.
3. Observer turn completes cleanly.
4. No empty-response steering is injected and no no-op `job_list`/`exec_command` call is needed.

## Failure Handling

- Invalid predicate fields return `invalid_request` at watch creation.
- Unsupported predicate/event combinations return `invalid_request` at watch creation.
- `watch_ack` outside a watch-originated turn returns `invalid_request`.
- If `watch_ack` fails for an internal reason, the delegate turn remains subject to normal error handling.
- If the observer ignores a frame by doing nothing, existing empty-response repair still applies. The explicit ack is the contract.

## Testing Plan

### Event predicate tests

- `assistant.tool` + `tool_name=read_file` fires on `read_file` and does not fire on `job_list`.
- `assistant.tool` + `tool_name=read_file` + `status=ok` does not fire on a failed `read_file`.
- Unknown event filter fields fail watch creation.
- `event_filter` without an event condition fails watch creation.
- Unsupported event kind plus `tool_name` or `status` fails watch creation.

### Watch ack tests

- A watch-originated observer turn can call `watch_ack(action="ignored")` and complete without empty-response steering.
- A watch-originated observer turn can call `watch_ack(action="handled")` and complete without `communicate`.
- `watch_ack` outside a watch-originated turn fails.
- Ordinary empty responses outside watch-originated turns still trigger `handleNoToolCalls` retries.

### Integration tests

- Recreate the file-read encouragement sidecar using `event_filter={tool_name:"read_file", status:"ok"}`. Run several non-read tools and one successful read. Assert the observer receives only the read frame and sends one encouragement.
- Recreate a broad observer that receives multiple frames and acks ignored frames. Assert no no-op `job_list`/`exec_command true` churn.
- Preserve existing observer loop-suppression tests: `delegate_send(to="caller")` from a watch-originated observer must not retrigger the same watch.

## Compatibility and Migration

Existing watches without `event_filter` keep their current behavior. Existing sidecar patterns continue to work.

The new filter is additive and opt-in. The new ack tool is also additive and only matters for observers that choose to use it.

Tool descriptions should be updated to guide models toward the safer pattern:

- Use `event_filter` instead of broad event watches when possible.
- In watch-originated observers, call `watch_ack(action="ignored")` for frames that need no action.
- Do not use harmless tools as no-op acknowledgements.

## Open Questions

1. Should `watch_ack` be model-visible as a normal tool card, or should it be hidden behind a more declarative observer runtime interface? Recommendation: model-visible in v1, because it is explicit and testable.
2. Should observer dispositions be persisted for doctoring? Recommendation: defer; current transcript structural counts are enough for v1 diagnosis.
3. Should `event_filter` later support arrays, negation, or regex? Recommendation: defer until real use cases require them. Keep v1 exact-match only.

## Recommendation

Implement this in two phases:

1. Add structured `assistant.tool` predicates to `job_watch` so passive observers receive fewer frames.
2. Add `watch_ack` for watch-originated delegate turns so ignored delivered frames terminate cleanly.

This fixes the observed failure mode without introducing a separate observer runtime, and it leaves the existing provenance-based loop suppression as the safety boundary.
