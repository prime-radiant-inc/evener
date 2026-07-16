# Job Supervision Surface Cleanup Design

Date: 2026-07-15
Status: Approved
Tracker: #20

## Purpose

Serf currently exposes overlapping ways to check a job, read its output, wait for
activity, and inspect its transcript. The overlap encourages repeated reads and
large output replay. Serf already has the intended primitives: durable job state,
`job_status`, transcript references, bounded transcript reading, watches, and
terminal notifications.

This design removes the redundant model-facing path and makes completion
notification-driven.

## Model-Facing Contract

| Need | Tool |
|---|---|
| Current state, timing, phase, terminal reason | `job_status(job_id)` |
| Compact inventory of jobs/delegates | `job_list` |
| Shell output or delegate conversation evidence | `read_transcript(transcript_ref)` |
| Future readiness/completion notification | `job_watch` or built-in terminal notification |
| Stop work | `job_stop` |

`job_read_output` is removed from the model-facing tool definitions and dispatch
surface. There is no compatibility alias.

Internal output-store helpers may remain where `read_transcript` and watches use
them; the obsolete public result shape and prompt references are deleted.

## `job_status`

`job_status` stays compact. It returns state and orientation, not output:

- job ID and kind;
- lifecycle status, execution phase, and terminal reason;
- running/duration/quiet timing;
- start, last-event, and end timestamps;
- transcript reference;
- shell exit code when applicable;
- resumability for delegate jobs;
- exhausted status from the budget-truthfulness design when that slice has
  landed.

It does not include retained output prefixes, raw transcript turns, or terminal
notification delivery internals.

## Transcript-Based Output Reading

`read_transcript` accepts `job:<job_id>` references for shell output and session
references for delegates.

- Default reads are bounded and oriented to the newest evidence.
- Range/cursor parameters allow incremental reading without replaying the
  retained prefix.
- Results report total bytes, dropped bytes, returned byte range, and the next
  expansion cursor when more retained output exists.
- An unchanged cursor at end-of-log returns no new output rather than replaying
  the tail.
- Grep/future-match behavior belongs to `job_watch`, not transcript reading.

Session transcript reads follow the separate transcript/API-log design and do
not expose provider API bodies by default.

## Terminal Ordering

For every job kind:

1. Runtime work ends.
2. Serf persists the terminal job record and terminal generation.
3. Output/transcript state is flushed to the durable boundary.
4. Serf marks the terminal notification pending.
5. The parent receives the notification.
6. Serf records delivery.

A notification may never be the only evidence that a job completed. A crash at
any point replays from the durable state without rerunning completed work.

## Notification Coalescing

When several terminal notifications are pending before the parent next runs,
Serf may present them in one steering frame. Coalescing affects presentation
only:

- each job keeps its own terminal generation and pending/delivered state;
- the frame contains one compact section per job;
- delivery is recorded for every included notification only after the combined
  frame is durably queued;
- stale intermediate activity notices may be omitted when a newer terminal
  state for the same job is in the frame;
- terminal truth is never deduplicated across different jobs.

## Prompt and Tool Guidance

The job-control prompt teaches:

- rely on terminal notifications instead of polling;
- use `job_status` to orient when needed;
- follow the returned transcript reference for evidence;
- use bounded ranges/cursors for additional output;
- do not infer completion from quiet time.

All references to `job_read_output` are removed from prompts, tool descriptions,
examples, probes, and documentation.

## Testing

Use the durable job store and scripted shell/delegate runtimes.

Cover:

- `job_read_output` absent from callable tool definitions and dispatch;
- status contains no retained output;
- shell transcript ranges advance without replay and respect byte limits;
- dropped-output counters remain accurate;
- terminal state and output flush precede notification delivery;
- crash before delivery replays one pending notification;
- multiple terminal notifications coalesce into one frame while preserving
  per-job delivery state;
- no coalescing loses distinct terminal jobs;
- exhausted, failed, stopped, and completed jobs render distinctly;
- docs/tool-fluency probes use the new surface.

## Scope Lock

This spec does not:

- retain a model-facing `job_read_output` compatibility path;
- add a new gate-execution service;
- change shell process execution or retention limits;
- expose provider API logs through ordinary job transcripts;
- change watch trigger semantics;
- modify Superpowers.
