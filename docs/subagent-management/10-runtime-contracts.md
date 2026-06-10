# Runtime Contracts

Status: Proposed evergreen spec. This is doc 10 for subagent management and replaces the skipped declarative workflow-template spec. Serf already has task/template design coverage and plugin agent task seeds; this spec defines the cross-cutting runtime contracts that docs 6-9 depend on instead of adding another workflow-template layer.

## Purpose

Define shared contracts for policy, events, diagnostics, compatibility, lightweight helper isolation, and history invariants across subagent management features. These contracts keep plugin/agent validation, lifecycle hooks, standalone LLM helpers, and history/tree work consistent without introducing a new framework.

Declarative workflow templates are intentionally not specified here. Agent `tasks` remain child task-list seeds, not parent-side orchestration graphs. Repeatable workflows should use explicit model/tool instructions, SDK code, or existing task workflow primitives until a separate workflow-template product requirement exists.

## Goals

- Give docs 6-9 one vocabulary for runtime behavior.
- Define effective capability policy so child agents can narrow but not expand authority.
- Define event ordering and blocking boundaries for tools, subagents, hooks, helpers, and clients.
- Define diagnostic/error conventions that are useful and safe to display.
- Define compatibility tiers for Serf-native behavior and Claude/Codex-compatible subsets.
- Define lightweight helper isolation so helper LLM calls do not become hidden subagents.
- Define history metadata invariants for forks, subagents, transcripts, and derived tree views.
- Prefer small reusable helpers and existing infrastructure over new registries, event buses, middleware stacks, or schema frameworks.

## Non-goals

- No declarative workflow-template spec.
- No automatic workflow scheduler, DAG runner, or hidden subagent fanout.
- No full event-bus rewrite.
- No second provider/client middleware abstraction.
- No persistent session tree index unless measurement proves append logs plus lineage lookup are insufficient.
- No full Claude hook parity claim.
- No universal error type required across every package.
- No hidden broad validation framework when focused parser/runtime checks are enough.

## Current implementation anchors

These current files are the implementation surface this contract should cite and preserve where possible:

- Job/delegate model-facing tools: `agent/session_tools_jobs.go`, `agent/job_delegate.go`, `agent/internal/tool/definitions.go`.
- Subagent runtime and policy checks: `agent/subagents.go`.
- Subagent in-memory manager/status: `agent/subagent_manager.go`, `agent/status.go`.
- Subagent policy tests, including grant-tool rejection: `agent/plugin_agents_integration_test.go`.
- Session event taxonomy and payloads: `agent/events/events.go`, `agent/events/payloads.go`.
- Event publication/streaming: `agent/session_events.go`, `agent/session_stream.go`.
- Plugin manifest loading and `.codex-plugin` / `.claude-plugin` discovery: `agent/plugin/plugin.go`.
- Plugin agent parsing and `tools: all` representation: `agent/plugin/agents.go`.
- Plugin hook parsing: `agent/plugin/hooks.go`, `agent/plugin/hooks_test.go`.
- Hook runner and prompt/model hook helpers: `agent/internal/hooks/hooks.go`.
- Tool execution and model calls: `agent/session_tools.go`, `agent/session_model_call.go`, `agent/session_stream.go`.
- Standalone helper examples: `agent/tool_web_fetch.go`, `agent/session_namer.go`, `agent/session_tools.go` / `describeImage`, `agent/eval_probes.go`, `agent/internal/contextmgr/context_manager.go`, `agent/internal/contextmgr/strategy_memory_crystals.go`, `agent/internal/contextmgr/strategy_checkpoint_pred.go`.
- LLM client/retry/rate-limit package: `llm/`.
- Fork and lineage metadata: `agent/fork.go`, `agent/fork_test.go`, `agent/session_init.go`, `agent/session_state.go`, `agent/transcript/transcript.go`.
- Transcript discovery/read tools and outline pivots: `agent/session_tools_find.go` for discovery/`children_of`, `agent/session_tools_transcript.go` for transcript reads, `agent/session_outline.go`, `docs/tools/transcripts.md`.
- Existing task workflow design and implementation: `docs/superpowers/specs/2026-03-30-task-driven-workflow-design.md`, `agent/task/task_store.go`, `agent/plugin/agents.go`.
- Existing subagent-management docs that depend on this contract: `docs/subagent-management/06-plugin-agent-validation.md`, `docs/subagent-management/07-lifecycle-hooks-claude-compat.md`, `docs/subagent-management/08-standalone-llm-calls.md`, and `docs/subagent-management/09-session-tree-history-assessment.md`.

## Vocabulary

- **Effective capability**: the final capability set available at a runtime boundary after combining built-ins, profile/session policy, plugin agent definition, MCP discovery, parent-to-child restrictions, permission/approval mode, and provider feature support.
- **Visibility policy**: the subset of tool/model capabilities exposed to the model before a request.
- **Execution policy**: the final allow/deny decision immediately before executing a requested action.
- **Lifecycle event**: an observation of a runtime boundary. It may feed clients, logs, metrics, or hooks, but is not automatically blocking.
- **Blocking hook**: a lifecycle integration point that can alter, block, or continue execution under explicit timeout/cancellation policy.
- **Validation diagnostic**: a structured or consistently formatted issue with source, field/component, severity, and sanitized message.
- **Compatibility tier**: a documented stability/compatibility label: `serf-native`, `claude-compatible-subset`, `reserved-placeholder`, or `experimental`.
- **Lightweight helper**: a bounded one-shot LLM call owned by another operation and isolated from subagent registry/transcripts/tools.
- **Lineage metadata**: parent/child/fork fields that let clients pivot between related transcripts without merging transcript bodies.

## Contract 1: effective capability policy

### Definition

At every model-call, tool-call, plugin-agent, child-agent, hook, and helper boundary, Serf must compute or reuse an effective capability set. Effective capabilities include:

- registered built-in tools;
- root-only tool filters;
- plugin-provided agent defaults and requested tool lists;
- plugin `tools: all` semantics;
- explicit `grant_tools` from the parent;
- parent session profile/config restrictions;
- MCP server discovery and availability;
- provider/model feature support;
- approval and permission mode;
- subagent depth/recursion limits;
- cancellation/closed-session state.

### Rules

1. A child agent can inherit or narrow capabilities; it cannot expand beyond the parent/session effective capability set.
2. `tools: all` means all tools effective for this child in this session, not all registered tools in the process.
3. Explicit `grant_tools` can only add tools that are already available to the parent/session and not root-only for the child.
4. Visibility policy and execution policy must agree. If a tool is hidden from the model, a forged or stale model-returned call to that tool must still be denied before execution.
5. Root-only management tools (`delegate`, `job_watch`, and future registry-management tools) must not become visible or executable inside ordinary child agents unless an explicit future root-delegation mode is designed and tested. This applies to plugin agents that request `tools: all` as well as explicit tool lists. Ordinary child agents may use their own shell job tools and may send permitted follow-up messages through `job_send_message`.
6. Policy decisions become immutable after execution starts. Later lifecycle events may observe the decision/result but must not retroactively mutate an already-started tool execution.
7. Partial MCP startup, if supported, must be explicit. Unknown or unavailable MCP tools must either fail deterministically at session/agent startup or at spawn/execution time according to the MCP partial/fail-fast mode; do not silently ignore requested tools.
8. Provider feature policy must run before request construction. A helper/subagent/session cannot request unsupported provider features merely because an agent definition names them.
9. Cancellation and close state are part of effective policy. A closed/cancelling session must deny new child work and unblock/abort pending approvals where possible.

Current Serf treats plugin-agent `tools: all` as an unrestricted child registry request after child session initialization, with root-only management tools stripped by depth. The stronger parent/session effective-policy intersection above is the target contract; implementation should characterize current root-only stripping and add tests that hidden non-root tools cannot be re-enabled before claiming the target is enforced.

### Implementation guidance

- Reuse current canonical tool-name mapping and grant checks in `agent/subagents.go`.
- Keep the `agent/plugin/agents.go` `AllTools` flag as a request, not an authorization result.
- Centralize policy checks in small helpers that can be called by prompt visibility, spawn validation, and execution-time validation.
- Do not introduce an ACL engine unless current checks become duplicated and inconsistent.

## Contract 2: event and ordering

### Canonical order

Runtime surfaces should observe the same order. Existing event names may remain; the contract is about sequencing and semantics.

Session lifecycle:

1. session starting;
2. session started;
3. user input accepted;
4. model request starting;
5. model request finished or failed;
6. optional compaction starting/finished;
7. session finishing;
8. session finished or failed.

Tool lifecycle:

1. effective tool catalog resolved;
2. tool visibility policy applied before model request;
3. model emits tool call;
4. tool name decoded and arguments parsed best-effort for hook input;
5. compatibility plugin `PreToolUse` hook runs if configured and may update input;
6. final arguments are decoded and schema-validated;
7. final execution policy runs on the validated final input;
8. typed SDK policy hook observes or blocks only if explicitly designed to block;
9. tool-start event emits after the call is accepted for execution, or the contract explicitly labels start events as attempted calls if current code emits earlier;
10. tool executes;
11. tool-end/failure event emits;
12. compatibility `PostToolUse` hooks run according to the lifecycle-hooks spec. Failure/batch hook names are reserved until implemented and tiered in the lifecycle-hooks spec.

Current Serf emits `TOOL_CALL_START` before registry lookup, schema validation, middleware, and execution policy. Until migrated, treat that event as an attempted-call observation and test both current and target ordering during the migration.

Current Serf starts the delegate child run through `spawnAgent` before `attachDelegateJobWithID` emits `JOB_STARTED`. On completion, `finishJob` emits `JOB_FINISHED` before `armFinalizedJob` closes the run's `done` channel. The sequence below remains the **target canonical order for new lifecycle/hook work**. If implementation changes the ordering to match this target, it must do so deliberately and with regression tests for bounded readers, event subscribers, and hook execution.

Delegate job lifecycle target:

1. delegate request decoded;
2. agent type resolved;
3. effective child policy computed;
4. child session/job created;
5. optional `SubagentStart` compatibility/SDK hooks run before first child model turn if they can add context;
6. `JOB_STARTED` event emits with stable job and child identity;
7. child run starts;
8. child run completes, fails, or is cancelled;
9. `SubagentStop` compatibility hook runs, with loop guard if it can block completion;
10. result snapshot/diagnostic is finalized;
11. registry/status updates;
12. bounded readers/watchers are released and `JOB_FINISHED` event emits in a deterministic tested order.

Helper lifecycle:

1. owning operation validates helper request;
2. helper model/profile options resolved;
3. one model request runs through the intended client/retry/rate-limit path;
4. helper result/error attaches to owning operation diagnostics;
5. no subagent lifecycle event or transcript is created.

### Event payload rules

1. Events must carry stable IDs or enough fields for idempotent projection where clients use them. Current `JOB_STARTED` payloads carry `job_id`, `job_type`, `status`, and `from_watch`; current `JOB_FINISHED` payloads carry `job_id`, `job_type`, `status`, `reason`, `exit_code`, `output_bytes`, `transcript_ref`, and `from_watch`. Parent session ID is available on the event envelope.
2. Observability events must not block critical paths. Blocking behavior belongs to explicit hooks with context, timeout, and error policy.
3. Event payloads must avoid raw secrets and unbounded data by default.
4. Event payloads should include references or offsets for large outputs/transcripts, not inline full transcript bodies.
5. Hook start/end events should identify hook event/type and result status without leaking hook command env or secret output.
6. Client projections must treat events as live observations, not durable truth after reconnect. Refresh from status/registry/transcripts on reconnect.

### Implementation guidance

- Extend existing `agent/events/events.go` and `agent/events/payloads.go` additively when possible.
- Keep compatibility with current `JOB_STARTED` and `JOB_FINISHED` consumers.
- Prefer a simple projector from existing session events to Hub/TUI/SDK DTOs over a new event bus.

## Contract 3: diagnostics and validation errors

### Required fields when available

Diagnostics should include:

- source path, transcript ref, plugin name, agent type, tool name, or component name;
- field/path, such as `agents[0].name`, `tools[2]`, or `mcpServers.github.env.GITHUB_TOKEN` with the value redacted;
- severity: `error`, `warning`, or `info` only if there is a visible surface for non-errors;
- concise message with the violated rule;
- sanitized contextual metadata, such as plugin directory, manifest file name, hook event name, provider name, or MCP server name;
- causal error category where useful: validation, policy_denied, unavailable, timeout, cancellation, provider_error, hook_blocked, hook_failed, transcript_unavailable.

Optional DTO shape:

```go
type Diagnostic struct {
    Source   string `json:"source,omitempty"`
    Kind     string `json:"kind"`
    Severity string `json:"severity"`
    Field    string `json:"field,omitempty"`
    Message  string `json:"message"`
}
```

This type is illustrative. Runtime errors and validation diagnostics do not need one universal exported type if package-local types are simpler.

### Rules

1. Validation diagnostics for plugin manifests and agent definitions must include the manifest/agent file path and field where practical.
2. Runtime policy denials must identify the denied action and the policy boundary, not only say "not allowed".
3. Secret values must never appear in diagnostic messages, event payloads, hook errors, or test snapshots. Redact API keys, tokens, authorization headers, env values, MCP secrets, and provider request bodies unless a field is explicitly known safe.
4. Group related validation issues when practical so plugin authors can fix multiple problems in one pass. Do not build a broad framework if a `[]ValidationIssue` plus formatter is sufficient.
5. Warnings should not be added unless CLI/TUI/Hub/API surfaces display them. Silent warnings are bugs.
6. Error strings should be stable enough for users to understand, but tests should prefer structured fields over brittle full-string matching where structured fields exist.
7. Diagnostics should report compatibility gaps as unsupported, ignored, or reserved. Do not half-accept unsupported config with runtime surprises.

### Implementation guidance

- Build narrow validation helpers near `agent/plugin/plugin.go`, `agent/plugin/agents.go`, and `agent/plugin/hooks.go`.
- Use existing `agent/events.WarningData`, `agent/events.ErrorData`, and `agent/events.ErrorCause` for session-level reports where suitable.
- Keep redaction helpers small and shared where MCP/plugin/hook/provider diagnostics overlap.

## Contract 4: compatibility tiers

### Tiers

- `serf-native`: behavior owned by Serf, expected to remain stable except for documented migrations.
- `claude-compatible-subset`: a current implemented subset of Claude/Codex plugin/hook/agent behavior. Compatibility is only claimed for tested fields/events/semantics.
- `reserved-placeholder`: names or mapping points reserved for future compatibility, but not guaranteed as implemented behavior.
- `experimental`: opt-in behavior that may change and must be labeled in docs/API/tests.

### Rules

1. Do not claim full Claude hook or plugin parity until event types, handler types, matcher semantics, output parsing, environment variables, permission filters, async behavior, UI/status fields, and config-change behavior are implemented and tested.
2. The current `.codex-plugin` and `.claude-plugin` loading behavior in `agent/plugin/plugin.go` must be documented and tested: when both `<dir>/.codex-plugin/plugin.json` and `<dir>/.claude-plugin/plugin.json` exist, Serf loads `.codex-plugin` and ignores `.claude-plugin` for that plugin root.
3. Namespacing rules must be stable and unambiguous: plugin agents use `plugin:agent`; plugin skills and MCP names must have documented equivalent prefixes or mappings. If plugin-provided tools are added later, define their namespace as a reserved or experimental surface before documenting them as supported.
4. Unsupported high-risk fields should fail clearly or be explicitly ignored according to the relevant compatibility policy. They must not be documented as working.
5. Compatibility shims must not bypass Serf-native effective policy, diagnostics, event ordering, cancellation, or helper-isolation contracts.
6. Reserved placeholders may appear in docs only with clear language that they are not yet implemented guarantees.

### Implementation guidance

- Keep compatibility parsing close to existing plugin parsers.
- Add tests for accepted subset, rejected unsupported shapes, and reserved-but-not-implemented fields.
- Prefer additive payload fields over renaming existing event/DTO fields.

## Contract 5: lightweight helper isolation

### Rules

1. A lightweight helper performs one bounded model request, not an agent loop.
2. A helper must not create a subagent job, subagent manager entry, child transcript, fork, or lineage node.
3. A helper must not execute tools.
4. A helper must not mutate task stores, session history, plugin agent state, or project docs.
5. A helper must not run plugin hooks by default. If a future hook handler uses an LLM internally, that is hook-owned behavior and still must obey hook policy.
6. A helper may reuse provider/profile/model resolution, the retry semantics of its chosen underlying LLM API, rate-limit header metadata, Complete-based API logging middleware when attached, stream middleware for streaming helpers, prompt cache, and usage accounting where safe.
7. Helper diagnostics belong to the owning operation: tool result metadata, events, debug logs, or SDK callback data. They must not appear in `job_list` or child job status.
8. Helper prompts must be bounded and testable. Cacheable helper outputs require input hash, prompt version, model/profile version, and relevant option keys in the cache key.
9. Helper failures are surfaced as part of the owning operation. They should not be reported as child-agent failures.

### Implementation guidance

- Inventory current direct `llm.Client.Complete` helper-like calls before adding an API.
- If a helper service is added, make it a thin wrapper over `llm.Client`/`llm.Generate` and document which underlying path supplies retry behavior; do not imply active rate limiting beyond current rate-limit metadata handling.
- Do not add a second middleware abstraction; current `llm` package infrastructure is the baseline.

## Contract 6: history metadata invariants

### Rules

1. Parent/child/fork lineage must be recorded in metadata discoverable without reading full transcript bodies when persistence is enabled.
2. `ParentSessionID` and fork `DivergenceTurn` are immutable after creation except deliberate repair/migration.
3. Transcript header and session meta should agree on parent lineage where both store it.
4. Meta rewrite, resume, compaction, and repair must preserve lineage and fork labels.
5. Missing parent references are diagnostics for list/tree views, not fatal crashes.
6. Parent transcript rendering should contain child lifecycle/result pivots and transcript refs, not inline child transcript bodies by default.
7. Child transcript reads are explicit and bounded through transcript tools/clients.
8. Forks and named subagents are different relations. Do not require named subagents to inherit parent history merely because forks can.
9. Subagent lineage and fork lineage may share DTOs/indexes, but the relation type must remain explicit.
10. Any optional tree view should be derived from lineage metadata first. Persistent child indexes require measurement and a concrete client need.

### Implementation guidance

- Preserve existing fork tests around `ParentSessionID`, `DivergenceTurn`, and `ForkLabel`.
- Normalize subagent lineage fields alongside `agent/subagents.go` spawn metadata and `agent/transcript/transcript.go` headers.
- Treat `find_session_transcripts(children_of=...)` and outline pivots as the primary audit path. `children_of` returns both subagents and forks derived from lineage metadata; consumers that need one relation type can inspect the returned `kind` field, while consumers needing full lineage metadata must use session metadata APIs or another explicit read path.

## YAGNI/DRY implementation plan

1. **Name the contracts before adding framework.** Document policy/event/diagnostic/helper/history vocabulary in docs and comments near existing code.
2. **Audit duplication.** Identify the smallest set of existing call sites where policy, event ordering, redaction, and helper isolation are currently duplicated.
3. **Extract narrow helpers only where duplication exists.** Examples: canonical effective tool checks, plugin validation issue formatting, redaction, helper-call observation. Avoid speculative manager types.
4. **Keep current packages authoritative.** Plugin validation stays near `agent/plugin`; subagent policy stays near `agent/subagents.go`; events stay in `agent/events`; LLM helpers reuse `llm` and session model-call paths where appropriate.
5. **Add additive DTO fields.** Preserve old event/status fields and add new fields only when consumers need them.
6. **Write characterization tests first for current behavior that must be preserved.** Then add failing tests for the new contract point.
7. **Prefer derived views.** Build client/tree/status projections from existing registry/transcript/session metadata before adding new storage.
8. **Do not create workflow templates as a side effect.** If an implementation task wants a workflow-like feature, route it back to existing task workflow design or a separate future spec.
9. **Stop when contracts are enforceable.** A doc, a few helpers, and targeted tests are enough if behavior is now consistent.

## Acceptance criteria

- This doc explicitly replaces the skipped declarative workflow-template spec.
- The doc defines effective capability, visibility policy, execution policy, lifecycle event, blocking hook, validation diagnostic, compatibility tier, lightweight helper, and lineage metadata.
- `tools: all` is specified as effective child/session tools, not all registered tools.
- Child agents cannot gain tools/capabilities unavailable to the parent/session.
- Visibility and execution policy parity is specified and testable.
- Tool, subagent, session, and helper event ordering is specified enough for tests.
- Diagnostics require source/field/context where available and require secret redaction.
- Compatibility tiers distinguish implemented subset from reserved placeholders.
- Lightweight helpers are isolated from subagent registry/transcripts/tools/task mutation.
- History invariants preserve parent lineage across fork/resume/meta rewrite/compaction/repair.
- The implementation plan is YAGNI/DRY and does not introduce large new frameworks.
- All citations point to current Serf files rather than external inspiration repos.

## Test matrix

Policy tests:

- Existing grant-tool rejection remains covered by `agent/plugin_agents_integration_test.go`; extend it to assert children cannot gain tools hidden from the parent/session.
- `tools: all` plugin agent receives only the parent/session-effective child registry, excludes root-only management tools, and cannot re-enable tools filtered from the parent/session/profile.
- Visibility/execution parity: a tool hidden from the model is also denied if returned by a forged/stale model response.
- MCP unavailable tool behavior matches the configured fail-fast/partial mode.
- Cancellation/closed-session state denies new child work.

Event and ordering tests:

- Tool lifecycle ordering: visibility policy, decoded/best-effort hook input, `PreToolUse`, updated-input merge, final schema validation, final policy, tool start, execution, tool end, `PostToolUse`.
- Delegate job start ordering: policy validation, child creation, optional start hook, `JOB_STARTED`, first child turn.
- Delegate job stop ordering: child completion, `SubagentStop`, result finalization/status update, bounded reader release/`JOB_FINISHED` order matching the documented current or migrated semantics.
- Session start/end exactly once on normal completion, provider failure, cancellation, and close.
- Observability events do not block unless the tested surface is an explicit blocking hook.

Diagnostics tests:

- Plugin manifest validation includes source path and field.
- Plugin agent validation includes agent file path, field, and duplicate name context.
- Hook failure diagnostic includes event name and sanitized component context.
- MCP/provider/plugin diagnostics redact env values, authorization headers, API keys, tokens, and request secrets.
- Multi-issue validation formatting is deterministic.

Compatibility tests:

- Current valid `.codex-plugin` and `.claude-plugin` fixtures load successfully.
- Precedence when both plugin directories exist matches `agent/plugin/plugin.go` behavior.
- Unsupported/reserved hook fields are either rejected or ignored exactly as documented.
- Existing hook events in `agent/plugin/hooks.go` remain accepted.
- Namespaced plugin agents remain `plugin:agent`.

Helper-isolation tests:

- `web_fetch`/session-namer/`describeImage`/helper-like calls do not register subagents or create child transcripts.
- Helper calls use the intended middleware path, documented retry semantics, rate-limit metadata handling, and context cancellation.
- Helper diagnostics attach to the owning operation and not `DetailedStatus.Subagents`.
- Cacheable helpers include prompt/model/input version keys.

History tests:

- Fork creation records `ParentSessionID`, `DivergenceTurn`, and preserves `ForkLabel` as current `agent/fork_test.go` expects.
- Meta rewrite preserves lineage fields.
- Subagent child transcripts record parent session/task/tool-call metadata when persistence is enabled.
- `children_of` transcript discovery returns subagents/forks from lineage metadata or index.
- Missing parent references produce diagnostics/orphan nodes, not crashes.
- Parent compaction and history repair preserve child lifecycle pivots and transcript refs without replaying tools.
