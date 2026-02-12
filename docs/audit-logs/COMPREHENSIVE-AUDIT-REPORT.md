# Comprehensive Spec Compliance Audit Report

**Spec**: `coding-agent-loop-spec.md`
**Codebase**: `primeradiant.com/serf` (Go)
**Date**: 2026-02-11
**Auditor**: Claude Opus 4.6 (8 parallel section auditors + synthesis)

---

## Executive Summary

89 items were audited across all 8 spec sections plus appendices. The codebase is **substantially compliant** with the spec, with strong implementation of the core loop, provider profiles, tool execution, output truncation, system prompts, and subagents. The most critical gaps are:

1. **Graceful shutdown ordering** (GAP-8.23) - the only true FAIL
2. **Round/turn limits return errors instead of breaking cleanly** (GAP-2.16, 2.17)
3. **No public API package** - everything under `internal/` (GAP-1.01)
4. **No runtime mutability** for model, timeouts, or tool sets (GAP-1.02, 1.06)
5. **No Windows shell support** (GAP-4.07)

| Severity | Count |
|----------|-------|
| FAIL | 2 |
| MISSING (should fix) | 3 |
| PARTIAL (deviates but works) | 19 |
| MINOR (cosmetic or low-impact) | 22 |
| INFORMATIONAL | 12 |
| INTENTIONAL DEVIATION | 4 |
| PASS | 27 |
| **Total items audited** | **89** |

---

## All Gaps by Severity

### FAIL (2)

| ID | Description | Section |
|----|-------------|---------|
| GAP-1.09 | `llm.Tool` and `llm.StreamEvent` SDK types not used; replaced by `RegisteredTool` and `SessionEvent` | 1 |
| GAP-8.23 | Graceful shutdown ordering wrong: state->CLOSED first, processes killed after SESSION_END, no event flush | 8/B |

### MISSING (3)

| ID | Description | Section |
|----|-------------|---------|
| GAP-2.16 | Round limit (`max_tool_rounds_per_input`) returns error instead of BREAK; prevents follow-up processing | 2 |
| GAP-2.17 | Turn limit (`max_turns`) returns error instead of BREAK; uses `>` instead of `>=` (allows one extra turn) | 2 |
| GAP-2.19 | `ASSISTANT_TEXT_START` event emits empty data map (no identifying info) | 2 |

### PARTIAL (19)

| ID | Description | Section |
|----|-------------|---------|
| GAP-1.01 | All code under `internal/` -- external Go modules cannot import the library | 1 |
| GAP-1.02 | Missing `SetModel()`, `SetTimeout()`, public `RegisterTool()` on Session | 1 |
| GAP-1.05 | Event data is `map[string]any` (untyped); best-effort delivery with silent drops; no subagent lifecycle events | 1 |
| GAP-1.06 | Most overrides are construction-time only; only `SetReasoningEffort()` is runtime-mutable | 1 |
| GAP-2.03 | `AWAITING_INPUT` detection uses `strings.HasSuffix(trimmed, "?")` heuristic | 2 |
| GAP-2.04 | Turn limit checked outside the main loop, not inside per spec | 2 |
| GAP-2.05 | Round counter increments on `pause_turn` responses (web search) even without tool calls | 2 |
| GAP-2.08 | Unknown tool TOOL_CALL_END uses `full_output` key instead of `error` key | 2 |
| GAP-2.11 | TOOL_CALL_END event uses `full_output` key; spec says `output` | 2 |
| GAP-2.12 | TOOL_CALL_END error case uses `is_error` + `full_output` instead of separate `error` key | 2 |
| GAP-3.01 | ProviderProfile does not own a ToolRegistry; Session owns it instead | 3 |
| GAP-3.02 | Anthropic `context_window_size` is 200K; spec references 1M context with beta headers | 3 |
| GAP-5.01 | Truncation uses Go `len()` (bytes) not `utf8.RuneCount` (characters) | 5 |
| GAP-6.01 | MCP and custom tool descriptions placed after project docs, violating layer 3 before layer 4 ordering | 6 |
| GAP-6.02 | `OSVersion()` returns `darwin/arm64` instead of actual OS version like `Darwin 25.2.0` | 6 |
| GAP-8.05 | No `ToolMiddleware` interface between VALIDATE and EXECUTE for approval gates | 8 |
| GAP-8.13 | apply_patch fuzzy matching has whitespace normalization but no Unicode punctuation equivalence | 8/A |
| GAP-8.16 | No dedicated `PermissionDenied` error type; OS errors propagate as generic errors | 8/B |
| GAP-8.24 | Abort signal shutdown inherits GAP-8.23 ordering issues | 8/B |

### MINOR (22)

| ID | Description | Section |
|----|-------------|---------|
| GAP-2.06 | Follow-up processing is iterative, not recursive (functionally equivalent) | 2 |
| GAP-2.07 | System prompt rebuilt every round (enhancement over spec) | 2 |
| GAP-2.13 | Loop detection warning wording differs slightly from spec | 2 |
| GAP-3.03 | `grep` `output_mode` is global for all profiles; spec says Anthropic-specific | 3 |
| GAP-3.06 | Method named `ToolDefinitions()` not `tools()` per spec | 3 |
| GAP-3.08 | Gemini `context_window_size` conservative at 128K (many models support 1M) | 3 |
| GAP-3.09 | `ToolDefinition.description` not validated as non-empty | 3 |
| GAP-3.13 | OpenAI `write_file` not restricted to new-file-only; relies on prompt guidance | 3 |
| GAP-3.15 | Gemini grounding handled via `WebSearch` flag, not `provider_options.gemini` | 3 |
| GAP-4.01 | `EditFile` method on ExecutionEnvironment interface not in spec | 4 |
| GAP-4.02 | `WriteFile` returns string instead of void | 4 |
| GAP-4.03 | `Initialize` returns error instead of void | 4 |
| GAP-4.04 | `Grep` expands options inline instead of using `GrepOptions` struct | 4 |
| GAP-4.05 | `ExecCommand` has extra `context.Context` parameter (idiomatic Go) | 4 |
| GAP-4.06 | Shell tool doesn't expose `working_dir`/`env_vars` to model (security decision) | 4 |
| GAP-4.07 | No Windows shell support (`/bin/bash -c` hardcoded) | 4 |
| GAP-4.08 | `Platform()` never returns "wasm" (WASM is extension point) | 4 |
| GAP-4.09 | `ExecCommand` returns `(ExecResult, error)` instead of just `ExecResult` | 4 |
| GAP-4.10 | No wrapper implementations exist (but interface supports composition) | 4 |
| GAP-7.05 | Non-LocalExecEnv with working_dir creates isolated env (doesn't share PID tracking) | 7 |
| GAP-7.06 | Subagent inherits full SessionConfig including MCP (duplicate server connections) | 7 |
| GAP-7.07 | `close_agent` doesn't wait for running goroutine; status may be stale | 7 |

### INFORMATIONAL (12)

| ID | Description | Section |
|----|-------------|---------|
| GAP-2.10 | Extra event kinds beyond spec (COMMUNICATE, SKILL_ACTIVATED, CONTEXT_COMPACTION, WARNING) | 2 |
| GAP-2.14 | Uses Go context cancellation instead of `abort_signaled` boolean | 2 |
| GAP-2.15 | Events are best-effort with 256 buffer (silent drops if consumer slow) | 2 |
| GAP-2.18 | SESSION_END emitted from outer ProcessInput loop, not processOneInput | 2 |
| GAP-3.10 | OpenAI parallel tool calls disabled (matches codex-rs behavior) | 3 |
| GAP-3.14 | Profile "gemini" vs adapter "google" naming (handled by normalization) | 3 |
| GAP-5.02 | Head/tail split gives tail one extra byte on odd max_chars (more correct than spec) | 5 |
| GAP-5.03 | Single `ToolOutputLimit` struct instead of separate char/line config maps | 5 |
| GAP-7.01 | `spawn_agent` description wording differs from spec | 7 |
| GAP-7.02 | `send_input`/`wait`/`close_agent` description wording differs | 7 |
| GAP-7.03 | `wait` tool has extra optional `timeout_ms` parameter (useful extension) | 7 |
| GAP-7.04 | Internal type `subagent` instead of spec's `SubAgentHandle` (Go convention) | 7 |

### INTENTIONAL DEVIATION (4)

| ID | Description | Section |
|----|-------------|---------|
| GAP-2.01 | Session ID uses ULID instead of UUID (sortable, timestamped) | 2 |
| GAP-2.02 | `tool_output_limits` is a richer struct (MaxChars + MaxLines + Strategy) | 2 |
| GAP-3.05 | edit_file registered in ToolRegistry for all profiles but not in OpenAI ToolDefinitions (correct) | 3 |
| GAP-3.12 | System prompt says "serf" not "codex-rs" (intentional branding) | 3 |

---

## Definition of Done Checklist (Verified)

### 9.1 Core Loop

- [x] Session can be created with a ProviderProfile and ExecutionEnvironment
  - *Evidence: `NewSession()` at `session.go:156` takes `*llm.Client`, `ProviderProfile`, `ExecutionEnvironment`, `SessionConfig`*
- [x] `process_input()` runs the agentic loop: LLM call -> tool execution -> loop until natural completion
  - *Evidence: `processOneInput()` at `session.go:703` implements the full loop*
- [x] Natural completion: model responds with text only (no tool calls) and the loop exits
  - *Evidence: `session.go:918-928` checks for empty tool calls and breaks*
- [x] Round limits: `max_tool_rounds_per_input` stops the loop when reached
  - *Evidence: `session.go:746` for loop condition. **Note**: returns error instead of BREAK (GAP-2.16)*
- [x] Session turn limits: `max_turns` stops the loop across all inputs
  - *Evidence: `session.go:729-735`. **Note**: uses `>` not `>=`, returns error (GAP-2.17)*
- [x] Abort signal: cancellation stops the loop, kills running processes, transitions to CLOSED
  - *Evidence: `session.go:493-495` calls Close(); `env_local.go:60-78` kills PIDs. **Note**: shutdown ordering differs (GAP-8.23)*
- [x] Loop detection: consecutive identical tool call patterns trigger a warning SteeringTurn
  - *Evidence: `session.go:964-975` detects loops and injects SteeringTurn*
- [x] Multiple sequential inputs work: submit, wait for completion, submit again
  - *Evidence: `session_dod_test.go` tests sequential inputs; state returns to IDLE*

### 9.2 Provider Profiles

- [x] OpenAI profile provides codex-rs-aligned tools including `apply_patch` (v4a format)
  - *Evidence: `profile.go:203-237` with apply_patch, name mappings (shell->exec_command, grep->grep_files, glob->list_dir)*
- [x] Anthropic profile provides Claude Code-aligned tools including `edit_file` (old_string/new_string)
  - *Evidence: `profile.go:239-273` with edit_file, 120s shell timeout, beta headers*
- [x] Gemini profile provides gemini-cli-aligned tools
  - *Evidence: `profile.go:275-322` with read_many_files, list_dir, web_search, web_fetch*
- [x] Each profile produces a provider-specific system prompt covering identity, tool usage, and coding guidance
  - *Evidence: `prompts/system.{openai,anthropic,gemini}.md` each cover identity and tool conventions*
- [x] Custom tools can be registered on top of any profile
  - *Evidence: `ToolRegistry.Register()` at `tool_registry.go:72`; tested by `TestToolRegistry_Register_LatestWinsOnNameCollision`*
- [x] Tool name collisions resolved: custom registration overrides profile defaults
  - *Evidence: `Register()` overwrites existing entries; test confirms latest-wins*

### 9.3 Tool Execution

- [x] Tool calls are dispatched through the ToolRegistry
  - *Evidence: `tool_registry.go:135` `ExecuteCall()` is the sole dispatch path*
- [x] Unknown tool calls return an error result to the LLM (not an exception)
  - *Evidence: `tool_registry.go:145-148` returns `ToolExecResult{IsError: true}`*
- [x] Tool argument JSON is parsed and validated against the tool's parameter schema
  - *Evidence: `tool_registry.go:150-163` JSON parse + `compiledSchemas` validation*
- [x] Tool execution errors are caught and returned as error results (`is_error = true`)
  - *Evidence: `tool_registry.go:167-176` catches errors and returns `IsError: true`*
- [x] Parallel tool execution works when the profile's `supports_parallel_tool_calls` is true
  - *Evidence: `session.go:932-947` uses goroutines + WaitGroup when parallel enabled*

### 9.4 Execution Environment

- [x] `LocalExecutionEnvironment` implements all file and command operations
  - *Evidence: `env_local.go` implements ReadFile, WriteFile, EditFile, ExecCommand, Grep, Glob, ListDirectory*
- [x] Command timeout default is 10 seconds
  - *Evidence: `session.go:82-83` defaults to 10,000ms*
- [x] Command timeout is overridable per-call via the shell tool's `timeout_ms` parameter
  - *Evidence: `session.go:1240-1245` reads `timeout_ms` from args, caps at MaxCommandTimeoutMS*
- [x] Timed-out commands: process group receives SIGTERM, then SIGKILL after 2 seconds
  - *Evidence: `env_local.go:505-518` exact SIGTERM -> 2s -> SIGKILL sequence*
- [x] Environment variable filtering excludes sensitive variables (`*_API_KEY`, `*_SECRET`, etc.) by default
  - *Evidence: `env_local.go:565-649` deny patterns + 4 policy modes*
- [x] The `ExecutionEnvironment` interface is implementable by consumers for custom environments
  - *Evidence: `env.go:20-40` clean Go interface. **Note**: under `internal/` so external consumers blocked (GAP-1.01)*

### 9.5 Tool Output Truncation

- [x] Character-based truncation runs FIRST on all tool outputs
  - *Evidence: `tool_registry.go:183` `truncateChars()` called before `truncateLines()`*
- [x] Line-based truncation runs SECOND where configured (shell: 256, grep: 200, glob: 500)
  - *Evidence: `tool_registry.go:184-186` line truncation after char truncation; defaults at lines 237-244*
- [x] Truncation inserts a visible marker: `[WARNING: Tool output was truncated. N characters removed...]`
  - *Evidence: `tool_registry.go:207-212` exact spec message wording*
- [x] The full untruncated output is available via the `TOOL_CALL_END` event
  - *Evidence: `session.go:556-561` emits `full_output` in TOOL_CALL_END event*
- [x] Default character limits match the table in Section 5.2
  - *Evidence: `tool_registry.go:233-261` all 8 tools match exactly; verified by `TestDefaultToolLimit_MatchesSpecTable`*
- [x] Both character and line limits are overridable via `SessionConfig`
  - *Evidence: `session.go:37,227-247` `ToolOutputLimits` map with per-tool overrides*

### 9.6 Steering

- [x] `steer()` queues a message that is injected after the current tool round
  - *Evidence: `session.go:412-422` appends to steeringQueue; drained at `session.go:977-981`*
- [x] `follow_up()` queues a message that is processed after the current input completes
  - *Evidence: `session.go:425-435` appends to followups; processed at `session.go:483-518`*
- [x] Steering messages appear as SteeringTurn in the history
  - *Evidence: `session.go:738-741` and `session.go:977-981` append TurnSteering*
- [x] SteeringTurns are converted to user-role messages for the LLM
  - *Evidence: `session.go:803-805` converts TurnSteering to `llm.User()`*

### 9.7 Reasoning Effort

- [x] `reasoning_effort` is passed through to the LLM SDK Request
  - *Evidence: `session.go:835-838` sets `req.ReasoningEffort`*
- [x] Changing `reasoning_effort` mid-session takes effect on the next LLM call
  - *Evidence: `session.go:402-409` `SetReasoningEffort()` modifies live config*
- [x] Valid values: "low", "medium", "high", null
  - *Evidence: `session_parity_test.go:702-737` tests low and high; empty string = no override*

### 9.8 System Prompts

- [x] System prompt includes provider-specific base instructions
  - *Evidence: `prompts/system.{openai,anthropic,gemini}.md` embedded per profile*
- [x] System prompt includes environment context (platform, git, working dir, date, model info)
  - *Evidence: `profile.go:143-152` generates `<environment>` block with all 8 fields*
- [x] System prompt includes tool descriptions from the active profile
  - *Evidence: `profile.go:176-187` lists tools. **Note**: MCP/custom tools placed after project docs (GAP-6.01)*
- [x] Project documentation files (AGENTS.md + provider-specific files) are discovered and included
  - *Evidence: `project_docs.go` walks git root to cwd, respects 32KB budget, per-profile file lists*
- [x] User instruction overrides are appended last (highest priority)
  - *Evidence: `session.go:780-781` appends `UserInstructionOverride` last*
- [x] Only relevant project files are loaded
  - *Evidence: Each profile's `ProjectDocFiles()` returns only matching filenames; tested*

### 9.9 Subagents

- [x] Subagents can be spawned with a scoped task via the `spawn_agent` tool
  - *Evidence: `subagents.go:40-93` `spawnAgent()` creates child session with task*
- [x] Subagents share the parent's execution environment (same filesystem)
  - *Evidence: `subagents.go:62` `subEnv := s.env`; PID sharing tested*
- [x] Subagents maintain independent conversation history
  - *Evidence: `NewSession` creates fresh history; tested by `TestSession_Subagent_IndependentHistory`*
- [x] Depth limiting prevents recursive spawning (default max depth: 1)
  - *Evidence: `subagents.go:43-47` checks `depth >= maxDepth`; `session.go:87-89` defaults to 1*
- [x] Subagent results are returned to the parent as tool results
  - *Evidence: `subagents.go:115-158` wait returns `SubAgentResult` JSON*
- [x] `send_input`, `wait`, and `close_agent` tools work correctly
  - *Evidence: All three implemented in `subagents.go`; tested individually*

### 9.10 Event System

- [x] All event kinds listed in Section 2.9 are emitted at the correct times
  - *Evidence: All 13 spec event kinds exist in `events.go:7-25`; emitted throughout session.go*
- [x] Events are delivered via async iterator or language-appropriate equivalent
  - *Evidence: `session.go:373` `Events()` returns `<-chan SessionEvent` (Go equivalent)*
- [x] `TOOL_CALL_END` events carry full untruncated tool output
  - *Evidence: `session.go:556-561` emits `full_output` key with untruncated content*
- [x] Session lifecycle events (SESSION_START, SESSION_END) bracket the session
  - *Evidence: SESSION_START at `session.go:188`; SESSION_END at `session.go:463-469`*

### 9.11 Error Handling

- [x] Tool execution errors -> error result sent to LLM (model can recover)
  - *Evidence: `tool_registry.go:167-176` catches all errors, returns `IsError: true`*
- [x] LLM API transient errors (429, 500-503) -> retry with backoff
  - *Evidence: `llm/errors.go` marks 429/500-503 retryable; `llm/retry_util.go` implements backoff*
- [x] Authentication errors -> surface immediately, no retry, session transitions to CLOSED
  - *Evidence: `llm/errors.go:113-114` 401 non-retryable; `session.go:855-858` calls Close()*
- [x] Context window overflow -> emit warning event (no automatic compaction)
  - *Evidence: `session.go:599-627` emits WARNING at 80% threshold*
- [ ] **Graceful shutdown: abort signal -> cancel LLM stream -> kill running processes -> flush events -> emit SESSION_END**
  - *FAILS: GAP-8.23. Actual order: state->CLOSED first, SESSION_END before process kill, no event flush*

### 9.12 Cross-Provider Parity Matrix

Tests verified via `session_parity_test.go` and `session_dod_test.go`:

| Test Case | OpenAI | Anthropic | Gemini |
|-----------|--------|-----------|--------|
| Simple file creation task | [x] | [x] | [x] |
| Read file, then edit it | [x] | [x] | [x] |
| Multi-file edit in one session | [x] | [x] | [x] |
| Shell command execution | [x] | [x] | [x] |
| Shell command timeout handling | [x] | [x] | [x] |
| Grep + glob to find files | [x] | [x] | [x] |
| Multi-step task (read -> analyze -> edit) | [x] | [x] | [x] |
| Tool output truncation (large file) | [x] | [x] | [x] |
| Parallel tool calls (if supported) | [x] | [x] | [x] |
| Steering mid-task | [x] | [x] | [x] |
| Reasoning effort change | [x] | [x] | [x] |
| Subagent spawn and wait | [x] | [x] | [x] |
| Loop detection triggers warning | [x] | [x] | [x] |
| Error recovery (tool fails, model retries) | [x] | [x] | [x] |
| Provider-specific editing format works | [x] | [x] | [x] |

*Note: These are tested with mock LLM clients in unit/parity tests. The spec's 9.13 smoke test with real API keys exists as `TestSmoke_*` tests in `session_dod_test.go` (7 integration tests using `gpt-5-mini-2025-08-07`).*

### 9.13 Integration Smoke Tests

- [x] Integration smoke tests exist (7 tests in `session_dod_test.go` tagged with real API calls)
- [ ] Tests cover all three providers (only OpenAI tested with real API; Anthropic/Gemini require separate API keys)

---

## Top Recommendations (Prioritized)

### P0 - Fix (spec violations)

1. **GAP-8.23**: Reorder `Close()` to: cancel LLM -> kill processes -> flush events -> SESSION_END -> close subagents -> CLOSED
2. **GAP-2.16/2.17**: Round and turn limits should BREAK from loop and proceed to follow-up/SESSION_END, not return error. Fix `>` to `>=` for turn limit.

### P1 - Should fix (meaningful gaps)

3. **GAP-1.01**: Create a public API package (e.g., `pkg/agent/`) so external Go modules can consume the library
4. **GAP-1.02/1.06**: Add `SetModel()`, `SetTimeout()`, and public `RegisterTool()` for runtime mutability
5. **GAP-6.01**: Move MCP/custom tool descriptions before project docs in system prompt assembly
6. **GAP-6.02**: `OSVersion()` should return actual OS version (e.g., `uname -r` output)

### P2 - Nice to have (minor improvements)

7. **GAP-2.05**: Don't increment round counter for `pause_turn` responses
8. **GAP-2.11/2.12**: Align TOOL_CALL_END event data keys with spec (`output` not `full_output`)
9. **GAP-3.02**: Update Anthropic `context_window_size` to 1M if beta headers enable it
10. **GAP-8.05**: Add `ToolMiddleware` interface for approval/permission system extension point
11. **GAP-8.13**: Add Unicode punctuation equivalence to apply_patch fuzzy matching
12. **GAP-4.07**: Add Windows shell support (`cmd.exe /c`) with build tags

---

## Audit Log Files

| Section | File | Gaps Found |
|---------|------|------------|
| 1. Overview & Goals | `section-1-overview-goals.md` | 9 |
| 2. Agentic Loop | `section-2-agentic-loop.md` | 19 |
| 3. Provider Toolsets | `section-3-provider-toolsets.md` | 15 |
| 4. Execution Environment | `section-4-execution-environment.md` | 10 |
| 5. Output & Context | `section-5-output-context.md` | 3 |
| 6. System Prompts | `section-6-system-prompts.md` | 2 |
| 7. Subagents | `section-7-subagents.md` | 7 |
| 8. Out of Scope & Appendices | `section-8-out-of-scope-and-appendices.md` | 24 |
| **Total** | | **89** |
