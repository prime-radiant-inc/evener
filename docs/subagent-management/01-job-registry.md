# Subagent Job Registry

Status: Proposed evergreen spec for doc 01 only. Current serf has a private in-memory, per-parent `subagentManager`; this spec defines the stable job-registry API/contract that implementation should grow into while preserving the existing subagent tools.

## Purpose

Make subagents first-class, queryable jobs owned by a parent session. A subagent job is not just the immediate return value of `spawn_agent`; it is the management record for a child `Session`, its current or most recent run, status, transcript reference, result-consumption state, and cleanup lifecycle.

This document is intended to become the evergreen user/developer reference after implementation. Until then:

- **Current** statements cite observed serf code.
- **Target** statements define the API/contract to implement.

## Goals

- Preserve current root-only `spawn_agent`, `resume_agent`, `wait`, and `close_agent` behavior.
- Add a small parent-scoped registry that can be listed without reading child transcripts.
- Make job state explicit enough for CLI/TUI/Hub/SDK clients to show running, completed, failed, cancelled/closing, closed/removed, and result-consumed states.
- Keep registry ownership scoped to one parent session. Do not add a global scheduler.
- Reuse existing `subagentManager`, `subagent`, `Session`, transcript, status, and event code paths.
- Keep child output out of the registry except for the current/last result metadata and `transcript_ref`.

## Non-goals

- No nested delegation. Subagent-management tools remain top-level/root-only.
- No workflow templates, DAG engine, or declarative orchestration layer.
- No new helper-subagent layer or lightweight helper-agent abstraction.
- No persistent distributed job queue.
- No automatic replay of child transcript content into parent context.
- No new process sandbox; execution isolation remains separate `execenv` work.
- No required tree-native session/fork history. Tree history may be useful later, but is optional and out of scope for this doc.
- No human approval workflow in this doc; capability/approval details belong in the separate capability spec.

## Current implementation anchors

Current serf already has the core pieces this registry should refine rather than replace:

- `agent/subagent_manager.go:9-23` defines `subagentManager` as the parent session's locked child map plus lifecycle-event emitter.
- `agent/subagent_manager.go:34-87` implements `track`, `get`, `remove`, `drainForClose`, and `infos`.
- `agent/subagents.go:23-42` defines current statuses and the result JSON shape: `status`, `output`, `success`, `turns_used`, `transcript_ref`.
- `agent/subagents.go:52-69` defines root-only management tool names and the private `subagent` fields, including `running`, `status`, `turnsUsed`, `done`, `result`, `err`, and `resultConsumed`.
- `agent/subagents.go:141-151` rejects child-origin `spawn_agent` calls and enforces max depth; child exposure for management tools is denied through the centralized root-only tool policy in `agent/subagents.go:52-130`, explicit `grant_tools` rejection in `agent/subagents.go:247-253`, and child registry filtering in `agent/session_init.go:467-470`.
- `agent/subagents.go:360-407` resumes idle children or steers running children.
- `agent/subagents.go:409-449` waits for completion, returns a result snapshot, and marks the result consumed.
- `agent/subagents.go:452-485` closes a child session, waits up to five seconds for any active run, returns a result snapshot on success, removes the child from the manager on success, and returns a timeout error without removal if the close wait expires.
- `agent/session_tools_subagent.go:14-170` registers the model-facing subagent tools.
- `agent/internal/tool/definitions.go:193-293` defines the current tool schemas and descriptions.
- `agent/status.go:27-32` defines current `SubagentInfo` with only `id`, `status`, and `turns_used`; `agent/status.go:106-107` fills detailed status from `s.subagents.infos()`.
- `agent/events/events.go:57-60` defines current `SUBAGENT_START` and `SUBAGENT_END` events.
- `agent/events/payloads.go:185-196` defines current event payloads.

Current public status DTO:

```go
type SubagentInfo struct {
    ID        string         `json:"id"`
    Status    SubagentStatus `json:"status"`
    TurnsUsed int            `json:"turns_used"`
}
```

## Concepts

### Job

A **job** is the parent-visible management record for one child `Session` and its current or most recent run.

A job has one stable `agent_id`. In current serf this is the raw child session ID. Keep that unless a migration proves necessary. The registry key and `transcript_ref` are related but not the same string today: `agent_id` is the raw ID, while `transcript_ref` is an encoded reference such as `local:<sessionID>`. The API should name them by purpose:

- `agent_id`: control-plane handle used by `resume_agent`, `wait`, `close_agent`, and `list_agents`.
- `transcript_ref`: diagnostic handle used by transcript-reading tools.

### Run

A **run** is one invocation of the child session:

- the initial run started by `spawn_agent`;
- a later run started by `resume_agent` when the child is idle;
- a steering message injected by `resume_agent` while the child is running does **not** create a new run.

YAGNI rule: do not create a public `run_id` in the first implementation. Add one later only if result history per resume becomes necessary. For now, job fields describe the current or last run.

### Scope and retention

- Registry scope is one parent session.
- Jobs are visible to their parent only.
- Completed/failed jobs remain visible until `close_agent`, parent session close, or an explicitly documented retention policy removes them.
- Current implementation removes entries on `close_agent` and drains children on parent close.
- Registry persistence across process restart is optional for v1. If added, it must be best-effort and parent-scoped; do not invent a global durable queue.

## Status values

Current implemented values are:

```text
running
completed
failed
```

Target v1 additive values:

```text
registered   created/tracked but not yet running; normally transient
running      child is actively processing a run
completed    last run ended without engine/provider/tool/session error
failed       last run ended with an engine/provider/tool/session error
closing      close requested and cleanup in progress, if exposed by the registry
closed       child session closed and retained as terminal history, if retention is enabled
```

Reserved future value:

```text
cancelled    current run was interrupted/cancelled but the child may remain resumable; do not emit in v1 unless a dedicated cancel/interrupt API defines the transition
```

Compatibility rule: existing `running|completed|failed` values remain valid. New values are additive. If v1 keeps current close behavior, closed jobs are removed rather than returned as `closed`.

## Exact API / contract

### Existing management tools remain unchanged

This registry does not replace existing tools. Their current contracts remain:

#### `spawn_agent`

Current schema fields from `agent/internal/tool/definitions.go:193-227`:

```json
{
  "task": "required string",
  "model": "optional string; default parent model",
  "max_turns": "optional integer; default 500",
  "agent_type": "optional string",
  "blocking": "optional boolean; default false",
  "reasoning_effort": "optional low|medium|high|xhigh",
  "grant_tools": ["optional string tool names"],
  "task_list": [
    {
      "title": "required string",
      "prompt": "required string",
      "reasoning_effort": "optional low|medium|high|xhigh"
    }
  ]
}
```

Non-blocking return:

```json
{"agent_id":"01CHILD...","status":"running"}
```

Blocking return: same result shape as `wait`, with `agent_id` added by `agent/session_tools_subagent.go:75-95` when the internal wait returns parseable result JSON; if wait fails before a snapshot, the current wrapper returns the wait error/result without an augmented envelope.

#### `resume_agent`

Current schema fields from `agent/internal/tool/definitions.go:231-260`:

```json
{
  "agent_id": "required string",
  "message": "required string",
  "blocking": "optional boolean; default false",
  "task_list": [
    {
      "title": "required string",
      "prompt": "required string",
      "reasoning_effort": "optional low|medium|high|xhigh"
    }
  ]
}
```

Current semantics:

- If the child is running, `resume_agent` steers it with `sub.sess.Steer(input)` and returns `"ok"` unless `blocking=true` waits for the current run.
- If the child is idle, it starts a new child run with preserved child history/context, resets run-local result fields, and returns `"ok"` unless `blocking=true` waits.

#### `wait`

Current schema from `agent/internal/tool/definitions.go:264-277`:

```json
{
  "agent_id": "required string",
  "timeout_ms": "optional integer"
}
```

Current result shape from `agent/subagents.go:35-42`:

```json
{
  "status": "completed|failed",
  "output": "subagent final report or error text",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Contract details:

- A wait timeout is not cancellation; the child continues running.
- Tool handler clamps positive timeouts below `120000` ms to `120000` ms (`agent/session_tools_subagent.go:152-160`).
- A successful wait consumes the current result. Repeated wait on an idle consumed result errors and tells the caller to resume or close (`agent/subagents.go:415-419`).
- `success` is `true` only when the child run ended without an error; failed runs return `success:false`.
- Use `transcript_ref`, not `transcript`, in this spec. The current tool description mentions `transcript`, but the actual result field is `transcript_ref`.

#### `close_agent`

Current schema from `agent/internal/tool/definitions.go:280-293`:

```json
{
  "agent_id": "required string"
}
```

Contract details:

- Close calls child `Session.Close()`.
- On successful close, it returns the same result snapshot shape as `wait`.
- On successful close, it removes the child from the active session list under current behavior.
- If the five-second close wait times out, current code returns an error and leaves the child tracked.
- It is the current cancellation/cleanup path for a running child; there is no separate cancel-but-keep-resumable API in this doc.

### New model-facing tool: `list_agents`

Add one root-only read-only model tool:

```json
{
  "name": "list_agents",
  "description": "List subagent jobs owned by this parent session. This is a read-only status API; it does not wait, resume, or close agents.",
  "parameters": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "status": {
        "type": "string",
        "enum": ["registered", "running", "completed", "failed", "closing", "closed", "all"],
        "description": "Optional status filter. Default all non-closed statuses. The `all` value is a filter sentinel, not a returned job status; `status=closed` implies `include_closed=true`."
      },
      "include_closed": {
        "type": "boolean",
        "description": "Include closed terminal records if retained. Default false unless `status=closed` is explicitly requested."
      }
    }
  }
}
```

Return:

```json
{
  "agents": [
    {
      "agent_id": "01CHILD...",
      "id": "01CHILD...",
      "status": "running",
      "task": "Inspect the auth module and report risks",
      "agent_type": "explorer",
      "parent_session_id": "01ROOT...",
      "turns_used": 1,
      "result_available": false,
      "result_consumed": false,
      "transcript_ref": "local:01CHILD...",
      "created_at": "2026-06-08T12:00:00Z",
      "started_at": "2026-06-08T12:00:01Z",
      "ended_at": null
    }
  ],
  "count": 1
}
```

Do not call this tool `list_jobs` unless all user-facing language moves from “agent” to “job”. Internally, names like `subagentJob` or `jobRecord` are acceptable.

`list_agents` must be denied to child sessions along with the existing root-only tools. The root-only list should become:

```go
[]string{"spawn_agent", "resume_agent", "wait", "close_agent", "list_agents"}
```

### Registry record

Required v1 record fields:

```json
{
  "agent_id": "string",
  "id": "string, backward-compatible alias equal to agent_id",
  "status": "registered|running|completed|failed|closing|closed (never the filter sentinel all)",
  "parent_session_id": "string",
  "agent_type": "string",
  "task": "string",
  "created_at": "RFC3339 string",
  "started_at": "RFC3339 string|null",
  "ended_at": "RFC3339 string|null",
  "turns_used": 0,
  "result_available": false,
  "result_consumed": false,
  "transcript_ref": "string"
}
```

Optional diagnostic fields may be omitted until cheaply available:

```json
{
  "parent_tool_call_id": "call_...",
  "agent_name": "explorer",
  "task_path": "auth-review/backend-scout",
  "model": "gpt-5.5",
  "profile": "default",
  "token_usage": {
    "input_tokens": 1234,
    "output_tokens": 456,
    "total_tokens": 1690
  },
  "tool_counts": {
    "read_file": 7,
    "grep_files": 2
  },
  "last_error": "",
  "close_requested": false
}
```

YAGNI: do not implement token/tool counts unless they are already available from existing metrics with no expensive transcript scan.

### Detailed status

Extend `DetailedStatus.Subagents` to use the richer record or add fields to `SubagentInfo` while preserving current JSON fields:

```go
type SubagentInfo struct {
    ID             string         `json:"id"`
    AgentID        string         `json:"agent_id,omitempty"`
    Status         SubagentStatus `json:"status"`
    Task           string         `json:"task,omitempty"`
    AgentType      string         `json:"agent_type,omitempty"`
    ParentSession  string         `json:"parent_session_id,omitempty"`
    TranscriptRef  string         `json:"transcript_ref,omitempty"`
    TurnsUsed      int            `json:"turns_used"`
    ResultAvailable bool          `json:"result_available,omitempty"`
    ResultConsumed bool           `json:"result_consumed,omitempty"`
    CreatedAt      time.Time      `json:"created_at,omitempty"`
    StartedAt      *time.Time     `json:"started_at,omitempty"`
    EndedAt        *time.Time     `json:"ended_at,omitempty"`
    LastError      string         `json:"last_error,omitempty"`
}
```

Keep `id`, `status`, and `turns_used` for backward compatibility. Store timestamps as `time.Time`/`*time.Time` internally; JSON edges may marshal unset optional timestamps as `null` in `list_agents` records or omit them from legacy `DetailedStatus` DTOs.

## Lifecycle semantics

### Spawn, non-blocking

1. Validate root-only depth and max depth.
2. Resolve agent type, model/profile, reasoning effort, tool policy, and task-list templates.
3. Create child `Session`.
4. Create and track registry record with `status=running`, `created_at`, and `started_at`.
5. Start child goroutine. Current implementation uses `context.Background()` for detached execution; preserve the distinction that wait-context cancellation does not cancel the child.
6. Emit `SUBAGENT_START` using existing event path.
7. Return `{"agent_id":"...","status":"running"}`.

### Spawn, blocking

Same as non-blocking, then call `wait` internally. The returned result includes `agent_id` when the internal wait produces parseable result JSON; pre-snapshot wait errors remain errors unless a future error envelope is defined. The result may be marked consumed because the blocking call already delivered it.

### Resume

- If running: inject steering into the active child session; registry status remains `running`; do not create a new registry entry.
- If idle: reset run-local fields (`result`, `err`, `done`, `resultConsumed`, `endEmitted`), update `started_at`, clear/update `ended_at`, and run child again with preserved child history/context.
- `agent_id` must not change across resumes.

### Wait

- Wait reads the current run's `done` channel.
- Timeout returns an error but does not cancel the child.
- Successful wait returns a result snapshot and sets `result_consumed=true`.
- In v1, define `result_available` as “waitable unconsumed result”: `true` for completed/failed unconsumed results, then `false` after a successful consuming wait. `list_agents` must still show `result_consumed=true` after consumption while the job remains tracked.

### Close

- Close calls child `Session.Close()`.
- It waits up to the implementation timeout for the child goroutine to end.
- On successful close, it returns a final result snapshot.
- On successful close, v1 may remove the record from the active map, matching current behavior. If retention is implemented, mark retained records `closed` and hide them unless `include_closed=true`.
- If the close wait times out, current behavior returns an error and leaves the child tracked; a registry implementation may expose that as `closing`, `close_timeout`, or a diagnostic field rather than pretending the child was removed.

### Parent session close

Parent `Session.Close()` must drain the registry and close child sessions outside the manager lock, preserving the existing lock-order/deadlock constraint documented in `agent/subagent_manager.go:15-18` and implemented by `drainForClose` at `agent/subagent_manager.go:57-68`.

## YAGNI / DRY implementation plan

1. Extend existing `subagent` with metadata fields; do not introduce a new manager package yet.
2. Add one internal snapshot method, e.g. `subagent.infoLocked(parentID string) SubagentInfo`, and reuse it from both `subagentManager.infos()` and `list_agents`.
3. Store timestamps as `time.Time` internally and marshal to RFC3339 at the API/status edge.
4. Use existing event emission points; do not add a second event bus.
5. Add `DefListAgents` near the existing subagent tool definitions and register it in `registerSubagentTools`; keep it root-only with the other management tools.
6. Keep persistence optional in v1. If persistence is required later, write minimal parent-scoped registry snapshots into the parent session state dir and rebuild best-effort from child transcript/session metadata after restart.
7. Do not store duplicate child output in the registry. Store `transcript_ref`, `result_available`, `result_consumed`, and optional `last_error`.
8. Do not add workflow templates, helper agents, run IDs, global scheduling, or tree-native history as part of this implementation.
9. Preserve current names and result shapes where possible; this should be an additive control-plane improvement, not a breaking rewrite.

## Acceptance criteria

- `list_agents` returns running children immediately after non-blocking `spawn_agent`.
- `list_agents` returns at least `agent_id`, `id`, `status`, `turns_used`, `task`, `result_available`, `result_consumed`, and `transcript_ref` for each active child.
- Completed/failed children remain visible until `close_agent`, parent close, or the documented retention policy removes them.
- Blocking `spawn_agent` returns `agent_id` plus completed result JSON when the internal wait produces a parseable result snapshot; pre-snapshot wait errors remain errors without an augmented envelope.
- Blocking `resume_agent` returns `agent_id` plus completed result JSON when the internal wait produces a parseable result snapshot; pre-snapshot wait errors remain errors without an augmented envelope.
- Repeated `wait` after result consumption reports a clear error, and `list_agents` shows `result_consumed=true` while the job remains tracked.
- `wait(timeout_ms)` does not cancel the child; after timeout, `list_agents` still shows it `running` if the child has not finished.
- `resume_agent` on an idle job preserves `agent_id` and updates current/last-run registry fields.
- `resume_agent` on a running job steers the active run and does not create a second job.
- `close_agent` closes/removes or marks the record according to the documented retention semantics and returns the final result snapshot on success; close timeout returns an error and leaves the job queryable.
- Parent session close drains children without holding the manager mutex while closing child sessions.
- No subagent receives `list_agents` or any other management tool.
- Existing status JSON fields `id`, `status`, and `turns_used` remain present and compatible.
- Docs and tool descriptions use `transcript_ref` for the result field.

## Tests

Add focused tests near existing subagent/session tests:

- Spawn/list/wait/close lifecycle unit test.
- Non-blocking spawn appears in `list_agents` before completion.
- Blocking spawn consumes or marks the result consistently and updates registry metadata.
- Blocking resume returns `agent_id` and updates registry metadata.
- Resume idle updates `started_at`, `ended_at`, `result_available`, and `result_consumed` without changing `agent_id`.
- Resume running injects steering without creating a new registry entry.
- Wait timeout leaves the job running and waitable later.
- Repeated wait after consumption returns the documented error and leaves status queryable.
- Close running agent updates status/cleanup and does not deadlock.
- Parent session close drains children without deadlock.
- JSON compatibility test: old fields `id`, `status`, and `turns_used` still serialize.
- Root-only policy test: child sessions do not receive `spawn_agent`, `resume_agent`, `wait`, `close_agent`, or `list_agents`, and `grant_tools` cannot grant them.
