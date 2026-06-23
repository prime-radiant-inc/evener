# Job Status and Transcript-Based Job Reading Design

Date: 2026-06-23
Status: Draft for Jesse review
Scope: Serf agent job-control tool surface

## Problem

`job_read_output` is doing too many jobs. It is presented as the way to read
job output, inspect status, wait for new output, search logs, retrieve delegate
results, and diagnose active work. That overloaded contract is actively
misleading for agent jobs.

The bad case is a running delegate. Today the delegate job output log is not
the child transcript. It only receives the child's final prose report when the
delegate finalizes. A healthy delegate can therefore show:

```text
status = running
total_bytes = 0
output_status = all_retained
```

That result is technically consistent with the implementation, but it is a bad
supervision signal. It makes active work look silent, encourages bounded polling,
and gives the parent no obvious way to distinguish "still working" from "stuck".

The intended mental model is already different: background jobs notify their
owner when they complete. A parent should normally trust completion
notifications, use status to understand current state, and read raw transcripts
only when it needs evidence or diagnosis.

## Goals

- Make completion notification-driven, not polling-driven.
- Make `job_status` the normal supervision primitive for shell and agent jobs.
- Give every job one `transcript_ref` that can be handed to the transcript
  reader for raw evidence.
- Stop using output bytes as the liveness signal for agent jobs.
- Represent lifecycle, current execution phase, and quiet time as separate
  concepts.
- Remove `job_read_output` from the primary model-facing design.
- Keep the first implementation small enough to land without redesigning all of
  job control.

## Non-Goals

- Do not preserve `job_read_output` as a compatibility abstraction unless Jesse
  explicitly asks for backward compatibility.
- Do not make shell logs and agent conversations share the same internal storage
  format.
- Do not expose notification delivery state as normal job status.
- Do not try to infer unobservable model internals such as "thinking".
- Do not build a live transcript-follow UI in this work.

## Core Model

Separate the surface into three concepts:

```text
job_status(job_id)
  -> compact supervision facts

read_transcript(transcript_ref, ...)
  -> raw or summarized evidence for that job

completion notification
  -> the normal way the parent learns a background job is done
```

The parent should not need to read the transcript to know whether a job is
alive. `job_status` should provide enough compact state for patience and
diagnosis. Transcript reads are explicit peeks at raw evidence.

## Job Kinds

Expose two public job kinds:

| Kind | Meaning |
|---|---|
| `agent` | A Serf agent/delegate turn with a session transcript. |
| `shell` | A shell process with stdout/stderr/process events. |

Internally, existing delegate jobs map to public `kind: "agent"`. The public
surface should not force parents to reason about the implementation name
"delegate" when the important fact is that the job has an agent transcript.

## Lifecycle Status

Keep status as the durable lifecycle state:

| Status | Terminal | Meaning |
|---|---:|---|
| `running` | no | Runtime work is still expected. |
| `completed` | yes | Work ended successfully. |
| `failed` | yes | Work ended with an error or non-zero result. |
| `cancelled` | yes | Work was cancelled by an owner or caller. |
| `stopped` | yes | Work stopped because the runtime could not continue, such as timeout or lost runtime. |

Do not add a separate `completed` boolean. `status` already carries that
information, and a boolean would erase the distinction between failed,
cancelled, and stopped jobs.

## Execution Phase

`phase` is separate from lifecycle status. It describes the current observable
execution state while a job is running.

Agent phases:

| Phase | Meaning |
|---|---|
| `starting` | The agent job has been created but has not emitted useful transcript events yet. |
| `awaiting_model` | A model request is in flight and no stream event is currently being emitted. |
| `model_streaming` | The model is actively emitting response events. |
| `tool_running` | One or more tool calls are executing. |
| `stopping` | Stop/cancel has been requested and cleanup is underway. |

Shell phases:

| Phase | Meaning |
|---|---|
| `starting` | The process is being launched. |
| `process_running` | The process is running. |
| `stopping` | Stop/cancel has been requested and cleanup is underway. |

Avoid `model_thinking`. Serf does not observe thinking. It observes a request in
flight, stream events, tool calls, and durable transcript/log events.

For terminal jobs, `phase` may be omitted. The lifecycle `status` is the useful
state after completion.

## Time Fields

Use elapsed durations as the primary model-facing timing fields:

| Field | Applies To | Meaning |
|---|---|---|
| `running_for_ms` | running jobs | Milliseconds since the current job started. |
| `duration_ms` | terminal jobs | Milliseconds from job start to terminal end. |
| `quiet_for_ms` | running jobs | Milliseconds since the last observed event for this job. |
| `started_at` | all jobs | Start timestamp for audit/correlation. |
| `ended_at` | terminal jobs | End timestamp for audit/correlation. |
| `last_event_at` | jobs with events | Timestamp form of the event used to compute `quiet_for_ms`. |

`quiet_for_ms` is not "idle for". It does not mean the agent ended its turn, and
it does not mean the job is doing no work. It means Serf has not observed a
progress event for that job for this long.

Observed events:

| Kind | Events That Reset `quiet_for_ms` |
|---|---|
| `agent` | transcript append, model stream event, tool call start, tool call result, status/phase transition |
| `shell` | stdout bytes, stderr bytes, process start, process exit, status/phase transition |

When a running job has no event after launch, `last_event_at` is `started_at`
and `quiet_for_ms` is time since start.

## `job_status` Tool

Add `job_status` as the compact, read-only supervision tool.

Input:

```json
{
  "job_id": "job_..."
}
```

Running agent result:

```json
{
  "job_id": "job_01K...",
  "kind": "agent",
  "status": "running",
  "phase": "tool_running",
  "running_for_ms": 184000,
  "quiet_for_ms": 2400,
  "started_at": "2026-06-23T10:31:14.120000-07:00",
  "last_event_at": "2026-06-23T10:34:15.720000-07:00",
  "transcript_ref": "local:01K..."
}
```

Running shell result:

```json
{
  "job_id": "job_01K...",
  "kind": "shell",
  "status": "running",
  "phase": "process_running",
  "running_for_ms": 184000,
  "quiet_for_ms": 2400,
  "started_at": "2026-06-23T10:31:14.120000-07:00",
  "last_event_at": "2026-06-23T10:34:15.720000-07:00",
  "transcript_ref": "job:job_01K..."
}
```

Terminal result:

```json
{
  "job_id": "job_01K...",
  "kind": "agent",
  "status": "completed",
  "duration_ms": 221300,
  "started_at": "2026-06-23T10:31:14.120000-07:00",
  "ended_at": "2026-06-23T10:34:55.420000-07:00",
  "last_event_at": "2026-06-23T10:34:55.420000-07:00",
  "transcript_ref": "local:01K..."
}
```

`job_status` should not include `notification_status` in the normal result.
Notification delivery is system machinery. Exposing it to the model invites
micromanagement and second-guessing. If terminal notification delivery fails,
surface that as an exceptional warning or diagnostic field, not as routine state.

## Transcript References

Every job status result includes exactly one `transcript_ref`.

| Job Kind | `transcript_ref` Shape | Reader Behavior |
|---|---|---|
| `agent` | existing session transcript ref, such as `local:<session_id>` | Read the child session transcript. |
| `shell` | job transcript ref, such as `job:<job_id>` | Read the shell job's stdout/stderr/process-event transcript. |

Do not wrap this in an `artifact` object. A single reference is the useful value:
the parent can hand it directly to the transcript reader.

If future work needs multiple readable artifacts per job, add explicit fields at
that time. Do not add a future-proof wrapper now.

## Transcript Reader

Make the transcript reader the raw evidence path for both job kinds. The exact
tool name can be decided during implementation, but the public model should be:

```text
read_transcript(transcript_ref, ...)
```

The existing `read_session_transcript` contract can either be renamed or
replaced by a generic transcript reader. The important behavior is that a parent
does not need a special job-output tool to inspect raw job evidence.

Reader requirements:

- Accept agent session refs and shell job refs.
- Support a compact default view.
- Support raw JSONL/debug view for forensics.
- Support bounded ranges such as `last:40`.
- For shell refs, preserve stdout/stderr distinction in raw form.
- For shell refs, provide a human-readable default that behaves like a log tail,
  not like a chat conversation.

The reader may render different transcript kinds differently. The unification is
the reference-passing model, not the internal storage format.

## Shell Job Transcript

Shell jobs should expose a transcript-like log consisting of process and stream
events, for example:

```jsonl
{"type":"process_started","time":"...","command":"go test ./..."}
{"type":"stdout","time":"...","text":"ok primeradiant.com/serf/agent"}
{"type":"stderr","time":"...","text":"..."}
{"type":"process_exited","time":"...","exit_code":0}
```

The implementation can project this from the existing output store at first if
that is the smallest safe change. The public contract should still call it a
transcript ref so the parent has one raw-read path.

## Completion Notifications

Completion notifications remain the normal completion path.

The parent should not poll `job_status` waiting for `completed`. It should use
`job_status` when it needs orientation, such as:

- "What kind of job is this?"
- "Is it still running?"
- "What phase is it in?"
- "How long has it been quiet?"
- "What transcript can I inspect?"

Terminal notifications should carry enough bounded result context that the
common case does not need an immediate transcript read:

| Kind | Notification Excerpt |
|---|---|
| `agent` | bounded excerpt of the final report/communicate result |
| `shell` | bounded tail of stdout/stderr output and exit code |

## `job_list`

`job_list` should align with `job_status`.

Each row should include the same supervision fields, possibly with a shorter
shape:

```json
{
  "job_id": "job_01K...",
  "kind": "agent",
  "status": "running",
  "phase": "awaiting_model",
  "running_for_ms": 184000,
  "quiet_for_ms": 12000,
  "transcript_ref": "local:01K..."
}
```

This lets the parent re-orient without immediately calling `job_status` for
every visible job.

## Removing `job_read_output`

The target model-facing surface should not include `job_read_output`.

Replace its use cases as follows:

| Old Use | New Use |
|---|---|
| Check status | `job_status` or `job_list` |
| Retrieve final delegate result | completion notification, then transcript if audit is needed |
| Inspect active delegate work | `job_status` then `read_transcript(transcript_ref)` |
| Tail shell output | `job_status` then `read_transcript(transcript_ref, range="last:...")` |
| Grep shell output | transcript reader grep/search support, or a future scoped log-search tool |
| Wait for "ready" text | explicit watch/wait mechanism over the shell transcript, not a read-output tool |

The one behavior that needs a deliberate replacement is "wait until shell output
matches a string". That is useful for dev servers and should survive, but it
should not be attached to a tool named "read output" if the design goal is to
teach notification-driven completion and status-driven supervision.

Replacement:

Use the existing `job_watch(output_match=...)` mechanism for readiness signals.
Do not add a blocking transcript wait. If a caller needs raw evidence, it can
read the `transcript_ref`; if it needs a future signal, it installs a watch and
continues from the notification.

Do not keep `job_read_output` merely because it currently carries this wait
behavior. That is how the current abstraction became overloaded.

## Prompt and Tool Description Changes

The background-jobs prompt should teach this decision tree:

```text
Need the result now from a quick command?
  Run foreground shell.

Need a long job to finish?
  Start it and trust the completion notification.

Need to know what is happening?
  Use job_status.

Need raw evidence?
  Read the transcript_ref from job_status.

Need a recurring or one-shot output condition?
  Use the watch/wait primitive for transcript events.
```

Tool descriptions should avoid saying that active delegate work uses job output
as evidence. For agent jobs, the transcript is the evidence surface.

## Error Handling

`job_status` errors:

| Case | Behavior |
|---|---|
| Unknown job id | `not_found` with a short message. |
| Job exists but is not visible to this session | `not_found` or `permission_denied`, following current Serf visibility conventions. |
| Transcript ref unavailable for a running job | Return status plus a warning; this should be rare and should be treated as a bug to investigate. |
| Phase cannot be determined | Omit `phase` or return `phase: "unknown"` with a warning; do not fail the whole status read. |

Transcript reader errors:

| Case | Behavior |
|---|---|
| Unknown transcript ref | `not_found`. |
| Unsupported transcript ref kind | `invalid_request`. |
| Shell log partially evicted | Return retained content with explicit evicted/dropped counters. |

## Observability and Quiet Watchdog

The quiet-job watchdog should use the same event clock as `quiet_for_ms`.

For agent jobs, this means the watchdog resets on transcript/progress events,
not only on final output. A child that is reading files, running tools, or
streaming model output is not quiet merely because its final report has not been
written.

The quiet notification should point at `job_status` and the `transcript_ref`, not
at `job_read_output`.

Example:

```text
job_01K... has been quiet for 10m.
status=running phase=awaiting_model transcript_ref=local:01K...
Use job_status for current state or read_transcript for evidence.
```

## Testing Strategy

Before implementation, read `docs/testing.md`.

Required tests should exercise real Serf plumbing with scripted providers:

- Running agent job status returns `kind: "agent"` and a usable
  `transcript_ref`.
- Agent `quiet_for_ms` advances from the last transcript/progress event, not
  from job output bytes.
- Agent tool execution updates `phase: "tool_running"` and resets quiet time.
- Agent model request/stream updates distinguish `awaiting_model` from
  `model_streaming` where the provider harness can expose both states
  deterministically.
- Terminal agent job returns terminal `status`, `duration_ms`, `ended_at`, and
  keeps the transcript ref.
- Running shell job status returns `kind: "shell"` and a shell transcript ref.
- Shell stdout/stderr events reset quiet time and can be read through the
  transcript reader.
- `job_list` rows include enough status fields to avoid a follow-up status call
  in the common case.
- Tool descriptions and prompt text discourage polling for completion and route
  diagnosis through `job_status` plus transcript reads.

Default tests must not require provider credentials, network access, quota, or
live model behavior.

## Rollout Plan

1. Add `job_status` and align `job_list` rows with the new status shape.
2. Expose `transcript_ref` for shell jobs and teach the transcript reader to
   resolve shell refs.
3. Update quiet activity tracking to use transcript/progress events for agent
   jobs.
4. Update background-job prompt and tool descriptions to make `job_status` the
   supervision primitive.
5. Move final-result retrieval to completion notifications and transcript reads.
6. Replace the useful `job_read_output` wait/search behaviors with transcript
   watch/wait/search behavior.
7. Remove `job_read_output` from the model-facing tool surface.

Each step should land with focused tests. If a step reveals that one behavior is
larger than expected, split that behavior rather than re-expanding
`job_read_output`.

## Open Decision

One API naming decision remains before implementation:

| Option | Trade-Off |
|---|---|
| Rename `read_session_transcript` to `read_transcript` | Cleanest user-facing model, but touches every prompt/tool reference. |
| Keep `read_session_transcript` and broaden it to shell refs | Smaller code change, but the name remains wrong for shell jobs. |

Recommendation: use `read_transcript` for the new public design. If
implementation sequencing needs an intermediate state, keep it short-lived and
do not document the old name as the desired model.
