# Subagent Run Rendering and Job Notification Linkage Design

Date: 2026-06-25
Status: Approved design

## Context

Serf currently shows delegate/subagent execution inconsistently across the web UI and TUI:

- Some subagent/job notifications show long raw `job_...` identifiers; some have no useful identifier at all.
- Job notifications are not reliably tied back to the originating `delegate` or `delegate_send` tool invocation.
- The original delegate tool call often remains visually disconnected from later job completion.
- The web UI already has an aggregated subagent module, but it reconciles mostly by `jobId` and can create orphan rows when ordering is unfavorable.
- The TUI renders delegate/job activity as ordinary tool rows and does not currently apply `serf/job/started` or `serf/job/finished` notifications to update delegate rows.
- The desired UI is a coherent inline subagent execution box that can show status, friendly identity, and a bounded preview of the last few child steps.

The durable backend model already carries much of the linkage needed for this: job records know `DelegateID`, `TranscriptRef`, task, and origin tool-call information. The current AppWire job notifications drop most of that linkage before clients see it.

The design below allows backend/AppWire changes as long as they are additive and do not break Codex app server compatibility.

## Goals

1. Tie delegate/subagent job lifecycle notifications to the originating delegate execution UI.
2. Replace raw long IDs as primary labels with task/status-oriented display and short/copyable IDs.
3. Let web and TUI share the same conceptual model for subagent runs.
4. Update delegate boxes when subagent jobs finish, including after background completion.
5. Support bounded inline child-step previews without dumping full child transcripts into notifications.
6. Preserve existing Codex-compatible AppWire shapes and notification methods.

## Non-goals

- Do not replace jobstore or the existing job notification machinery.
- Do not make Codex-compatible clients depend on Serf-specific fields.
- Do not recursively inline whole subagent transcript trees.
- Do not treat a subagent that reports bad findings as a failed job unless the job itself failed to run.

## Architecture

Introduce a first-class **SubagentRun** projection as the shared Serf UI concept. A SubagentRun has three identities:

- `delegateId`: the stable conversation handle. It survives `delegate_send` follow-up turns.
- `jobId`: the concrete execution attempt. It changes for resumed delegate runs.
- origin tool identity: the parent transcript location that should visually own the run, using `originToolCallId` and, when available, projected `originTurnId` / `originItemId`.

Backend owns truth and linkage. UI owns presentation.

- Backend/jobstore/AppWire identify which job belongs to which delegate and originating parent tool invocation.
- Web/TUI render SubagentRun boxes, shorten IDs, reconcile ordering races, and fetch optional previews.

## Compatibility boundary

Keep existing Codex-compatible `Thread`, `Turn`, `ThreadItem`, and notification methods intact. Changes are additive and Serf-specific.

Existing methods remain:

- `serf/job/started`
- `serf/job/finished`

Existing required fields remain valid. New clients use optional linkage fields; older clients ignore them.

Prefer enriching existing `SerfJobInfo` first. Add a dedicated Serf-only `serf/delegate/updated` notification only if enriched job notifications prove insufficient for updating the originating tool item cleanly.

## AppWire and backend data model

Extend `appwire.SerfJobInfo` with optional delegate linkage fields:

```go
type SerfJobInfo struct {
    JobID         string `json:"jobId"`
    JobType       string `json:"jobType"`
    Status        string `json:"status"`
    Reason        string `json:"reason,omitempty"`
    ExitCode      *int   `json:"exitCode,omitempty"`
    OutputBytes   int64  `json:"outputBytes"`
    TranscriptRef string `json:"transcriptRef,omitempty"`
    FromWatch     bool   `json:"fromWatch,omitempty"`

    // New optional Serf delegate linkage fields.
    DelegateID       string `json:"delegateId,omitempty"`
    Task             string `json:"task,omitempty"`
    OriginToolCallID string `json:"originToolCallId,omitempty"`
    OriginTurnID     string `json:"originTurnId,omitempty"`
    OriginItemID     string `json:"originItemId,omitempty"`
}
```

Internally, treat these fields as a `SubagentRunInfo` projection even if the public wire shape is `SerfJobInfo`:

```go
type SubagentRunInfo struct {
    DelegateID       string `json:"delegateId,omitempty"`
    JobID            string `json:"jobId"`
    JobType          string `json:"jobType"`
    Status           string `json:"status"`
    Reason           string `json:"reason,omitempty"`
    Task             string `json:"task,omitempty"`
    TranscriptRef    string `json:"transcriptRef,omitempty"`
    OriginToolCallID string `json:"originToolCallId,omitempty"`
    OriginTurnID     string `json:"originTurnId,omitempty"`
    OriginItemID     string `json:"originItemId,omitempty"`
    OutputBytes      int64  `json:"outputBytes,omitempty"`
}
```

### Backend projection rules

- Populate delegate fields from the live `run.rec` / folded job record, not only from the flattened event payload.
- Include `transcriptRef` on job start for delegate jobs, not only on finish.
- Include `task` so clients do not fall back to raw IDs or generic `delegate` labels.
- Include origin tool identity when known.
- Shell and other non-delegate jobs continue to omit delegate-only fields.

## Web UI design

The browser renderer should maintain a canonical `SubagentRun` map, keyed primarily by `jobId` and secondarily by `delegateId` when available:

```js
{
  delegateId,
  jobId,
  task,
  status,
  reason,
  transcriptRef,
  originToolCallId,
  originItemId,
  outputBytes,
  previewItems: [],
  rowEl,
  boxEl,
}
```

### Rendering

Render a durable subagent execution box at the delegate invocation site or inside the existing fan-out `.subs` container. The current `.subs` module can remain as the grouping container, but each row should behave like a stable execution component rather than a detached notification artifact.

Collapsed display should emphasize:

- status glyph and status text;
- task/label as the primary title;
- optional short machine identity as secondary metadata, e.g. `job 01KW0…P453`;
- duration;
- child transcript link/open-beside affordance;
- failure/unknown state at both row and group level.

Expanded/details display should include full copyable values:

- `delegateId`
- `jobId`
- `transcriptRef`
- origin call/item IDs
- reason/output bytes
- preview loading state

### Signal merging

Merge all of these into the same SubagentRun:

- delegate `TOOL_CALL_START` / `TOOL_CALL_END` tool state;
- `serf/job/started`;
- `serf/job/finished`;
- `delegate_send` / `job_read_output` / `job_list` reconciliation data;
- cold transcript reconciliation data after reload.

Do not duplicate rows when the same `jobId` appears from multiple signals.

## TUI design

Extend the TUI transcript model so delegate metadata is structured, not only parsed from raw output text:

```go
type ToolCallInfo struct {
    // existing fields...
    Subagent *SubagentRunInfo
}
```

Handle `NotifySerfJobStarted` and `NotifySerfJobFinished` in `hub_notifications.go`.

Resolution order for updating a row:

1. `originItemId`
2. `originToolCallId`
3. `jobId`
4. `delegateId`

TUI collapsed rows should use task/status as the primary display and short IDs as secondary metadata. Expanded bodies can include full IDs and transcript refs.

TUI `/status` and details drawer should not dump long raw IDs as the primary job label. Use short IDs in list rows and full IDs in expanded/detail contexts.

## Live data flow

### Delegate start

1. Parent model calls `delegate`.
2. Backend creates `delegateId` and `jobId`, persists delegate/job start events, and emits `JOB_STARTED`.
3. AppWire projects `serf/job/started` with enriched `SerfJobInfo`.
4. UI creates or updates a SubagentRun.

Ordering cases:

- If `JOB_STARTED` arrives before delegate `TOOL_CALL_END`, UI shows a provisional SubagentRun keyed by `jobId` / `delegateId`; later tool state enriches the same run.
- If delegate `TOOL_CALL_END` arrives before `JOB_STARTED`, tool state seeds the run; later job start enriches it.

### Delegate finish

1. Child completes and parent job manager finalizes the delegate job.
2. Backend persists `job_finished` and emits `JOB_FINISHED`.
3. AppWire projects `serf/job/finished` with linkage and terminal status.
4. UI updates the same SubagentRun:
   - running becomes completed, failed, stopped, cancelled, or unknown;
   - duration freezes;
   - result/status summary updates;
   - transcript link remains available;
   - full IDs remain in details, not primary labels.

### Delegate follow-up

`delegateId` is stable; `jobId` is per execution.

A `delegate_send(on_idle=start)` creates a new job under the same delegate. UI should represent this as:

- one group per `delegateId`;
- one row/card per `jobId` within that group.

This preserves history and avoids overwriting a terminal run with a later resumed run.

## Cold/reloaded transcript flow

Cold replay has two relevant inputs:

- transcript tool state, which may say a delegate job was `running` when the parent model saw the tool result;
- jobstore folded state, which may now say the job is terminal.

Thread read/projection should reconcile these before serving the thread:

1. Project the delegate tool call/result from transcript history.
2. Overlay folded jobstore state for known `jobId` / `delegateId` records.
3. Serve `ThreadItem.Raw` or equivalent SubagentRun metadata with terminal status when known.

A reload must not resurrect stale spinners.

## Inline child-step preview

Do not stuff unbounded child transcript text into every job notification.

Preferred design:

1. SubagentRun carries `transcriptRef` and preview state.
2. UI lazy-loads a bounded preview when the box is visible or expanded.
3. Backend returns only the latest N child items/turn summaries, default 3-5.
4. UI renders these snippets under the SubagentRun box and links to the full transcript for everything else.

The preview API can reuse existing transcript read/paging machinery if it can efficiently request the latest bounded child window by `transcriptRef`; otherwise add a small Serf-specific RPC for this purpose.

Preview content must be projected/sanitized like ordinary transcript items. Nested previews should not recursively inline grandchildren by default.

## Error handling and degraded states

### Missing linkage

Resolution order:

1. `originItemId` / `originToolCallId`
2. `jobId`
3. `delegateId`
4. `transcriptRef` for view-link attachment only
5. orphan rendering

An orphan job card should be clearly marked and should use a short ID:

```text
delegate job completed · job 01KW0…P453
```

Full metadata remains available in details.

### Stale running state

A subagent must not spin forever.

If the parent session is no longer live and a run has no terminal job signal, demote it to neutral `unknown`:

- glyph: `?`
- text: `never reported finishing`
- duration: approximate/frozen
- details: last known job/delegate metadata

Do not label this as failed unless the job status is failed.

### Preview failure

Preview loading is optional and soft-failing:

- keep the SubagentRun status intact;
- show `preview unavailable` only in expanded preview UI;
- do not block transcript rendering;
- do not mark the subagent failed unless the job itself failed.

### Duplicate and out-of-order signals

All SubagentRun updates are idempotent merges:

- terminal status wins over running for the same `jobId`;
- a new `jobId` under the same `delegateId` creates a new run row;
- repeated finish events update details but do not duplicate rows;
- `delegate_send` reconciliation does not downgrade a terminal job to running unless it names a new started job.

## ID display rules

Across web and TUI:

- Never use a raw long `job_...` or `dlg_...` as a primary label when task, job type, or transcript title exists.
- Use short or middle-truncated IDs in collapsed rows.
- Keep full IDs in expanded details, tooltips, datasets, or copyable fields.
- Use consistent labels: `delegate`, `job`, `transcript`, not mixed `subagent notification` / `job notification` wording in UI labels.

## Testing strategy

### Backend/AppWire projection tests

Extend job event projection tests to verify:

- delegate `JOB_STARTED` includes optional linkage fields;
- delegate `JOB_FINISHED` includes the same linkage plus terminal fields;
- shell jobs omit delegate fields;
- method names and existing required fields remain unchanged;
- new fields are `omitempty`.

Add job manager/delegate tests proving event emission uses `run.rec` linkage.

### Cold transcript reconciliation tests

Cover:

- transcript says `running`, jobstore says `completed`; served thread shows terminal status;
- missing jobstore record leaves original status and marks linkage incomplete;
- reload does not duplicate subagent rows from transcript plus jobstore.

### Web renderer tests

Add or extend JS tests for:

1. `JOB_STARTED` before delegate `TOOL_CALL_END` creates one run, then enriches it.
2. Delegate `TOOL_CALL_END` before `JOB_STARTED` produces the same final UI.
3. `JOB_FINISHED` updates the originating delegate execution box.
4. Long IDs are not primary labels; full IDs remain available in metadata/details.
5. `delegate_send` starts a second run under the same delegate group.
6. Missing linkage creates an orphan card, not a fake linked subagent.
7. Bounded preview renders child snippets without duplicating the whole transcript.

### TUI tests

Add or extend tests for:

- applying `NotifySerfJobStarted` / `NotifySerfJobFinished` to an existing delegate `MsgTool`;
- fallback update by `jobId`;
- short collapsed IDs and full expanded IDs;
- terminal notifications stop running display;
- stale-running demotion to `unknown`;
- `/status` and details drawer use short IDs as primary labels.

### Protocol compatibility tests

Assert:

- old `SerfJobInfo` fields still marshal and unmarshal;
- new fields omit cleanly when empty;
- clients decoding only old fields can parse enriched payloads;
- Codex source mapping remains unaffected.

### Scenario/E2E coverage

Add one deterministic scripted scenario:

1. Parent starts a background delegate.
2. UI receives start.
3. Child completes after parent turn.
4. Parent receives job notification.
5. Web/TUI transcript shows one coherent subagent execution box that becomes terminal and links to the child transcript.

Use scripted providers only. Default tests must not require provider credentials, network access, quota, or live model behavior.

## Implementation phasing

1. Add backend/AppWire linkage fields and projection tests.
2. Add web `SubagentRun` state model and update merge/render behavior.
3. Add TUI notification handling and structured subagent metadata on tool rows.
4. Add cold transcript/jobstore reconciliation.
5. Add bounded child preview loading.
6. Tighten ID display across job/delegate/status surfaces.

The first four phases solve the correctness problem. Preview and broader ID-polish can follow once the linkage is reliable.
