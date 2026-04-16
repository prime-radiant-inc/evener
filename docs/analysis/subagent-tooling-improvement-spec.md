# Subagent Tooling Improvement Spec

## Status
Draft

## Goal
Make subagent orchestration:
- unambiguous in failure handling
- lower-friction for async and parallel work
- easier to resume safely
- easier to audit
- stricter about scope when needed

This spec is based on observed behavior from smoke tests covering:
- synchronous delegation
- async delegation + wait
- parallel delegation
- non-zero command exit handling
- resume flow
- context preservation across resume

---

## 1. Design principles

1. **Separate transport success from task success**
   A successful RPC/tool call must not obscure a failed subagent task.

2. **Make the common path short**
   Spawning one async subagent or a small parallel batch should require minimal ceremony.

3. **Treat resumability as first-class**
   Resume should feel like continuing an ongoing conversation, not manually reconstructing one.

4. **Make provenance inspectable**
   Users should be able to tell whether a result came from preserved context, fresh tool calls, or inference.

5. **Keep auditing cheap**
   Raw transcripts are useful, but common review should not require reading JSONL manually.

6. **Allow strict scope control**
   The caller should be able to demand “do exactly this, nothing else.”

---

## 2. Terminology

- **Transport**: the parent tool invocation itself (`spawn_agent`, `wait`, `resume_agent`)
- **Run**: one execution of a subagent task
- **Step**: one internal action inside a run
- **Outcome**: the actual result of the subagent’s assigned task
- **Artifact**: transcript, produced files, logs, summaries

---

## 3. Required result model

All subagent-related tools should return a normalized envelope with distinct status fields.

### 3.1 Common response envelope
```json
{
  "transport_status": "ok",
  "agent_id": "01...",
  "run_id": "01...",
  "lifecycle_state": "completed",
  "task_status": "failed",
  "summary": "Command exited with status 7 after printing BEFORE_FAIL.",
  "error": null,
  "result": {
    "exit_code": 7,
    "stdout": "BEFORE_FAIL\n",
    "stderr": ""
  },
  "artifacts": {
    "transcript": "/path/to/transcript.jsonl",
    "files_written": [],
    "files_read": []
  },
  "metrics": {
    "turns_used": 1,
    "duration_ms": 842
  }
}
```

### 3.2 Field semantics

#### `transport_status`
Meaning: whether the orchestration tool call succeeded.

Allowed values:
- `ok`
- `error`

Examples:
- `ok`: subagent was spawned or waited successfully
- `error`: invalid agent id, timeout transport failure, internal tool crash

#### `lifecycle_state`
Meaning: current execution state of the subagent run.

Allowed values:
- `queued`
- `running`
- `completed`
- `cancelled`
- `timed_out`
- `interrupted`

#### `task_status`
Meaning: outcome of the assigned subagent task.

Allowed values:
- `succeeded`
- `failed`
- `partial`
- `cancelled`
- `unknown`

This must reflect the actual task result, not the transport result.

#### `error`
Meaning: orchestration-level failure details.
Present when `transport_status = error` or when a run fails for platform reasons.

Shape:
```json
{
  "code": "AGENT_NOT_FOUND",
  "message": "No active subagent with that id.",
  "retryable": false
}
```

### 3.3 Why this is required
Current behavior can report a command exit like `7` inside a wrapper that still says `success: true`. That is semantically ambiguous for both humans and automation.

---

## 4. Async API improvements

## 4.1 Problem
The current async flow is workable but verbose:
1. spawn with `blocking=false`
2. capture `agent_id`
3. call `wait`

## 4.2 Required additions

### Option A: explicit async handle
Introduce a lightweight handle object:
```json
{
  "transport_status": "ok",
  "handle": {
    "agent_id": "01...",
    "run_id": "01...",
    "kind": "subagent_run"
  },
  "lifecycle_state": "running"
}
```

Then support:
- `wait(handle)`
- `poll(handle)`
- `cancel(handle)`

### Option B: job-style grouped API
Alternative acceptable shape:
- `start_subagent(...)`
- `collect_subagent(...)`

But the system must standardize around one idiom.

## 4.3 Required behaviors
- waiting on a completed handle must be idempotent
- polling must not consume or mutate the final result
- cancellation must return explicit post-cancel state
- timeout must be surfaced distinctly from task failure

---

## 5. Parallel orchestration

## 5.1 Problem
Parallel execution works, but fan-out/fan-in is too manual.

## 5.2 Required API
Add first-class batch operations:

### `spawn_many`
Input:
```json
{
  "runs": [
    {"agent_type": "worker", "task": "..."},
    {"agent_type": "worker", "task": "..."}
  ]
}
```

Output:
```json
{
  "transport_status": "ok",
  "group_id": "grp_01...",
  "runs": [
    {"agent_id": "01...", "run_id": "01...", "lifecycle_state": "running"},
    {"agent_id": "01...", "run_id": "01...", "lifecycle_state": "running"}
  ]
}
```

### `wait_many`
Input:
```json
{
  "group_id": "grp_01..."
}
```
or
```json
{
  "handles": [ ... ]
}
```

Output:
- one normalized result per run
- aggregate summary
- aggregate counts by status

Example:
```json
{
  "transport_status": "ok",
  "group_id": "grp_01...",
  "summary": {
    "total": 2,
    "succeeded": 1,
    "failed": 1,
    "cancelled": 0
  },
  "results": [ ... ]
}
```

## 5.3 Required behaviors
- one failed child must not hide sibling results
- result ordering should preserve submission order unless requested otherwise
- partial completion should be representable
- caller should be able to choose fail-fast vs collect-all

---

## 6. Resume ergonomics

## 6.1 Problem
Resume works, but it is low-level and manual.

## 6.2 Required API behavior
Resume should accept either `agent_id` or a durable run handle.

Required call:
```json
{
  "agent_id": "01...",
  "message": "Continue using the previously observed token.",
  "mode": "continue"
}
```

Optional modes:
- `continue` — normal follow-up
- `revise` — reconsider prior output
- `retry` — rerun after failure
- `branch` — continue from prior context but produce a new child run

## 6.3 Required response metadata
Resume responses should include a compact context summary:
```json
{
  "context_summary": {
    "prior_turns_available": 2,
    "tools_used_previously": ["exec_command"],
    "memory_items": [
      {"key": "TOKEN", "value_preview": "pear"}
    ]
  }
}
```

This summary should be concise and safe to expose.

---

## 7. Context provenance and no-lookup controls

## 7.1 Problem
In context-preservation tests, it is difficult to verify whether the agent truly used prior context versus repeating values from the new instruction or re-reading state.

## 7.2 Required controls
Add optional execution constraints:
```json
{
  "constraints": {
    "allow_tool_calls": false,
    "allow_file_reads": false,
    "allow_network": false,
    "scope_mode": "strict"
  }
}
```

These constraints must be enforced by the runtime, not merely requested in natural language.

## 7.3 Required provenance output
Each final result should include provenance metadata:
```json
{
  "provenance": {
    "used_preserved_context": true,
    "tool_calls_made": 0,
    "files_read": [],
    "files_written": [],
    "network_accessed": false
  }
}
```

This need not prove internal cognition; it must at least prove what external actions did or did not occur.

---

## 8. Scope control

## 8.1 Problem
Subagents can be over-eager and perform extra checks not explicitly requested.

## 8.2 Required API
Allow explicit scope policies:
```json
{
  "scope_policy": {
    "mode": "strict",
    "allow_verification_steps": false,
    "allow_extra_commands": false,
    "allow_workspace_inspection": false
  }
}
```

Modes:
- `strict`: do only requested actions
- `balanced`: permit minimal helpful checks
- `exploratory`: permit broader initiative

## 8.3 Enforcement expectation
- in `strict` mode, extra shell commands or extra validation should count as a policy violation
- policy violations should be surfaced in result metadata

Example:
```json
{
  "policy_report": {
    "scope_policy": "strict",
    "violations": [
      "Executed `git status --short` though not requested."
    ]
  }
}
```

---

## 9. Transcript and auditability

## 9.1 Problem
Raw JSONL transcripts are valuable but inconvenient for routine inspection.

## 9.2 Required addition: transcript summary view
Add a helper that returns a compact audit summary.

### `summarize_transcript`
Input:
```json
{
  "agent_id": "01..."
}
```
or
```json
{
  "transcript": "/path/to/transcript.jsonl"
}
```

Output:
```json
{
  "summary": {
    "commands_run": [
      "pwd && echo ASYNC_OK && ls agent | head -n 5"
    ],
    "tool_calls": ["exec_command"],
    "files_read": [],
    "files_written": [],
    "final_exit_code": 0,
    "task_status": "succeeded"
  }
}
```

## 9.3 Minimum audit fields
- commands run
- tools called
- files read
- files written
- network accesses
- exit codes
- policy violations
- final status
- duration and turn count

---

## 10. Timeouts, cancellation, and partial results

These cases should be explicit.

### Required timeout response
```json
{
  "transport_status": "ok",
  "lifecycle_state": "timed_out",
  "task_status": "partial",
  "partial_result": {
    "stdout": "partial output..."
  }
}
```

### Required cancellation response
```json
{
  "transport_status": "ok",
  "lifecycle_state": "cancelled",
  "task_status": "cancelled"
}
```

The system must never silently collapse timeout/cancel into generic failure.

---

## 11. Backward compatibility

If existing fields like `success` remain, they should be deprecated and redefined clearly.

Preferred compatibility rule:
- keep `success` only as an alias for `transport_status == "ok"`
- add new explicit fields for task outcome
- document the distinction prominently

If that alias is kept, docs must warn that `success: true` does **not** imply the delegated task succeeded.

---

## 12. Minimum acceptance tests

A compliant implementation should pass these tests.

### 12.1 Non-zero exit clarity
- run a subagent command that exits `7`
- expected:
  - `transport_status = ok`
  - `task_status = failed`
  - `result.exit_code = 7`

### 12.2 Async idempotent wait
- start async run
- call `wait`
- call `wait` again
- expected: same stable completed result

### 12.3 Parallel mixed outcomes
- run two subagents in parallel
- one succeeds, one fails
- expected: both results preserved; aggregate summary correct

### 12.4 Resume continuity
- start subagent
- resume same subagent
- expected: prior context available and declared in context summary

### 12.5 Strict scope compliance
- run with `scope_policy.mode = strict`
- instruct one command only
- expected: no extra command execution; otherwise violation recorded

### 12.6 No-lookup enforcement
- resume with tool/file/network access disabled
- expected: zero external accesses in provenance

### 12.7 Transcript summary correctness
- summary must match actual transcript contents for commands, tools, and files

---

## 13. Non-goals

This spec does not require:
- changing the internal reasoning model
- proving the exact internal source of a remembered token
- replacing raw transcripts
- introducing autonomous planning features beyond current subagent scope

---

## 14. Short version

If I compress this to the essentials, I want:
1. explicit `transport_status` vs `task_status`
2. first-class async handles
3. first-class parallel group operations
4. easier resume semantics
5. enforceable no-lookup/scope controls
6. compact transcript summaries
7. policy/provenance metadata

That would solve nearly all of the friction I hit in testing.
