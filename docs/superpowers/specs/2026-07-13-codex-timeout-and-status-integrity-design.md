# Codex Timeout and Status Integrity Design

**Date:** 2026-07-13
**Status:** Approved

## Problem

Serf now bounds the wait for streaming response headers, but the resulting
timeout is classified like a retryable timeout. The session retry policy can
therefore submit the same model-generation request again after the first POST
was fully written and the provider spent ten minutes processing it. That is an
ambiguous outcome: the provider may have accepted the generation even though
Serf never received its headers. Replaying it can multiply a single stall into
many ten-minute waits and can create duplicate generations.

Session `01KXF0CAQQYX73BMT40NJKPXZN` establishes the useful bound and the
failure mode. One private Codex request returned usable output after about
9 minutes 26 seconds, while a later request remained in the pre-header phase.
The private endpoint rejects both `store: true` and `background: true`, so the
public Responses API background workflow is not available as an escape hatch.

The same live workflow exposed contradictory status projections that must be
investigated after the transport fix:

- running subagent sessions can be shown as `ended`;
- the root session can be shown as `Working` after it has stopped doing work.

Those observations are requirements for a root-cause investigation, not proof
that both symptoms share one cause.

## Goals

- Preserve one ten-minute response-header window for a streaming model call.
- Never automatically replay a model-generation POST after it was fully written
  and then timed out waiting for response headers.
- Preserve retries for failures known to be safe under the existing contract,
  including HTTP 429 and retryable 5xx responses.
- Preserve caller cancellation, connection timeout, total request timeout, and
  per-stream-read timeout semantics.
- Keep provider waits interruptible and return the session to a truthful
  settled state when the attempt ends.
- After the timeout behavior is verified, trace root and subagent status from
  their authoritative runtime records through daemon projection and the hub,
  then fix the confirmed source of each contradiction.
- Cover both fixes with deterministic tests below the provider boundary.

## Non-Goals

- Adding public Responses API background mode to the private Codex endpoint.
- Shortening the ten-minute response-header window without new latency data.
- Removing ordinary retries or changing their backoff policy.
- Preempting a healthy model call when a child job completes.
- Treating an `ended` label or a `Working` label as sufficient evidence of the
  faulty layer.
- Broadly redesigning session or job lifecycle state.

## Phase 1: Ambiguous Response-Header Timeout

### Decision

Keep `AdapterTimeout.Request` as the streaming response-header timeout described
by the existing streaming timeout design. Change only the classification of the
specific failure that occurs after the request was written and before response
headers arrived.

The standard HTTP transport wrapper will observe the request lifecycle with
`httptrace`:

1. Record successful completion of `WroteRequest`.
2. Let the cloned `http.Transport` enforce `ResponseHeaderTimeout`.
3. If the transport returns a timeout, the original request context is still
   live, and the request was written, wrap it as a response-header timeout.
4. Preserve the original timeout as the cause so timeout inspection and
   diagnostic metadata remain available.

The phase check does not depend on Go's timeout error text. A caller-owned
context deadline remains caller-owned because its context is no longer live at
classification time. An opaque injected `RoundTripper` remains authoritative,
as in the existing timeout contract.

The response-header timeout is an `llm` timeout error but is not retryable. It
is intentionally distinct from:

| Failure | Existing retry behavior |
|---|---|
| Caller cancellation or exhausted total budget | Do not retry |
| Connect or pre-submission transient failure | Preserve current classification |
| HTTP 408 response | Retry |
| HTTP 429 response | Retry |
| Retryable HTTP 5xx response | Retry |
| Written request times out before response headers | Do not retry |
| Stream fails after headers | Preserve current partial-stream behavior |

`WrapContextError` must preserve an already-classified `llm.Error` rather than
turning the new response-header timeout back into the generic retryable timeout.
This keeps retry policy centralized in the existing `llm.Error` contract.

### Session outcome

When the timeout surfaces, the current model attempt ends once. The normal
session error boundary records it, clears the active processing state, and
leaves the session interruptible throughout the wait. No special session-loop
retry or status workaround is added.

## Phase 2: Status Integrity

Phase 2 begins only after Phase 1 is implemented and its focused tests pass.
It follows the state, timestamps, and events rather than starting from UI text:

```text
runtime session/job state
        |
        v
daemon status and event stream
        |
        v
appwire thread/job projection
        |
        v
hub roster and visible labels
```

For the root session, `Working` is valid while that session is processing or
has queued work ready to resume it. A live child is work by the child, not by
the settled parent. Once the root turn has settled, a live child alone must not
keep the root row active; a pending completion notification or queued input may
still upgrade it because those are root work waiting to run.

For a subagent, `ended` is valid only after the authoritative child lifecycle is
terminal. A missing daemon attachment, a historical transcript boundary, or a
stale projected snapshot must not override evidence that the child job/session
is still running.

### Observed root causes

The saved artifacts and live daemon projection locate both contradictions:

1. The running delegate job `job_01KXFF39FMYA691F9H8JFD52JY` reported
   `status: running` and transcript ref
   `local:01KXFF39S6STGMB2EKGNBRBBDJ` in the parent daemon's `/status` result.
   The child transcript and meta continued updating, but the child had no
   rendezvous entry because delegate sessions run inside the parent process.
   `BuildTreeAt.stateFor` treats every session ID absent from the rendezvous
   live map as `ended`, so a running in-process child was necessarily rendered
   terminal.
2. The parent's internal turn had settled, but `Session.WireState` upgraded an
   idle session to `active` whenever `autonomyInFlight` found a live child.
   That intentionally assigned the child's activity to the parent row, which
   is why the root reported `Working` without an active root turn.

### Decision

Transfer the activity signal to the actor that owns it:

- Extend the hub's existing `/status` probe result with running delegate
  transcript session IDs. The data already exists in `detailed.jobs`; only
  delegate jobs in the non-terminal `running` state with valid local transcript
  refs qualify.
- Carry those IDs on the parent `LiveEntry`. Tree and past-thread projections
  use that parent-owned evidence to mark the corresponding in-process child
  `active`; they do not invent a child daemon or route child actions to the
  parent's endpoint.
- Stop upgrading an idle parent's wire state solely for a live child. Keep the
  active override for pending job notifications and queued input, and retain
  live children in the broader settle/restore autonomy checks.
- Preserve a project's working rollup while any displayed child is active,
  counting the parent task tree once rather than inflating the rollup by the
  number of children.

When the child reaches a terminal job state it disappears from the next probe's
running-child set. The child then projects from persisted history as ended, and
the parent becomes active only if its completion notification is waiting or its
next turn has begun.

## Testing

Before changing tests, implementation work must follow `docs/testing.md`.
Tests must use local transports, scripted providers, deterministic lifecycle
fixtures, and structured state. They must not use provider credentials, live
network calls, arbitrary long sleeps, or large rendered-string assertions.

Phase 1 must prove:

1. A streaming POST received by a local server and held before headers times out
   once and returns a non-retryable timeout error.
2. The generation retry loop submits exactly one request for that failure.
3. A response-header timeout remains identifiable through error wrapping and
   still unwraps to the underlying timeout.
4. HTTP 408, 429, and retryable 5xx behavior remains retryable.
5. A healthy stream is not subject to the response-header timer after headers.
6. Caller cancellation and total-budget deadlines retain their existing types
   and do not become response-header timeouts.

Phase 2 must prove the contracts at the first faulty boundary and at one
end-to-end projection boundary:

1. A running subagent remains non-terminal until its authoritative terminal
   transition.
2. A settled root with only a live child is idle while that child is active.
3. A pending notification or queued input still upgrades an idle root to active.
4. Tree, thread-list, and thread-read projections agree on the child's active
   state, while project rollups count its parent task tree once.

Focused package tests run first after each phase. The final verification runs
the relevant `llm`, `agent`, daemon/appwire, and hub tests, followed by the
repository's normal broader test target if focused evidence passes.

## Rollout and Evidence

The work is committed in small units:

1. design and test plan;
2. Phase 1 failing tests and implementation;
3. Phase 2 diagnosis evidence, failing tests, and minimal fix or fixes;
4. final verification.

The handoff reports the exact commits, test commands and results, the live or
saved-artifact evidence locating each root cause, and any remaining limitation.
