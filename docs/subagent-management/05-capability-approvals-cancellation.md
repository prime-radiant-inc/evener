# Subagent Capability, Approvals, and Cancellation

Status: Proposed evergreen spec. Current serf implements static subagent-management capability policy and `close_agent`-based destructive cleanup. Human approval flows, guaranteed run cancellation, and dedicated subagent web/appwire capabilities are deferred targets, not current behavior.

## Purpose

Define the stable capability, approval, and cancellation contract for subagent management. A parent/root session must be able to delegate only within its effective authority, children must not gain delegation powers, and callers must understand that `wait` timeouts, steering, cancellation, and close are distinct operations.

This document is intentionally implementation-facing and user-facing: it records what current serf does, the exact policy that must remain true, and the smallest forward-compatible additions needed for approvals and richer cancellation.

## Goals

- Preserve root-only subagent management for `spawn_agent`, `resume_agent`, `wait`, and `close_agent`.
- Prevent nested delegation and prevent children from receiving root-only management tools by any route.
- Keep `grant_tools` additive and canonicalized. When a requested grant is not already present in the child base policy, it must be bounded by the parent session's currently callable tools.
- Make current close semantics explicit: `close_agent` is the destructive close/cleanup request; it waits for the child to stop but can time out, and `wait(timeout_ms)` is not cancellation.
- Keep web/appwire capability claims accurate: no dedicated subagent-management capability is advertised today.
- Define approval DTOs as a deferred target without implying approvals currently exist.
- Keep the implementation plan YAGNI/DRY: reuse the existing registry, tool policy, session close, event, and appwire capability surfaces before adding new primitives.

## Non-goals

- No nested subagent management.
- No global, cross-session delegation capability registry.
- No human approval system in current v1 behavior.
- No dedicated `cancel_agent` or `interrupt_agent` API in current v1 behavior.
- No process sandbox, filesystem sandbox, or worktree isolation contract.
- No workflow/DAG engine or declarative approval workflow language.
- No claim that provider-native features are fully controlled by registry-tool grants.

## Current implementation anchors

- Root-only management tool list and child policy helpers: `agent/subagents.go:52-130`.
- Spawn capability checks, agent-type rejection, grant validation, child MCP clearing, child-session setup, and detached execution: `agent/subagents.go:141-357`.
- Resume/steering behavior: `agent/subagents.go:360-407`.
- Wait timeout and result-consumption behavior: `agent/subagents.go:409-450`.
- Close/cleanup implementation: `agent/subagents.go:452-485`.
- Result snapshot shape: `agent/subagents.go:35-42`, `agent/subagents.go:574-585`.
- The live `DefWait` description currently says `transcript`; that is stale for the implemented result field, which is `transcript_ref`.
- Public tool handlers and blocking wrappers: `agent/session_tools_subagent.go:14-170`.
- Tool schema/descriptions for `spawn_agent`, `resume_agent`, `wait`, and `close_agent`: `agent/internal/tool/definitions.go:193-293`.
- Server action capability surface and `/status` construction: `server/server.go:98-109`, `server/server_handlers.go:269-278`.
- Appwire interrupt gate and thread capabilities: `server/appwire_runtime.go:506-528`.

## Exact capability policy contract

### Management tools are root-only

The current root-only management tools are exactly:

```go
[]string{"spawn_agent", "resume_agent", "wait", "close_agent"}
```

Any future registry/control-plane management tool, including the proposed `list_agents` from `01-job-registry.md`, must join the same root-only set before it is exposed to a parent model.

Contract:

1. Only a root/parent session may call these tools.
2. A subagent may not spawn, resume, wait on, or close another subagent.
3. A child tool registry must not expose these tools by default.
4. `grant_tools` must reject these names even when the parent can call them.
5. Named/plugin agent definitions that explicitly require these tools are top-level-only and must be rejected for child execution. Current `tools: all` child agents are allowed because child registry construction still removes root-only management tools; full intersection with the parent's restricted callable surface is target policy unless separately implemented.

Current code anchors:

- `rootOnlyAgentManagementTools` is defined in `agent/subagents.go:52`.
- `spawnAgent` rejects non-root calls with `"subagent management is top-level only"` in `agent/subagents.go:141-148`.
- agent definitions explicitly listing root-only management tools are rejected in `agent/subagents.go:153-163`.
- `grant_tools` rejects root-only management tools in `agent/subagents.go:247-269`.
- default child policy denies root-only tools in `agent/subagents.go:121-130`.

### Grant tools are bounded by parent-effective scope

`grant_tools` is additive only within the parent's current callable surface.

Contract:

1. Canonicalize requested grant names before policy evaluation.
2. Reject any root-only management tool.
3. If the requested tool is not already in the child base policy, require it to be currently callable in the parent session.
4. After child session creation, verify the tool exists in the child registry; fail spawn if a requested grant disappeared during child assembly.
5. Do not use `grant_tools` to imply approval, sandboxing, or provider-native feature gating.

Current code anchors:

- canonicalization occurs before validation in `agent/subagents.go:214`.
- parent registry membership is checked in `agent/subagents.go:249-262`.
- child registry verification occurs in `agent/subagents.go:292-302`.
- child MCP config is cleared during spawn in `agent/subagents.go:190-195`, so MCP inheritance must not be assumed from parent config files alone.

### Agent-type policy

Contract:

1. `agent_type` selects an available built-in agent role or a configured plugin/bundled agent role. Bundled plugin roles are selectable only when that plugin is loaded into the session.
2. Unknown agent types fail spawn.
3. Agent types whose explicit configured tool list includes root-only management tools fail spawn.
4. `tools: all` does not fail spawn by itself; it must not become a bypass for nested management because root-only tools remain hard denies and are removed from child registries.
5. Named agent model and prompt settings may shape the child session. Current all-tools/base role policy is constrained by child registry construction and root-only removal, but it is not fully intersected with a parent registry that has been restricted by allow/deny policy. Parent-current-callable checks currently apply to extra `grant_tools` that are not already available under the child base policy; enforcing full parent-effective intersection for `tools: all` agents is target work.

Current code anchors:

- plugin agent lookup and unknown-type rejection: `agent/subagents.go:153-159`.
- top-level-only agent-type rejection: `agent/subagents.go:160-162`.
- base subagent tool policy: `agent/subagents.go:121-130`.
- model/profile override order is implemented in `agent/subagents.go:166-188`.

### Web/appwire capabilities

Current web/appwire action capabilities do not advertise subagent management as a first-class capability. Appwire diagnostics may still expose observational subagent metadata; that is not a management/action capability.

Contract:

1. Do not claim that `ActionCapabilities` or `ThreadCapabilities` includes `spawn_subagent`, `resume_subagent`, `wait_subagent`, `close_subagent`, or equivalent fields today.
2. `interrupt` is top-level session-turn cancellation, not child-job cancellation.
3. Any future appwire subagent-management surface must be additive and must not alter the internal tool policy contract above.

Current code anchors:

- REST `ActionCapabilities` includes `send`, `steer`, `interrupt`, `compact`, `clear`, `shutdown`, `change_model`, `queue`, and `read_only_reason`, but no subagent-specific fields; the struct is defined in `server/server.go:98-109` and populated in `/status` at `server/server_handlers.go:269-278`.
- appwire `ThreadCapabilities` includes the thread-action fields plus `goal`, and appwire `Interrupt` is exposed when `cancelFunc != nil`: `appwire/types.go:226-242`, `server/appwire_runtime.go:506-528`.
- appwire diagnostics/status can include observational subagent metadata (`id`, `status`, `turnsUsed`) via `SerfDiagnostics.Subagents`, but this does not allow spawn/resume/wait/close: `appwire/types.go:245-252`, `appwire/types.go:279-283`.

## Approval DTOs as deferred target

Approvals are not implemented for subagent management today. The current system enforces static capability/tool policy only.

Deferred approval integration should be introduced after a shared permission engine exists. The intended approval decision model is:

```text
allow | deny | ask
```

The enforcement point should be after hook/policy preprocessing has enough context to compute the requested operation and before the registry mutates or child execution starts.

### Deferred capability advertisement DTO

```json
{
  "subagents": {
    "spawn": true,
    "resume": true,
    "wait": true,
    "close": true,
    "grant_tools": ["read_file", "grep_files"],
    "agent_types": ["subagent", "explorer", "plugin-name:agent-name"],
    "default_available": true,
    "approval_required": false
  }
}
```

Semantics:

- `spawn`, `resume`, `wait`, and `close` report whether the caller is allowed to request those operations from this session surface.
- `grant_tools` lists approved or approvable tool names, after canonicalization and parent-effective filtering.
- `agent_types` lists selectable named agent roles after rejecting top-level-only roles. The unnamed default role is selected today by omitting `agent_type`; expose it separately (for example `default_available:true`) unless a future API deliberately adds a `default` alias. Always-present built-ins are the embedded agent names; plugin or bundled names appear only when the relevant plugin is configured and loaded.
- `approval_required` means at least one requested operation or grant would produce `ask`; it is not evidence that an approval has already been granted.

### Deferred approval request DTO

```json
{
  "kind": "SUBAGENT_APPROVAL_REQUEST",
  "data": {
    "request_id": "apr_...",
    "operation": "spawn|resume|wait|grant_tool|close",
    "agent_id": "01CHILD...",
    "agent_type": "explorer",
    "task_preview": "first bounded preview of the task/message",
    "requested_tools": ["write_file", "exec_command"],
    "risk": "low|medium|high",
    "reason": "permission rule Agent(explorer) requires ask",
    "parent_session_id": "01PARENT...",
    "parent_tool_call_id": "toolu_..."
  }
}
```

Rules:

- `request_id` is required and stable for the pending decision.
- `operation` is required. If `wait` is intentionally treated as non-approvable/read-only in a future implementation, remove it from this enum and state that explicitly alongside the advertised `wait` capability.
- `agent_id` is omitted for new spawn requests and required for existing-job operations.
- `task_preview` must be bounded and safe for UI display; the full prompt remains in the transcript/session data.
- `requested_tools` uses canonical Serf tool names.
- `risk` is an advisory classification, not a permission decision.

### Deferred approval response DTO

```json
{
  "request_id": "apr_...",
  "decision": "allow|deny",
  "remember": false,
  "additional_grants": ["read_file"],
  "reason": "optional human-readable explanation"
}
```

Rules:

- `decision=allow` permits only the operation described by the request plus explicitly listed `additional_grants`.
- `decision=deny` must fail the pending operation without mutating the job registry.
- `remember=true` may update durable policy only if a separate policy store exists; otherwise ignore or reject it explicitly.
- Approval responses never permit root-only management tools in a child.

Policy evaluation and human responses are separate phases: policy may return `allow`, `deny`, or `ask`; only `ask` creates an approval request, and approval responses carry final `allow` or `deny`.

## Cancellation and close API semantics

### Current semantics

`wait(timeout_ms)` waits for the current child run. It does not cancel the child.

- `timeout_ms <= 0` waits indefinitely, subject to the parent operation context.
- Positive timeouts lower than the implementation minimum are clamped by the handler.
- On timeout, the child continues running because subagent execution uses `context.Background()`.

Current code anchors:

- timeout clamp in `agent/session_tools_subagent.go:148-160`.
- wait timeout returns `"wait timeout"` without closing the child in `agent/subagents.go:409-450`.
- detached child execution is documented and implemented in `agent/subagents.go:342-349` and `agent/subagents.go:401-405`.

`resume_agent` on a running child is steering, not cancellation.

- It calls child `Steer(input)` and returns `"ok"` for non-blocking mode.
- It does not create a new run while the child is already running.

Current code anchor: `agent/subagents.go:360-373`.

`close_agent` is the current destructive close/cleanup request. It is the only current child cleanup path that can ask a running child session to stop, but it is not a guaranteed immediate run abort.

- It calls child `Session.Close()`.
- It waits up to five seconds for the active run goroutine to finish; timeout leaves the child tracked and does not prove the run stopped.
- It returns the same result snapshot shape as `wait` when available.
- It removes the child from the parent registry after successful close.
- If the close wait times out, it returns an error and does not remove the registry entry in the current implementation.

Current code anchor: `agent/subagents.go:452-485`.

Parent session close cascades to children through registry draining and child close. The parent should not depend on `wait` or appwire `interrupt` to clean up child jobs.

### Result shape

`wait`, successful `close_agent`, and blocking `spawn_agent`/`resume_agent` waits that produce parseable result JSON use the subagent result snapshot shape:

```json
{
  "status": "completed|failed",
  "output": "subagent result or error text",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Blocking wrappers add `agent_id` when the underlying wait returns parseable result JSON; if the wait fails before producing a snapshot, current wrappers return the wait error/result without the augmented envelope:

```json
{
  "agent_id": "01CHILD...",
  "status": "completed",
  "output": "subagent result",
  "success": true,
  "turns_used": 3,
  "transcript_ref": "local:01CHILD..."
}
```

Current code anchors:

- result struct: `agent/subagents.go:35-42`.
- snapshot construction: `agent/subagents.go:574-585`.
- blocking `spawn_agent` conditionally adds `agent_id` after a parseable wait result: `agent/session_tools_subagent.go:71-96`.
- blocking `resume_agent` conditionally adds `agent_id` after a parseable wait result: `agent/session_tools_subagent.go:127-145`.

### Future API split

A future implementation may split close from cancellation, but must not overload current semantics silently:

```json
{ "cancel_agent": { "agent_id": "01CHILD...", "keep_session": true } }
```

```json
{ "interrupt_agent": { "agent_id": "01CHILD...", "turn_id": "optional-child-turn-id" } }
```

```json
{ "close_agent": { "agent_id": "01CHILD..." } }
```

Future meanings:

- `cancel_agent`: abort the current child run but keep the child session tracked and resumable.
- `interrupt_agent`: mirror top-level appwire turn interruption for a specific child turn.
- `close_agent`: close the child session and remove the job from the registry.

Until those APIs exist, docs and UI must state that `close_agent` is the only supported child close/cleanup request and must include the timeout caveat; do not promise guaranteed child-run cancellation.

## YAGNI/DRY implementation plan

1. Keep the root-only list centralized. Do not duplicate management-tool names in multiple policy tables unless tests assert they stay in sync with `rootOnlyAgentManagementTools`.
2. Reuse the existing subagent registry and session lifecycle for close/cancel. Do not add a second cancellation registry.
3. Reuse `Session.Close()` for destructive child cleanup. Add new `cancel_agent` only if there is a concrete requirement to abort a run while preserving the child session.
4. Reuse the existing tool registry for parent-effective grant checks. Do not introduce a new capability database until approvals or appwire subagent controls require it. When tightening `tools: all`, derive the child allow set from the parent's effective callable registry minus root-only management tools.
5. Keep approval DTOs out of the live API until an approval engine can actually return `allow|deny|ask` and block operations before mutation.
6. If appwire needs subagent controls, derive advertised capabilities from the same policy functions used by the model tools; do not create a divergent UI-only policy.
7. Add narrow status/event fields only when consumers need them. Prefer existing `transcript_ref` and child transcript inspection for diagnostics.
8. Avoid broad status enum churn in legacy result paths. If richer states such as `closing`, `cancelled`, or `close_timeout` are added, make them explicit new registry/event states and update tests.

## Acceptance criteria

- `spawn_agent` from a child session fails with `subagent management is top-level only`.
- A default child cannot call `spawn_agent`, `resume_agent`, `wait`, or `close_agent`.
- `grant_tools` rejects each root-only management tool with a clear top-level-only error.
- `grant_tools` rejects a tool that is not currently callable in the parent session when that tool is not already available under the child base policy.
- A named/plugin agent whose explicit configured tools include a root-only management tool is rejected as top-level-only; an all-tools agent is allowed only if root-only management tools are stripped from the child registry, and target parent-effective behavior is tested separately if implemented.
- Grant validation uses canonical tool names before checking root-only and parent-callable policy.
- `wait(timeout_ms)` timeout returns an error and leaves the child running.
- `resume_agent` on a running child steers the active run and does not cancel it.
- `close_agent` asks the child session to close, waits for the active run, returns a result snapshot and removes the child from the registry on non-timeout success; timeout leaves the child tracked and does not prove the run stopped.
- `close_agent` timeout reports a close timeout and does not pretend the child was removed.
- Blocking `spawn_agent` and blocking `resume_agent` return `agent_id` plus the result snapshot and consume that run's wait result when the internal wait produces parseable result JSON; pre-snapshot wait errors remain unaugmented errors.
- Current web/appwire capability output does not claim dedicated subagent-management capabilities.
- Approval request/response DTOs remain documented as deferred until an implementation can block operations when policy returns `ask` and then continue or fail them based on a human `allow|deny` response.

## Tests

- Unit test root-only tool list behavior for default child policy, explicit grants, and named agent definitions.
- Unit test `grant_tools` canonicalization before policy checks.
- Unit test `grant_tools` failure messages for root-only, parent-unavailable, and child-registry-missing tools.
- Integration test that a spawned child lacks all root-only management tools in its registry.
- Integration test that child attempts to spawn another subagent fail rather than recurse.
- Lifecycle test that `wait` timeout does not call child close and the child can later finish or be closed.
- Lifecycle test that `resume_agent` while running invokes steering and leaves the same active run in progress.
- Lifecycle test that `close_agent` removes the child only after the child run ends or has no active run.
- Lifecycle test that close timeout returns an error without removing the child from the registry.
- Handler test that successful blocking `spawn_agent` and blocking `resume_agent` include `agent_id` and the canonical result fields: `status`, `output`, `success`, `turns_used`, and `transcript_ref`; also cover pre-snapshot wait-error behavior without an augmented envelope.
- Server/appwire test that capability DTOs do not include subagent-specific fields until such a surface is intentionally added.
- Future approval tests, once implemented: `ask` blocks registry mutation, `deny` fails without spawn/resume/close side effects, and `allow` never permits root-only management tools in a child.
