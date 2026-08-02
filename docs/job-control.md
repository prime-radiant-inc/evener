# Job control

Status: Evergreen contract for the shipped job-control system.

This document defines Serf's job-control model. It is an architecture/reference contract for the shipped system, not an implementation plan. Implementation sequencing belongs in separate ephemeral specs or plans.

## Summary

Serf job control is a generic, session-scoped background work system. A **job** is one asynchronous unit of work owned by a Serf session. Jobs are durable enough to list, inspect, notify about, and reconstruct after their creating turn has ended. Running processes are not required to survive a Serf process restart.

Two public job types are in scope:

- **`shell`**: a background-capable invocation of the existing shell/bash command tool.
- **`delegate`**: a background-capable subagent turn that starts a new child Serf conversation. The child transcript remains separate from the job output log.

The default model-facing posture is:

> Shell work runs foreground by default (set `background: true` to launch-and-return) and is promoted to a durable background job when the foreground wait times out. Delegate work returns a `job_id` immediately by default; use `max_wait_ms` to wait inline. Rely on automatic terminal notifications plus one-off `job_status`/`job_list` inspection for follow-up, and read a job's own output with `read_transcript(transcript_ref="job:<job_id>")`. Do not poll.

The target model intentionally does **not** expose:

- `job_kill`
- `job_ack`
- `wait_job`
- `close_agent`
- external `agent_id`

Stopping is handled by `job_stop`. Retention is policy-based, not model-acknowledgement-based. Waiting, when needed, is bounded — `max_wait_ms` on `delegate`/`job_stop`, or `background` on shell — not a separate wait tool. Reads never wait: no job-output read blocks.

## Model-facing guidance requirements

This reference contract is not itself the runtime system prompt, but the following guidance **must** be reflected in the tool descriptions and in Serf's `Background jobs` system-prompt section. These bullets are normative for model-facing documentation because they shape whether agents use jobs correctly:

- Shell commands run foreground by default and return inline output for quick commands. Set `background: true` to launch-and-return immediately; omit `background` (or set it `false`) for the session-default foreground wait. Foreground shell commands that exceed that wait are promoted to durable background jobs and return a `job_id`.
- Delegate work starts in the background by default (no `job_id` wait); use `max_wait_ms` when you want to wait inline for up to N ms. Shell and delegate defaults differ deliberately: shell commands are usually short decision-producing calls, while delegates are independent agentic work.
- Use `delegate` to start a new delegate conversation/job. It returns both a concrete `job_id` for that turn and a durable `delegate_id` for conversation follow-up. It does not continue an existing conversation.
- Use `delegate_send` for follow-up on a durable delegate conversation: if the delegate is running, it steers the active turn; if the delegate is idle, the default `on_idle="fail"` rejects instead of starting work. Use `on_idle="start"` only when you intend to start the delegate's next job in the same conversation.
- Use `delegate_send` only for follow-up on a durable child delegate conversation. Observer sidecars report through `communicate(end_turn=true)`, not `delegate_send`. `delegate_send` rejects `job_id`, `transcript_ref`, and runtime aliases on the public tool surface; ordinary delegate follow-up targets a `delegate_id`.
- When an observer readiness delegate result includes `watching:true` and `watches`, the observer is watching. Continue with the planned watched action and use the later observer `communicate(end_turn=true)` as the callback.
- After starting a background job, continue useful work or respond to the user. Do not immediately wait, poll `job_list`, or loop on `job_status`.
- Serf automatically injects one terminal notification for notification-armed background jobs when they complete, fail, are cancelled, or are stopped/lost. You are told when YOUR delegates finish; your delegates handle their own children's completions in their own turns (you are not interrupted about a job you did not create).
- Use `job_watch` when a condition should notify the watcher. `source` names what the watcher observes (`self`, `parent` when granted by `delegate(watch_parent=true)`, or a concrete `job_id` owned by this session or a live descendant). Delivery is implicit to the session that created the watch. Pick the trigger mode that matches the signal: `output_match` for concrete job output, `progress_interval_ms` for periodic progress, and `events`/`event_filter` for event frames. Do not use `job_watch` to learn when a job completes.
- Use `job_status` for a job's lifecycle: kind, status, phase, reason, running/quiet time, and `transcript_ref`. Use `read_transcript(transcript_ref="job:<job_id>")` for the bytes — shell stdout/stderr, and a delegate invocation's output/final report. For delegates, `status="completed"` means the delegate turn ended normally; it does not prove the delegated task succeeded. Read the report and its `structured_result` to judge task outcome.
- Use a session `transcript_ref` for delegate child conversation history; a `job:` ref reads that job's output log, never the child's conversation.
- Use `job_stop` only when the intent is to cancel or stop work. It does not delete output/history and it does not acknowledge results.
- Use `job_list` to recover or inspect job inventory, not to wait for completion. Branch primarily on `status`; treat `reason` as diagnostic detail except for documented operational cases such as `runtime_lost`, `run_timeout`, `awaiting_permission`, and `stop_pending`.

Tool descriptions should avoid these phrases because they train bad behavior:

- Do not describe `job_watch` as subscribing to job completion. Say: “When output/event/progress conditions happen, deliver a bounded watch notification/frame to the watcher that created the watch.”
- Do not describe a job-output read as a way to wait. Say: “Completion is notification-driven; do not poll this waiting for a job to finish.”
- Do not describe `job_stop` as cleanup. Say: “Request cancellation/stop; retained history/output remains.”
- Do not say `delegate` resumes an old agent. Say: “Start a fresh delegate conversation/job.”
- Do not say `delegate_send` resumes by default. Say: “Send a follow-up message to a delegate_id; running delegates receive it live, while idle delegates require `on_idle="start"` to start the next job.”
- Do not say `job_watch` sends arbitrary unbounded transcript context. Say: “Deliver bounded frame/excerpt metadata when the watch condition is met.”
- Do not say a `job:` transcript ref reads the delegate's conversation. Say: “Read the invocation's output/final report; use the child's session `transcript_ref` for the full conversation.”

## Choosing a wait primitive

Several tools can wait, but they wait on different things. Pick by intent, and do not poll.

| Intent | Use |
| --- | --- |
| Run a command and use its output | `shell` — foreground; promoted to a background job if it exceeds the session timeout |
| Launch a long command without waiting | `shell(background=true)` — returns a `job_id` immediately |
| Start a delegate and wait up to N ms for its result | `delegate(max_wait_ms=N)` — a timeout leaves it running |
| Learn when a backgrounded job finishes | the automatic terminal notification — nothing to call |
| Learn when a job's output contains X | `job_watch(operation="create", source=<job_id>, output_match=X)` to be notified |
| Re-observe progress on a long job | `job_watch(operation="create", source=<job_id>, progress_interval_ms=N)` (running targets only) |
| Resume an idle delegate and wait for its answer | `delegate_send(to=<delegate_id>, on_idle="start", max_wait_ms=N)` |
| Steer a running delegate | `delegate_send(to=<delegate_id>)` — returns on delivery; `max_wait_ms` is ignored and reported as `wait_ignored_reason` |

There is no "steer a running delegate and wait for its next reply" primitive: a live steer returns on delivery. To get an answer, let the delegate finish its turn (you are notified) and read `read_transcript(transcript_ref="job:<job_id>")`, or start its next turn with `delegate_send(on_idle="start", max_wait_ms=N)` once it is idle.

Terminal targets differ by watch type: only an `output_match`-only `job_watch` supports terminal catch-up (a one-shot scan of retained output on an already-terminal job). `events`, `progress_interval_ms`, and `every` watches require a running target and reject a terminal one with `target_terminal`. Use transcript reads for retained terminal output evidence.

## Vocabulary

| Term | Meaning |
| --- | --- |
| Session | A durable Serf conversation with transcript state. |
| Job | One asynchronous unit of work owned by a session. |
| Job type | The class of work: initially `shell` or `delegate`. |
| `job_id` | Durable opaque identifier for one job/turn. Use it for read, stop, watch, list, and notifications. |
| `delegate_id` | Durable opaque identifier for one delegate conversation. Use it for `delegate_send`. |
| `watch_id` | Durable opaque identifier for one watch configuration. Use it for `job_watch` inspect/clear. |
| Parent session | The session containing the job or turn that caused another session/job to exist. |
| Owner session | The session whose runtime/job manager owns the job. |
| Visible session | A session that may list/control the job because the job was forwarded, e.g. parent visibility into nested jobs. |
| Parent job | The job that caused this job to exist, for nested jobs. |
| Delegate session | The child Serf session used by a delegate job. |
| Transcript ref | A safe reference to a Serf session transcript, e.g. `local:<sessionID>`. |
| Notification | Metadata injected into a visible session to tell the agent that a job reached a lifecycle/progress event. |
| Output | Bounded textual/log content associated with a job. |

A **delegate job** is not the same as a **delegate conversation**. Each `delegate` invocation starts a new child conversation and a concrete delegate job/turn. Follow-up on that delegate conversation is performed through `delegate_send` against its `delegate_id`; if the delegate is idle and `on_idle="start"` is supplied, `delegate_send` creates a new job with a new `job_id` in the same delegate conversation. Observer sidecars use the normal result surface: `communicate(end_turn=true)` is the observer callback.

For a top-level job, `owner_session_id` and `visible_to_session_id` are the creating session; `parent_session_id` is omitted unless an implementation records a diagnostic lineage field. For a nested forwarded job, `owner_session_id` is the child/delegate session that owns the runtime, `visible_to_session_id` is the parent session that can see the forwarded record, `parent_session_id` identifies the session that caused the child session to exist when that lineage is recorded, and `parent_job_id` links to the delegate job that caused the nested job.

The lifecycle/output/notification contract is generic enough for future job types. The initial public job types are `shell` and `delegate`; clients must not assume type-specific fields exist unless the record's `type` is known.

## Design principles

1. **Generic jobs, not subagent-only handles.** Shell work and delegate/subagent work share lifecycle, output, notification, listing, and stopping infrastructure.
2. **Handles are purpose-specific.** `job_id` identifies a concrete unit of work; `delegate_id` identifies a delegate conversation; `watch_id` identifies a watch configuration. Session IDs and transcript refs identify transcripts, not job control targets.
3. **Defaults match common use.** Shell work is foreground by default with automatic promotion to durable background job on foreground wait timeout. Delegate work returns a `job_id` immediately by default because it is independent agentic work; callers use `max_wait_ms` to wait inline.
4. **Notifications replace waiting.** The parent should not poll or block just to discover completion.
5. **Output reads are non-consuming.** Reading job output never acknowledges, consumes, hides, or deletes a result.
6. **No model-facing ack.** Retention is automatic and policy-based.
7. **No model-facing kill.** `job_stop` is the single model-facing stop primitive; forceful cleanup is an implementation detail when needed.
8. **No automatic process resume after restart.** Durable job history survives; running processes do not have to. Serf must notify when a previously running job is discovered stopped/lost after restart.
9. **Nested shell jobs are supported; nested delegation is allowance-gated.** Subagents may start shell jobs. A subagent may itself delegate only when it was granted a non-zero `delegation_allowance`; a leaf delegate (allowance 0, the default) cannot delegate, so an observer sidecar started without an allowance still must not delegate. See the delegation-allowance amendment below.
10. **Delegate creation and follow-up are separate.** `delegate` starts a new delegate conversation; `delegate_send` follows up on an existing `delegate_id`.
11. **Watches are watcher-owned.** `job_watch` defines conditions over a source's output/events/progress; when a condition is met it delivers a bounded notification/frame to the watcher that created the watch.
12. **Observers are composed, not special.** An observer is a delegate granted `watch_parent:true`, a child-created `job_watch(source="parent")`, and an observer result through `communicate(end_turn=true)`.
13. **Transcript tools stay separate.** Job output is not a transcript. Delegate child transcripts remain readable through transcript tools.

### Delegation allowance (recursive delegation)

`delegate` accepts an optional `delegation_allowance` integer (default 0). The value follows the strict-zero rule used across the job-control surface — absent or 0 means a leaf delegate that cannot itself delegate, exactly today's behavior; there is no `minimum`/`maximum`/`default` keyword on the schema property.

**The grant rule.** A session may grant a child a `delegation_allowance` strictly less than its own allowance, so the chain always shortens and allowance 0 is a leaf. A grant `>=` the granter's own allowance is rejected with `invalid_request: delegation_allowance must be less than your own allowance (<A>); valid grants: <range>`, where `<A>` is the granter's allowance and `<range>` enumerates the grantable values (`0` at allowance 1, otherwise `0..<A-1>`). A session's own allowance rides its `spawnConfig` and its delegate restore descriptor, so it survives restore. The current allowance is also reported on every `job_list` result (see `job_list`), so an agent can read its budget without re-reading its system prompt.

**Availability matrix (allowance-gated).** Whether a child receives the delegation surface is governed by its granted allowance, not by a fixed depth gate. At allowance 0 the child is a leaf: it does not receive `delegate`/`job_watch`, agent-type listings that require those tools are filtered out of its prompt, and its system prompt shows the leaf limits block. At allowance > 0 the child receives `delegate` + `job_watch` (added to the default surface for an untyped child; a typed agent gets them only if its tool list names them), may grant onward allowances strictly smaller than its own, is told its allowance in its prompt, and sees the delegation + background-jobs prompt sections. A typed agent's tool list governs *what* the child gets; allowance governs *whether* the delegation tools are grantable at all — allowance never injects tools into a type that does not list them.

**Double opt-in (dark by default).** A root session's allowance equals `MaxSubagentDepth` (default 1). Under defaults the root's allowance is 1, so the root may grant only 0 — every delegate is a leaf and recursion never happens. Enabling recursion requires **both** raising `MaxSubagentDepth` in config **and** passing a non-zero `delegation_allowance` per spawn. Neither alone unlocks it; recursion stays dark until an operator deliberately does both.

## Job identity and visibility

Serf must mint `job_id` values that are globally unique enough to be used without string namespacing across all jobs visible to a session, including forwarded nested jobs. A parent-visible nested job uses the same opaque `job_id` in notifications, `job_list`, `job_status`, `job_watch`, `job_stop`, and the `job:<job_id>` transcript ref. Delegate jobs also expose a separate `delegate_id` for conversation follow-up.

The model-facing API must not expose two competing job handles such as `owner_job_id` and `parent_visible_job_id`. If an implementation internally maps child-owned IDs into parent-visible records, that mapping must be durable and invisible to the model-facing tools. The only accepted job handle is the parent-visible `job_id` returned or listed by Serf.

Job-control tools accept their purpose-specific handles, not `transcript_ref`. Transcript tools accept `transcript_ref`, not a bare `job_id` or `delegate_id`: the one job-derived lookup is `read_transcript`'s documented `job:<job_id>` ref, which is a transcript ref naming a job's output log. `delegate` creates a new conversation and does not accept any handle for continuation. `delegate_send` accepts `delegate_id`; `job_status` and `job_stop` accept concrete `job_id` handles; `job_watch(operation="create")` accepts a `source` (`self`, granted `parent`, or concrete `job_id`); `job_watch(operation="inspect"|"clear")` accepts `watch_id`.

## Job status and reason model

Canonical statuses:

| Status | Terminal? | Meaning | Normative reasons | Notification type |
| --- | --- | --- | --- | --- |
| `running` | no | Job has a live or believed-live runtime. | `awaiting_permission`, `stop_pending`, `foreground_timeout` | progress/match |
| `completed` | yes | Work succeeded. | `exit_zero` for shell; otherwise usually `null` | `completed` |
| `failed` | yes | A created job ran or attempted to run and failed. | `exit_nonzero`, `permission_denied`, `startup_failed` | `failed` |
| `cancelled` | yes | Serf intentionally stopped the job and confirmed cancellation. | `stopped_by_parent` | `cancelled` |
| `stopped` | yes | Work did not complete and Serf cannot attribute it to normal failure or confirmed cancellation. | `runtime_lost`, `stop_unconfirmed`, `supervision_lost`, `run_timeout` | `stopped` |

Validation, lookup, and routing errors are synchronous tool errors and do **not** create durable job records. Canonical synchronous errors include `invalid_request`, `permission_required`, `target_not_found`, `target_not_messageable`, `target_terminal`, `target_not_resumable`, `target_not_watchable`, `delegate_session_busy`, and `not_controllable`. If a `job_id` is returned, the job exists and must be listable and readable according to the durable job contract.

`status` is the primary machine branch field. `reason` is optional diagnostic metadata for a job that exists. The normative reason names above are the small portable subset whose behavior this contract defines because they affect recovery, stopping, runtime loss, timeout handling, approval/blocking state, or shell exit interpretation. Tool descriptions must say this plainly: agents should branch on `status` first, and consult `reason` only for documented operational cases such as `runtime_lost`, `run_timeout`, `awaiting_permission`, and `stop_pending`, or when summarizing diagnostics to the user. Implementations may attach additional diagnostic text or implementation-specific reason values, but agents should not need those values for ordinary control flow.

This deliberately differs from the current subagent-only output simplification: generic shell/delegate jobs need a small lifecycle diagnostic vocabulary for runtime loss, stop confirmation, and process exit cases. The vocabulary should stay small; do not add closed-enum reasons for diagnostics that can be represented as free-text `diagnostic` or `error` fields.

`cancelled` is for intentional, confirmed stop. `stopped` is for supervision/runtime loss, runtime timeout, or unconfirmed stop. Restart reconciliation uses `stopped` with reason `runtime_lost`; it means Serf restored durable state and found no live runtime for a previously running job. `supervision_lost` means the supervising owner runtime ended or became unable to supervise a nested job while Serf was otherwise live. Neither is command failure. `not_controllable` is a synchronous routing/control tool error when the owner runtime is believed live but rejects or cannot perform the requested operation; it is not a terminal job status reason.

```mermaid
stateDiagram-v2
    [*] --> running: job_started
    running --> completed: success / exit 0
    running --> failed: error / exit nonzero / denied
    running --> cancelled: job_stop confirmed
    running --> stopped: runtime_lost / stop_unconfirmed / run_timeout
    completed --> [*]
    failed --> [*]
    cancelled --> [*]
    stopped --> [*]
```

## Model-facing tools

### Existing shell/bash tool

The existing shell/bash tool becomes job-capable. It is the shell job creation surface; there is no separate `shell_job` tool.

Canonical foreground shape for ordinary commands:

```json
{
  "command": "make test",
  "description": "run tests"
}
```

Shell defaults to foreground because most shell calls are short and decision-producing. Omit `background` (or set it to `false`) for ordinary commands whose output determines the next step — the call waits up to the session command timeout (120s standard), then promotes the command to a durable background job if it is still running. Set `background: true` to launch-and-return immediately for deliberate background work such as a dev server or a long command the agent should not wait on.

Launch-and-return (immediate background) shape:

```json
{
  "command": "npm run dev",
  "background": true,
  "description": "start dev server"
}
```

Foreground with an explicit process-runtime cap:

```json
{
  "command": "go test ./...",
  "max_runtime_ms": 900000,
  "description": "run tests"
}
```

Defaults and timeout semantics:

- `background` unset (or `false`): the call runs the command in the foreground and waits up to the session command timeout (120s in stock provider profiles) for it to finish.
- A foreground shell command that completes within that wait returns inline output and is ephemeral by default: no durable `job_id` is required and it does not appear in `job_list`.
- A foreground shell command still running at the session command timeout is promoted to a durable background job: Serf returns the current bounded output/status with a `job_id`, and the normal terminal notification remains armed for the eventual terminal state.
- `background: true`: Serf starts the command and returns a `job_id` immediately without waiting. The terminal notification fires when it finishes.
- `max_runtime_ms` is an optional process runtime limit for shell jobs. If the process is still running after `max_runtime_ms`, Serf stops it and finalizes the job as `stopped` with reason `run_timeout`. It bounds how long the process may *run*, distinct from the foreground wait.
- Omitted `max_runtime_ms` means implementation-defined shell runtime policy. Recommended policy: default finite runtime for foreground/promoted shell jobs, no default runtime limit for `background: true` shell jobs unless configured by the user/tool call.
- A shell command that completes before the tool returns does not inject a terminal notification; the terminal result is already in the tool result. Return field `timed_out`, when present, means the foreground wait expired; it never means the process hit `max_runtime_ms`.
- To learn when a launch-and-return command reaches a state (e.g. a server printing "ready"), use `job_watch(operation="create", source=<job_id>, output_match=...)` and continue from the notification — an output-match watch catch-up-scans a job's retained output even if it already finished.

Normative foreground-wait bounds:

- The foreground wait is the session command timeout (`DefaultCommandTimeoutMS`, 120000 in stock provider profiles), clamped to `MaxCommandTimeoutMS`. Shell has no per-call wait parameter; `background` chooses foreground (wait, then promote) vs. immediate return.

Normative runtime timeout bounds:

- `max_runtime_ms` is the process-killing deadline and must be distinct from the foreground wait (the session command timeout), which only bounds how long the call waits before promoting to background.
- Minimum positive `max_runtime_ms`: `1000`.
- Implementations must document default/max/clamp behavior for `max_runtime_ms`.
- Negative values fail `invalid_request`.

Ephemeral foreground terminal return shape:

```json
{
  "type": "shell",
  "status": "completed",
  "reason": "exit_zero",
  "running_in_background": false,
  "timed_out": false,
  "exit_code": 0,
  "output": "bounded output text",
  "truncated": false
}
```

Explicit background return shape:

```json
{
  "job_id": "job_...",
  "type": "shell",
  "status": "running",
  "reason": null,
  "running_in_background": true,
  "timed_out": false
}
```

Foreground timeout / promotion return shape:

```json
{
  "job_id": "job_...",
  "type": "shell",
  "status": "running",
  "reason": "foreground_timeout",
  "running_in_background": true,
  "timed_out": true,
  "output": "bounded output text",
  "truncated": false
}
```

Shell approval is not fully designed here. If policy requires approval before a shell command may start, Serf must not execute before approval. The target contract permits either:

1. fail job creation synchronously with reason `permission_required` if no async approval flow is available; or
2. create a durable background job in `running` with reason `awaiting_permission`, then continue after approval or finalize as `failed`/`permission_denied`.

Whichever behavior an implementation chooses must be reflected consistently in `job_list`, `job_status`, the job output read, and notifications.

### `delegate`

`delegate` starts a new delegate/subagent conversation and job. It does not resume or steer an existing delegate conversation. Follow-up on an existing delegate is handled by `delegate_send` using the returned `delegate_id`. To start an observer sidecar, set `watch_parent:true`; the child can then observe its immediate parent with `job_watch(source="parent")` and report through `communicate(end_turn=true)`.

Canonical background shape:

```json
{
  "task": "Investigate the failing parser test and report findings.",
  "agent_type": "explorer",
  "model": "openai/gpt-5.5",
  "reasoning_effort": "high"
}
```

Full target shape:

```json
{
  "task": "Investigate the failing parser test and report findings.",
  "agent_type": "explorer",
  "model": "openai/gpt-5.5",
  "reasoning_effort": "high",
  "watch_parent": false,
  "max_wait_ms": 120000,
  "result_schema": {
    "type": "object",
    "properties": {
      "summary": {"type": "string"},
      "files": {"type": "array", "items": {"type": "string"}}
    },
    "required": ["summary"]
  }
}
```

Defaults:

- `max_wait_ms` unset (or `0`) means return the `job_id` immediately without waiting for the delegate to finish.
- Each `delegate` call creates a new delegate job and a new child session.
- `delegate` does not accept `target`, `mode`, `job_id`, `delegate_id`, or `transcript_ref` for continuation.
- `watch_parent:true` grants only the spawned child permission to observe the immediate parent with `job_watch(source="parent")`. It is not transitive and does not grant `delegate`.
- With a positive `max_wait_ms`, the tool performs one bounded foreground wait of up to that many ms; timeout leaves the delegate job running in the background with reason `foreground_timeout`.
- Delegates have no model-facing `max_runtime_ms` in v1. Delegate runtime limits, if any, are implementation policy rather than a tool argument.
- `result_schema`, when supplied, is a JSON Schema-like contract for the initial delegate final result and for resumed turns in the same delegate conversation. The delegate output remains readable as prose/log text, and Serf validates and surfaces a structured result when possible. When validation or capture fails for a schema-backed delegate result, Serf omits `structured_result`, sets `structured_result_valid:false`, and includes a machine-readable `structured_result_reason`.
- Delegate interaction is turn-based in this target contract. A delegate that needs more input should finish with a request for that input; the parent follows up with `delegate_send`. Mid-turn interactive input/awaiting-input notifications are not a v1 guarantee. Delegate `status="completed"` means the delegate turn ended normally; it does not assert that the requested task succeeded. Agents must inspect `output`, `structured_result`, or task-specific schema fields for task success/failure.
- A foreground delegate turn that installs watches and reports readiness is still a completed delegate job, but its result includes `watching:true` and `watches`. That means the watch remains active for the delegate conversation and later matching frames will resume it.

Background return shape:

```json
{
  "delegate_id": "dlg_...",
  "started_job_id": "job_...",
  "job_id": "job_...",
  "latest_job_id": "job_...",
  "type": "delegate",
  "status": "running",
  "reason": null,
  "running_in_background": true,
  "timed_out": false,
  "transcript_ref": "local:01JCHILD..."
}
```

Foreground terminal return shape:

```json
{
  "delegate_id": "dlg_...",
  "started_job_id": "job_...",
  "job_id": "job_...",
  "latest_job_id": "job_...",
  "type": "delegate",
  "status": "completed",
  "reason": null,
  "running_in_background": false,
  "timed_out": false,
  "transcript_ref": "local:01JCHILD...",
  "output": "bounded final report or invocation output",
  "truncated": false,
  "structured_result": {"summary": "...", "files": ["parser.go"]},
  "structured_result_valid": true,
  "watching": true,
  "watches": [
    {
      "id": "watch_...",
      "source": "parent",
      "condition": "assistant.tool where tool_name=read_file,status=ok",
      "deliveries": 0,
      "created_at": "2026-06-22T05:41:25Z"
    }
  ]
}
```

Foreground timeout return shape:

```json
{
  "delegate_id": "dlg_...",
  "started_job_id": "job_...",
  "job_id": "job_...",
  "latest_job_id": "job_...",
  "type": "delegate",
  "status": "running",
  "reason": "foreground_timeout",
  "running_in_background": true,
  "timed_out": true,
  "transcript_ref": "local:01JCHILD...",
  "output": "bounded output so far",
  "truncated": false
}
```

`transcript_ref` is included once the child session is known. If the implementation cannot know it at creation time, it must persist and expose it later via `job_list` and terminal notification.

### `delegate_send`

`delegate_send` sends a message to one of your durable child delegate conversations by `delegate_id`. It is the public follow-up surface for delegates. It is not the observer-to-parent callback surface; observers report through `communicate(end_turn=true)`.

Target shape:

```json
{
  "to": "dlg_prior_or_running_delegate",
  "message": "Check whether the serializer has the same issue.",
  "on_idle": "start",
  "max_wait_ms": 120000
}
```

Core target resolution:

| Target | Meaning |
| --- | --- |
| `dlg_...` | A durable delegate conversation owned by this session. |

`delegate_send` rejects `job_id` handles because they identify concrete turns, not delegate conversations. It also rejects `transcript_ref` handles and runtime/legacy aliases such as `caller`, `main`, or `watched`. Ordinary delegate follow-up should target a `delegate_id`.

Semantics:

- If `to` identifies a running or currently-driven delegate, Serf injects the message into that active run, returns the current `job_id`, and does not create another terminal notification. The return `action` is `steered`.
- If `to` identifies an idle delegate and `on_idle="fail"` or is omitted, the call fails synchronously with `target_idle`. This default prevents accidental restarts.
- If `to` identifies an idle/resumable delegate and `on_idle="start"`, Serf creates the delegate's next job in the same delegate conversation, with a new `job_id`. The return `action` is `started`.
- If `to` is `caller`, a `job_id`, `transcript_ref`, unknown delegate, unauthorized delegate, non-resumable delegate, or legacy alias, the call fails synchronously without creating a job record. A `job_id` error should guide the caller to the corresponding `delegate_id` when Serf can resolve it.
- If another job is already running in the same delegate conversation and the target is not that running job, the call fails synchronously with `delegate_session_busy` unless an implementation explicitly supports concurrent child turns.
- Target state is resolved atomically at delivery time. A race between terminal/running state and the tool call is resolved by the observed state at delivery.
- `on_idle` defaults to `fail`. Allowed values are `start` and `fail`.
- `max_wait_ms` unset (or `0`) for a started delegate returns the new `job_id` immediately without waiting. With a positive `max_wait_ms`, the started delegate performs one bounded foreground wait; if that wait expires, the new job remains running in the background with reason `foreground_timeout`. Sending to a running delegate returns promptly regardless of `max_wait_ms`. When a positive `max_wait_ms` is supplied to a live steer (which cannot honor it), the return carries `wait_ignored_reason` so the caller does not mistake delivery for a reply. There is no "steer and wait for the next reply" mode; let the live steer's turn finish (you are notified) and read its output, or start the next delegate turn once it is idle.
- `on_idle` and `max_wait_ms` apply to concrete delegate targets.

Watch-origin parent callbacks use `communicate(end_turn=true)` from the observer. Internal runtime plumbing may still steer a parent session, but `caller` is not a public `delegate_send` target.

```mermaid
stateDiagram-v2
    [*] --> ValidateTarget: delegate_send(to)
    ValidateTarget --> Error: unknown / unauthorized / wrong handle
    ValidateTarget --> TargetRunning: delegate running
    ValidateTarget --> TargetIdle: delegate idle
    TargetRunning --> MessageSameJob: inject guidance
    TargetIdle --> NewJobSameSession: on_idle=start + resumable
    TargetIdle --> Error: on_idle omitted/fail / not_resumable / session_busy
    MessageSameJob --> [*]
    NewJobSameSession --> [*]
    Error --> [*]
```

Return shape when messaging a running target:

```json
{
  "delegate_id": "dlg_...",
  "current_job_id": "job_...",
  "latest_job_id": "job_...",
  "type": "delegate",
  "status": "running",
  "reason": null,
  "running_in_background": true,
  "timed_out": false,
  "action": "steered",
  "transcript_ref": "local:01JCHILD..."
}
```

Return shape when starting an idle delegate's next job:

```json
{
  "delegate_id": "dlg_...",
  "started_job_id": "job_new...",
  "current_job_id": "job_new...",
  "latest_job_id": "job_new...",
  "type": "delegate",
  "status": "running",
  "reason": null,
  "running_in_background": true,
  "timed_out": false,
  "action": "started",
  "transcript_ref": "local:01JCHILD..."
}
```

### `job_watch`

`job_watch` creates, lists, inspects, or clears standing triggers. A watch is
owned by the session that creates it. Create names the observed `source`; when a
condition matches, Serf delivers a bounded notification/frame back to that
watcher. There is no model-facing `send` object.

Operations:

- `operation="create"` requires `source`.
- `operation="list"` takes no source or watch ID.
- `operation="inspect"` requires `watch_id`.
- `operation="clear"` requires `watch_id`.

Sources:

| Source | Meaning | Authorization |
| --- | --- | --- |
| `self` | This session's public events | Always allowed, subject to loop guards |
| `parent` | Immediate parent session's public events | Only inside a child spawned with `delegate(watch_parent:true)` |
| `job_...` | A concrete job's output/progress/events | Allowed when the job is owned by this session or a live descendant session |

`job_watch` is not a completion subscription: terminal
completion/failure/cancellation/stopped notifications are automatic for
notification-armed jobs.

Output-match shape:

```json
{
  "operation": "create",
  "source": "job_...",
  "output_match": "(?i)(ready|blocked|needs input)"
}
```

Periodic-progress shape:

```json
{
  "operation": "create",
  "source": "job_...",
  "progress_interval_ms": 300000
}
```

Filtered parent observer shape:

```json
{
  "operation": "create",
  "source": "parent",
  "events": ["assistant.tool"],
  "event_filter": {
    "tool_name": "read_file",
    "status": "ok"
  }
}
```

Clear an existing watch:

```json
{
  "operation": "clear",
  "watch_id": "watch_..."
}
```

Trigger fields:

- `output_match` is level-triggered: it fires once at attach if the job's already-retained output contains a match, then again as appended output matches the regex. It requires a concrete job source; `output_match` on a session source (`self` or `parent`) fails `invalid_request`.
- `progress_interval_ms` fires periodically with bounded progress/excerpt metadata even if no match occurred. It is a separate progress trigger, not an event-frame modifier.
- `events` selects session/job event kinds to include in the watch frame. `events: ["*"]` means all visible event kinds allowed by caller permissions and filtering. Event kind names are implementation-defined but must be discoverable by the model; the shipped vocabulary is `assistant.tool`, `communicate`, and `job.notification`. Plain assistant prose is an internal transcript/UI event and is not watchable through `job_watch`.
- `event_filter` narrows event watches before any delivery is recorded. In v1 it applies only to `events: ["assistant.tool"]` and supports exact `tool_name` plus `status` (`"ok"` or `"error"`). Non-matching events do not create a delivery, pending row, notification, or observer wake. Assistant-tool frames include the resulting `status` plus the original tool `arguments_json`, so an observer can usually decide from the delivered frame before using audit tools.
- `every` gates event delivery: `every: N` fires on each Nth occurrence of the watched event kind, for example `events: ["communicate"], every: 3` for every third result-tool message. `every: 1` is the semantic default and reads as unset, whatever `events` contains. `every > 1` is valid only when `events` names exactly one concrete kind; supplying it with zero, multiple, or wildcard (`"*"`) kinds fails `invalid_request`.
- Session event watches that set `events` use `event_filter` and optional `every` for precision. Combining session `events` with `progress_interval_ms` fails `invalid_request` because periodic progress is a different trigger mode.

For cross-session session sources such as `parent`, omitting trigger fields means
deliver all bounded public watch frames for that source. For concrete jobs and
`self`, create still requires a meaningful output/event/progress condition.

Observer/sidecar pattern:

```text
1. Parent starts a child with delegate(watch_parent:true).
2. The child creates job_watch(operation="create", source="parent", ...).
3. Matching parent frames are delivered to that child.
4. The child reports useful findings with communicate(end_turn=true).
5. The parent treats that communicate as the observer callback and finishes from it.
```

Observer sidecars are composition, not a separate model-facing observer tool.
Use `delegate_send(to=<delegate_id>)` only for parent-to-child follow-up.

Practical observer guidance for agents:

- Start the observer with `watch_parent:true` when it needs to observe the parent.
- Inside the observer, call `job_watch(source:"parent")`; omit trigger fields for the default bounded parent frame stream, or add `events`/`event_filter` for precision.
- When the readiness delegate result carries `watching:true` and `watches`, treat the observer as watching and perform the planned watched action.
- Report readiness or continuing status with `communicate(end_turn=false)` only when the observer will continue working in that same turn.
- Report the observer result with `communicate(end_turn=true)`. That terminal communicate is the callback to the parent. The parent receives it as an "Observer callback" block containing the observer message and canonical output envelope.
- Keep observer instructions narrow and frame-driven. Tell the observer what frame fields to read (`watch_id`, `delivery_id`, `job_id`, event kind/status/arguments, and optional excerpt), what action to take, and when to stop.
- A watch-origin observer job that has already sent its terminal `communicate(end_turn=true)` records terminal state without adding another owner notification for the same job. Other terminal job notifications remain lifecycle confirmations, not additional watch frames. Clear long-lived session watches before continuing a free-form conversation when later acknowledgements should not themselves be observed.

Rules:

- Every notification-armed background job emits one terminal notification when Serf observes or reconstructs terminal state, subject to durable duplicate suppression. `job_watch` is unrelated to that terminal notification.
- `job_watch` only adds extra notifications or frames while the watched source is active/visible.
- For an already-terminal concrete job that still has retained output, an `output_match`-only `job_watch` (no `events` or `progress_interval_ms`) performs a one-shot catch-up scan of that retained output instead of installing a live watch: it returns `terminal_catchup=true` with `watching=false`, `fired=true` and a frame/notification on a match or `fired=false` on none, and the terminal `status`. Any other condition on a terminal target still fails synchronously with `target_terminal` (nothing can ever fire). Unknown or no-longer-retained concrete job targets still fail synchronously with `target_not_found`.
- `job_watch(operation="clear", watch_id=...)` does not stop the watched job. Clearing is by `watch_id`, is idempotent, and returns a no-op success (`watching:false`) when the watch is already inactive, unknown, or was auto-removed because the source reached terminal state. The caller never has to reconstruct the original source/condition identity.
- `job_watch` fails synchronously with `source_not_watchable` or `target_not_watchable` for sources the caller is not allowed to observe. A concrete descendant job can be watched by forwarding the watch install to the live owning descendant while keeping the ancestor as receiver. Parent, sibling, unrelated, closed, or non-live sources are not watchable unless explicitly granted.
- A watcher may observe the events it generates itself. A `self` source on a self-generated kind (`assistant.tool`, `communicate`, including via `["*"]`), delivering back to the same session, installs and returns `watching:true`; nothing is rejected at creation for being a potential feedback loop. The loop is bounded at runtime instead, by three mechanisms: a self-influenced frame is prefixed with a gradient notice telling the receiving turn how deep it stands in its own influence (`↳ this turn responded to your last message.`, sharpening to `↳ you're ~N exchanges deep responding to your own influence — consider disengaging.`); a send descending from too many delivered self-influenced priors is dropped as `runaway` instead of delivered; and the per-watch delivery budget below auto-clears a watch that keeps firing. Parent-source sidecars are cross-session and are not self-influenced by this rule at all.
- Watches expire automatically when their concrete watched job reaches terminal state. Session-level watches remain active until their configured scope ends, the session/job manager closes, or retention policy removes them.
- A structured terminal job notification always names `read_transcript(transcript_ref="job:<job_id>")`. Other frames do not advertise a job read. This is a reference, not an access grant: exact local `job:` reads follow the transcript reader's resolution rules, while `job_list`, `job_status`, `job_stop`, and delivery controls retain their existing scope.
- `SessionMeta.ObservedBy` is append-only, deduplicated UI metadata for hub auto-open. A worker is never recorded as observing itself; metadata failures never change or delay delivery.
- Watches are not required to survive Serf process restart unless an implementation explicitly marks them durable.
- Already-fired parent-watch frames are durable until delivered, replaced by a newer frame for the same durable key, evicted by watch cleanup, or dropped with a caller-visible diagnostic on hard/non-resumable failure. The durable key includes the `watch_id`, visible session, configured watch source/target, receiver identity, resolved watched identity, and watch generation.
- `job_watch(operation="clear", watch_id=...)` is the model-facing unwatch operation; there is no separate unwatch tool.
- There is at most one active watch configuration per `(watcher_session_id, source identity, receiver identity, condition hash)` unless an implementation documents additive watches. A duplicate call with the same configuration is idempotent. A different call replaces the previous configuration for that key, and the return value must make replacement explicit with `replaced_existing=true`.
- `output_match` is a Go/RE2 regular expression over the watched job's retained output at attach and then output appended while the watch is active.
- Regex matching is case-sensitive by default; use inline flags such as `(?i)` or `(?i:plugh)` for case-insensitive matching.
- Go/RE2 syntax is leftmost-first and excludes backreferences/lookaround. `.` does not match newline unless `(?s)` is used; use `(?m)` for multiline `^`/`$` behavior.
- Invalid regexes fail synchronously at watch creation time.
- For the retained output present at attach and for bytes successfully appended while a watch is active, Serf must not silently miss a regex match because of preview-window eviction. The no-silent-miss guarantee extends to the attach scan: a token already retained at attach, or one straddling the attach boundary, must still match. Implementations may use line-buffered append-stream matching, chunk-overlap matching, or another mechanism, but the contract is no silent miss for retained/appended watched output.
- Event frames and output excerpts are bounded and filtered before notification or observer delivery. Implementations may apply redaction/scrubbing for cross-session or observer delivery, but this contract does not promise perfect secret detection; callers must not treat frames as guaranteed secret-free.
- Default `progress_interval_ms` is absent/no periodic progress wake-up. If supplied, minimum is `1000`, maximum is `3600000`, and omitted/`0` means no periodic progress notification. Negative values fail `invalid_request`. Session event watches use `events`/`event_filter` instead of combining events with periodic progress.
- Match/event/progress notifications are batched/throttled. Multiple triggers may be coalesced. For parent-watch observer frames, coalescing is latest-frame-wins by durable key and must not turn a matched condition into silence: Serf either delivers the current pending frame, replaces it with a newer pending frame for the same key, or emits a caller-visible diagnostic for hard failure.
- Each watch configuration has a model-facing delivery budget of 50 (watch notifications plus observer frames, the count `job_list` reports per watch). A watch that exhausts its budget is auto-cleared with one final notification telling the caller to re-arm with a tighter condition (higher `every`, narrower `output_match`, or longer `progress_interval_ms`).
- Terminal notification ordering: flush any queued watch notification/frame for a concrete job, then deliver the terminal notification.
- If no watch condition is supplied (`output_match`, `events`, or `progress_interval_ms`) for `operation="create"`, the tool fails unless the source is a granted cross-session session source such as `parent`, where the default is the bounded public frame stream.

Return shape:

```json
{
  "watch_id": "watch_...",
  "source": "job_...",
  "watching": true,
  "output_match": "(?i)(ready|blocked|needs input)",
  "events": ["communicate", "assistant.tool", "job.notification"],
  "progress_interval_ms": 300000,
  "replaced_existing": false,
  "fired": false
}
```

`replaced_existing` and `fired` are always present, explicitly `false` when they did not happen — `fired` is `true` only for an attach scan or terminal catch-up that matched (§7.1).

```mermaid
stateDiagram-v2
    [*] --> Unwatched
    Unwatched --> Watched: job_watch(operation=create, source, condition)
    Watched --> Watched: duplicate idempotent / replacement update
    Watched --> ConditionMet: output / event / progress trigger
    ConditionMet --> NotifyWatcher: deliver bounded frame/notification
    NotifyWatcher --> Watched: delivered/coalesced
    Watched --> Expired: concrete job terminal / scope ends
    Expired --> [*]
```

### `job_status`

`job_status` inspects one durable job by `job_id`. It is the orientation read: what the job is, where it is in its lifecycle, how long it has been running or quiet, and the `transcript_ref` that names its evidence. It carries no output bytes; those are read separately (below).

Canonical return shape:

```json
{
  "job_id": "job_...",
  "kind": "shell",
  "status": "running",
  "phase": "process_running",
  "reason": null,
  "resumable": null,
  "running_for_ms": 12000,
  "quiet_for_ms": 4000,
  "started_at": "...",
  "ended_at": null,
  "last_event_at": "...",
  "transcript_ref": "job:job_...",
  "exit_code": null
}
```

Canonical behavior:

- `kind` is the public job class: `shell` or `agent` (a delegate job's kind is `agent`; `type` in durable records stays `delegate`).
- `phase` is the observable sub-state of a RUNNING job (`starting`, `awaiting_model`, `model_streaming`, `tool_running`, `process_running`). A terminal job omits `phase` and reports `duration_ms` instead of `running_for_ms`/`quiet_for_ms`.
- `last_event_at` is the most recent parent-observable activity for the job (an output append, or the job's start when nothing newer is observable; for a terminal record with no live stamp it falls back to `ended_at`, then `started_at`). It is a supervision hint for spotting a stalled or quiet job; it is not a substitute for terminal notifications.
- `transcript_ref` is the delegate conversation's session ref for a delegate job, and `job:<job_id>` for a shell job. Hand it to `read_transcript`.
- Completion is notification-driven. `job_status` is for orientation and recovery, not for waiting: it never blocks, and calling it in a loop is the polling anti-pattern.
- A `job_id` the caller cannot resolve fails `job "job_..." not found — use job_list to see this session's jobs`.

### Reading job output

Job output is read with the generic transcript reader: `read_transcript(transcript_ref="job:<job_id>")`. There is no separate job-output tool. One reader serves both shell logs and delegate reports, and the same `job:` ref appears in `job_status`/`job_list` rows and in terminal notifications.

Canonical target shape:

```json
{
  "transcript_ref": "job:job_...",
  "format": "markdown"
}
```

Canonical behavior:

- The read is a snapshot. It never waits, so there is no `max_wait_ms` and no way to turn a read into a wait; terminal notifications and `job_watch` are how conditions reach a caller.
- `markdown` is the only supported format for a `job:` ref; any other value fails. The session-transcript parameters — `range`, `expand_turn`, `offset_bytes`, `max_bytes` — are rejected with `invalid_request: <name> applies only to session transcript refs`. There is no line paging, no head/tail selection, and no `grep`: use `job_watch(output_match=...)` to be told when watched output matches.
- Reads are non-consuming and non-acknowledging.
- The returned envelope carries `transcript_ref`, `format`, `content_type`, the rendered `content`, and `meta` (`turns_total`/`turns_rendered` 1, `range: "shell-log"`, `truncated`).
- `content` for a shell job is a `# Shell Job <job_id>` heading, then `- status:`, `- command:`, `- total_bytes:` (the job's lifetime output byte count), `- dropped_bytes:` when bytes were permanently evicted past the retention cap, then the retained output in a fenced block.
- `content` for a delegate job is a `# Delegate Job <job_id>` heading, then `- status:`, `- reason:`, `- task:`, `- total_bytes:`, optional `- dropped_bytes:`, the report in a fenced block, and — when the invocation captured one — a trailing `structured_result (valid=<bool>): <json>` line plus `structured_result_reason:` when the record carries one. A schema-backed result that is invalid, missing, too large to persist, or uncapturable renders no `structured_result` line and reports its `structured_result_reason` instead. Resumed delegate turns inherit the original delegate conversation's `result_schema`.
- A delegate `job:` read deliberately omits the delegate's own session `transcript_ref`: session refs are not access-controlled. Read the delegate's conversation through the ref `job_status`/`job_list` reports, which only its owner can get.
- Delegate job output is the parent-visible execution log for that invocation: the final user-facing report or terminal error/cancellation diagnostic, and it may include streamed assistant text, tool-use summaries, permission/status diagnostics, and nested job notifications. It is not the complete child transcript.
- The window is the tail of retained output, and the envelope is bounded. An oversized render keeps a head and a tail with an elision marker between them (`… N characters elided from this oversized turn; additional output is not available from this transcript view …`) and sets `meta.truncated`. That is a rendering bound, not an eviction — only `- dropped_bytes:` means bytes are gone.
- The read must work for terminal durable jobs after the live runtime is gone, as long as the job record and output file are retained.

Resolution order for a `job:` ref — the same chain the runtime walks for any job read, so reachability cannot drift between surfaces:

1. the caller's own job store;
2. a live direct child's store (one-hop);
3. a live descendant at depth ≥ 2, resolved through the recursive owner path;
4. otherwise the error the earlier steps produced.

### `job_list`

`job_list` returns durable job records for the current session, optionally filtered.

Target shape:

```json
{
  "status": ["running", "completed", "failed", "cancelled", "stopped"],
  "type": ["shell", "delegate"],
  "limit": 50,
  "include_nested": false
}
```

Rules:

- Default `include_nested=false`.
- Default `limit=50`; maximum `limit=100`. Omitted uses the default. Values above the maximum are clamped downward. Values `<=0` fail `invalid_request`.
- Results are sorted by `started_at` descending, tie-broken by `job_id`.
- `job_list` is authoritative durable inventory for the visible session. Use it to recover known jobs, find `job_id`s, inspect durable state, or include nested jobs. Do not call it repeatedly to wait for completion. For completion, rely on automatic terminal notifications; for output, read the relevant job's `job:<job_id>` ref once.
- The owning session can list its own jobs.
- A parent session may list nested jobs owned by delegate child sessions only when those jobs have been forwarded into parent-visible durable job records.
- Delegate records include `delegate_id`, `current_job_id`, `latest_job_id`, `transcript_ref`, `resumable`, and optional `not_resumable_reason`. `delegate_send(to=..., on_idle="start")` uses the same resumability contract when the target delegate is idle. After restart reconciliation, `stopped/runtime_lost` delegate jobs are resumable from retained transcript/session state when strict restore preflight passes. If required retained state is missing, pruned, inconsistent, or otherwise fails strict preflight, Serf reports the delegate as not resumable and follow-up fails synchronously with `target_not_resumable`.
- Most agents should ignore session identity fields and use only `job_id`, `status`, `type`, `reason`, `transcript_ref`, and output metadata. Session fields are for diagnostics and nested/routed job visibility.

Good inventory example:

```json
{
  "status": ["running"],
  "limit": 20
}
```

Bad polling anti-example:

```text
Bad: job_list -> job_list -> job_list waiting for a job to finish.
```

Return shape:

```json
{
  "jobs": [
    {
      "job_id": "job_...",
      "type": "delegate",
      "status": "running",
      "description": "Investigate parser test",
      "owner_session_id": "01JPARENT...",
      "transcript_ref": "local:01JCHILD...",
      "resumable": true,
      "started_at": "...",
      "last_activity": "...",
      "total_bytes": 1234
    }
  ],
  "count": 1,
  "watches": [
    {
      "id": "watch_1",
      "source": "job_...",
      "condition": "output_match: ready",
      "deliveries": 0,
      "created_at": "..."
    }
  ],
  "recent_watches": [
    {
      "id": "watch_0",
      "source": "job_...",
      "condition": "output_match: ready",
      "deliveries": 2,
      "end_reason": "auto_removed_terminal",
      "ended_at": "..."
    }
  ],
  "delegation_allowance": 2
}
```

Rows are lean for scanning: a field that is null/absent for a job is omitted (a running shell row carries no `reason`, `exit_code`, `ended_at`, `transcript_ref`, or resumability keys), `visible_to_session_id` is internal routing and not emitted, and `owner_session_id` is only interesting under tree walks though it is always present. Detail beyond the scan comes from `job_status` (lifecycle specifics) and a `job:<job_id>` transcript read (the output).

- `delegation_allowance` reports the calling session's current recursive-delegation budget: the largest value it may grant a child is one less (see Delegation allowance). It is omitted when `<= 1` (a leaf with no `delegate` tool, or a budget that can only grant `0` — a no-op knob) and present when the session can actually fan out, so an agent sees a meaningful budget without re-reading its system prompt.
- `watches` enumerates the session's currently active watch configurations (the same set `job_watch` installs), so an agent can re-orient on what it is already watching without re-deriving it. Each entry carries a stable `id` (preserved across an idempotent re-configure; a replacement gets a fresh `id`), the public `source`, a one-line `condition` summary of the watch's trigger (`output_match`, `progress_interval_ms`, or `events` with an optional `every N`), `deliveries` (model-facing deliveries so far against the per-watch budget), and `created_at`. Receiver-owned watches are visible to the receiver, not to the descendant manager that physically observes the source. Drain-only residue from already-terminal watched jobs is not listed. `watches` rides with the result when non-empty (omitted from the lean scan when there are none); it is not subject to the job list's size bounding.
- `recent_watches` is a bounded, latest-first ring of watches that have left the active set, so a watch that fired and then disappeared stays legible (it is not a watch vanishing into ambiguity). Each entry carries the same `id`/`source`/`condition`/`deliveries` plus `end_reason` — `auto_removed_terminal` (the watched job went terminal), `cleared` (`job_watch(operation="clear", watch_id=...)`), `replaced` (a different configuration superseded it), or `budget_exhausted` (it hit the per-watch delivery budget) — and `ended_at`. Combined with `deliveries`, this distinguishes a watch that fired before it was removed from one that never fired, and both from a watch that was never installed (absent from both lists). Receiver-owned history is visible to the receiver, not the physical source owner. It is omitted from the lean scan when empty. The ring is a debugging aid, not a durable audit log, and does not survive process restart.

`description` is optional display metadata. For shell jobs it comes from the shell tool's `description` argument. Delegate jobs have no separate `description` argument in v1; implementations may derive a display label from the delegate `task` or leave `description` empty while retaining `task` in durable storage.

`last_activity` is the most recent parent-observable activity timestamp for the job, derived exactly like `job_status`'s `last_event_at` (an output append, or the job's start when nothing newer is observable; for a terminal record with no live stamp it falls back to `ended_at`, then `started_at`). A running delegate that is working silently stays at its `started_at` until it appends output, which is the signal the quiet-job watchdog (see Notifications) acts on. A per-action "current action" field is intentionally not provided: a running delegate's mid-run action is not cheaply readable from parent-side state.

`total_bytes` is the job's lifetime output byte count: the live so-far count for a running job, the final count once terminal. It carries the same name in the shell result and in a `job:` transcript read's `- total_bytes:` line, so the count is identical across every surface.

`command` is the shell command line for a shell job, so a row is identifiable without opening the transcript when `description` is sparse. It is omitted for delegate jobs (which have no command).

`exit_code` is the process's own exit status only for a job that exited on its own (`completed`/`failed`). A `cancelled`, `stopped`, or `run_timeout` job was signalled rather than exiting cleanly, so it has no real exit status: `exit_code` is `-1` (a sentinel, not a shell code). Interpret a non-`completed` job from its `status` + `reason`, never from `exit_code`.

`job_list` returns a collection. Control/read tools operate on one `job_id`.

### `job_stop`

`job_stop` requests cancellation of a running job. Use it only when the desired outcome is to stop work. Do not call it after completion, to acknowledge a result, to hide notifications, to delete history, or to free ordinary retained output.

Target shape:

```json
{
  "job_id": "job_...",
  "max_wait_ms": 5000,
  "include_children": false
}
```

Semantics:

- `max_wait_ms` unset (or `0`): the call returns promptly after the stop request is signalled, with whatever status the stop has reached. With a positive `max_wait_ms`, the tool performs one bounded wait of up to that many ms for the stop to finalize.
- `max_wait_ms` is the caller-visible wait budget after Serf sends the stop request. It is not a runtime limit and does not delete the job if it expires. Default `max_wait_ms` for `job_stop` when positive is `5000`; minimum is `1000`; maximum is `60000`; `0` and absent mean return promptly; negative values fail `invalid_request`.
- For shell jobs, signal the process/process group where supported.
- For delegate jobs, cancel the active child run and discard queued `delegate_send` deliveries that have not yet been delivered.
- If graceful stop needs internal forceful cleanup after timeout, that is an implementation detail of `job_stop`; there is no separate model-facing `job_kill`.
- Implementations may continue escalation asynchronously after returning `running/stop_pending`; terminal notification remains armed and must report the eventual final state. If an implementation completes escalation before returning, it must still stay within the caller-visible wait budget or return `running/stop_pending`.
- `job_stop` must actually signal/abort the runtime before finalizing as stopped/cancelled.
- Stopping does not delete output, transcript, or durable job records.
- Stopping does not require or imply acknowledgement.
- If the job already completed before stop lands, return the actual terminal status.
- If stop is confirmed, terminal status is `cancelled` with reason `stopped_by_parent`.
- If no live handle remains and cancellation cannot be confirmed, terminal status is `stopped` with reason `stop_unconfirmed` or `runtime_lost`.
- If still running after timeout, status remains `running` with reason `stop_pending`, and a later terminal notification remains guaranteed.
- The return classifies the result in `outcome`: `cancelled_by_request` (the stop cancelled a live job), `already_terminal` (the job had already finished before the stop), `completed_during_stop` (the job finished on its own as the stop landed), or `stop_requested` (still finalizing, e.g. reason `stop_pending`). `previous_status` reports the status the job held immediately before the stop signal, so a race between completion and cancellation is unambiguous.
- For a shell job, `job_stop` targets exactly the supplied `job_id` and is not recursive by default; pass `include_children=true` only when the user intends to cancel visible active nested jobs too.
- For a delegate job, `job_stop` **cascades into the subtree**: it stops the delegate and the running jobs the delegate's subtree owns (its workers' delegate and shell jobs), recursively. Stopping a coordinator therefore stops its whole live subtree's work rather than leaving the coordinator's workers running orphaned. Each cascade-stopped job finalizes as `cancelled/stopped_by_parent`. The cascade fires **regardless of the coordinator's own terminal status**: a fire-and-return coordinator (one that spawns workers and ends its turn) has its own delegate job go `completed` while its workers keep running, and `job_stop` on it must still halt that live subtree — the cascade targets the subtree's running jobs (already-terminal jobs in the subtree are skipped, so a fully-finished subtree is a harmless no-op). The cascade is keyed on the stopped job being the child's **current** parent delegate: a stale, superseded delegate `job_id` (the child was resumed to a newer job in the same session) does not cascade into the resumed child's current work — stop the current job instead.

Delegate stop (cascades into the subtree):

```json
{
  "job_id": "job_parent_delegate"
}
```

Stopping a delegate job cascades: it stops the delegate and the running jobs in its live subtree (its workers' delegate and shell jobs), recursively, so the workers do not survive orphaned. The cascade stops the running jobs inside the subtree without closing the sessions; Session Close, which tears down whole sessions, is unchanged.

Shell stop with `include_children`:

```json
{
  "job_id": "job_parent_shell",
  "include_children": true
}
```

For a shell job this also stops its visible active nested jobs.

Return shape:

```json
{
  "job_id": "job_...",
  "status": "cancelled",
  "reason": "stopped_by_parent",
  "previous_status": "running",
  "outcome": "cancelled_by_request"
}
```

```mermaid
stateDiagram-v2
    [*] --> StopRequested: job_stop
    StopRequested --> AlreadyTerminal: job already terminal
    StopRequested --> Signalled: signal/cancel delivered
    Signalled --> Cancelled: runtime confirms stop
    Signalled --> RunningPending: timeout, escalation may continue async
    Signalled --> Stopped: live handle lost / unconfirmed
    AlreadyTerminal --> [*]
    Cancelled --> [*]
    RunningPending --> [*]
    Stopped --> [*]
```

## V1 tool availability

The v1 model-facing tool matrix is:

| Caller context | Available job tools | Notes |
| --- | --- | --- |
| Root session | shell, `delegate`, `job_watch`, `delegate_send`, `job_status`, `job_list`, `job_stop` | Root may create delegates and watches, and message its direct delegate conversations by `delegate_id`. |
| Delegate/subagent session | shell, `delegate_send`, `job_status`, `job_list`, `job_stop` | Delegates may start shell jobs. `delegate` and `job_watch` are allowance-gated, with the separate `watch_parent:true` grant exposing `job_watch(source="parent")` to observer leaves. Concrete `delegate_id` targets are scoped to the session's **own direct delegates** at every level — a coordinator may message its own worker delegate by `delegate_id`, but not an arbitrary descendant's delegate (which fails `not_controllable`). |
| Root session, interactive only | `ask_user` | Not a job-control tool, but the same root/delegate split governs it: never available to a non-interactive root (`--non-interactive`, one-shot `serf <prompt>`) or to any delegate/subagent — root-only, hard-enforced; `grant_tools` rejects an explicit attempt to grant it. |

Job output is not in this matrix because it is not a job tool: `read_transcript` is available to every session, including a leaf delegate, and a `job:<job_id>` ref is the job-output read. What a caller may read is decided by the resolution chain and the grant table, not by tool availability.

While a session is `awaiting` an `ask_user` reply, its autonomous job notifications are held rather than delivered, and drain at the turn boundary that follows the user's reply.

Tool availability is part of the model-facing contract. If an implementation narrows these permissions for policy reasons, it must make that visible in tool availability or tool descriptions rather than failing late with surprising generic errors.

This matrix is the job-tool slice of the broader effective-capability policy (how a child's whole tool set is narrowed, visibility vs execution, `grant_tools`). For that and the other cross-cutting subagent runtime contracts (lifecycle hooks, helper isolation, lineage), see [`subagent-runtime-contracts.md`](subagent-runtime-contracts.md).

## Removed / intentionally absent tools

### No `wait_job`

There is no direct replacement for `wait_job`, and no read waits. Normal completion discovery comes from automatic terminal notifications. If a caller needs to be told about a condition rather than a completion, `job_watch` delivers it; nothing else blocks.

### No `job_ack`

There is no model-facing acknowledgement tool. It is unclear when the model should call it, and it creates unnecessary cognitive load.

Retention is policy-based:

- durable job metadata is retained at least as long as the owning session transcript;
- output bytes are capped per job;
- old output may be pruned under session/state retention policies;
- the durable job record outlives its output bytes: `job_status` and `job_list` still answer for a job whose output was pruned;
- never require model acknowledgement to make progress.

Notification delivery may have internal delivered/undelivered bookkeeping, but it is not a model tool.

### No `job_read_output`

There is no dedicated job-output tool. A job's output is evidence, and it is read with the same reader as every other piece of evidence: `read_transcript(transcript_ref="job:<job_id>")` (see "Reading job output"). One reader means one resolution chain, so a job that can be listed and a job that can be read cannot drift apart.

What went with the tool, deliberately:

- **the blocking read.** There was a `max_wait_ms` that waited for new output, terminal state, or a `grep` match. No read blocks now. Completion arrives as a notification; a condition on watched output is `job_watch(output_match=...)`.
- **model-facing grep.** Searching a job's retained output server-side is not part of the read. `job_watch(output_match=...)` is the trigger-side answer.
- **line paging.** `head_lines`/`tail_lines`/`from_line`+`line_count` are gone; the read returns the tail of retained output, bounded by the envelope.
- **`output_status` on the read.** A `job:` read reports `- total_bytes:` always and `- dropped_bytes:` only when retention evicted bytes; it has no window/eviction enum of its own. The shell tool's own result still carries `output_status`, because that result is the one place a caller has to be told whether the inline text is the whole thing.

### No `job_kill`

`job_stop` is the single model-facing stop primitive. If an implementation must escalate from graceful stop to forceful process cleanup, it does so inside `job_stop` according to documented timeout/escalation policy.

### No `close_agent` or `agent_id`

Delegate jobs are controlled by `job_id`. Child conversations are identified by session ID/transcript ref. There is no separate `agent_id` namespace and no model-facing close operation for child sessions.

## Legacy Serf surface mapping

The target model removes the current subagent-specific control plane. Replacement mapping:

| Legacy concept | Target replacement | Semantic difference |
| --- | --- | --- |
| `spawn_agent(blocking=false)` | `delegate` (no `max_wait_ms`) | always starts a new delegate conversation and returns `job_id`, not `agent_id` |
| `spawn_agent(blocking=true)` | `delegate(max_wait_ms=...)` | timeout leaves job running |
| `resume_agent` / `send_input` | `delegate_send(to=<delegate_id>, message=..., on_idle="start")` | steers if running; starts the delegate's next job only when `on_idle="start"` is supplied |
| `wait` / `wait_job` | no direct replacement; wait for the automatic terminal notification | no read blocks; `job_watch` is the only way to be told about a condition |
| `cancel_agent` / `job_kill` | `job_stop` | graceful stop plus internal escalation |
| `close_agent` | none | retention automatic; transcript remains accessible |
| `subagent_output` | `read_transcript(transcript_ref="job:<job_id>")` plus session transcript tools | output and conversation are separate refs into one reader |
| `list_agents` | `job_list(type=["delegate"])` | generic job inventory |

## Durable job records

A durable job record exists for promoted shell jobs (including within-bound completions whose output cannot ride inline per the complete-or-handle invariant), delegate jobs, resumed delegate jobs, and other durable asynchronous work. Foreground shell commands that complete inline with output that fits in the tool result are ephemeral and need not create durable job records. A durable job record contains:

```json
{
  "job_id": "job_...",
  "type": "shell|delegate",
  "status": "running|completed|failed|cancelled|stopped",
  "reason": null,
  "description": "...",
  "command": "...",
  "task": "...",
  "parent_session_id": "01J...",
  "owner_session_id": "01J...",
  "visible_to_session_id": "01J...",
  "parent_job_id": "job_...",
  "origin_turn_id": "...",
  "origin_tool_call_id": "...",
  "transcript_ref": "local:01JCHILD...",
  "resumable": true,
  "not_resumable_reason": null,
  "started_at": "...",
  "ended_at": "...",
  "exit_code": 0,
  "output_path": "...",
  "output_bytes": 0,
  "terminal_generation": null,
  "terminal_notification_state": "not_armed|pending|delivered"
}
```

Fields that do not apply to a job type are omitted. `command` is shell-specific. `task`, `transcript_ref`, and resumability fields are delegate-specific. `delegate_session_id` may exist internally or in diagnostics, but `transcript_ref` is the model-facing child-conversation handle.

Retained output start offsets and detailed output availability may be stored with the output store rather than duplicated into the model-facing job record. The invariant is durable reconstruction, not that every storage-level field appears in `job_list`.

## Durable reconstruction invariants

Serf persists job history as append-only session events or equivalent durable records. The physical encoding may be events, database rows, or another durable representation. The contract is that Serf can reconstruct:

- job identity and type;
- parent/owner/visible session identity;
- parent job linkage;
- lifecycle status/reason;
- start/end times;
- shell exit code when meaningful;
- delegate transcript ref and resumability when known;
- output byte count, internal/UI retained start offset, and output availability;
- terminal-notification dedupe and delivery state.

Non-normative event names that can satisfy this contract:

```text
job_started
job_output_delta       optional if output is independently stored
job_session_assigned   delegate jobs only
job_finished           canonical terminal event, including stopped/runtime_lost
job_message_sent            delegate/session message event
job_notification_pending
job_notification_delivered
```

The first canonical terminal durable record/event for a job defines `terminal_generation`. A duplicate reconstructed terminal write for the same job must not create a new `terminal_generation`. Implementations may use a durable event ID, monotonic sequence, or equivalent stable identity, but the identity must be stable across restart and visible-session forwarding.

`job_list` reconstructs from durable job state and overlays in-memory state for currently running jobs.

## Output storage

Each job has a bounded output file under the owning session's state directory. The exact path is implementation-specific, but conceptually:

```text
.serf/sessions/<session_id>/jobs/<job_id>.log
```

Rules:

- Output is capped per job.
- Output appends are serialized enough to avoid corruption.
- The storage layer's reads support offset/tail/limit and report truncation and byte offsets. That is substrate: the model-facing read exposes none of it, and takes the tail (see "Reading job output").
- Output files are retained according to session/job retention policy.
- Delegate job output is not a substitute for the child transcript.
- Parent-visible nested job output must be readable through the parent's own `job:<job_id>` read, either by mirroring output into the parent job store or by durable routing metadata. This routing is not model-visible.

## Notifications

Terminal job notifications are automatic for notification-armed jobs. A model should not need to subscribe to learn that a background job finished.

A job is notification-armed if its creating tool call returned before terminal state, including shell foreground calls that timed out and were promoted to durable background jobs. A job that completed synchronously before the creating tool returned is not required to inject a duplicate terminal notification; the terminal result is already in the tool result. `delegate_send` against a running delegate does not arm an additional terminal notification. `delegate_send(on_idle="start")` against an idle/resumable delegate creates a new notification-armed delegate job. A watch-origin observer job that successfully calls `communicate(end_turn=true)` uses that callback as the owner signal and skips the redundant terminal owner notification. V1 sidecars are ordinary delegate jobs for public semantics and notification behavior; Serf keeps only internal watch-origin bookkeeping for frame routing and feedback-loop suppression, plus durable observer read grants keyed on the sidecar's session identity.

Notification example:

```xml
<job-notification job_id="job_..." event="completed" job_type="shell|delegate" status="completed" reason="exit_zero" output_bytes="12345">
Job job_... completed. Output is available through read_transcript(transcript_ref="job:job_...") if needed.
excerpt:
<bounded 8000-character result excerpt: shell tail / delegate report head>
[excerpt truncated]
</job-notification>
```

When the excerpt contains the job's complete output (nothing was truncated away), the body says `Complete output below.` instead of the read pointer — a read of what the notification already carries in full must not be nudged. The pointer appears only when there is no excerpt or the excerpt is truncated, and it names the job's own `transcript_ref` (the delegate conversation's ref when the notification carries one, otherwise `job:<job_id>`).

Rules:

- Terminal job notifications carry a concrete `job_id`, `event` (the lifecycle/progress notification kind), `job_type` (`shell` or `delegate`), status, reason, output byte count, exit code when known, and optional transcript ref for delegate jobs. `reason` is rendered even when the status carries no reason (`reason=""`), for example an ordinary delegate completion. Watch notifications use `event="watch"` and `job_type="watch"`; output/progress/job-event watches carry the concrete watched `job_id` when one exists, while session-level event watches may omit a concrete `job_id`. Notification `event` must not be named `type`, because durable job records already use `type` for the job class. The v1 event vocabulary is terminal statuses `completed`, `failed`, `cancelled`, and `stopped`, plus `watch` for watch output/event/progress notifications and observer frames.
- Terminal `job_finished` notifications carry a bounded result excerpt in a labeled `excerpt:` section, re-read from the durable job record at render time (consistent on the durable-replay path). The excerpt is directional: a `shell` job shows the last 8000 characters of retained output (the tail); a `delegate` job shows the first 8000 characters of its report (the head). Over-budget excerpts end with a `[excerpt truncated]` marker. A job with no output omits the `excerpt:` section entirely, and a failed output read degrades to no excerpt rather than failing the notification. Watch notifications carry no terminal-notification excerpt; concrete-job watch frames may carry a bounded output excerpt. This 8000-character budget is the wire excerpt the model sees; the web UI's notification card independently bounds its own default collapsed-card preview to a smaller size, preserving the same tail/head direction per job type, with the full excerpt always available in the card's raw disclosure. The two are separate budgets for separate audiences, not two descriptions of the same number.
- Notifications include a bounded excerpt that may be the complete output when the whole result fits the budget (rendered with `Complete output below.`), and is otherwise a truncated preview pointing the reader at `read_transcript`.
- Notifications wake the visible session if idle. Notifications render only in their owner's own turns: a session is never asked to render a notification for a job it does not own. A child/delegate session with undelivered attention (a queued owner notification or a pending observer frame) is **driven** by its parent at the parent's own loop boundaries — the parent launches one notification turn on the child's own drain loop so the child's model receives its own notifications. The root is driven the same way by serve.go's wake. The drive mints no new job record and notification-arms nothing new; it is the child processing its own durable queue. A child's owner-side notification state remains durable and is delivered when that child is next driven (or resumed).
- A parent **drives** a child for the child's own (delegate-owned) job notifications; it still **renders** its OWN jobs' terminal notifications, including its direct delegates' terminals — the parent is told when its own coordinator finishes, because that is the parent's job ending, not noise about a job it did not create.
- A successful drive handoff settles the parent's forwarded pending COPY of a child-owned terminal (marks it delivered) so the same stale signal does not re-drive forever. The forwarded copy is only a drive signal; the child's own durable queue is the delivery ledger and is never touched by the parent's settle. On restart the parent re-arms only its OWN jobs' terminal notifications; a forwarded copy of a direct child's own job is a drive signal, not the parent's render, so it is not re-armed onto the parent's rail (no restart wake-storm). A child whose latest delegate record (by durable append order) terminated by deliberate stop is not driven for attention that predates the stop; new work clears the gate. If a child cannot be driven (non-resumable, closed, descriptor-less) the parent renders the pending itself prefixed `child unreachable:`, so attention escalates one honest level instead of vanishing.
- If the parent is mid-turn, notifications queue for a safe turn boundary.
- Duplicate terminal notifications for the same job are suppressed.
- Watch wake-ups and observer frames are opt-in through `job_watch`.
- Serf supervises running delegate jobs with a built-in quiet-job watchdog. A running delegate that produces no parent-observable activity (no output append; `last_activity` unchanged) for 10 minutes triggers one owner notification reading `quiet for 10m; last activity: <timestamp>`, delivered like a watch notification (`event="watch"`, the delegate's `job_id`). It fires at most once per quiet stretch and re-arms only after the delegate shows fresh activity. This is always-on supervision, not an opt-in `job_watch`; a quiet delegate that is genuinely working (for example reading a large file) is reported the same as a stalled one, because the two are not distinguishable from parent-observable state. The watchdog only notifies the owner; it never steers, resumes, or stops the delegate.
- Notification delivery state is internal; there is no `job_ack`.

Terminal notification dedupe is durable. The dedupe key is `(visible_session_id, job_id, terminal_generation)`, where `terminal_generation` is the stable identity of the first canonical terminal durable event for that job. Serf may implement exactly-once visible delivery or at-least-once delivery with durable duplicate suppression, but it must not repeatedly notify about the same terminal event on every restore.

Notification delivery must also avoid lost notifications, not only duplicates. A notification-armed terminal event must remain in a durable `pending` state until the notification is successfully injected into the visible session. Serf must not mark it `delivered` before injection is durable. This can be implemented as a transactional inject-and-mark-delivered operation or as pending replay with duplicate suppression, but restart between queueing and delivery must not silently lose the terminal notification. This no-loss guarantee is keyed on the OWNER's copy — the session whose own turn renders the notification. A parent's forwarded copy of a child-owned terminal is a drive signal, not the ledger: the parent may settle it at drive handoff (above) without weakening no-loss, because the owner's copy still carries the guarantee.

```mermaid
stateDiagram-v2
    [*] --> Armed: job returned before terminal (delegate immediate-return / shell promotion)
    [*] --> NotArmed: synchronous terminal return
    Armed --> Pending: terminal event observed + durable pending
    Pending --> Delivered: inject notification + durably mark delivered
    Delivered --> [*]
    NotArmed --> [*]
    Pending --> Pending: restart before delivery (replay pending)
    Delivered --> Delivered: duplicate suppressed
```

## Restart behavior

Jobs do not auto-resume after a Serf process restart.

On session restore or job-manager initialization:

1. Reconstruct durable job records.
2. Find records whose latest lifecycle state is `running` but have no live in-memory runtime.
3. Finalize each such job exactly once as `stopped` with reason `runtime_lost`, using the canonical terminal durable record/event and first stable `terminal_generation`.
4. Inject or queue the terminal notification according to durable notification pending/delivered state.

Notification example:

```xml
<job-notification job_id="job_..." event="stopped" job_type="shell|delegate" status="stopped" reason="runtime_lost">
Job job_... stopped. Output is available through read_transcript(transcript_ref="job:job_...") if needed.
</job-notification>
```

This is not command failure. It is supervision loss.

Parent-visible forwarded nested jobs follow the same reconciliation rule: if the visible record says `running` and the owner runtime is absent after restore, Serf finalizes the parent-visible job exactly once as `stopped/runtime_lost` using the same parent-visible `job_id` and terminal notification dedupe key. Restart loss is never reported as job failure; active control attempts whose owner runtime is believed live but cannot route/control the job fail synchronously with `not_controllable`.

Delegate jobs finalized as `stopped/runtime_lost` are not automatically resumed by restart reconciliation. Their delegate conversations remain resumable through `delegate_send(on_idle="start")` when retained transcript/session state is present and strict restore preflight can reconstruct the conversation safely. Missing or pruned retained state makes the delegate not resumable instead of triggering a best-effort replay.

```mermaid
stateDiagram-v2
    [*] --> Reconstruct: restore session
    Reconstruct --> LiveRunning: running record + live runtime
    Reconstruct --> RuntimeLost: running record + no live runtime
    RuntimeLost --> FinalizedStopped: append terminal stopped/runtime_lost once
    FinalizedStopped --> NotifyPending: terminal notification pending
    NotifyPending --> NotifyDelivered: inject notification
    LiveRunning --> [*]
    NotifyDelivered --> [*]
```

Graceful shutdown is the deliberate counterpart of restart reconciliation. When an owner session closes cleanly, Serf stop-signals each still-running job and waits a bounded grace period (5 seconds in the shipped implementation) for runtimes to confirm; a job that stops within the grace period finalizes as `cancelled` with reason `stopped_by_parent`, while a job already finalizing under an explicit stop keeps that stop's status/reason. A job that does not confirm in time is abandoned with its durable record still `running`; the next restore of that session finalizes it as `stopped/runtime_lost` through the reconciliation steps above. Child/delegate sessions close before the parent's durable store, so nested jobs finalize through their owner sessions and forward terminal events while the parent can still record them. Undelivered pending observer frames are evicted by watch cleanup at close.

## Nested jobs

Subagents must be able to start shell jobs. Nested delegate jobs run on the same parent-job machinery and are allowance-gated: a subagent may itself delegate only when it was granted a non-zero `delegation_allowance` (see the delegation-allowance amendment). An observer sidecar started without an allowance (allowance 0, the default) is a leaf and cannot delegate.

Nested jobs use `parent_job_id` rather than a separate control plane.

Rules:

- Every job has an owner session.
- A nested job records the job that caused it in `parent_job_id`.
- Parent-visible job lists may include nested jobs when `include_nested=true`. This is the one-hop view: the parent's own store, which holds its owned jobs plus the records forwarded up one level from its direct children.
- `job_list(include_descendants=true)` walks the live descendant tree at read time instead of one hop. It returns the caller's own jobs plus every live descendant's jobs, reading each descendant's job store independently under its own lock (no lock is held across the recursion). Each row carries `owner_session_id` and a `depth` annotation: `depth` is the live-walk distance to the store the row was surfaced from — `0` for the caller's own store, `1` for a direct child, and so on. The dedupe rule below applies across the whole walk: a forwarded copy of a `job_id` whose owner is reached live during the walk is suppressed in favor of that owner's record (so each job appears once, at its real owner's depth). The walk is live-only: it recurses only into live child sessions. A dead or terminated descendant contributes just the terminal forwarded copy that survives in an ancestor store (at that ancestor's depth); the walk does not reopen the gone session's store to dig deeper — resume the descendant to inspect its subtree. Default `job_list` and `include_nested` semantics are unchanged; `include_descendants` is additive.
- A `job:<job_id>` read resolves a descendant job at depth ≥ 2 (a grandchild-or-deeper session's job) through the recursive owner path: when the one-hop resolver does not find the job locally or in a live direct child, the read recurses the live subtree (the same live-only enumeration the descendant walk uses), applying the single-hop owner resolution at each hop until it reaches the session whose store owns the `job_id`. Each store is read under its own lock; no lock is held across the recursion. The read is served from the resolved owner's store, and the owner session is what the result's owner/resumability projection keys on. Own-job (depth 0) and direct-child (depth 1) reads are unchanged. The owning branch must be live; if the descendant that owns the job is closed, the read falls back to whatever forwarded terminal copy survives in the owner's DIRECT PARENT store — forwarding is single-hop, so that is where a forwarded terminal copy lands — the same as the descendant walk.
- For any parent-visible nested job, the parent-visible `job_id` is the only control handle accepted by parent job tools.
- Job IDs must be globally unique enough that parent job tools do not need string namespacing or a separate owner/visible ID choice.
- Notifications, `job_list`, `job_status`, `job_watch`, `job_stop`, and the `job:<job_id>` transcript ref all use the same parent-visible `job_id`. `delegate_send` uses the corresponding `delegate_id` for delegate conversations.
- Shell jobs created by subagents are visible to the parent through forwarded durable job events.
- Delegate jobs created by subagents join the same one-hop forwarding: delegate-job creation forwards its `job_started` one hop to the parent's store, carrying `parent_job_id` plus owner/type identity (`owner_session_id`, `type=delegate`). This seeds a typed parent-visible record at start, so the later forwarded terminal event merges onto it rather than producing a type-less phantom record.
- Dedupe rule: the owner session's durable record is authoritative for a forwarded job. A forwarded copy of the same `job_id` in an ancestor store is suppressed in favor of the owner record; the forwarded start carries enough identity (owner session + type) to make that suppression unambiguous.
- Terminal notifications for a parent-visible nested background job are **owner-scoped** (spec §3/§10 drive-down): the notification renders only on the OWNER's rail — the subagent that created the nested job — never on the ancestor's. The parent is told only about its OWN jobs, including its direct delegates' terminals (the subagent itself finishing). It is **not** interrupted about a job a descendant created; the forwarded copy is a drive signal for the parent, and the parent drives the subagent so the subagent renders its own notification. The ancestor retains on-demand visibility into the nested job via `job_list(include_descendants=true)`. `job_watch` is only for extra output/event/progress notifications or observer frames.
- Parent `job_stop` on a nested job routes to the owning session/runtime if live.
- If routing is unavailable after restart, Serf reports terminal `stopped/runtime_lost` according to restart reconciliation.
- If routing fails while the owner runtime is believed live, an active control attempt may fail or finalize according to the status matrix by failing synchronously with `not_controllable`.
- For a shell job, `job_stop` is not recursive by default; `job_stop(shell_job_id, include_children=true)` recursively stops visible active nested jobs.
- `job_stop` on a delegate job **cascades into its subtree** (no flag needed): stopping a coordinator's delegate also stops the coordinator's own running jobs (its workers' delegate and shell jobs) and recurses into the live subtree, so stopping a subtree means stopping its direct child. The cascade stops the running jobs inside the subtree without closing the sessions; each cascade-stopped job finalizes as `cancelled/stopped_by_parent`, the same terminal as the directly stopped delegate. It reuses the live-walk leaf-lock discipline — each store's running jobs are read under that store's own lock, only live direct children are recursed into, and no lock is held across the recursion. Session Close still tears down whole sessions recursively and is unchanged.
- `job_stop` on a non-direct descendant — a grandchild-or-deeper job the caller neither owns nor reaches through a direct child it owns the job through — fails synchronously with `not_controllable`. The error names the owning descendant session and the caller's direct delegate that controls that subtree, guiding the caller to stop that direct delegate (which cascade-stops the subtree) rather than silently routing a control attempt the caller is not entitled to make.

Target nested shell support:

- subagents can start shell jobs;
- those jobs are recorded with `parent_job_id`;
- the parent can list/output/watch/stop them through the parent-visible `job_id`; delegate conversations are messaged separately through `delegate_id`;
- nested delegate jobs run on the same parent-job semantics, allowance-gated per the delegation-allowance amendment.

Example flow:

```text
1. Parent starts delegate job job_A.
2. Delegate starts shell job job_B.
3. Parent sees job_B in job_list(include_nested=true) with parent_job_id="job_A".
4. Parent reads shell output with read_transcript(transcript_ref="job:job_B").
5. Parent stops just job_B with job_stop(job_B), or stops the visible tree with job_stop(job_A, include_children=true).
6. job_stop(job_A) on the delegate cascades: it stops the delegate plus the running jobs in its subtree (job_B and anything job_A's child delegated onward), recursively.
```

```mermaid
flowchart TD
    ParentDelegateJob -->|subagent starts shell| ChildShellJob
    ChildShellJob -->|job_started forwarded| ForwardedRecord
    ForwardedRecord -->|job_list include_nested=true| ParentVisible
    ParentVisible -->|read_transcript job: parent-visible job_id| ParentCanRead
    ParentVisible -->|job_watch source=job_id on live descendant| ParentCanWatch
    ParentVisible -->|job_stop own delegate cascades subtree| ParentCanStop
```

## Observer and sidecar composition

Observer sidecars are a v1 Serf composition pattern. Claude Monitor covers only the basic stream-notification profile; Serf also supports sidecars that receive bounded event/output frames and report back through the normal result surface. Serf does not need a separate observer-comment command or a Sprout-style raw handle model. The shipped composition uses existing job primitives:

1. Start a sidecar with `delegate(watch_parent:true, ...)`; this creates an ordinary delegate job with a non-transitive parent-watch grant.
2. Inside that child, configure `job_watch(operation="create", source="parent", ...)`.
3. Serf delivers matching bounded event/output frames to the child that created the watch.
4. The sidecar responds with `communicate(end_turn=true, ...)` when it has useful commentary or advice.
5. The caller receives an "Observer callback" block with the observer message and canonical output envelope, then continues from it. Follow-up `job_list`, `job_status` or `job:` transcript reads are audit/diagnostic evidence after the callback, not the callback mechanism itself.

This makes observer behavior a composition of two primitives:

```text
delegate         create observer with watch_parent
job_watch        source-owned condition -> bounded frame to watcher
communicate      observer callback -> parent resumes from the result
```

Safety and behavior rules:

- Watch frames are bounded and filtered. Redaction may be applied for cross-session or observer delivery, but frames are not guaranteed secret-free.
- Observer/sidecar telemetry should be excluded from frames by default to avoid feedback loops.
- Observer advice is runtime-originated commentary, not user instruction.
- Observer failures should not fail the watched session; they produce diagnostics or warnings. A failed observer-frame delivery must surface as a caller-visible diagnostic notification rather than silently dropping the matched condition.
- Watch frames to busy sidecars are retried from durable latest-frame-wins pending state. Hard/non-resumable delivery failure drops the pending frame only after emitting a caller-visible diagnostic.
- Access control is source-resolution based: `parent` requires `delegate(watch_parent:true)`, concrete jobs must be owned by this session or a live descendant, and unrelated sessions are rejected.
- Broad unbounded transcript watches are not part of v1; watch a concrete `job_id`, `self`, or a granted `parent` session source.
- Same-watch feedback is causal, and it is classified rather than suppressed: watch-originated events carry provenance, so an event whose provenance already holds this watch's `(watch_id, generation)` is recognised as the watch's own echo. It still fires and still delivers — marked self-influenced, with the gradient notice on the frame — and the runaway fuse is what stops a runaway. The classification is scoped to the current generation, so a re-arm reads as fresh, and a later top-level user input starts with fresh provenance.
- An observer that needs the output of a job a frame told it about spends the read grant that delivery minted: `read_transcript(transcript_ref="job:<job_id>")`, the call the frame's trailing `read with:` line names. That is the whole of the capability — the frame still carries the event payload (status, reason, exit code, output bytes) for everything else, and the grant opens no transcript, listing, or control access beyond that one job's output.

## Relationship to transcript tools

Transcript tools remain separate:

```text
find_session_transcripts
read_session_transcript
read_transcript
```

Use job tools for a work unit's lifecycle and control. Use transcript tools to read evidence — a session `transcript_ref` for conversation history, a `job:<job_id>` ref for a job's own output.

For delegate jobs:

- `job_list` and notifications expose the child `transcript_ref` when known.
- `read_transcript(transcript_ref="job:<job_id>")` returns the delegate job output/log/final report for that invocation.
- `read_session_transcript` reads the child conversation.

Decision table:

| Need | Tool |
| --- | --- |
| Did the job finish? | Wait for automatic notification, or check `job_list` once when recovering state |
| Shell stdout/stderr | `read_transcript(transcript_ref="job:<job_id>")` |
| Delegate invocation final report/log | `read_transcript(transcript_ref="job:<job_id>")` |
| Delegate full child conversation/tool history | `read_session_transcript(transcript_ref)` |
| Trigger observer/sidecar review | Parent: `delegate(watch_parent:true)`; observer: `job_watch(operation="create", source="parent", ...)`; callback: `communicate(end_turn=true)` |
| Start a fresh delegate conversation | `delegate(...)` |
| Follow up on an existing delegate conversation | `delegate_send(to=<delegate_id>, message=...)` |
| Start an idle delegate's next turn | `delegate_send(to=<delegate_id>, message=..., on_idle="start")` |

Canonical model-facing delegate conversation field is `transcript_ref`. `delegate_session_id` is diagnostics/internal-only unless an implementation-specific diagnostic view exposes it; ordinary agents should not need to reconcile it with `job_id`.

## Anti-patterns

Tool descriptions and prompts should warn against:

- starting a background job and immediately waiting on it with `max_wait_ms`;
- polling `job_list` for completion;
- using `job_watch` as a terminal completion subscription;
- re-reading a job's output in a loop to discover that it finished;
- expecting jobs to auto-resume after process restart;
- using job output as a replacement for delegate transcripts;
- passing `transcript_ref` to job-control tools or using `delegate` to resume old conversations;
- stopping a job when the intent is only to inspect output;
- stopping a job as cleanup or acknowledgement;
- assuming a nested job is hidden from the parent;
- expecting `job_stop` to delete durable history.

## V1 non-goals

V1 does not define multi-job barriers, any-of/all-of watches, or named job groups. Agents coordinate multiple background jobs through individual terminal notifications and `job_list` recovery. Fan-in/barrier coordination is the likely first future coordination extension if heavy parallel workflows need less manual state tracking, but it remains out of v1 until that surface is deliberately designed.

Nested delegate jobs are allowance-gated rather than deferred: a subagent delegates only with a granted non-zero `delegation_allowance`, and recursion stays dark by default behind the double opt-in (see the delegation-allowance amendment). Shell jobs are not messageable in v1; long-running REPL stdin is outside this contract.

## Capacity and discovery requirements

Implementations must enforce a documented concurrency policy for jobs. The policy may queue or fail excess work, but it must bound at least:

- concurrent shell jobs;
- concurrent delegate jobs;
- total jobs visible/running in one session;
- observer work within the normal delegate/job concurrency limits.

Exact limits are implementation/configuration details, but unbounded delegate fan-out or unbounded shell process creation is not part of the target contract.

**Tree-wide running-delegate cap.** The number of delegate turns running concurrently across the whole session tree is bounded by a tree-wide counter shared by every session in the tree. The cap is configurable per root session via `--max-concurrent-delegates` (config `max_concurrent_delegate_turns`, wire `maxConcurrentDelegateTurns`), default **50** (raised from the original hardcoded 16). A slot is reserved on each path that launches a running delegate turn — a spawn or a resume — and released when that turn ends: on terminal finalize and on the abandon path. (Drive-down notification turns reserve on the separate drive budget described below, not on this counter.) An idle (turn-ended) delegate holds no slot, so a coordinator that has ended its turn frees its slot and a later drive re-reserves one for that drive turn. When the tree is already at the cap:

- a spawn or resume from a tool call fails synchronously with `tree_at_capacity: N delegate turn slots in use across this session tree (J delegate jobs, D drive turns). Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.` (N is the configured spawn-budget cap; J counts jobs holding spawn-budget slots and D counts drive turns in flight on the separate drive budget, so a drive-saturated tree is diagnosable rather than reporting a dead 0);
- a drive-down notification turn simply does not launch this pass; the child's durable notification ledger stays queued and the next loop boundary retries (the ledger is durable, so nothing is lost and no retry daemon is needed).

**Drive-down budget.** Drive-down notification turns budget separately from spawns: a second tree-wide counter (constant cap 8) bounds concurrent drive turns, so notification maintenance can never starve user fan-out, and a full spawn budget never blocks notification delivery. A drive turn times out after 5 minutes (a hung child frees its slot rather than pinning it until parent close), and the post-turn re-drive is paced at 1 second so a child whose attention never drains cannot hot-loop its slot. `job_list` and `status` report slot occupancy (`delegate turn slots: X/Y in use (J jobs, D drive turns).`) while any slot on either budget is held — X/Y/J describe the spawn budget, D the live drive-budget occupancy.

On restart the root rebuilds the counter from its post-reconciliation state (zero); descendants re-reserve as they re-attach or resume. A subtree that is detached from its live parent is uncounted until it is resumed — an accepted v1 looseness, not a leak: such a subtree holds no live runtime to count, and resuming it re-reserves.

This cap binds existing single-level fan-out today: a root that exceeds the configured cap of concurrent delegates fails loudly with `tree_at_capacity` even with no recursion grants. Concurrent **shell** jobs are not bounded by this counter — the shell-job concurrency bound named above remains an acknowledged standing gap, not closed here.

**Retained terminal delegate records.** Separately from the running-turn cap, each parent retains terminal child records (completed|failed|cancelled|exhausted) for history and resume. The retention cap is configurable via `--max-retained-terminal` (config `max_retained_terminal`), default **2048** (raised from 128); at the cap the spawn path GCs closed/consumed records oldest-first and fails loudly only when nothing is reclaimable.

**`job_list` windowing.** `job_list` paginates the newest-first listing with `limit` (default 50, max 100) and `offset` (default 0); a truncated or offset listing reports its window and the full filtered total (`showing 51-100 of 257 jobs.`) so a page size can never be mistaken for a system cap.

`delegate.agent_type` values must be discoverable by the model. This can be done through the tool description/system prompt, a future discovery tool, or a session context section. If no discovery tool exists, the `delegate` tool description must enumerate the valid agent types available in the current session.

`job_watch.events` event-kind values must be discoverable by the same mechanisms. If no discovery tool exists, the `job_watch` tool description or session context must enumerate the event kinds available in the current session, or document that agents should use `events:["*"]` plus filtering when targeted event names are unavailable.

## Rollout (live vs. dark)

The recursion campaign that landed the delegation-allowance, drive-down, counter, `include_descendants`, and stop-cascade amendments is **dark by default for recursion** but carries **two live behavior changes** that bind existing single-level (root + direct delegates) use at merge with no opt-in. This section discloses both honestly.

**Live — the tree-wide running-delegate cap binds existing fan-out.** The tree-wide counter is wired on the spawn, resume, and drive paths today; the cap is configurable (`--max-concurrent-delegates`, default 50 — raised from the original hardcoded 16, a deliberate headroom increase). Even with no recursion grants, a root that launches one more concurrent running delegate than the cap fails loudly with `tree_at_capacity` instead of fanning out unbounded. Idle (turn-ended) delegates hold no slot, so the bound is on *concurrently running* delegate turns, not lifetime fan-out. Shell jobs are not counted.

**Live — nested-job terminal notifications are owner-scoped.** Previously a subagent's nested job pushed a terminal notification onto the parent's model. Under drive-down the notification renders only on the OWNER's rail — the subagent that created the nested job — and the parent is **driven** so the subagent renders its own notification, rather than the parent being interrupted about a job it did not create (Jesse's ruling: an agent is never interrupted about a *subagent's* children). Visibility is preserved, not removed: the parent is still told when its OWN direct delegate finishes (its own job ending), and an ancestor retains on-demand visibility into the whole subtree through `job_list(include_descendants=true)`. This is a live change to the existing nested-shell-job case; it is not dark.

**Live — delegate-start forwarding changes nested-store contents.** Delegate-job creation now forwards a typed `job_started` one hop, so a parent's store carries an owner/type-identified record for a child's delegate job from the start (seeding the later terminal merge rather than producing a type-less phantom). These forwarded records are invisible to a default `job_list` (they surface only under `include_nested`/`include_descendants`), but the nested-store contents differ from before merge. Stated for completeness.

**Dark — recursion (depth > 1) stays behind the double opt-in.** A subagent can itself delegate only when granted a non-zero `delegation_allowance`, and the root's allowance equals `MaxSubagentDepth` (default 1, so the root may grant only 0). Enabling recursion requires **both** raising `MaxSubagentDepth` in config **and** passing a per-spawn `delegation_allowance` — neither alone unlocks it. Recursion stays dark until an operator deliberately does both.

### Why drive-down, not a flat session scheduler

Drive-down delivers the inbox-wakes-an-idle-session behavior using the machinery serf already has: `serve.go` drives the root, and the existing parent-owns-child lifecycle lets a parent drive each direct child at its own loop boundaries — the tree is eventually-driven, level by level (see `docs/architecture.md`, "Drive-down"). The obvious-looking alternative — a flat session scheduler that wakes every session directly from its own inbox, independent of the tree — was war-gamed adversarially in `docs/superpowers/research/2026-06-13-flat-scheduler-wargame.md` and deliberately deferred. The hard part is **teardown quiescence**: making a session's loop goroutine scheduler-owned rather than parent-owned breaks the `sendersWG.Wait()` join and the `closing`-latch "no late goroutine escapes the drain" guarantee that the mailbox and recursion campaigns spent significant effort getting right, converting the simplest, most correct part of the system (Close) into a distributed quiescence protocol. The flat scheduler is therefore a campaign-sized runtime re-architecture — a smaller diff against a far more dangerous invariant — and is deferred under a **named trigger**: measured end-to-end notification latency on deep idle trees, a persistent / cross-process child requirement, or a parent-cadence correctness bug (a child that must take a turn while its parent is blocked in a long LLM call). Until one of those is concretely observed, drive-down is the shipped delivery model. The durable principle drive-down realizes — an agent is never interrupted about a *subagent's* children — is the same principle the owner-scoped notification change above enforces.

## Implementation notes that are not part of the contract

The implementation may choose specific package names, event encodings, output paths, batching windows, and retention limits. Those details belong in implementation specs unless they affect model-facing behavior.

The contract-level requirements are:

- generic jobs for shell and delegate work;
- globally unique opaque `job_id` as primary handle;
- foreground shell default (`background:false` waits the session timeout) with promotion to a durable background job on timeout; delegate default returns the job immediately (unset `max_wait_ms`);
- automatic terminal notification with durable no-loss/dedupe semantics;
- `job_watch` for source-owned condition-triggered notifications and v1 observer sidecar event/frame delivery;
- bounded output inspection through the `job:<job_id>` transcript read;
- durable listing/history;
- clear stopped/runtime-lost restart behavior;
- observer/sidecar composition through `delegate(watch_parent:true)` + `job_watch(source:"parent")` + `communicate(end_turn:true)`;
- nested shell jobs from subagents;
- no model-facing `wait`, `ack`, `kill`, `close_agent`, or `agent_id`.
