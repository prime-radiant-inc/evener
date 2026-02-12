# Audit: Section 1 - Overview and Goals

**Spec**: `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` (lines 23-121)
**Auditor**: Bot
**Date**: 2026-02-11

---

## Summary

Section 1 defines the agent loop's identity: a programmable library (not just a CLI), provider-aligned tool profiles, extensible execution environments, event-driven architecture, and hackable defaults. Of the 8 auditable requirements identified, 3 pass fully, 4 pass partially, and 1 fails.

| Status   | Count |
|----------|-------|
| PASS     | 3     |
| PARTIAL  | 4     |
| FAIL     | 1     |

---

## Findings

### GAP-1.01: Programmable-first -- library, not a CLI

**Status**: PARTIAL

**Spec text** (1.2, 1.3):
> This spec defines a **library** -- a programmable agentic loop that a host application controls at every step.

**Evidence**:
- All agent logic resides under `internal/agent/` and `internal/llm/` (Go `internal` packages).
- Go's `internal` package convention makes these packages **unimportable by external modules**. Only code inside the `primeradiant.com/serf` module tree can import `internal/agent`.
- The CLI (`cmd/serf/`) consumes the library, demonstrating the architecture works internally.
- A third-party Go project cannot `import "primeradiant.com/serf/internal/agent"` -- the Go compiler rejects it.

**What works**:
- Architecturally, the code is cleanly organized as a library consumed by a CLI host. Session, events, profiles, and execution environments are distinct abstractions, not tangled into the CLI.

**What's missing**:
- No public API package (e.g., `pkg/agent` or a top-level `agent` package) exists. External consumers cannot programmatically use the library.
- The spec says "CLIs, IDEs, web UIs, batch systems, CI pipelines, evaluation harnesses, and agent-to-agent coordination systems all consume the same library." This is only possible today for code inside the serf module itself.

**Recommendation**: Move `internal/agent` to a public package (e.g., `agent/` or `pkg/agent/`), or provide a public facade package that re-exports the key types.

---

### GAP-1.02: Programmatic control points -- submit, observe, steer, configure, swap, compose

**Status**: PARTIAL

**Spec text** (1.2):
> The host can:
> - Submit input and observe every event
> - Steer the agent mid-task
> - Change configuration on the fly -- reasoning effort, model, timeouts
> - Swap where tools run
> - Compose agents by spawning subagents

**Evidence** -- what exists:
- `Session.ProcessInput(ctx, input)` -- submit input: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:483`
- `Session.Events()` -- observe events: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:373`
- `Session.Steer(msg)` -- mid-task steering: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:412`
- `Session.FollowUp(msg)` -- follow-up queue: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:425`
- `Session.SetReasoningEffort(effort)` -- change reasoning effort on the fly: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:402`
- `ExecutionEnvironment` interface -- swap execution target: `/Users/jesse/prime-radiant/serf/internal/agent/env.go:20`
- Subagent tools (`spawn_agent`, `send_input`, `wait`, `close_agent`): `/Users/jesse/prime-radiant/serf/internal/agent/subagents.go`

**Evidence** -- what's missing:
- **No `SetModel()` method.** The spec says the host can "change configuration on the fly -- reasoning effort, **model**, timeouts -- between any two turns." No method exists to change the model after session creation. `Grep` for `SetModel|ChangeModel` returns zero results across the entire codebase.
- **No `SetTimeout()` or equivalent.** No method to change `DefaultCommandTimeoutMS`, `MaxCommandTimeoutMS`, or `MaxToolRoundsPerInput` after session creation. The spec says the host can change "timeouts" on the fly.
- **No public `RegisterTool()` on Session.** The `reg` field is unexported (`session.go:115`). Tests access `sess.reg` directly only because they're in the same package. An external consumer (even within the module) would need a public method. The spec's "tool execution" control point requires this.

**Recommendation**: Add `Session.SetModel(model string)`, `Session.SetCommandTimeout(ms int)`, and `Session.RegisterTool(tool RegisteredTool) error` as exported methods.

---

### GAP-1.03: Provider-aligned tool profiles

**Status**: PASS

**Spec text** (1.3):
> Provider-aligned. Each model family works best with its native agent's tools and system prompts. The spec defines provider-specific tool profiles, not a single universal set.

**Evidence**:
- `NewOpenAIProfile(model)`: uses `apply_patch`, maps `shell`->`exec_command`, `grep`->`grep_files`, `glob`->`list_dir`: `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:203-237`
- `NewAnthropicProfile(model)`: uses `edit_file`, no `apply_patch`, no name mapping: `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:239-273`
- `NewGeminiProfile(model)`: uses `edit_file` and `read_many_files`, includes `web_search`, maps `shell`->`run_shell_command`: `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:275-322`
- `ProviderProfile` interface with `ToolDefinitions()`, `ToolNameMap()`, `BuildSystemPrompt()`: `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:35-54`
- Each profile has provider-specific `docFiles` (AGENTS.md for OpenAI, CLAUDE.md for Anthropic, GEMINI.md for Gemini).
- Provider-specific `providerOpts` for Anthropic (beta headers) and Gemini (safety settings): `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:251-255`, `287-295`

**Assessment**: Fully compliant. Three distinct profiles with provider-specific tool sets, name mappings, system prompts, and options.

---

### GAP-1.04: Extensible execution -- ExecutionEnvironment interface

**Status**: PASS

**Spec text** (1.3):
> Tool execution is abstracted behind an `ExecutionEnvironment` interface. The default runs locally. Implementations can target Docker, Kubernetes, WASM, or any remote host.

**Evidence**:
- `ExecutionEnvironment` interface: `/Users/jesse/prime-radiant/serf/internal/agent/env.go:20-40`
  - Methods: `Initialize()`, `Cleanup()`, `WorkingDirectory()`, `Platform()`, `OSVersion()`, `ReadFile()`, `WriteFile()`, `EditFile()`, `FileExists()`, `Glob()`, `Grep()`, `ListDirectory()`, `ExecCommand()`
- `LocalExecutionEnvironment` implements the interface: `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go:33-44`
- Tool functions accept `ExecutionEnvironment` as a parameter: `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:47`
- `NewSession()` takes `ExecutionEnvironment` as a required parameter: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:156`

**Assessment**: Fully compliant. The interface is well-defined, tools pass through it, and swapping implementations is straightforward. Only the local implementation exists (which is expected -- Docker/K8s are out of scope for v1).

---

### GAP-1.05: Event-driven -- typed events as primary interface

**Status**: PARTIAL

**Spec text** (1.3):
> Every agent action emits a typed event for UI rendering, logging, and integration. The event stream is the primary interface for host applications.

**Evidence** -- what exists:
- 17 typed event kinds defined: `/Users/jesse/prime-radiant/serf/internal/agent/events.go:7-25`
  - `SESSION_START`, `SESSION_END`, `USER_INPUT`, `ASSISTANT_TEXT_START`, `ASSISTANT_TEXT_DELTA`, `ASSISTANT_TEXT_END`, `TOOL_CALL_START`, `TOOL_CALL_OUTPUT_DELTA`, `TOOL_CALL_END`, `STEERING_INJECTED`, `TURN_LIMIT`, `LOOP_DETECTION`, `COMMUNICATE`, `SKILL_ACTIVATED`, `CONTEXT_COMPACTION`, `WARNING`, `ERROR`
- `SessionEvent` struct with `Kind`, `Timestamp`, `SessionID`, `Data`: `/Users/jesse/prime-radiant/serf/internal/agent/events.go:27-32`
- `Session.Events()` returns `<-chan SessionEvent`: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:373`
- Events emitted throughout the loop: tool calls, text, errors, steering, etc.

**Evidence** -- what's partial:
- `SessionEvent.Data` is typed as `map[string]any` -- an untyped bag. The spec says "typed event" which implies each event kind should have a well-defined payload shape. Currently, consumers must cast/assert data fields with no compile-time guarantees. For example, `ev.Data["text"].(string)` is runtime-checked only (see `/Users/jesse/prime-radiant/serf/cmd/serf/run.go:195`).
- Events are **best-effort delivery** -- the channel is buffered (256) and events are dropped if the consumer is too slow: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:687`. The `emit()` method recovers from panics on closed channels: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:683`. This is pragmatic but means events can be silently lost. The spec calls the event stream "the primary interface" -- losing events silently could be problematic.
- No event for subagent lifecycle (spawn, complete, fail). Subagent events are only visible through the `spawn_agent` tool result and the parent's `TOOL_CALL_START`/`END`.

**Recommendation**: Consider adding typed payload structs per event kind (at least for the most important ones). Document the best-effort delivery semantics.

---

### GAP-1.06: Hackable -- override points for timeouts, output sizes, tool sets, execution environments, system prompts, reasoning effort

**Status**: PARTIAL

**Spec text** (1.3):
> Reasonable defaults with override points everywhere -- timeouts, output sizes, tool sets, execution environments, system prompts, reasoning effort.

**Evidence** -- what exists:
- **Timeouts**: `SessionConfig.DefaultCommandTimeoutMS` (default 10s), `MaxCommandTimeoutMS` (default 600s): `/Users/jesse/prime-radiant/serf/internal/agent/session.go:32-33`
- **Output sizes**: `SessionConfig.ToolOutputLimits` overrides per-tool truncation: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:37`; default limits per tool: `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:233-262`
- **Execution environments**: `ExecutionEnvironment` interface is a constructor parameter.
- **System prompts**: Multi-layer resolution with `SystemPromptFile`, `SystemPromptAppend`, `UserInstructionOverride`: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:56-62`
- **Reasoning effort**: `SessionConfig.ReasoningEffort` + `Session.SetReasoningEffort()`: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:43-44`, `402-409`

**Evidence** -- what's missing:
- **Model**: No runtime override. `SessionConfig` does not include a model field; the model comes from `ProviderProfile` which is immutable after session creation.
- **Timeouts on the fly**: `DefaultCommandTimeoutMS` cannot be changed after session creation. No setter method exists.
- **Tool sets on the fly**: `reg` is unexported; no `Session.RegisterTool()` or `Session.UnregisterTool()` public methods. The `ToolRegistry` has `Register()` and `Unregister()` methods, but they're only accessible through the unexported `reg` field.

**Assessment**: Most override points exist at construction time. The gap is in runtime mutability -- the spec says "between any two turns" which implies runtime changes, but only `SetReasoningEffort` supports this today.

---

### GAP-1.07: Architecture diagram compliance

**Status**: PASS

**Spec text** (1.3 Architecture):
> Session (history, steering queue, event emitter), Provider Profiles, Tool Registry, Execution Env -- all present in the diagram.

**Evidence**:
- **Session**: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:99-154`
  - `history []Turn` (line 113)
  - `steeringQueue []string` (line 117)
  - `events chan SessionEvent` (line 107)
- **Provider Profiles**: `/Users/jesse/prime-radiant/serf/internal/agent/profile.go:35-54` (interface) + `203-322` (OpenAI, Anthropic, Gemini)
- **Tool Registry**: `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:52-55`
  - Tool dispatch: `ExecuteCall()` at line 135
  - Truncation: `truncateResult()` at line 182
  - Validation: JSON schema validation at line 161
- **Execution Environment**: `/Users/jesse/prime-radiant/serf/internal/agent/env.go:20-40` (interface) + `env_local.go` (local impl)
- **Unified LLM SDK**: `/Users/jesse/prime-radiant/serf/internal/llm/client.go` (`Client.Complete()`, `Client.Stream()`)
- **Host Application (CLI)**: `/Users/jesse/prime-radiant/serf/cmd/serf/` consuming the agent library

**Assessment**: All components from the architecture diagram are present and correctly layered.

---

### GAP-1.08: Uses Client.complete() not generate()

**Status**: PASS (with note)

**Spec text** (1.3 Architecture, para below diagram):
> The agent loop does NOT use the Unified LLM SDK's `generate()` high-level function. It uses the low-level `Client.complete()` and implements its own loop.

**Evidence**:
- The agent loop calls `s.client.Complete(ctx, req)` directly: `/Users/jesse/prime-radiant/serf/internal/agent/session.go:845`
- `Grep` for `Generate(` in the agent package returns zero matches -- the agent never calls `llm.Generate()`.
- The agent implements its own tool loop with steering injection, event emission, truncation, and loop detection -- exactly as specified.

**Assessment**: Fully compliant.

---

### GAP-1.09: Imports from Unified LLM SDK -- required types

**Status**: FAIL

**Spec text** (1.5):
> The agent loop imports and uses these types directly:
> - `Client`, `Request`, `Response` -- for LLM communication
> - `Message`, `ContentPart`, `Role` -- for conversation history
> - `Tool`, `ToolCall`, `ToolResult` -- for tool definitions and results
> - `StreamEvent` -- for streaming responses
> - `Usage` -- for token tracking
> - `FinishReason` -- for stop condition detection

**Evidence** -- what's used:

| SDK Type | Used in agent? | Where |
|----------|---------------|-------|
| `llm.Client` | Yes | `session.go:102` (field type), `session.go:156` (constructor param) |
| `llm.Request` | Yes | `session.go:824` (built per LLM call) |
| `llm.Response` | Yes | `session.go:573`, `844` |
| `llm.Message` | Yes | `session.go:565`, `801`, throughout |
| `llm.ContentPart` | Yes | `session.go:950-954`, `tool_registry.go` |
| `llm.Role` | Yes | `session.go:962` (`llm.RoleTool`) |
| `llm.Tool` | **No** | The agent uses its own `RegisteredTool` type, not `llm.Tool` from `generate.go:15` |
| `llm.ToolCallData` | Yes (as `ToolCall` equivalent) | `session.go:520`, `903` |
| `llm.ToolResultData` | Yes (as `ToolResult` equivalent) | `session.go:954` |
| `llm.StreamEvent` | **No** | Agent uses its own `SessionEvent` type; never imports `llm.StreamEvent` |
| `llm.Usage` | Yes | `session.go:576`, `turns.go:26` |
| `llm.FinishReason` | Yes | `session.go:899` (`llm.FinishReasonPauseTurn`) |

**What's missing**:
1. **`llm.Tool`**: The spec says the agent imports `Tool` from the SDK. The SDK has `llm.Tool` (in `generate.go:15`) which bundles `ToolDefinition` + `Execute`. The agent instead uses `llm.ToolDefinition` (for schemas) and its own `RegisteredTool` (for execution). This is a semantic deviation: the agent's `RegisteredTool` is functionally equivalent but is not the same type as `llm.Tool`.

2. **`llm.StreamEvent`**: The spec explicitly lists `StreamEvent` as an import. The agent does not use streaming at all -- it calls `Complete()` only. The agent has its own `SessionEvent` type with completely different event names and semantics. While this is a reasonable design choice (the agent doesn't stream LLM responses; it emits higher-level session events), it deviates from the spec's stated imports.

**Assessment**: FAIL. Two of the eight required SDK type imports are not used. The agent substitutes its own types (`RegisteredTool` for `Tool`, `SessionEvent` for `StreamEvent`). The functional equivalence is arguable, but the spec says "imports and uses these types directly."

---

## Cross-cutting Observations

### Observation 1: `internal` package placement limits library usability

The entire library lives under `internal/`, which in Go means external modules cannot import it. The spec's core thesis is that this is a library -- "CLIs, IDEs, web UIs, batch systems, CI pipelines, evaluation harnesses, and agent-to-agent coordination systems all consume the same library." This is architecturally true (the code is well-separated) but not practically achievable for external Go consumers today.

### Observation 2: Streaming not implemented in the agent loop

The spec mentions `Client.stream()` as an alternative to `Client.complete()` (line 4, 97, 120). The agent only uses `Complete()`. While the spec allows this ("calls `Client.complete()` or `Client.stream()`"), streaming would enable real-time text deltas from the LLM, which would make the `ASSISTANT_TEXT_DELTA` event more meaningful. Currently, `ASSISTANT_TEXT_DELTA` emits the full text at once after `Complete()` returns, which defeats the purpose of incremental delivery.

### Observation 3: `RegisteredTool` vs `llm.Tool` -- a justified divergence?

The agent's `RegisteredTool` adds truncation limits and JSON schema validation that `llm.Tool` lacks. The divergence is functionally motivated: the agent needs per-tool output limits and schema enforcement, which the SDK's `Tool` type doesn't provide. However, the spec says "uses these types directly" which suggests wrapping or extending rather than replacing.

---

## File References

| File | Relevant To |
|------|-------------|
| `/Users/jesse/prime-radiant/serf/internal/agent/session.go` | GAP-1.01 through GAP-1.08 |
| `/Users/jesse/prime-radiant/serf/internal/agent/events.go` | GAP-1.05 |
| `/Users/jesse/prime-radiant/serf/internal/agent/env.go` | GAP-1.04 |
| `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` | GAP-1.04 |
| `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go` | GAP-1.06, GAP-1.07, GAP-1.09 |
| `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` | GAP-1.03, GAP-1.07 |
| `/Users/jesse/prime-radiant/serf/internal/agent/turns.go` | GAP-1.07 |
| `/Users/jesse/prime-radiant/serf/internal/llm/types.go` | GAP-1.09 |
| `/Users/jesse/prime-radiant/serf/internal/llm/client.go` | GAP-1.08, GAP-1.09 |
| `/Users/jesse/prime-radiant/serf/internal/llm/generate.go` | GAP-1.08, GAP-1.09 |
| `/Users/jesse/prime-radiant/serf/internal/llm/stream.go` | GAP-1.09 |
| `/Users/jesse/prime-radiant/serf/cmd/serf/run.go` | GAP-1.01, GAP-1.05 |
| `/Users/jesse/prime-radiant/serf/go.mod` | GAP-1.01 |
