# Section 9: Definition of Done + Appendix B: Error Handling - Audit Findings

## Summary

**102 checklist items verified, 13 gaps found.**

- Section 9.1 Core Loop: 8/8 pass
- Section 9.2 Provider Profiles: 6/6 pass
- Section 9.3 Tool Execution: 5/5 pass
- Section 9.4 Execution Environment: 6/6 pass
- Section 9.5 Tool Output Truncation: 6/6 pass
- Section 9.6 Steering: 4/4 pass
- Section 9.7 Reasoning Effort: 3/3 pass
- Section 9.8 System Prompts: 5/6 pass (1 gap: tool descriptions not from profile in system prompt)
- Section 9.9 Subagents: 6/6 pass
- Section 9.10 Event System: 4/4 pass
- Section 9.11 Error Handling: 4/5 partial (1 gap: graceful shutdown sequence incomplete)
- Section 9.12 Cross-Provider Parity Matrix: 33/45 cells pass (4 test cases missing x3 providers = 12 gaps)
- Section 9.13 Integration Smoke Test: 0/7 at agent level (LLM-layer smoke tests exist, not agent-layer)
- Appendix B Tool-Level Errors: 7/7 pass
- Appendix B Session-Level Errors: 6/6 pass
- Appendix B Graceful Shutdown: 5/8 steps verified (3 steps partially missing)

---

## Findings

### GAP-9.08A: System prompt tool descriptions sourced from profile definitions, not registry

- **Spec requirement:** "System prompt includes tool descriptions from the active profile" (9.8 item 3)
- **Current state:** In `profile.go` `BuildSystemPrompt()`, tool descriptions are listed from `p.ToolDefinitions()` which includes only profile-defined tools. However, the actual system prompt is built in `session.go` `processOneInput()` which does append MCP tool descriptions separately. The spec says "tool descriptions from the active profile" which is satisfied. However, the system prompt does NOT include descriptions of custom tools registered after session creation via `sess.reg.Register()` -- only profile-defined tools and MCP tools get listed. This is a minor gap; a custom tool registered post-creation would still be sent in the `req.Tools` array but would NOT appear in the human-readable "Tools:" section of the system prompt.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 131-201), `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 694-715)

### GAP-9.11A: Graceful shutdown does not explicitly cancel in-flight LLM stream (step 1)

- **Spec requirement:** Graceful Shutdown step 1: "Cancel any in-flight LLM stream" (Appendix B, line 1425)
- **Current state:** The `Close()` method in `session.go` transitions state to CLOSED, closes subagents, calls `env.Cleanup()`, and emits `SESSION_END`. However, there is no explicit LLM stream cancellation. The abort signal path works via context cancellation (when `ProcessInput`'s ctx is cancelled, the `s.client.Complete(ctx, req)` call is cancelled). But `Close()` itself does not cancel any in-flight context. If `ProcessInput` is running concurrently, the context must be cancelled externally by the caller. The `Close()` method does not own or cancel the context passed to `ProcessInput`. This is a design choice (Go's context propagation model), not necessarily a bug, but the spec's explicit step 1 ("Cancel any in-flight LLM stream") is not independently implemented in `Close()`.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 419-453)

### GAP-9.11B: Graceful shutdown step 5 (flush pending events) not explicitly implemented

- **Spec requirement:** Graceful Shutdown step 5: "Flush pending events" (Appendix B, line 1429)
- **Current state:** The events channel is a buffered channel (size 256). The `Close()` method emits `SESSION_END` and then calls `close(s.events)`. Events that are already buffered will be deliverable, but there is no explicit "flush" step that ensures all pending events are consumed before closing. The `emit()` method uses a `select/default` pattern that drops events if the buffer is full. The comment says "v1 is best-effort." This is an acknowledged limitation but does not match the spec's explicit flush step.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 633-651, 419-453)

### GAP-9.11C: Graceful shutdown ordering -- subagent cleanup (step 7) happens BEFORE SESSION_END (step 6)

- **Spec requirement:** Graceful Shutdown Sequence: step 6 is "Emit SESSION_END event with final state", step 7 is "Clean up subagents (close_agent on all active subagents)" (Appendix B, lines 1430-1431)
- **Current state:** In `Close()`, subagents are closed BEFORE `SESSION_END` is emitted (lines 429-438 close subagents, then line 447 emits SESSION_END). The spec says emit SESSION_END first (step 6), then clean up subagents (step 7). The actual order is reversed.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 419-453)

### GAP-9.12A: Parity matrix missing test -- "Tool output truncation (large file)" across providers

- **Spec requirement:** Cross-Provider Parity Matrix row: "Tool output truncation (large file)" for OpenAI, Anthropic, Gemini (line 1244)
- **Current state:** `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` exists in `session_test.go` but only tests with OpenAI profile. There is no cross-provider parity test for truncation. The `session_parity_test.go` does not contain a `TestParity_ToolOutputTruncation` test.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session_parity_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_test.go`

### GAP-9.12B: Parity matrix missing test -- "Reasoning effort change" across providers

- **Spec requirement:** Cross-Provider Parity Matrix row: "Reasoning effort change" for OpenAI, Anthropic, Gemini (line 1247)
- **Current state:** `TestSession_ReasoningEffort_PassedThroughAndCanChange` exists in `session_dod_test.go` but only tests with OpenAI profile. There is no cross-provider parity test. The `session_parity_test.go` does not contain a `TestParity_ReasoningEffort` test.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session_parity_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_dod_test.go`

### GAP-9.12C: Parity matrix missing test -- "Subagent spawn and wait" across providers

- **Spec requirement:** Cross-Provider Parity Matrix row: "Subagent spawn and wait" for OpenAI, Anthropic, Gemini (line 1248)
- **Current state:** `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` exists in `session_dod_test.go` but only tests with OpenAI profile. There is no cross-provider parity test. The `session_parity_test.go` does not contain a `TestParity_Subagent` test.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session_parity_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_dod_test.go`

### GAP-9.12D: Parity matrix missing test -- "Provider-specific editing format works" across providers

- **Spec requirement:** Cross-Provider Parity Matrix row: "Provider-specific editing format works" for OpenAI, Anthropic, Gemini (line 1251)
- **Current state:** `TestParity_ReadFileThenEdit` tests editing across all three providers and uses `makeEditCall()` which correctly uses `apply_patch` for OpenAI and `edit_file` for Anthropic/Gemini. This partially covers "provider-specific editing format works." However, there is no explicit test named or labeled for this specific row that validates the FORMAT itself works (e.g., that apply_patch actually applies correctly for OpenAI, that edit_file old_string/new_string works for Anthropic/Gemini). The `TestParity_ReadFileThenEdit` does effectively validate this, so this is a partial pass. The spec asks for a distinct test case to validate provider-specific editing format, which is technically covered by `TestParity_ReadFileThenEdit` and `TestParity_MultiFileEdit`.
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session_parity_test.go`

### GAP-9.13: Integration smoke test at agent level does not exist

- **Spec requirement:** Section 9.13 defines 7 end-to-end scenarios with real API keys at the agent/session level (lines 1253-1295)
- **Current state:** Integration smoke tests exist only at the LLM SDK layer (`internal/llm/integration_smoke_test.go`) covering basic generation, streaming, tool calling, image input, structured output, and error handling. There are NO agent-level integration tests that create a `Session`, submit real inputs, and verify file creation, editing, shell execution, truncation, steering, subagents, and timeout handling against real LLM APIs. All agent tests in `session_test.go`, `session_dod_test.go`, and `session_parity_test.go` use `fakeAdapter` stubs.
- **Severity:** Critical
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/llm/integration_smoke_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_dod_test.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session_parity_test.go`

### GAP-9.10A: Event system -- WARNING event kind not in spec's EventKind enum but used in code

- **Spec requirement:** Section 2.9 defines the EventKind enum (lines 411-424) which includes: SESSION_START, SESSION_END, USER_INPUT, ASSISTANT_TEXT_START, ASSISTANT_TEXT_DELTA, ASSISTANT_TEXT_END, TOOL_CALL_START, TOOL_CALL_OUTPUT_DELTA, TOOL_CALL_END, STEERING_INJECTED, TURN_LIMIT, LOOP_DETECTION, ERROR.
- **Current state:** The code defines additional event kinds not in the spec: `EventWarning`, `EventCommunicate`, `EventSkillActivated`, `EventContextCompaction`. These are implementation extensions. The spec does reference emitting a "WARNING" event for context usage (line 969) and context overflow (Section 9.11), but `WARNING` is not listed in the formal EventKind enum at Section 2.9. The code's behavior of emitting WARNING events is correct per the spec's intent (spec says "emit warning event" in 9.11 item 4), but the EventKind enum in the spec should be updated to include WARNING. This is a spec inconsistency rather than a code gap.
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/events.go`, `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` (lines 411-424)

### GAP-9.04A: Environment variable filtering -- default policy inherits non-sensitive vars, not just explicitly allowed

- **Spec requirement:** "Environment variable filtering excludes sensitive variables (`*_API_KEY`, `*_SECRET`, etc.) by default" (9.4 item 5)
- **Current state:** The `filteredEnv()` function in `env_local.go` (default policy) correctly denies vars containing API_KEY, SECRET, TOKEN, PASSWORD, CREDENTIAL. All other vars are inherited. This matches the spec's "excludes sensitive variables by default." PASS -- no gap here. The implementation is correct.
- **Severity:** N/A (pass)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 554-596)

### GAP-B.SHUTDOWN: Graceful shutdown step 2-4 ordering relies on env.Cleanup(), not session-level orchestration

- **Spec requirement:** Steps 2-4: "Send SIGTERM to all running command process groups" -> "Wait 2 seconds" -> "Send SIGKILL to any remaining processes" (Appendix B, lines 1426-1428)
- **Current state:** The `Close()` method calls `s.env.Cleanup()` which delegates to `LocalExecutionEnvironment.Cleanup()`. This method correctly implements SIGTERM -> 2s wait -> SIGKILL (lines 47-65 of `env_local.go`). However, `Close()` calls `Cleanup()` AFTER closing subagents and BEFORE emitting SESSION_END. The spec says steps 2-4 happen before step 5 (flush events) and step 6 (emit SESSION_END), which is partially satisfied. The test `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation` in `env_local_test.go` validates the SIGTERM->SIGKILL escalation. `TestCleanup_TerminatesRunningProcesses` validates session-level cleanup. This is implemented correctly at the env layer.
- **Severity:** N/A (pass, with ordering note covered in GAP-9.11C)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 47-65), `/Users/jesse/prime-radiant/serf/internal/agent/env_local_test.go` (lines 485-538)

### GAP-B.TURNLIMIT: TurnLimitExceeded transitions to IDLE -- spec says emit TURN_LIMIT event first

- **Spec requirement:** "TurnLimitExceeded: Emit TURN_LIMIT event, session -> IDLE" (Appendix B, line 1418)
- **Current state:** In `processOneInput()` (lines 680-686), when `MaxTurns` is exceeded, the code emits `EventTurnLimit` BEFORE setting state to `SessionIdle`. When `MaxToolRoundsPerInput` is exceeded (lines 940-944), the code emits `EventTurnLimit` BEFORE setting state to `SessionIdle`. Both correctly emit the event and transition to IDLE. PASS.
- **Severity:** N/A (pass)
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 680-686, 940-944)

---

## Checklist Status

### 9.1 Core Loop

| Item | Status | Evidence |
|------|--------|----------|
| Session created with ProviderProfile + ExecutionEnvironment | PASS | `NewSession()` requires client, profile, env; tested in every test |
| `process_input()` runs agentic loop | PASS | `ProcessInput()` -> `processOneInput()` implements LLM call -> tool exec -> loop |
| Natural completion (text only, no tool calls) | PASS | Lines 862-872: `if len(calls) == 0` returns text. Tested in `TestSession_EventSystem_NaturalCompletion_*` |
| Round limits (max_tool_rounds_per_input) | PASS | Lines 720, 940-944. Tested in `TestSession_MaxToolRoundsPerInput_StopsLoop` |
| Session turn limits (max_turns) | PASS | Lines 680-686. Tested in `TestSession_MaxTurns_StopsAcrossInputsAndEmitsEvent` |
| Abort signal handling | PASS | Lines 464-468, 721-726. Tested in `TestSession_AbortSignal_ClosesSessionAndEmitsSessionEnd` |
| Loop detection | PASS | Lines 908-919, `detectLoop()`. Tested in `TestSession_LoopDetection_EmitsEventAndInjectsSteering` |
| Multiple sequential inputs | PASS | `ProcessInput()` can be called multiple times. Tested in `TestSession_MultipleSequentialInputs_Work` |

### 9.2 Provider Profiles

| Item | Status | Evidence |
|------|--------|----------|
| OpenAI with codex-rs tools including apply_patch | PASS | `NewOpenAIProfile()` includes `defApplyPatch()` + v4a format tool names |
| Anthropic with Claude Code tools including edit_file | PASS | `NewAnthropicProfile()` includes `defEditFile()` |
| Gemini with gemini-cli tools | PASS | `NewGeminiProfile()` includes `defEditFile()`, `defReadManyFiles()`, `defListDir()`, `defWebSearch()` |
| Provider-specific system prompts | PASS | Each profile has `embeddedBasePrompt(provider)` + `BuildSystemPrompt()` |
| Custom tool registration | PASS | `sess.reg.Register()` works. Tested in `TestSession_CustomToolRegistration_OverridesExistingTool` |
| Tool name collision resolution | PASS | `ToolRegistry.Register()` overwrites by key. Tested in `TestSession_CustomToolRegistration_OverridesExistingTool` |

### 9.3 Tool Execution

| Item | Status | Evidence |
|------|--------|----------|
| Tool dispatch through ToolRegistry | PASS | `s.reg.ExecuteCall()` in `execTool()` |
| Unknown tool -> error result | PASS | `ExecuteCall()` returns `truncateResult(name, callID, msg, true, ...)`. Tested in `TestToolRegistry_UnknownTool_ReturnsErrorResult` |
| JSON argument parsing + schema validation | PASS | `json.Unmarshal` + `t.Schema.Validate()` in `ExecuteCall()`. Tested in `TestToolRegistry_SchemaValidationError_*` and `TestToolRegistry_InvalidArgumentsJSON_*` |
| Error results with is_error=true | PASS | Errors return `isErr: true` in `truncateResult()`. Multiple tests verify `res.IsError` |
| Parallel tool execution | PASS | Lines 876-891 check `s.profile.SupportsParallelToolCalls()`. Tested in `TestParity_ParallelToolCalls` |

### 9.4 Execution Environment

| Item | Status | Evidence |
|------|--------|----------|
| LocalExecutionEnvironment implements all operations | PASS | All interface methods implemented in `env_local.go` |
| Command timeout default 10 seconds | PASS | `DefaultCommandTimeoutMS: 10_000` in `applyDefaults()`. Tested in `TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride` |
| Per-call timeout override | PASS | Shell tool reads `timeout_ms` from args. Tested in `TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride` |
| SIGTERM -> SIGKILL after 2 seconds | PASS | `ExecCommand()` lines 456-468. Tested in `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation` |
| Env var filtering | PASS | `filteredEnv()` denies API_KEY, SECRET, TOKEN, PASSWORD, CREDENTIAL |
| ExecutionEnvironment interface implementable | PASS | `ExecutionEnvironment` is a Go interface in `env.go`; multiple test implementations (captureEnv, timeoutEnv, fakeEnvForMCP) |

### 9.5 Tool Output Truncation

| Item | Status | Evidence |
|------|--------|----------|
| Character truncation runs FIRST | PASS | `truncateResult()`: `truncateChars()` runs before `truncateLines()` |
| Line truncation runs SECOND | PASS | `truncateResult()`: lines 173-176 |
| Visible truncation marker | PASS | `truncateChars()` inserts `[WARNING: Tool output was truncated. ...]` |
| Full output in TOOL_CALL_END event | PASS | `execTool()` emits `full_output` in TOOL_CALL_END data. Tested in `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` |
| Default limits match Section 5.2 | PASS | `defaultToolLimit()` matches: read_file=50k, shell=30k/256lines, grep=20k/200lines, glob=20k/500lines, edit_file=10k, apply_patch=10k, write_file=1k, spawn_agent=20k |
| Limits overridable via SessionConfig | PASS | `SessionConfig.ToolOutputLimits` applied in `NewSession()`. Tested in `TestSession_ToolOutputTruncation_CanOverrideLineLimitViaSessionConfig` |

### 9.6 Steering

| Item | Status | Evidence |
|------|--------|----------|
| steer() queues message after current tool round | PASS | `Steer()` appends to `steeringQueue`; drained after tool results (line 922). Tested in `TestSession_Steer_IsInjectedAfterCurrentToolRound` |
| follow_up() queues message after current input | PASS | `FollowUp()` appends to `followups`; popped in `ProcessInput()` loop. Tested in `TestSession_FollowUp_ProcessesAfterCompletion` |
| SteeringTurn in history | PASS | `s.appendTurn(TurnSteering, ...)`. Verified in steering tests |
| Converted to user-role messages for LLM | PASS | Lines 747-749: `if t.Kind == TurnSteering { history = append(history, llm.User(t.Message.Text())) }` |

### 9.7 Reasoning Effort

| Item | Status | Evidence |
|------|--------|----------|
| Passed through to LLM SDK Request | PASS | Lines 779-782: `req.ReasoningEffort = &v` |
| Changeable mid-session | PASS | `SetReasoningEffort()` updates `s.cfg.ReasoningEffort`. Tested in `TestSession_ReasoningEffort_PassedThroughAndCanChange` |
| Valid values: low, medium, high, null | PASS | String passthrough; provider handles validation |

### 9.8 System Prompts

| Item | Status | Evidence |
|------|--------|----------|
| Provider-specific base instructions | PASS | `embeddedBasePrompt(provider)` + `ResolveSystemPrompt()` |
| Environment context | PASS | `BuildSystemPrompt()` includes `<environment>` block with working dir, platform, OS, date, model, cutoff |
| Tool descriptions from active profile | PARTIAL | `BuildSystemPrompt()` lists profile tools; MCP tools appended in `processOneInput()`. Custom-registered tools NOT listed in system prompt. See GAP-9.08A |
| Project doc discovery | PASS | `LoadProjectDocs()` called with `s.profile.ProjectDocFiles()`. OpenAI loads AGENTS.md + .codex/instructions.md, Anthropic loads CLAUDE.md + AGENTS.md, Gemini loads GEMINI.md + AGENTS.md |
| User instruction overrides appended last | PASS | Lines 713-715: `UserInstructionOverride` appended after everything else |
| Provider-specific file filtering | PASS | Each profile defines its own `docFiles` list. Anthropic loads CLAUDE.md not GEMINI.md, etc. |

### 9.9 Subagents

| Item | Status | Evidence |
|------|--------|----------|
| Spawn with scoped task | PASS | `spawnAgent()` creates subagent session. Tested in `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` |
| Shared execution environment | PASS | `subEnv := s.env` (line 62 of subagents.go); only overridden if `working_dir` specified |
| Independent history | PASS | Subagent creates its own `NewSession()` with fresh history |
| Depth limiting (default 1) | PASS | `MaxSubagentDepth: 1` in `applyDefaults()`. Tested in depth limit assertion |
| Results returned as tool results | PASS | `waitAgent()` returns `SubAgentResult` JSON. Tested in `TestSession_WaitAgent_ReturnsSubAgentResult` |
| send_input, wait, close_agent work | PASS | All three tested in `TestSession_SendInput_UsesMessageParam`, `TestSession_WaitAgent_ReturnsSubAgentResult`, `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` |

### 9.10 Event System

| Item | Status | Evidence |
|------|--------|----------|
| All event kinds emitted at correct times | PASS | All spec-defined event kinds present. Code also adds WARNING, COMMUNICATE, SKILL_ACTIVATED, CONTEXT_COMPACTION as extensions (see GAP-9.10A) |
| Async iterator delivery | PASS | `Events()` returns `<-chan SessionEvent` (Go channel = async iterator) |
| TOOL_CALL_END has full untruncated output | PASS | `execTool()` emits `full_output` in data map. Tested in `TestSession_ToolOutputTruncation_*` |
| SESSION_START/SESSION_END bracket session | PASS | Tested in `TestSession_LifecycleEvents_BracketSession` |

### 9.11 Error Handling

| Item | Status | Evidence |
|------|--------|----------|
| Tool errors -> error result to LLM | PASS | `ExecuteCall()` catches errors, returns `isErr: true`. Multiple tests |
| Transient API errors -> retry with backoff | PASS | `llm.Retry()` in session.go line 788. Tested in `TestSession_LLMTransientErrors_RetryWithBackoff` |
| Auth errors -> surface immediately, CLOSED | PASS | Lines 799-802 check `!le.Retryable()` and call `s.Close()`. Tested in `TestSession_AuthenticationError_ClosesSession` |
| Context overflow -> warning event | PASS | Lines 794-797 emit WARNING for ContextLengthError. Tested in `TestSession_ContextLengthError_EmitsWarningAndClosesSession` |
| Graceful shutdown sequence | PARTIAL | Subagent cleanup happens before SESSION_END (reversed from spec). No explicit LLM stream cancellation in Close(). No explicit event flush. See GAP-9.11A, GAP-9.11B, GAP-9.11C |

### 9.12 Cross-Provider Parity Matrix

| Test Case | OpenAI | Anthropic | Gemini | Notes |
|-----------|--------|-----------|--------|-------|
| Simple file creation | PASS | PASS | PASS | `TestParity_SimpleFileCreation` |
| Read file then edit | PASS | PASS | PASS | `TestParity_ReadFileThenEdit` |
| Multi-file edit | PASS | PASS | PASS | `TestParity_MultiFileEdit` |
| Shell command execution | PASS | PASS | PASS | `TestParity_ShellCommandExecution` |
| Shell command timeout | PASS | PASS | PASS | `TestParity_ShellCommandTimeout` |
| Grep + glob | PASS | PASS | PASS | `TestParity_GrepAndGlob` |
| Multi-step task | PASS | PASS | PASS | `TestParity_MultiStepTask` |
| Tool output truncation (large file) | FAIL | FAIL | FAIL | Only OpenAI tested in session_test.go; no parity test. GAP-9.12A |
| Parallel tool calls | PASS | PASS | PASS | `TestParity_ParallelToolCalls` |
| Steering mid-task | PASS | PASS | PASS | `TestParity_SteeringMidTask` |
| Reasoning effort change | FAIL | FAIL | FAIL | Only OpenAI tested; no parity test. GAP-9.12B |
| Subagent spawn and wait | FAIL | FAIL | FAIL | Only OpenAI tested; no parity test. GAP-9.12C |
| Loop detection | PASS | PASS | PASS | `TestParity_LoopDetectionWarning` |
| Error recovery | PASS | PASS | PASS | `TestParity_ErrorRecovery` |
| Provider-specific editing format | PASS | PASS | PASS | Covered by `TestParity_ReadFileThenEdit` + `TestParity_MultiFileEdit` (see GAP-9.12D) |

### 9.13 Integration Smoke Test

| Scenario | Status | Notes |
|----------|--------|-------|
| 1. Simple file creation with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 2. Read and edit with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 3. Shell execution with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 4. Truncation verification with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 5. Steering with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 6. Subagent with real API | MISSING | No agent-level integration test. GAP-9.13 |
| 7. Timeout handling with real API | MISSING | No agent-level integration test. GAP-9.13 |

### Appendix B: Tool-Level Errors

| Error Type | Status | Evidence |
|------------|--------|----------|
| FileNotFound | PASS | `env.ReadFile()` returns os.ErrNotExist -> returned as error result. Tested via `TestParity_ErrorRecovery` |
| EditConflict | PASS | `EditFile()` returns "old_string not found" or "not unique" errors |
| ShellExitError | PASS | Shell tool returns exit_code in output string; nonzero = error info |
| ShellTimeout | PASS | Shell tool returns `[ERROR: Command timed out...]` message |
| PermissionDenied | PASS | OS-level permission errors propagated as error results |
| ValidationError | PASS | Schema validation failure returns error result. Tested in `TestToolRegistry_SchemaValidationError_*` |
| UnknownTool | PASS | `ExecuteCall()` returns "unknown tool: ..." error result. Tested in `TestToolRegistry_UnknownTool_ReturnsErrorResult` |

### Appendix B: Session-Level Errors

| Error Type | Retryable | Status | Evidence |
|------------|-----------|--------|----------|
| ProviderError 429 | Yes | PASS | `RateLimitError` with `retryable: true`. Tested in `TestSession_LLMTransientErrors_RetryWithBackoff` |
| ProviderError 500-503 | Yes | PASS | `ServerError` with `retryable: true`. `ErrorFromHTTPStatus` handles 500/502/503/504 |
| AuthenticationError | No | PASS | `retryable: false`, status 401. Session closes. Tested in `TestSession_AuthenticationError_ClosesSession` |
| ContextLengthError | No | PASS | `retryable: false`, status 413. WARNING + CLOSED. Tested in `TestSession_ContextLengthError_EmitsWarningAndClosesSession` |
| NetworkError | Yes | PASS | `NewNetworkError()` with `retryable: true` in `sdk_errors.go` |
| TurnLimitExceeded | No | PASS | Session transitions to IDLE, emits TURN_LIMIT. Tested in `TestSession_MaxTurns_StopsAcrossInputsAndEmitsEvent` |

### Appendix B: Graceful Shutdown Sequence

| Step | Status | Evidence |
|------|--------|----------|
| 1. Cancel in-flight LLM stream | PARTIAL | Context cancellation propagates to `client.Complete(ctx, req)`, but `Close()` does not own/cancel the context. GAP-9.11A |
| 2. SIGTERM to all running processes | PASS | `env.Cleanup()` -> `terminateProcessGroup()` |
| 3. Wait 2 seconds | PASS | `time.Sleep(2 * time.Second)` in `Cleanup()` |
| 4. SIGKILL remaining | PASS | `killProcessGroup()` in `Cleanup()` |
| 5. Flush pending events | PARTIAL | No explicit flush; buffered channel is best-effort. GAP-9.11B |
| 6. Emit SESSION_END | PASS | `Close()` emits `EventSessionEnd` |
| 7. Clean up subagents | PASS | Subagents closed in `Close()` (but before SESSION_END, not after). GAP-9.11C |
| 8. Transition to CLOSED | PASS | `s.state = SessionClosed` at start of `Close()` |
