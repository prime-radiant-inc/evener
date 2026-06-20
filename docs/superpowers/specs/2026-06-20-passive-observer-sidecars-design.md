# Passive Observer Sidecars Design

Date: 2026-06-20
Status: implemented root-cause fix
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

The root cause was not watch delivery. The sidecar was a normal delegate being asked to act like a passive callback. Normal delegate turns were built with required tool choice and then expected to end with a result-tool response. When a watch frame needed no action, the delegate still had to produce some tool call. The sidecar therefore used harmless tools (`job_list`, later `exec_command true`) as a substitute for a missing “nothing to do for this frame” outcome.

A second issue amplified the churn: `events: ["assistant.tool"]` delivered every parent tool event to the sidecar. The observer had to inspect and ignore `job_watch`, `job_read_output`, `use_skill`, `job_list`, `task_list`, `exec_command`, and transcript tools even though only successful `read_file` results were relevant.

A later Kimi fluency run exposed a related API-shape problem: the model chose
`events: ["assistant.message"]` when it needed result-tool messages. That was
not a passive-observer runtime failure; it was a public vocabulary problem.
Plain assistant prose is an internal transcript/UI event, while `communicate`
is the explicit result/status channel models should watch. Serf should reject
`assistant.message` at `job_watch` creation with guidance rather than silently
upgrading it to `communicate`, because auto-upgrade would hide the model's
confused event selection.

## Design Thesis

Keep observer sidecars as composition: `delegate` + `job_watch` + `delegate_send`. Do not introduce a full observer-agent runtime in v1.

Add one API cleanup and two narrowly scoped capabilities:

1. **Public event vocabulary cleanup** so `job_watch` exposes
   `assistant.tool`, `communicate`, and `job.notification`, and rejects
   `assistant.message` with actionable guidance.
2. **Structured event predicates** at the watch boundary, so irrelevant events are never delivered.
3. **Watch-delivery turn semantics** for watch-originated observer turns, so a delegate can finish with non-empty bare text when it has no tool work to do.

Together these make passive sidecars cheap and deterministic while preserving the existing watch mailbox, causal provenance, and same-watch loop suppression design.

## Goals

- Support passive observer sidecars that ignore non-matching frames without tool churn.
- Make `communicate` the only public watch kind for result/status messages and keep bare assistant prose internal.
- Let `assistant.tool` watches filter on stable structured fields such as tool name and success/error status.
- Preserve existing watch delivery accounting: pending, delivered, dropped, evicted, coalesced, and self-loop diagnostics remain meaningful.
- Preserve existing same-watch causal suppression. Observer output caused by a watch delivery must not retrigger that same watch.
- Keep normal session empty-response and bare-text protections unchanged.
- Keep the public API small and implementable incrementally.
- Avoid a new no-op tool whose only purpose is to satisfy forced tool choice.

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
| Internal disposition | Non-empty bare assistant text recorded in the observer transcript for a watch-originated turn that needs no tool action. |
| Qualifying frame | A watch frame that passes the watch condition and event predicate. |
| Ignored frame | A delivered frame for which the observer produces an internal disposition and no side effect. |

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

### Layer 2: Watch-delivery turn semantics

Observers need a way to consume a watch frame without producing user-facing output or calling a no-op tool. When a delegate run is started or resumed by watch delivery:

- the model request uses `tool_choice:auto` instead of `required`;
- a non-empty no-tool response is accepted as an internal disposition;
- a truly empty response still uses the existing empty-response retry path;
- the default delegate `communicate` nudge is skipped for that watch-delivery run.

This is intentionally scoped to the run source, not just provenance. A normal delegate that happens to receive watch-stamped steering still keeps the normal delegate contract.

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

## Watch-Delivery Turn Details

### Turn classification

The runtime snapshots `runFromWatch` when `subagent.run` starts. If true, the child session processes the input as `EntryWatchDelivery`; otherwise it remains an ordinary `EntryUserInput` delegate turn.

### Turn completion

For `EntryWatchDelivery`, a non-empty response with no tool calls terminates the turn cleanly. The text remains in the child transcript as the observer's internal disposition, but no `communicate` output is produced and no parent steering is injected.

This is narrower than changing `handleNoToolCalls`: ordinary empty model responses remain errors/retries, and ordinary delegate bare text still triggers the result-tool repair path.

### Delivery accounting

A watch delivery is still considered delivered when the frame reaches the observer, matching current delivery semantics. Internal disposition is observer-turn state, not a fifth watch-send terminal.

Optional future diagnostics may count observer dispositions (`ignored`, `handled`, `responded`) in the delegate transcript or doctor tooling, but v1 does not need a new durable watch-send terminal.

### Provenance and loop suppression

If an observer calls `delegate_send(to="caller")`, the caller steering continues to inherit watch provenance. Parent events caused by acknowledging that steering continue to be suppressed by the same-watch key.

If an observer produces only an internal disposition, no parent steering is created, so there is no parent-side echo to suppress.

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
2. Observer produces non-empty bare text such as “ignored: not relevant”.
3. Observer turn completes cleanly.
4. No empty-response steering is injected and no no-op `job_list`/`exec_command` call is needed.

## Failure Handling

- Invalid predicate fields return `invalid_request` at watch creation.
- Unsupported predicate/event combinations return `invalid_request` at watch creation.
- If the observer returns truly empty output, existing empty-response repair still applies.
- If an ordinary delegate turn returns bare text without `communicate`, existing result-tool repair still applies.

## Testing Plan

### Event predicate tests

- `assistant.tool` + `tool_name=read_file` fires on `read_file` and does not fire on `job_list`.
- `assistant.tool` + `tool_name=read_file` + `status=ok` does not fire on a failed `read_file`.
- Unknown event filter fields fail watch creation.
- `event_filter` without an event condition fails watch creation.
- Unsupported event kind plus `tool_name` or `status` fails watch creation.

### Watch-delivery turn tests

- A watch-originated observer turn uses `tool_choice:auto`.
- A watch-originated observer turn can return non-empty bare text and complete without empty-response steering.
- A watch-originated observer turn that returns bare text does not trigger the default `communicate` nudge.
- A truly empty watch-originated response still uses empty-response repair.
- Ordinary empty responses outside watch-originated turns still trigger `handleNoToolCalls` retries.

### Integration tests

- Recreate the file-read encouragement sidecar using `event_filter={tool_name:"read_file", status:"ok"}`. Run several non-read tools and one successful read. Assert the observer receives only the read frame and sends one encouragement.
- Recreate a broad observer that receives multiple frames and ignores irrelevant frames with bare-text dispositions. Assert no no-op `job_list`/`exec_command true` churn.
- Preserve existing observer loop-suppression tests: `delegate_send(to="caller")` from a watch-originated observer must not retrigger the same watch.

## Compatibility and Migration

Existing watches without `event_filter` keep their current behavior. Existing sidecar patterns continue to work.

The new filter is additive and opt-in. The watch-delivery turn mode is internal and only affects delegate runs started or resumed by watch delivery.

Tool descriptions should be updated to guide models toward the safer pattern:

- Use `event_filter` instead of broad event watches when possible.
- In watch-originated observers, return a short internal disposition for frames that need no action.
- Do not use harmless tools as no-op acknowledgements.

## Open Questions

1. Should observer dispositions be summarized by doctor tooling? Recommendation: defer; current transcript structural counts are enough for v1 diagnosis.
2. Should `event_filter` later support arrays, negation, or regex? Recommendation: defer until real use cases require them. Keep v1 exact-match only.

## Recommendation

Implement this in two phases:

1. Add structured `assistant.tool` predicates to `job_watch` so passive observers receive fewer frames.
2. Add `EntryWatchDelivery` for watch-originated delegate turns so ignored delivered frames can terminate cleanly without a no-op tool call.

This fixes the observed failure mode without introducing a separate observer runtime, and it leaves the existing provenance-based loop suppression as the safety boundary.
