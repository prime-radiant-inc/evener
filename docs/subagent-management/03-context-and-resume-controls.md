# Subagent Context and Resume Controls

Status: Proposed evergreen spec. Current serf already creates subagents as full child `Session`s, exposes root-only lifecycle tools, and preserves child history across `resume_agent`. This document defines the stable context and resume contract for those tools without adding public working-directory controls or a workflow engine.

## Purpose

Make subagent context boundaries explicit. A parent session should know what state a child receives at spawn time, what state remains isolated in the child, when `resume_agent` preserves prior child context, and when a fresh `spawn_agent` is the correct choice.

This spec complements:

- `01-job-registry.md`, which defines parent-visible job records;
- `02-lifecycle-events-and-notifications.md`, which defines lifecycle event and notification contracts;
- `04-raw-output-and-diagnostics.md`, which defines parent/child result, transcript, and diagnostic handoff.

## Goals

- Preserve the current root-only subagent management model.
- Keep `spawn_agent` as the fresh-context operation for independent work.
- Keep `resume_agent` as the same-child-history operation for iteration and steering.
- Define exactly what context is copied, inherited, reset, shared, or isolated at spawn time.
- Keep parent context clean by returning compact result JSON and `transcript_ref` instead of child transcript bodies.
- Keep target APIs backward-compatible with current tool names and common result shapes.
- Centralize shared lifecycle/result behavior instead of duplicating spawn/resume/wait/close JSON handling.

## Non-goals

- No nested subagent management tools.
- No public child working-directory override in the lifecycle tool API.
- No automatic import of the parent transcript into the child beyond the explicit task, role prompt, task templates, and startup context selected by existing prompt/session machinery.
- No automatic import of child transcript contents into the parent after completion.
- No persistent cross-process resume of in-memory job mappings in this spec.
- No new planner/workflow DSL, DAG scheduler, or declarative parallel map primitive.
- No human approval flow; capability/approval policy belongs in the dedicated capability spec.

## Current implementation anchors

- Current tool schemas: `agent/internal/tool/definitions.go:193-293`.
- Current tool handlers and blocking wrappers: `agent/session_tools_subagent.go:14-170`.
- Current subagent result envelope: `agent/subagents.go:35-42`.
- Current root-only management tool list and policy helpers: `agent/subagents.go:52-130`.
- Current spawn configuration and context shaping: `agent/subagents.go:141-357`, including named/plugin agent skill-body injection at `agent/subagents.go:235-244`.
- Current resume, wait, and close behavior: `agent/subagents.go:360-485`.
- Current subagent run/result capture and transcript ref: `agent/subagents.go:501-585`.
- Current per-parent registry and lock discipline: `agent/subagent_manager.go:9-87`.
- Selected current session config fields relevant to context behavior: `agent/session_config.go:36-102`, `agent/session_config.go:180-212`. Other copied prompt/plugin/skill/session fields remain inherited unless spawn-specific code overrides or clears them.
- Current session initialization from `spawnConfig.depth`: `agent/session_init.go:96-114`.
- Current lifecycle events and payloads: `agent/events/events.go:57-60`, `agent/events/payloads.go:185-196`.

## Current API contract

### `spawn_agent` current public API

Current schema is defined by `DefSpawnAgent` (`agent/internal/tool/definitions.go:193-229`) and handled in `registerSubagentTools` (`agent/session_tools_subagent.go:14-97`).

Parameters:

```json
{
  "task": "required string",
  "model": "optional model/profile override; default parent model/profile",
  "max_turns": "optional integer; default 500 in spawnAgent",
  "agent_type": "optional built-in/bundled/plugin agent type",
  "blocking": "optional boolean; default false",
  "reasoning_effort": "optional low|medium|high|xhigh; default inherits parent config",
  "grant_tools": ["optional extra callable tool names"],
  "task_list": [
    {
      "title": "required short task title",
      "prompt": "required detailed task prompt",
      "reasoning_effort": "optional low|medium|high|xhigh"
    }
  ]
}
```

Required fields:

```json
{"task":"string"}
```

Current non-blocking return:

```json
{"agent_id":"01CHILD...","status":"running"}
```

Current blocking return is produced by spawning, then calling `waitAgent(ctx, agentID, 0)`, then adding `agent_id` to the wait-shaped JSON when `waitAgent` returns parseable result JSON (`agent/session_tools_subagent.go:71-95`). If the wait fails before producing a snapshot, the current wrapper returns the wait error/result without an augmented `agent_id` envelope:

```json
{
  "agent_id": "01CHILD...",
  "status": "completed|failed",
  "output": "child result or error text",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Current spawn rules:

- Only root sessions can spawn. `spawnAgent` rejects when `depth > 0` and also checks the configured subagent depth limit (`agent/subagents.go:141-151`).
- The child receives a copied `SessionConfig`, then spawn-specific fields are set: parent session ID, task, depth, optional parent tool-call ID, optional shared task store, max turns, reasoning effort, and agent name (`agent/subagents.go:190-230`).
- Child MCP config file and inline MCP config lists are cleared before child session creation (`agent/subagents.go:190-194`).
- Explicit `max_turns` wins; otherwise current code sets child `MaxTurns` to `500` (`agent/subagents.go:206-210`).
- `model` resolves against the current profile; a named agent model override can take precedence unless it is empty or `inherit` (`agent/subagents.go:166-188`).
- `grant_tools` are canonicalized, cannot include root-only management tools, and must refer to tools currently callable in the parent unless already allowed by the child policy (`agent/subagents.go:214-277`).
- The handler currently passes an empty internal working-directory argument to `spawnAgent` (`agent/session_tools_subagent.go:71`); there is no public `working_dir` field in the schema (`agent/internal/tool/definitions.go:197-227`).
- The child run starts in a goroutine with `context.Background()` so it can outlive the parent tool-call context (`agent/subagents.go:342-349`).

### `resume_agent` current public API

Current schema is defined by `DefSendInput` (`agent/internal/tool/definitions.go:231-262`) and handled in `registerSubagentTools` (`agent/session_tools_subagent.go:98-147`).

Parameters:

```json
{
  "agent_id": "required child session/job id",
  "message": "required follow-up instruction or steering text",
  "blocking": "optional boolean; default false",
  "task_list": [
    {
      "title": "required short task title",
      "prompt": "required detailed task prompt",
      "reasoning_effort": "optional low|medium|high|xhigh"
    }
  ]
}
```

Required fields:

```json
{"agent_id":"01CHILD...","message":"string"}
```

Current non-blocking return:

```json
"ok"
```

Current blocking return is produced by `sendInput`, then `waitAgent(ctx, agentID, 0)`, then adding `agent_id` to the wait-shaped JSON when `waitAgent` returns parseable result JSON (`agent/session_tools_subagent.go:127-145`). If the wait fails before producing a snapshot, the current wrapper returns the wait error/result without an augmented `agent_id` envelope:

```json
{
  "agent_id": "01CHILD...",
  "status": "completed|failed",
  "output": "child result or error text",
  "success": true,
  "turns_used": 4,
  "transcript_ref": "local:01CHILD..."
}
```

Current resume rules:

- The `agent_id` must already be tracked in the parent registry; otherwise `sendInput` returns `unknown agent_id` (`agent/subagents.go:360-364`).
- If the child is running, `resume_agent` calls `sub.sess.Steer(message)` and returns `"ok"`; it does not create a new child session or a new run record (`agent/subagents.go:365-373`).
- If the child is idle, `resume_agent` resets the run-local fields, creates a new `done` channel, marks the same child `running`, emits `SUBAGENT_START`, and runs `ProcessInput` on the existing child session/history with `context.Background()` (`agent/subagents.go:375-405`).
- `task_list` items append to the child task store before the message is sent (`agent/session_tools_subagent.go:103-126`).

### `wait` current public API

Current schema is defined by `DefWait` (`agent/internal/tool/definitions.go:264-278`) and handled in `registerSubagentTools` (`agent/session_tools_subagent.go:148-162`).

Parameters:

```json
{
  "agent_id": "required child session/job id",
  "timeout_ms": "optional integer"
}
```

Required fields:

```json
{"agent_id":"01CHILD..."}
```

Current return:

```json
{
  "status": "completed|failed",
  "output": "child result or error text",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Current wait rules:

- `timeout_ms` is optional. The handler clamps positive timeouts lower than `120000` ms to `120000` ms (`agent/subagents.go:19-21`, `agent/session_tools_subagent.go:152-160`).
- `timeout_ms <= 0` means wait indefinitely, subject to the parent operation context (`agent/subagents.go:426-431`).
- A wait timeout returns `wait timeout` and does not cancel the child run (`agent/subagents.go:432-441`).
- A successful wait returns the run snapshot and marks the run result consumed (`agent/subagents.go:443-449`).
- A repeated wait after an idle run's result was consumed returns a clear error telling the caller to resume or close (`agent/subagents.go:415-419`).

### `close_agent` current public API

Current schema is defined by `DefCloseAgent` (`agent/internal/tool/definitions.go:280-293`) and handled in `registerSubagentTools` (`agent/session_tools_subagent.go:163-170`).

Parameters:

```json
{"agent_id":"required child session/job id"}
```

Current return is the same snapshot shape as `wait`:

```json
{
  "status": "completed|failed",
  "output": "child result or error text",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Current close rules:

- The `agent_id` must be tracked; otherwise `closeAgent` returns `unknown agent_id` (`agent/subagents.go:452-456`).
- Close calls the child session's `Close()` (`agent/subagents.go:458`).
- If a run is active, close waits up to five seconds for the child goroutine to finish (`agent/subagents.go:460-476`).
- Close returns the final snapshot and removes the child from the parent registry on success (`agent/subagents.go:478-485`). If the five-second close wait times out, it returns an error and does not remove the registry entry (`agent/subagents.go:472-475`).

## Target API contract

The target v1 API keeps the current names and parameters. Additive changes are allowed only when they do not break existing tool callers or transcript readers.

### `spawn_agent` target API

Parameters stay:

```json
{
  "task": "required string",
  "model": "optional string",
  "max_turns": "optional integer, default 500",
  "agent_type": "optional string",
  "blocking": "optional boolean, default false",
  "reasoning_effort": "optional low|medium|high|xhigh",
  "grant_tools": ["optional string"],
  "task_list": [
    {"title":"string", "prompt":"string", "reasoning_effort":"optional low|medium|high|xhigh"}
  ]
}
```

No public working-directory field is part of the target API.

Target non-blocking return:

```json
{"agent_id":"01CHILD...","status":"running"}
```

Target blocking return:

```json
{
  "agent_id": "01CHILD...",
  "status": "completed|failed",
  "success": true,
  "output": "string",
  "turns_used": 0,
  "transcript_ref": "local:01CHILD..."
}
```

Rules:

- `spawn_agent` creates a new child session with fresh child history.
- `spawn_agent` is the correct operation for independent research, implementation, or review threads.
- Blocking spawn consumes the first run's result; callers must not call `wait` for that same run.
- Blocking spawn adds `agent_id` to the wait-shaped result when the internal wait produces a result snapshot; wait errors are returned as errors unless a future error envelope is defined.

### `resume_agent` target API

Parameters stay:

```json
{
  "agent_id": "required string",
  "message": "required string",
  "blocking": "optional boolean, default false",
  "task_list": [
    {"title":"string", "prompt":"string", "reasoning_effort":"optional low|medium|high|xhigh"}
  ]
}
```

Target non-blocking return remains:

```json
"ok"
```

Target blocking return:

```json
{
  "agent_id": "01CHILD...",
  "status": "completed|failed",
  "success": true,
  "output": "string",
  "turns_used": 0,
  "transcript_ref": "local:01CHILD..."
}
```

Rules:

- Resume of a running child is steering, not a new run.
- Resume of an idle child starts a new run in the same child session and preserves child history/context.
- `resume_agent` is the correct operation for reviewer feedback, refinement, retries after incomplete answers, and continuing a delegated thread.
- Blocking resume on an idle child consumes the newly started run's result; blocking resume on a running child steers the active run, then waits for and consumes that active run's result. Callers must not call `wait` for the consumed run.
- Blocking resume adds `agent_id` to the wait-shaped result when the internal wait produces a result snapshot; wait errors are returned as errors unless a future error envelope is defined.

### `wait` target API

Parameters stay:

```json
{
  "agent_id": "required string",
  "timeout_ms": "optional integer; positive values below 120000 are clamped to 120000"
}
```

Target return:

```json
{
  "status": "completed|failed",
  "success": true,
  "output": "string",
  "turns_used": 0,
  "transcript_ref": "local:01CHILD..."
}
```

Rules:

- `wait` observes completion and consumes one result snapshot. The normal successful result is terminal (`completed|failed`); `running` is reserved for defensive/shared snapshot or future peek/diagnostic surfaces, not ordinary wait/blocking returns.
- `wait` timeout never cancels the child.
- Repeated wait after result consumption remains an error unless a future registry API explicitly exposes a non-consuming peek.

### `close_agent` target API

Parameters stay:

```json
{"agent_id":"required string"}
```

Target return:

```json
{
  "status": "completed|failed",
  "success": true,
  "output": "string",
  "turns_used": 0,
  "transcript_ref": "local:01CHILD..."
}
```

Rules:

- `close_agent` is the current destructive cleanup/close path.
- Close removes the child from the active parent registry after returning its final snapshot on success.
- If the close wait times out, current behavior returns an error and leaves the child tracked; this spec does not introduce `cancelled` or `closed` result statuses for that branch.
- Close should remain safe as a cleanup operation for running or idle children. After a result was already consumed, callers must not rely on close as a second result-read API unless a future non-consuming peek/snapshot contract is added.

## Context policy proposal

### Context ownership

A subagent owns a separate child `Session`. Its context is not a borrowed slice of the parent conversation; it is a new session initialized from copied config plus explicit spawn metadata. Current `NewSession` initializes a distinct session ID, session context, history slice, and subagent manager in `agent/session_init.go:96-116`; transcript writer/state is initialized later when state persistence is enabled in `agent/session_init.go:147-176`, and tool/prompt setup follows the regular session initialization path.

Target policy:

| Context area | Current behavior | Target policy |
| --- | --- | --- |
| Parent transcript/history | Not copied directly by `spawnAgent`; child starts as a new `Session`. | Preserve. Pass only explicit task/startup material, not parent transcript bodies. |
| Child transcript/history | Stored in the child session and reflected through `transcript_ref` when transcript persistence is enabled. | Preserve. Parent must use transcript tools explicitly to inspect it; `transcript_ref` is only readable when state persistence/transcript tools are available. |
| Resume context | Same child session/history is reused. | Preserve. Resume means continue the same delegated thread. |
| Spawn context | New child session/history. | Preserve. Spawn means independent fresh delegated thread. |
| Parent task store | Shared only when `ShareTasksWithChildren` is true; otherwise child gets its own task store (`agent/subagents.go:198-202`, `agent/session_config.go:94-97`). | Preserve default isolation; make sharing explicit. |
| Task templates | `task_list`/agent templates populate or append child tasks (`agent/subagents.go:305-317`, `agent/session_tools_subagent.go:103-126`). | Preserve as explicit context injection. |
| Activated named/plugin agent skills | Selected agent `skills` entries are resolved and injected into child prompt context when available (`agent/subagents.go:235-244`, `agent/session_config.go:206-211`). | Preserve as explicit context injection; do not treat this as parent transcript sharing. |
| Model/profile | Inherits parent unless `model` or named agent model overrides (`agent/subagents.go:166-188`). | Preserve. |
| Reasoning effort | Inherits copied config unless overridden (`agent/subagents.go:211-213`). | Preserve. |
| Max turns | Explicit `max_turns` or default child `500` (`agent/subagents.go:206-210`). | Preserve unless global defaults change deliberately. |
| MCP config inputs | Cleared before child session creation (`agent/subagents.go:190-194`). | Preserve unless a separate capability policy enables explicit child MCP grants. |
| Root-only management tools | Denied or rejected for children (`agent/subagents.go:52-130`, `agent/subagents.go:247-277`). | Preserve. |
| Execution environment | Uses parent environment. | Preserve. Do not expose public per-spawn working-directory controls in this spec. |

### Resume decision rule

Use `resume_agent` when the next instruction depends on the child's prior work or should modify the same delegated thread:

- reviewer feedback to a worker;
- asking a researcher to check one more source after reading several;
- asking a child to repair an incomplete result;
- steering a still-running child away from a wrong path.

Use `spawn_agent` when the next instruction should not inherit child state:

- independent parallel research questions;
- competing implementation approaches;
- fresh review after a child polluted its own context;
- work that needs different tools, model, agent type, or turn budget.

### Parent context hygiene

The parent receives only lifecycle tool results and lifecycle events by default. Current result snapshots contain `status`, `output`, `success`, `turns_used`, and `transcript_ref` (`agent/subagents.go:35-42`, `agent/subagents.go:574-585`). Current `SUBAGENT_START` events carry only `agent_id` and `task`; current `SUBAGENT_END` events carry only `agent_id`, `status`, and `turns_used` (`agent/events/payloads.go:185-196`).

Target policy:

- Do not stream child tool output into parent active context by default.
- Do not append child transcript bodies to parent history automatically.
- Keep `transcript_ref` as the explicit audit pivot.
- Keep `output` as the child handoff summary, not a full transcript.
- Model-facing tool descriptions should continue to tell callers to inspect `success`, `status`, and `output` before trusting completion, and should advertise the implemented `transcript_ref` field rather than stale `transcript` wording.

### Timeout and cancellation context

Current child runs use `context.Background()` on spawn and idle resume so the run can outlive parent tool-call waiting (`agent/subagents.go:342-349`, `agent/subagents.go:401-405`). The parent wait operation can time out or be cancelled without cancelling the child (`agent/subagents.go:426-441`).

Target policy:

- Preserve detached child execution.
- Treat wait timeout as parent observation timeout only.
- Treat `close_agent` as the current destructive close/cleanup mechanism; successful close preserves the last run's `success`/error semantics in the returned snapshot, and close timeout is an error path rather than a `cancelled` result.
- If future cancellation adds a child-owned run context, implement it as a separate controller and keep wait timeout non-destructive.

## YAGNI/DRY implementation plan

1. Keep the public lifecycle tool schemas unchanged. Do not add context knobs until there is a proven consumer need.
2. Do not add public per-spawn working-directory support.
3. Extract one shared subagent result marshaling helper used by `waitAgent`, `closeAgent`, blocking `spawn_agent`, and blocking `resume_agent`. The helper should preserve `status`, `output`, `success`, `turns_used`, and `transcript_ref`, and add `agent_id` only where intended.
4. Extract one shared run-start helper for initial spawn and idle resume that resets run-local fields, emits `SUBAGENT_START`, enrolls the goroutine in `sendersWG`, and starts `sub.run(context.Background(), input)`. Keep construction-only concerns in `spawnAgent`.
5. Keep policy checks close to spawn construction: root-only depth checks, agent-type checks, grant-tool validation, and child allow/deny setup should remain in one path to avoid schema/policy drift.
6. Keep resume steering simple: if `sub.running`, call `Steer` and return; do not invent a second queue or run record until a registry spec requires it.
7. Keep child context isolation as configuration, not ad hoc prompt concatenation. The explicit spawn context inputs should be task text, agent role/system prompt, activated named/plugin agent skill bodies, task templates, model/profile, reasoning effort, max turns, allowed tools, and shared task-store option.
8. Keep transcript inspection explicit. Do not add automatic child transcript summarization or parent-history injection as part of context/resume controls.
9. Add tests at the tool/Session boundary rather than a new end-to-end harness where existing subagent tests can exercise the behavior.

## Acceptance criteria

- `spawn_agent` creates a new child session with a distinct `agent_id` and fresh child history.
- `resume_agent` on an idle child preserves the same `agent_id` and prior child history.
- `resume_agent` on a running child calls the steering path and does not create a new child session.
- Blocking `spawn_agent` and blocking `resume_agent` return wait-shaped result JSON with `agent_id` added when the internal wait produces a result snapshot.
- Non-blocking `spawn_agent` returns only `agent_id` and `status:"running"`.
- Non-blocking `resume_agent` returns `"ok"` after steering or starting a run.
- `wait` returns `status`, `output`, `success`, `turns_used`, and `transcript_ref`, then consumes that run's result.
- Repeated `wait` after consumption returns the existing clear resume-or-close error.
- `wait(timeout_ms)` timeout leaves the child running.
- `close_agent` closes the child, returns a final snapshot when available, and removes it from the parent registry on success; close timeout returns an error and does not remove the child.
- Child sessions cannot see or call `spawn_agent`, `resume_agent`, `wait`, or `close_agent`.
- `grant_tools` cannot grant root-only management tools and, when a requested tool is not already available under the child base policy, cannot grant tools unavailable to the parent.
- Child MCP config inputs remain cleared unless a separate capability spec changes that policy.
- Child task-store sharing occurs only when `ShareTasksWithChildren` is true.
- Parent result handoff does not include full child transcript content; it includes `transcript_ref` for explicit inspection.
- No docs, schemas, or tests claim public working-directory override support for subagents.

## Tests

Add or preserve focused tests for:

- Spawn config copies parent config but sets child depth, parent session metadata, child max turns default, child agent name, and optional reasoning/model overrides.
- Spawn clears child MCP config slices.
- Spawn with `blocking=false` returns `agent_id`/`running` and leaves the child registered.
- Blocking spawn returns wait-shaped JSON with `agent_id` when the internal wait produces a parseable result snapshot and marks the result consumed; pre-snapshot wait errors remain unaugmented errors.
- Idle resume starts a new run on the same child session, preserves prior transcript/history, resets run-local result fields, and emits `SUBAGENT_START`.
- Running resume calls `Steer` and returns `"ok"` without replacing the `done` channel or child session.
- Blocking resume returns wait-shaped JSON with `agent_id` when the internal wait produces a parseable result snapshot and marks the result consumed; for a running child, this consumes the active run that was steered, not a newly created run. Pre-snapshot wait errors remain unaugmented errors.
- Wait clamps short positive timeouts to the implementation minimum and does not cancel the child on timeout.
- Repeated wait after consumed result returns the documented error.
- Close on a running child calls child `Close`, waits for completion, returns a snapshot and removes the registry entry on success, or returns a timeout error without removal.
- Child tool registry excludes root-only management tools.
- `grant_tools` rejects root-only management tools and rejects tools not currently callable by the parent when they are not already present in the child base policy.
- Parent close drains multiple child sessions outside the manager mutex.
- Result JSON and model-facing `wait` descriptions use `transcript_ref`, not stale `transcript` wording.
- No public `spawn_agent` schema field exposes working-directory override.
