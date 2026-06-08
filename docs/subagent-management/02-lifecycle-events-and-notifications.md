# Subagent Lifecycle Events and Notifications

Status: Proposed evergreen spec. Current serf emits parent-session `SUBAGENT_START` and `SUBAGENT_END` events for subagent runs; this spec defines the exact existing event contract and the minimal implementation path for richer lifecycle notifications without changing the core subagent execution model.

## Purpose

Make subagent lifecycle visibility explicit for parent agents, CLI/TUI clients, SDK callers, and future job-registry surfaces, while documenting that the current event channel is best-effort under backpressure.

A subagent is currently a child `Session` tracked by its parent session. The parent can control it through `spawn_agent`, `resume_agent`, `wait`, and `close_agent`, but the parent does not automatically receive the child transcript or internal activity. Lifecycle events are the low-volume notification channel that tells the parent-visible control plane when a child run starts and ends.

This document is limited to events and notifications. It complements:

- `01-job-registry.md` for the parent-scoped job record.
- `03-context-and-resume-controls.md` for spawn/resume/wait/close operation semantics.
- `04-raw-output-and-diagnostics.md` for final result, transcript access, and diagnostic boundaries.

## Goals

- Preserve the current parent-session event stream contract for subagent lifecycle transitions.
- Document the exact `SUBAGENT_START` and `SUBAGENT_END` payloads that exist today.
- Make clear that lifecycle events summarize child state; they are not child transcript streaming.
- Document current firing points for spawn, idle resume, running resume, run completion, wait timeout, and close, and target deterministic ordering where implementation supports it.
- Add future notification fields/events only where they remove ambiguity for users or clients.
- Keep the implementation YAGNI and DRY by routing all lifecycle emission through a small shared helper layer.

## Non-goals

- No live streaming of every child assistant token, tool call, or tool result into the parent event stream.
- No global event bus or cross-parent subscription registry.
- No durable event store beyond existing session transcript/event persistence behavior.
- No workflow/DAG progress protocol.
- No approval event protocol in this spec.
- No separate cancellation API in this spec; current cancellation/cleanup remains `close_agent`.
- No change to the child session's own transcript format.

## Current implementation anchors

- Event kind constants are `EventSubagentStart = "SUBAGENT_START"` and `EventSubagentEnd = "SUBAGENT_END"` in `agent/events/events.go:57-60`.
- A generic session event carries `kind`, `timestamp`, `session_id`, and optional typed `data` in `agent/events/events.go:81-89`; current subagent lifecycle emit sites always provide typed data.
- Current subagent payload structs are `SubagentStartData` and `SubagentEndData` in `agent/events/payloads.go:185-196`.
- Current subagent status values are `running`, `completed`, and `failed` in `agent/subagents.go:23-33`.
- Current wait/result payload shape is `status`, `output`, `success`, `turns_used`, and optional `transcript_ref` in `agent/subagents.go:35-42`.
- The parent-scoped subagent map is in `agent/subagent_manager.go:19-23`, and status enumeration exposes only `id`, `status`, and `turns_used` today in `agent/subagent_manager.go:71-87` and `agent/status.go:27-32`.
- Non-blocking spawn tracks a child, starts a goroutine using `context.Background()`, emits `SUBAGENT_START`, and returns `{"agent_id":...,"status":"running"}` in `agent/subagents.go:319-357`.
- Idle resume resets run-local state, emits `SUBAGENT_START`, and starts another `context.Background()` child run in `agent/subagents.go:375-406`.
- Running resume only calls `sub.sess.Steer(input)` and returns `"ok"`; it does not emit a lifecycle event today in `agent/subagents.go:365-372`.
- Completion records result/error/status/turn count, closes the run's `done` channel, and emits `SUBAGENT_END` once in `agent/subagents.go:520-547`.
- Wait timeout returns `wait timeout` but does not cancel the child in `agent/subagents.go:426-441`.
- `close_agent` closes the child session, waits up to five seconds, returns a final result snapshot and removes the child from the manager on success, and returns a timeout error without removal if the close wait expires in `agent/subagents.go:452-485`.
- Tool handlers implement blocking spawn/resume by calling `waitAgent` and adding `agent_id` to the wait-shaped result in `agent/session_tools_subagent.go:71-95` and `agent/session_tools_subagent.go:127-145`.
- The model-facing tool descriptions already warn that blocking results are returned directly and should not be followed by `wait` in `agent/internal/tool/definitions.go:193-267`.

## Exact current event API contract

All subagent lifecycle events are emitted on the **parent session** event stream. The event `session_id` is the parent session ID, not the child session ID. The child session ID is carried as `data.agent_id`.

### Event envelope

The shared session event envelope is:

```json
{
  "kind": "SUBAGENT_START",
  "timestamp": "UTC time.Time JSON timestamp encoded by Go as RFC3339Nano-compatible text",
  "session_id": "parent_session_id",
  "data": {}
}
```

Contract:

- `kind` is an event kind string.
- `timestamp` is the event creation time: a UTC `time.Time` JSON timestamp encoded by Go as RFC3339Nano-compatible text.
- `session_id` is the session whose stream receives the event. For subagent lifecycle events, this is the parent session.
- `data` is optional only at the generic `SessionEvent` type level. For `SUBAGENT_START` and `SUBAGENT_END`, `data` is required and must be one of the typed payloads below.

### `SUBAGENT_START`

Current Go payload:

```go
type SubagentStartData struct {
    AgentID string `json:"agent_id"`
    Task    string `json:"task"`
}
```

Current JSON payload:

```json
{
  "kind": "SUBAGENT_START",
  "session_id": "parent_session_id",
  "timestamp": "<UTC RFC3339Nano-compatible timestamp>",
  "data": {
    "agent_id": "01CHILD...",
    "task": "task text or idle-resume message"
  }
}
```

Semantics:

- For `spawn_agent`, the code calls the lifecycle emitter after a child is registered and its run goroutine has been started. Because the current goroutine starts before this emit, a very fast child can race and attempt `SUBAGENT_END` before `SUBAGENT_START`; delivery is best-effort, and ordering is not guaranteed.
- For idle `resume_agent`, emitted after run-local state is reset and before the new run goroutine is started.
- Not emitted when `resume_agent` steers a currently running child.
- `agent_id` is the child session ID and parent registry key.
- `task` is the initial spawn task or the idle-resume input message.

### `SUBAGENT_END`

Current Go payload:

```go
type SubagentEndData struct {
    AgentID   string `json:"agent_id"`
    Status    string `json:"status"`
    TurnsUsed int    `json:"turns_used"`
}
```

Current JSON payload:

```json
{
  "kind": "SUBAGENT_END",
  "session_id": "parent_session_id",
  "timestamp": "<UTC RFC3339Nano-compatible timestamp>",
  "data": {
    "agent_id": "01CHILD...",
    "status": "completed",
    "turns_used": 4
  }
}
```

Semantics:

- Emitted once per child run after `ProcessInput` finishes, the optional communicate nudge/stop hook path completes, result state is captured, and the run's `done` channel is closed.
- `status` is `completed` when the child run has no error and `failed` when it has an error.
- `turns_used` is the child session turn count captured at run end.
- The payload does not include `output`, `success`, `transcript_ref`, error text, tool counts, token counts, elapsed time, or close/removal state today.

## Lifecycle notification state machine

```text
spawn_agent(blocking=false|true)
  -> child session created
  -> child registered in parent manager
  -> child run goroutine started
  -> SUBAGENT_START
  -> running
  -> SUBAGENT_END with completed|failed (current spawn can race and emit this before SUBAGENT_START for very fast children)
  -> idle tracked, result available until consumed/closed

resume_agent on idle tracked child
  -> run-local result fields reset
  -> SUBAGENT_START
  -> child run goroutine started
  -> running
  -> SUBAGENT_END with completed|failed
  -> idle tracked, new result available until consumed/closed

resume_agent on running child
  -> steering message injected
  -> no current lifecycle event
  -> remains running

wait(agent_id, timeout_ms)
  -> if run ends: returns result snapshot and consumes that run result
  -> if timeout: returns wait timeout; child remains running; no current lifecycle event

close_agent on running child
  -> child Session.Close()
  -> wait up to 5 seconds for run goroutine
  -> if run exits: SUBAGENT_END may already have fired from the run path
  -> if run exits: return final snapshot and remove child from parent manager
  -> if timeout expires: return close-timeout error and leave child tracked
  -> no current close/removal event

parent Session.Close()
  -> manager drains tracked children
  -> closes each child outside manager lock
  -> waits for active subagent run goroutines before closing the parent event stream
  -> normal run-terminal SUBAGENT_END may still be emitted at most once during close
  -> no separate current close/removal event
```

## Notification semantics and warnings

- Current `Session.emit` delivery is best-effort: it attempts a non-blocking send and may drop events if the session event channel is full. Exact-count requirements below are about implementation emission attempts, not guaranteed observation by a slow subscriber.
- Lifecycle events are parent-facing summaries. They do not expose child tool calls, assistant text, raw provider output, or intermediate reasoning.
- The parent must use blocking spawn/resume, `wait`, `close_agent`, or transcript tools to retrieve final child output.
- `SUBAGENT_END` means a child run ended; it does not mean the parent consumed the result.
- `SUBAGENT_END` does not mean the child job was removed from the registry. Current completed/failed children remain tracked until `close_agent` or parent close.
- A wait timeout is not cancellation. The child keeps running because spawn and idle resume intentionally use `context.Background()` for child execution.
- Blocking spawn/resume are notification-equivalent to non-blocking spawn/resume plus wait: the same start/end events occur, but the tool call also returns the wait-shaped result. Current spawn does not guarantee start-before-end ordering for immediately completing children.
- A running resume is steering, not a new run. It should not emit `SUBAGENT_START` unless the implementation introduces a separate `SUBAGENT_MESSAGE`/`SUBAGENT_STEER` event.

## Proposed evergreen event contract

The current contract should remain backward-compatible. New fields may be added to event payload structs as optional JSON fields, but existing field names must not be renamed.

### Required v1 fields

`SUBAGENT_START` required fields:

```json
{
  "agent_id": "string",
  "task": "string"
}
```

`SUBAGENT_END` required fields:

```json
{
  "agent_id": "string",
  "status": "completed|failed",
  "turns_used": 0
}
```

### Recommended optional fields

Add only when the corresponding data is already available from existing session/subagent state or the job registry from `01-job-registry.md`:

```json
{
  "agent_id": "01CHILD...",
  "task": "task text or resume message",
  "transcript_ref": "local:01CHILD...",
  "agent_type": "subagent",
  "model": "effective model/profile ref",
  "parent_tool_call_id": "toolu_...",
  "run_id": "optional per-run id if the registry adds one"
}
```

```json
{
  "agent_id": "01CHILD...",
  "status": "completed|failed",
  "turns_used": 4,
  "transcript_ref": "local:01CHILD...",
  "error": "optional short diagnostic; omit on success",
  "elapsed_ms": 12345,
  "run_id": "optional per-run id if the registry adds one"
}
```

Guidelines:

- Prefer optional fields on `SUBAGENT_START`/`SUBAGENT_END` before adding new event kinds.
- Do not include `output` in `SUBAGENT_END`; keep result retrieval centralized through lifecycle tools and transcript access.
- Include `transcript_ref` once available because it lets clients link terminal notifications to diagnostics without waiting.
- Include `run_id` only if a per-run registry identity exists; do not fabricate one from turn counts.
- Future terminal status values such as `cancelled` change the value domain of the required `status` field; add them only with an explicit cancellation/status migration, not as an optional-field-only change.

## Future event kinds, only if needed

These are optional and should be implemented only when a concrete client needs them:

| Event | Purpose | YAGNI gate |
| --- | --- | --- |
| `SUBAGENT_RESUME` or `SUBAGENT_MESSAGE` | Notify that a running child was steered. | Add only if UI/SDK needs visible steering history outside transcripts. |
| `SUBAGENT_WAIT_TIMEOUT` | Notify that a parent wait timed out while child kept running. | Add only if clients cannot reliably observe tool errors. |
| `SUBAGENT_RESULT_CONSUMED` | Mark that a wait/blocking call consumed the run result. | Add only with a job list/status UI that exposes result availability. |
| `SUBAGENT_CLOSED` | Distinguish registry removal/session close from run completion. | Add with retained job registry or client-side job panels. |
| `SUBAGENT_CLOSE_TIMEOUT` | Surface failure to close within the five-second close wait. | Add if close timeout needs to be observable outside tool result errors. |
| `SUBAGENT_ACTIVITY` | Coarse child heartbeat/progress. | Add only with throttling and no child transcript streaming. |

Do not add `SUBAGENT_OUTPUT_DELTA` in the first lifecycle-events implementation. It duplicates transcript/result surfaces and risks leaking large or sensitive child output into the parent stream.

## YAGNI/DRY implementation plan

1. **Keep current events as the v1 baseline.** Preserve `SUBAGENT_START` and `SUBAGENT_END` names and required payload fields.
2. **Create a tiny shared lifecycle emitter.** Add helper methods near the subagent implementation, for example `emitSubagentStart(sub, input, metadata)` and `emitSubagentEnd(sub, snapshot, metadata)`, so spawn and idle resume cannot diverge.
3. **Do not introduce a general notification framework.** The existing `Session.emit(events.EventKind, events.EventData)` path is enough for this spec.
4. **Add optional payload fields only after the data has one canonical source.** For example, `transcript_ref` can come from the same `encodeRef("", a.sess.ID())` logic used by `resultSnapshotLocked`; registry metadata should come from the job registry, not duplicate fields on `subagent` unless necessary.
5. **Keep terminal emission in the run path.** `SUBAGENT_END` should continue to be emitted by `subagent.run` after result state is finalized, not by each caller of `wait`, blocking spawn, blocking resume, or close.
6. **Do not emit start for running resume.** If steering notifications are needed, add a distinct message event rather than overloading `SUBAGENT_START`.
7. **Do not emit output in events.** Keep event payloads small; result output remains in wait-shaped tool results and transcripts.
8. **If adding close events, emit them from close paths only.** `SUBAGENT_END` remains run-terminal; `SUBAGENT_CLOSED` would mean registry/session cleanup.
9. **Preserve lock ordering.** Never hold the manager mutex while calling into a child session. Current manager documentation requires manager mutex outer, subagent mutex inner, and no child `Session` calls while holding the manager mutex.
10. **Keep event tests table-driven.** Test event order and payload fields from public tool paths rather than duplicating implementation details.

## Acceptance criteria

### Current-contract acceptance

- `spawn_agent(blocking=false)` calls the lifecycle emitter exactly once with `SUBAGENT_START` for the new child run.
- `spawn_agent(blocking=true)` calls the lifecycle emitter exactly once with `SUBAGENT_START` and exactly once with `SUBAGENT_END` for the child run, and returns a wait-shaped result with `agent_id` when the internal wait produces parseable result JSON; current spawn does not guarantee which lifecycle event is attempted or observed first for an immediately completing child.
- `resume_agent` on an idle child calls the lifecycle emitter exactly once with `SUBAGENT_START` for the new run.
- `resume_agent` on a running child does not emit `SUBAGENT_START` or `SUBAGENT_END` immediately; it only steers the active run.
- A successful child run calls the lifecycle emitter exactly once with `SUBAGENT_END` and `status: "completed"`.
- A failed child run calls the lifecycle emitter exactly once with `SUBAGENT_END` and `status: "failed"`.
- `SUBAGENT_START.data` contains exactly the required current fields `agent_id` and `task` unless optional fields have been intentionally added.
- `SUBAGENT_END.data` contains exactly the required current fields `agent_id`, `status`, and `turns_used` unless optional fields have been intentionally added.
- The run path attempts `SUBAGENT_END` emission on run completion even if no caller is currently waiting.
- `wait(timeout_ms)` timeout does not emit `SUBAGENT_END` by itself and does not cancel the child.
- Repeated `wait` after result consumption does not re-emit lifecycle events.
- `close_agent` removes the child from the active manager on successful close; close timeout returns an error and leaves the child tracked. Any terminal run event is still emitted at most once.
- Parent session detailed status remains consistent with lifecycle state while the child is tracked.

### Future-field acceptance

If optional fields are added:

- Existing tests and clients that only require current fields continue to pass.
- New optional fields are omitted rather than set to misleading empty values when unknown.
- `transcript_ref`, if present, matches the transcript ref returned by wait-shaped results for the same child.
- `run_id`, if present, is stable for one run and changes only when idle resume starts a new run.

## Tests

Recommended tests should be narrow and should exercise public paths.

1. **Non-blocking spawn emits start.** Spawn a child with `blocking=false`; assert the parent event stream has `SUBAGENT_START` with the returned `agent_id` and original task.
2. **Run completion emits end once.** Let a child complete; assert exactly one `SUBAGENT_END` with matching `agent_id`, terminal status, and non-negative `turns_used`.
3. **Blocking spawn event count.** Use `blocking=true`; with a concurrently drained event stream or emitter instrumentation, assert one `SUBAGENT_START` attempt and one `SUBAGENT_END` attempt for the same `agent_id`, and that a successful parseable tool result includes that `agent_id`. Add a stricter start-before-end assertion only after implementation emits spawn start before launching the child goroutine.
4. **Idle resume emits a new start.** Complete a run, call `resume_agent` on the idle child, and assert a second `SUBAGENT_START` with the resume message.
5. **Running resume is steering only.** Resume while a child is running; assert no additional `SUBAGENT_START` is emitted for that steering call.
6. **Wait timeout is non-terminal.** Wait with a timeout against a still-running child; assert the wait returns timeout, no `SUBAGENT_END` is caused by the timeout, and the child remains tracked/running.
7. **End emitted without waiter.** Spawn non-blocking and never call wait until after completion; assert `SUBAGENT_END` still appears.
8. **End emission is idempotent.** Call wait/close paths after completion as allowed; assert no duplicate `SUBAGENT_END` for the same run.
9. **Close running child.** Close a running child; assert close returns a final snapshot or close-timeout error according to implementation, and that any `SUBAGENT_END` is emitted no more than once.
10. **Payload compatibility.** Marshal/unmarshal `SubagentStartData` and `SubagentEndData` and assert the JSON field names are `agent_id`, `task`, `status`, and `turns_used`.

## Documentation notes for users and model prompts

Model/tool documentation should say:

- Use lifecycle events to know that a child started or finished.
- Use `wait`, blocking spawn/resume, or `close_agent` to get the child result JSON.
- Use `transcript_ref` from the result for diagnostics; lifecycle events do not include child output today.
- Do not treat a wait timeout as cancellation.
- Do not call `wait` after a blocking spawn/resume for the same run; the result has already been returned and consumed.
- If a completed child output is inadequate, use `resume_agent` with sharper instructions or spawn a better-suited agent.
