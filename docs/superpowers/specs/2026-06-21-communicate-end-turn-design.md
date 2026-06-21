# Communicate End-Turn Semantics Design

Date: 2026-06-21
Status: implemented
Builds on: `docs/superpowers/specs/2026-06-20-passive-observer-sidecars-design.md`

## Problem

`communicate` currently has one lifecycle control: `await_reply`.

That name describes whether the message expects a reply, but the runtime uses it
to decide whether the current model activation ends in `awaiting` or `idle`.
Both branches end the activation. The real load-bearing question is not "is a
reply expected?" It is "should this visible message end the current turn?"

This mismatch caused a live `job_watch.observer_callback` failure:

1. The model started an observer delegate.
2. In the same assistant response, it called
   `communicate(await_reply=false, "Starting observer delegate and watch flow.")`.
3. Serf executed both tools, accepted `communicate`, and ended the turn.
4. The model never created the watch or read the trigger file.

The model was trying to narrate while working. Because plain assistant messages
are not the public user-visible channel, `communicate` is the only available
narration surface. Making every `communicate(await_reply=false)` terminal forces
agents to choose between staying silent and accidentally stopping.

## Decision

Replace `await_reply` with `end_turn`.

No backward compatibility is included. `await_reply` is removed from the
model-facing schema and runtime argument contract in the same change.

```json
{
  "message": "Starting observer delegate and watch flow.",
  "end_turn": false,
  "output": {
    "message": "",
    "data": {},
    "artifacts": []
  }
}
```

```json
{
  "message": "RESULT_JOB_WATCH",
  "end_turn": true,
  "output": {
    "message": "RESULT_JOB_WATCH",
    "data": {
      "observer_callback": "WATCH_OBSERVED"
    },
    "artifacts": []
  }
}
```

## Semantics

`end_turn=false` means:

- emit a visible status/narration message;
- do not run Stop hooks;
- do not transition the session out of processing because of this message;
- keep executing the current tool batch and continue the turn after tool results.

`end_turn=true` means:

- emit a visible final/blocking/readiness message;
- record the structured result envelope;
- run Stop hooks;
- end the current activation and return the message/output as the turn result.

There is intentionally no separate `wait_for_reply` mode. A question to the user
or caller, or a readiness marker that waits for a future watch frame, is just a
visible message that ends the current activation. The other side of the system
may present it as a question or waiting state, but the runtime does not need a
separate model-facing lifecycle mode to represent that.

## Output Envelope

`output` remains the structured result envelope:

```json
{
  "message": "human-readable structured summary",
  "data": {},
  "artifacts": []
}
```

The envelope is meaningful when `end_turn=true`. For ordinary narration with
`end_turn=false`, callers should provide the empty default envelope:

```json
{"message": "", "data": {}, "artifacts": []}
```

`purpose` remains a top-level tool argument injected by the registry. It is not
part of `output`.

## Runtime Contract

`communicate(end_turn=false)` is a side-effecting visible-message tool, not a
result tool invocation for turn completion.

`communicate(end_turn=true)` is the result/turn-completion tool invocation.

If a model emits multiple `communicate` calls in one response:

- all `end_turn=false` messages are emitted in order;
- the first successful `end_turn=true` message becomes the turn result;
- later tool calls in that same model response still execute if they were
already emitted by the model, but the next model round is skipped because the
turn is complete.

This preserves the existing ordered tool execution contract while making status
messages non-terminal.

## Affected Surfaces

The implementation must update all model-facing, runtime, event, and projection
surfaces in one atomic change:

- `agent/internal/tool/definitions.go`: replace `await_reply` with `end_turn`.
- `agent/session_tools_communicate.go`: require `end_turn`; set completion only
  when `end_turn=true`.
- `agent/session_tool_round.go`: Stop-hook reasons become
  `communicate.end_turn` or equivalent; no `await_reply` branch remains.
- `agent/events/payloads.go`: replace `CommunicateData.AwaitReply` with
  `EndTurn`.
- appwire projection, CLI output, transcript rendering, context manager
  communicate extraction, watch frame rendering, and tests: rename and adjust
  behavior.
- prompts and docs: describe `end_turn=false` as narration/status before
  immediate continued work and `end_turn=true` as activation-ending
  final/blocking/readiness completion.

## Testing Requirements

Add behavior tests that prove:

1. `communicate(end_turn=false)` emits a visible communicate event and does not
   finish the turn.
2. A response that batches `delegate` plus `communicate(end_turn=false)` keeps
   going to the next model round.
3. `communicate(end_turn=true)` still ends the turn and runs Stop hooks.
4. Legacy `await_reply` is rejected by schema validation.
5. Watch frames render `end_turn` for communicate events so observers can see
   whether a watched communicate was terminal/status.
6. Context compaction/result extraction preserves only terminal
   `communicate(end_turn=true)` messages as completed user/caller results.

Then rerun the `job_watch.observer_callback` fluency scenario. The premature
progress failure should disappear because "Starting observer delegate..." can be
represented as `communicate(end_turn=false)` without ending the workflow.

Validation found an additional observer fluency gap: assistant-tool watch frames
rendered the tool name and output/error but omitted the matched status and
original tool arguments. Observers that were asked to verify a
`read_file(status=ok)` trigger could not see `status=ok` or
`file_path=watch-trigger.txt` in the frame, so they reached for audit tools.
The implemented contract now includes `status` and `arguments_json` in
assistant-tool watch frames.

Validation also confirmed that a terminal owner notification after an observer
callback could wake the caller after it had already produced the final result.
Watch-origin delegate jobs now record terminal state without adding a redundant
owner notification after a successful `delegate_send(to="caller")` callback.

Live scenario validation after these fixes:

- `kimi/kimi-for-coding`, `job_watch.observer_callback`: 2/3 passed cleanly;
  the remaining repetition was blocked by provider quota before the behavior
  under test ran.
- `openai/gpt-5.4-mini`, `job_watch.observer_callback`: 3/3 passed cleanly.

## Follow-On Work

This design fixes the premature progress/narration failure and the two
observer-callback validation issues above.

The remaining known failure modes still need separate decisions:

- `job_watch` mode-shape friction for caller/session event watches;
- `communicate.output.purpose` strict-envelope mistakes.
