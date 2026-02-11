# Section 2: Agentic Loop - Audit Findings

## Summary
11 gaps found (1 Critical, 3 Important, 5 Minor, 2 Info)

## Findings

### GAP-2.01: Session ID is ULID, not UUID
- **Spec requirement:** "id : String -- UUID, assigned at creation" (Section 2.1)
- **Current state:** The session ID is generated via `ulid.Make().String()` (session.go line 166). ULIDs are lexicographically sortable and include a timestamp component, which is arguably better than UUIDs, but the spec explicitly says "UUID".
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (line 14, 166)

### GAP-2.02: Session lacks a dedicated EventEmitter abstraction
- **Spec requirement:** "event_emitter : EventEmitter -- delivers events to host application" (Section 2.1)
- **Current state:** The session uses a raw `chan SessionEvent` (session.go line 107) plus an `emit()` method. There is no `EventEmitter` type or interface. The spec models this as a first-class record field with a named type. The implementation achieves the same result but through a Go channel directly rather than an abstracted EventEmitter interface. The `emit()` helper method on Session does the equivalent work.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 107, 633-651), `/Users/jesse/prime-radiant/serf/internal/agent/events.go`

### GAP-2.03: tool_output_limits type mismatch with spec
- **Spec requirement:** "tool_output_limits : Map<String, Integer> -- per-tool char limits (see Section 5)" (Section 2.2)
- **Current state:** The implementation uses `map[string]ToolOutputLimit` where `ToolOutputLimit` is a struct with `MaxChars int`, `MaxLines int`, and `Strategy TruncationStrategy`. This is strictly richer than the spec's `Map<String, Integer>`. The spec describes a simple string-to-integer mapping for per-tool character limits, while the implementation supports per-tool line limits and truncation strategies as well.
- **Severity:** Info - This is an intentional enrichment beyond spec, not a regression.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (line 37), `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go` (lines 25-29)

### GAP-2.04: Turn types diverge from spec structure
- **Spec requirement:** Section 2.4 defines five distinct turn types (UserTurn, AssistantTurn, ToolResultsTurn, SystemTurn, SteeringTurn), each with specific typed fields. AssistantTurn has fields: content, tool_calls, reasoning, usage, response_id, timestamp.
- **Current state:** The implementation uses a single `Turn` struct with a `Kind` discriminator and an embedded `llm.Message` plus optional `Usage` and `ResponseID` fields (turns.go). This is a tagged-union approach rather than distinct record types per the spec. The critical difference: the spec's AssistantTurn has explicit `tool_calls`, `reasoning`, and `content` fields, while the implementation stores everything inside `llm.Message.Content` as ContentParts. The data IS all present and accessible, but through the Message's ContentPart list rather than as named top-level fields. Also, the deprecated `TurnTool` kind still exists alongside `TurnToolResults`.
- **Severity:** Minor - Functionally equivalent, uses Go's tagged-union pattern rather than discriminated record types.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/turns.go`

### GAP-2.05: System prompt is built outside the loop but spec shows it built inside
- **Spec requirement:** Section 2.5 pseudocode shows the system prompt being built inside the LOOP on every iteration: `system_prompt = session.provider_profile.build_system_prompt(environment, project_docs)` is inside the LOOP block.
- **Current state:** The system prompt (`sys` variable) is built once before the `for round` loop (session.go lines 694-715). The system prompt does NOT change between rounds within a single processOneInput call. This means changes to environment info or project docs during tool execution within a single input would not be reflected. The spec's placement inside the loop suggests the system prompt should be rebuilt each round.
- **Severity:** Important - If the system prompt is supposed to be refreshed each round (e.g., after tools modify the environment), this is a behavioral gap. However, rebuilding every round has performance implications, and the spec's pseudocode placement may be aspirational rather than prescriptive.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 694-715, 720)

### GAP-2.06: Round count increment placement differs from spec
- **Spec requirement:** In the pseudocode (Section 2.5), `round_count += 1` happens at step 6, after tool calls are confirmed and before execution: "6. Execute tool calls... round_count += 1, results = execute_tool_calls(...)".
- **Current state:** The implementation uses a `for round := 0; round < s.cfg.MaxToolRoundsPerInput; round++` loop (session.go line 720), where the round count is the loop variable itself, incremented at the top of each iteration. The limit check `round_count >= max_tool_rounds_per_input` from the spec is effectively `round < MaxToolRoundsPerInput` in the for-loop condition. The net effect is the same: the loop body runs at most MaxToolRoundsPerInput times. However, a subtle difference: the spec increments round_count only when tool calls are actually executed, while the code increments on every loop iteration including pure text responses and pause_turn continuations. In practice this is harmless because the loop breaks/continues before reaching tool execution in those cases, but the round count semantics differ slightly from spec.
- **Severity:** Minor - Same effective behavior due to break/continue before round_count would matter.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (line 720)

### GAP-2.07: MaxTurns check placement and semantics differ from spec
- **Spec requirement:** The spec checks `max_turns` INSIDE the tool loop: "IF session.config.max_turns > 0 AND count_turns(session) >= session.config.max_turns: ... BREAK". The spec's `count_turns(session)` implies counting turns dynamically from the history.
- **Current state:** The implementation checks MaxTurns BEFORE the loop starts (session.go lines 675-686), using a simple incremental counter (`s.turns++`) rather than counting turns from history. The turn counter is incremented per user input, not per tool round. This means the turn limit check happens once per processOneInput, not on each loop iteration. This is actually more correct for the spec's intent (max_turns is across the session, not per tool round), but the placement differs from the pseudocode which checks it inside the loop on each round.
- **Severity:** Minor - The functional difference: a session at max_turns could still complete the current tool-loop rounds before hitting the limit on the next input, rather than stopping mid-loop as the spec implies.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 674-686)

### GAP-2.08: Abort signal transitions to CLOSED but only on context cancellation errors
- **Spec requirement:** "any -> CLOSED -- abort signal" (Section 2.3). "Abort signal. The host application signals cancellation. The current LLM stream is closed, running processes are killed, and the session transitions to CLOSED." (Section 2.8)
- **Current state:** The implementation handles abort via Go context cancellation. In ProcessInput (session.go lines 465-467), if the error is `context.Canceled` or `context.DeadlineExceeded` or `ctx.Err() != nil`, it calls `s.Close()`. This covers the standard Go patterns for signaling cancellation. However, the spec says "any -> CLOSED" which implies any state (including AWAITING_INPUT) should transition on abort. The current code only checks for abort in ProcessInput (during PROCESSING), not when the session is IDLE or AWAITING_INPUT. Calling Close() directly when IDLE works correctly (session.go lines 419-453), but there is no mechanism to abort from AWAITING_INPUT state triggered by a signal - it relies on the host calling Close() directly.
- **Severity:** Minor - In practice, the host application would call Close() which handles any->CLOSED correctly. The abort signal via context cancellation only applies when ProcessInput is running.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 455-480, 419-453)

### GAP-2.09: ASSISTANT_TEXT_START event is emitted but does not carry any data
- **Spec requirement:** "ASSISTANT_TEXT_START -- model began generating text" (Section 2.9). The spec does not specify data fields, but the streaming nature implies this event would occur before any text tokens arrive.
- **Current state:** `EventAssistantTextStart` is emitted with an empty map: `s.emit(EventAssistantTextStart, map[string]any{})` (session.go line 825). This is correct per spec since no specific data fields are required. However, since the implementation uses single-shot (non-streaming) LLM calls, the START event is emitted right before the full text is available (not truly "streaming"). The entire response text comes in one DELTA event rather than incrementally. This is a known v1 limitation (non-streaming).
- **Severity:** Important - The events are present but the streaming semantics are simulated. ASSISTANT_TEXT_DELTA delivers the entire text at once rather than incrementally. The spec envisions these as streaming events, but the implementation explicitly notes "single-shot, no SDK-level tool loop" in the spec pseudocode comment.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 825-839)

### GAP-2.10: SESSION_END emitted both after ProcessInput and after Close
- **Spec requirement:** The pseudocode shows `session.emit(SESSION_END)` at the end of `process_input` when the follow-up queue is empty (Section 2.5, line ~302). SESSION_END is also implied when Close() is called.
- **Current state:** The implementation emits SESSION_END in two places: (1) in ProcessInput after follow-up processing completes (line 472, with reason "input_complete"), and (2) in Close() (line 447, with reason "session_closed"). This means a normal flow emits SESSION_END twice: once when the input is fully processed and once when the session is explicitly closed. The spec's pseudocode only shows one SESSION_END emission at the end of process_input. The test `TestSession_SessionEnd_AfterProcessInput` explicitly validates both emissions occur.
- **Severity:** Important - Consumers must handle receiving multiple SESSION_END events. The spec implies a single SESSION_END per session, but the implementation sends one per completed input processing cycle plus one on close.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 472, 447), `/Users/jesse/prime-radiant/serf/internal/agent/session_dod_test.go` (lines 2400-2421)

### GAP-2.11: Three extra event kinds not in spec
- **Spec requirement:** Section 2.9 defines exactly 13 event kinds: SESSION_START, SESSION_END, USER_INPUT, ASSISTANT_TEXT_START, ASSISTANT_TEXT_DELTA, ASSISTANT_TEXT_END, TOOL_CALL_START, TOOL_CALL_OUTPUT_DELTA, TOOL_CALL_END, STEERING_INJECTED, TURN_LIMIT, LOOP_DETECTION, ERROR.
- **Current state:** The implementation defines 17 event kinds (events.go). The 13 spec kinds are all present. Four additional kinds exist: `COMMUNICATE`, `SKILL_ACTIVATED`, `CONTEXT_COMPACTION`, and `WARNING`. These are extensions beyond the spec.
- **Severity:** Critical - While extending beyond spec is generally acceptable, having events not documented in the spec means consumers may not handle them. The `WARNING` event is used for context window awareness and auto-save failures. `COMMUNICATE` is used by the communicate tool. `SKILL_ACTIVATED` fires when a skill is loaded. `CONTEXT_COMPACTION` fires during context management. These should either be added to the spec or explicitly documented as extensions.
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/events.go` (lines 7-25)

## Fully Implemented (Verified)

### Section 2.1 - Session Record Fields
- **id**: Present as `id string`, assigned at creation via ULID (functional equivalent of UUID). VERIFIED.
- **provider_profile**: Present as `profile ProviderProfile`. VERIFIED.
- **execution_env**: Present as `env ExecutionEnvironment`. VERIFIED.
- **history**: Present as `history []Turn`. VERIFIED.
- **event_emitter**: Implemented via `events chan SessionEvent` + `emit()` method. Functional equivalent. VERIFIED.
- **config**: Present as `cfg SessionConfig`. VERIFIED.
- **state**: Present as `state SessionState`. VERIFIED.
- **llm_client**: Present as `client *llm.Client`. VERIFIED.
- **steering_queue**: Present as `steeringQueue []string`. VERIFIED.
- **followup_queue**: Present as `followups []string`. VERIFIED.
- **subagents**: Present as `subagents map[string]*subagent`. VERIFIED.

### Section 2.2 - SessionConfig Fields and Defaults
- **max_turns** (default 0 = unlimited): `MaxTurns int`, defaults to 0 (no applyDefaults override). VERIFIED.
- **max_tool_rounds_per_input** (default 200): `MaxToolRoundsPerInput int`, default set to 200 in applyDefaults(). VERIFIED.
- **default_command_timeout_ms** (default 10000): `DefaultCommandTimeoutMS int`, default set to 10_000 in applyDefaults(). VERIFIED.
- **max_command_timeout_ms** (default 600000): `MaxCommandTimeoutMS int`, default set to 600_000 in applyDefaults(). VERIFIED.
- **reasoning_effort**: `ReasoningEffort string`. VERIFIED.
- **tool_output_limits**: `ToolOutputLimits map[string]ToolOutputLimit` (richer type than spec, see GAP-2.03). VERIFIED.
- **enable_loop_detection** (default true): `EnableLoopDetection *bool`, defaults to true via pointer in applyDefaults(). VERIFIED.
- **loop_detection_window** (default 10): `LoopDetectionWindow int`, default set to 10 in applyDefaults(). VERIFIED.
- **max_subagent_depth** (default 1): `MaxSubagentDepth int`, default set to 1 in applyDefaults(). VERIFIED.

### Section 2.3 - Session Lifecycle States
- **IDLE, PROCESSING, AWAITING_INPUT, CLOSED**: All four states defined as constants. VERIFIED.
- **IDLE -> PROCESSING**: on processOneInput(), `s.state = SessionProcessing` (line 659). VERIFIED.
- **PROCESSING -> PROCESSING**: tool loop continues within for-loop. VERIFIED.
- **PROCESSING -> AWAITING_INPUT**: when response ends with "?" and no tool calls (line 865). VERIFIED.
- **PROCESSING -> IDLE**: natural completion or turn limit (lines 868, 683, 942). VERIFIED.
- **PROCESSING -> CLOSED**: unrecoverable error via s.Close() (line 801). VERIFIED.
- **IDLE -> CLOSED**: via explicit Close() call (line 419). VERIFIED.
- **any -> CLOSED**: abort signal via context cancellation triggers Close() (line 466). VERIFIED.
- **AWAITING_INPUT -> PROCESSING**: next ProcessInput call transitions from any non-CLOSED state (line 654-659). VERIFIED via test TestSession_AwaitingInput_TransitionsToProcessing.

### Section 2.4 - Turn Types
- **UserTurn**: TurnUserInput with content as llm.Message. VERIFIED.
- **AssistantTurn**: TurnAssistant with content, tool_calls, reasoning (in Message.Content), usage, response_id. VERIFIED.
- **ToolResultsTurn**: TurnToolResults with aggregated results. VERIFIED.
- **SystemTurn**: TurnSystem exists. VERIFIED.
- **SteeringTurn**: TurnSteering, converted to user-role messages for LLM. VERIFIED (session.go line 748).

### Section 2.5 - Core Agentic Loop
- **UserTurn appended on input**: `s.appendTurn(TurnUserInput, llm.User(input))` (line 672). VERIFIED.
- **USER_INPUT event emitted**: `s.emit(EventUserInput, ...)` (line 671). VERIFIED.
- **Drain steering before first LLM call**: Lines 689-692. VERIFIED.
- **Round limit check**: `for round := 0; round < s.cfg.MaxToolRoundsPerInput; round++` (line 720). VERIFIED.
- **Abort check**: `select { case <-ctx.Done(): ... }` (lines 721-726). VERIFIED.
- **System prompt built with env + project docs**: Lines 694-715. VERIFIED.
- **LLM request built with model, messages, tools, tool_choice auto, reasoning_effort**: Lines 768-782. VERIFIED.
- **LLM call via client.Complete**: Line 789. VERIFIED.
- **Assistant turn recorded with usage and response_id**: appendAssistantTurn (line 826). VERIFIED.
- **ASSISTANT_TEXT_END emitted with text, reasoning, usage, finish_reason, model**: Lines 830-839. VERIFIED.
- **Natural completion (no tool calls) breaks**: Lines 862-872. VERIFIED.
- **Tool calls executed**: Lines 875-891 with parallel support. VERIFIED.
- **ToolResultsTurn appended**: Lines 893-906. VERIFIED.
- **Steering drained after tool execution**: Lines 922-925. VERIFIED.
- **Loop detection checked**: Lines 908-919. VERIFIED.
- **Follow-up queue processed**: ProcessInput lines 470-479. VERIFIED.
- **Parallel tool calls**: Lines 876-891, uses goroutines with WaitGroup. VERIFIED via test.
- **Concurrent execution gated by profile.SupportsParallelToolCalls()**: VERIFIED (line 876).

### Section 2.6 - Steering
- **steer() method**: `Steer(msg string)` (line 394). Thread-safe, ignores empty messages, ignores if closed. VERIFIED.
- **follow_up() method**: `FollowUp(msg string)` (line 407). Thread-safe, ignores empty messages, ignores if closed. VERIFIED.
- **SteeringTurns converted to user-role messages**: Line 748, steering turns emit `llm.User(t.Message.Text())`. VERIFIED.

### Section 2.7 - Reasoning Effort
- **Reasoning effort mapped to LLM Request**: Lines 779-782, set as `req.ReasoningEffort = &v`. VERIFIED.
- **low/medium/high/null mapping**: The string is passed through to the LLM SDK's `ReasoningEffort *string` field. VERIFIED.
- **Mid-session change takes effect on next call**: `SetReasoningEffort()` method (line 384) updates cfg under lock. VERIFIED via test TestSession_ReasoningEffort_PassedThroughAndCanChange.

### Section 2.8 - Stop Conditions
- **Natural completion**: No tool calls -> break. VERIFIED.
- **Round limit**: for-loop condition. VERIFIED.
- **Turn limit**: MaxTurns check before loop (lines 680-686). VERIFIED.
- **Abort signal**: Context cancellation -> Close(). VERIFIED via test TestSession_AbortSignal_ClosesSessionAndEmitsSessionEnd.
- **Unrecoverable error**: Non-retryable LLM errors -> Close() (lines 800-802). VERIFIED via test TestSession_AuthenticationError_ClosesSession.

### Section 2.9 - Event System
- **SessionEvent record**: Has Kind, Timestamp, SessionID, Data. VERIFIED (events.go lines 27-32).
- All 13 spec EventKinds present: SESSION_START, SESSION_END, USER_INPUT, ASSISTANT_TEXT_START, ASSISTANT_TEXT_DELTA, ASSISTANT_TEXT_END, TOOL_CALL_START, TOOL_CALL_OUTPUT_DELTA, TOOL_CALL_END, STEERING_INJECTED, TURN_LIMIT, LOOP_DETECTION, ERROR. VERIFIED.
- **TOOL_CALL_END carries full untruncated output**: `"full_output": res.FullOutput` (line 522). VERIFIED via test.
- **TOOL_CALL_OUTPUT_DELTA emitted**: Lines 504-516, chunked at 4000 chars. VERIFIED.

### Section 2.10 - Loop Detection
- **Signature = name + arguments hash**: `call.Name + ":" + shortHash(call.Arguments)` (line 910). VERIFIED.
- **Pattern lengths 1, 2, 3**: `for patLen := 1; patLen <= 3; patLen++` (line 976). VERIFIED.
- **Window size check**: `if len(signatures) < windowSize: return false` (line 973). VERIFIED.
- **Divisibility check**: `if windowSize%patLen != 0: continue` (line 977). VERIFIED.
- **Warning injected as SteeringTurn**: Line 916. VERIFIED.
- **LOOP_DETECTION event emitted**: Line 915. VERIFIED.
- **detectLoop algorithm matches spec pseudocode**: Full comparison verified. VERIFIED.
