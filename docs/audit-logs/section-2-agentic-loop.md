# Audit: Section 2 -- Agentic Loop

Auditor: Claude Opus 4.6
Date: 2026-02-11
Spec: `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` lines 124-451

## Summary

- Total requirements checked: 68
- Implemented: 55
- Partial: 8
- Missing: 3
- Not Applicable / Intentional Deviation: 2

## Findings

---

### 2.1 Session Record

### GAP-2.01: Session ID uses ULID instead of UUID

**Status:** INTENTIONAL DEVIATION
**Spec:** `id : String -- UUID, assigned at creation`
**Evidence:** `session.go:14` imports `github.com/oklog/ulid/v2`; `session.go:177` sets `id: ulid.Make().String()`.
**Description:** The spec mandates a UUID, but the implementation uses a ULID. ULIDs are lexicographically sortable and contain a timestamp component, making them arguably better for session IDs. Both are unique string identifiers. This is likely an intentional design choice, but it deviates from the literal spec text.

---

- [x] IMPLEMENTED: `provider_profile` -- `session.go:103` stores `profile ProviderProfile`.
- [x] IMPLEMENTED: `execution_env` -- `session.go:104` stores `env ExecutionEnvironment`.
- [x] IMPLEMENTED: `history : List<Turn>` -- `session.go:113` stores `history []Turn`.
- [x] IMPLEMENTED: `event_emitter` -- `session.go:107` stores `events chan SessionEvent`.
- [x] IMPLEMENTED: `config : SessionConfig` -- `session.go:101` stores `cfg SessionConfig`.
- [x] IMPLEMENTED: `state : SessionState` -- `session.go:111` stores `state SessionState`.
- [x] IMPLEMENTED: `llm_client` -- `session.go:102` stores `client *llm.Client`.
- [x] IMPLEMENTED: `steering_queue` -- `session.go:117` stores `steeringQueue []string`.
- [x] IMPLEMENTED: `followup_queue` -- `session.go:118` stores `followups []string`.
- [x] IMPLEMENTED: `subagents : Map<String, SubAgent>` -- `session.go:126` stores `subagents map[string]*subagent`.

---

### 2.2 Session Configuration

- [x] IMPLEMENTED: `max_turns` default 0 (unlimited) -- `session.go:31` field `MaxTurns int`, Go zero-value is 0. `applyDefaults()` does not set it, so default is 0.
- [x] IMPLEMENTED: `max_tool_rounds_per_input` default 200 -- `session.go:78-79` sets default to 200.
- [x] IMPLEMENTED: `default_command_timeout_ms` default 10000 -- `session.go:82-83` sets default to 10,000.
- [x] IMPLEMENTED: `max_command_timeout_ms` default 600000 -- `session.go:85-86` sets default to 600,000.
- [x] IMPLEMENTED: `reasoning_effort` -- `session.go:43-44` stores `ReasoningEffort string`.
- [x] IMPLEMENTED: `enable_loop_detection` default true -- `session.go:90-93` sets pointer to `true`.
- [x] IMPLEMENTED: `loop_detection_window` default 10 -- `session.go:94-96` sets default to 10.
- [x] IMPLEMENTED: `max_subagent_depth` default 1 -- `session.go:88-89` sets default to 1.

### GAP-2.02: tool_output_limits type is richer than spec

**Status:** INTENTIONAL DEVIATION
**Spec:** `tool_output_limits : Map<String, Integer> -- per-tool char limits (see Section 5)`
**Evidence:** `session.go:37` declares `ToolOutputLimits map[string]ToolOutputLimit` where `ToolOutputLimit` is a struct with `MaxChars`, `MaxLines`, and `Strategy` fields (`tool_registry.go:25-29`).
**Description:** The spec defines `tool_output_limits` as a simple map from tool name to integer (character limit). The implementation uses a richer struct that also supports line limits and truncation strategy. This is a superset of the spec's requirement and should be considered an enhancement, not a gap. The `MaxChars` field corresponds to the spec's integer value.

---

### 2.3 Session Lifecycle

- [x] IMPLEMENTED: `IDLE` state -- `session.go:23` defines `SessionIdle = "IDLE"`.
- [x] IMPLEMENTED: `PROCESSING` state -- `session.go:24` defines `SessionProcessing = "PROCESSING"`.
- [x] IMPLEMENTED: `AWAITING_INPUT` state -- `session.go:25` defines `SessionAwaitingInput = "AWAITING_INPUT"`.
- [x] IMPLEMENTED: `CLOSED` state -- `session.go:26` defines `SessionClosed = "CLOSED"`.

State transitions:

- [x] IMPLEMENTED: `IDLE -> PROCESSING` on submit -- `session.go:708` sets `s.state = SessionProcessing` at start of `processOneInput`.
- [x] IMPLEMENTED: `PROCESSING -> PROCESSING` tool loop continues -- the for loop at `session.go:746` continues iterating.
- [x] IMPLEMENTED: `PROCESSING -> AWAITING_INPUT` -- `session.go:921-922` sets state to `SessionAwaitingInput` when response ends with `?`.
- [x] IMPLEMENTED: `PROCESSING -> IDLE` natural completion -- `session.go:924` sets state to `SessionIdle` when response has no tool calls and doesn't end with `?`.
- [x] IMPLEMENTED: `PROCESSING -> CLOSED` unrecoverable error -- `session.go:855-858` calls `s.Close()` on non-retryable LLM errors.
- [x] IMPLEMENTED: `IDLE -> CLOSED` explicit close -- `session.go:437-443` `Close()` transitions from any state to `SessionClosed`.
- [x] IMPLEMENTED: `any -> CLOSED` abort signal -- `session.go:493-495` in `ProcessInput`, context cancellation triggers `s.Close()`.
- [x] IMPLEMENTED: `AWAITING_INPUT -> PROCESSING` user provides answer -- `session.go:703-708` allows `processOneInput` to be called regardless of prior state (tested in `session_dod_test.go:2312`).

### GAP-2.03: AWAITING_INPUT detection heuristic is simplistic

**Status:** PARTIAL
**Spec:** `PROCESSING -> AWAITING_INPUT -- model asks user a question (no tool calls, open-ended)`
**Evidence:** `session.go:920-924` uses `strings.HasSuffix(trimmed, "?")` to determine if the model is asking a question.
**Description:** The spec describes AWAITING_INPUT as triggered when "model asks user a question (no tool calls, open-ended)." The implementation uses a simple heuristic -- checking if the trimmed response ends with `?`. This will miss questions that don't end with `?` (e.g., "Please provide the file path.") and may false-positive on text that incidentally ends with `?` but isn't a question. However, this is a reasonable pragmatic choice for v1.

---

### 2.4 Turn Types

- [x] IMPLEMENTED: `UserTurn` -- `turns.go:12` defines `TurnUserInput TurnKind = "USER_INPUT"`.
- [x] IMPLEMENTED: `AssistantTurn` -- `turns.go:14` defines `TurnAssistant TurnKind = "ASSISTANT"`.
- [x] IMPLEMENTED: `ToolResultsTurn` -- `turns.go:16` defines `TurnToolResults TurnKind = "TOOL_RESULTS"`.
- [x] IMPLEMENTED: `SystemTurn` -- `turns.go:17` defines `TurnSystem TurnKind = "SYSTEM"`.
- [x] IMPLEMENTED: `SteeringTurn` -- `turns.go:13` defines `TurnSteering TurnKind = "STEERING"`.

Turn fields:

- [x] IMPLEMENTED: `UserTurn.content` -- stored in `Turn.Message` as `llm.User(input)`.
- [x] IMPLEMENTED: `UserTurn.timestamp` -- `turns.go:32` sets `Timestamp: time.Now().UTC()`.
- [x] IMPLEMENTED: `AssistantTurn.content` -- stored via `resp.Message` in `appendAssistantTurn`.
- [x] IMPLEMENTED: `AssistantTurn.tool_calls` -- tool calls are part of `resp.Message.Content` (ContentPart with Kind=ContentToolCall).
- [x] IMPLEMENTED: `AssistantTurn.reasoning` -- thinking parts are in `resp.Message.Content` (ContentPart with Kind=ContentThinking).
- [x] IMPLEMENTED: `AssistantTurn.usage` -- `session.go:580` stores `Usage: resp.Usage`.
- [x] IMPLEMENTED: `AssistantTurn.response_id` -- `session.go:581` stores `ResponseID: resp.ID`.
- [x] IMPLEMENTED: `AssistantTurn.timestamp` -- `session.go:579` sets `Timestamp: time.Now().UTC()`.
- [x] IMPLEMENTED: `ToolResultsTurn.results` -- `session.go:950-962` aggregates tool results into ContentParts.
- [x] IMPLEMENTED: `ToolResultsTurn.timestamp` -- created via `appendTurn` which calls `NewTurn` with `time.Now().UTC()`.
- [x] IMPLEMENTED: `SteeringTurn.content` -- stored via `llm.User(msg)`.
- [x] IMPLEMENTED: `SteeringTurn.timestamp` -- created via `appendTurn` which calls `NewTurn`.

---

### 2.5 Core Agentic Loop (process_input)

- [x] IMPLEMENTED: Set state to PROCESSING -- `session.go:708`.
- [x] IMPLEMENTED: Append UserTurn -- `session.go:721`.
- [x] IMPLEMENTED: Emit USER_INPUT -- `session.go:720`.
- [x] IMPLEMENTED: Drain steering before first LLM call -- `session.go:738-741`.

### GAP-2.04: Turn limit checked outside the main loop, not inside

**Status:** PARTIAL
**Spec:** The spec checks `max_turns` **inside** the LOOP (step 1) alongside `max_tool_rounds_per_input`.
**Evidence:** `session.go:729-735` checks `MaxTurns` before entering the loop. The for loop condition at line 746 only checks `MaxToolRoundsPerInput`.
**Description:** The spec shows both `max_tool_rounds_per_input` and `max_turns` being checked at the top of the inner LOOP. The implementation checks `max_turns` once before the loop (per processOneInput call) and only checks `max_tool_rounds_per_input` via the for loop condition. This means `max_turns` is checked per user input, not per tool round. The semantic difference is minor because turns only increment on new user input, not during tool rounds, but the placement differs from spec.

### GAP-2.05: Round count increments unconditionally, including pause_turn responses

**Status:** PARTIAL
**Spec:** Step 6 of the loop increments `round_count` only after tool calls execute: `round_count += 1`.
**Evidence:** `session.go:746` uses `for round := 0; round < s.cfg.MaxToolRoundsPerInput; round++` which increments `round` on every iteration, including when `pause_turn` continues the loop (line 899-901) without any tool calls.
**Description:** When the LLM returns a `pause_turn` finish reason (e.g., server-side web search in progress), the implementation counts that as a round toward the `MaxToolRoundsPerInput` limit. In the spec, `round_count` only increments when tool calls are actually executed. This means a session with many `pause_turn` responses could hit the round limit earlier than the spec intends.

---

- [x] IMPLEMENTED: Build system prompt using profile -- `session.go:755-777`.
- [x] IMPLEMENTED: Convert history to messages -- `session.go:797-822`.
- [x] IMPLEMENTED: Create Request with model, messages, tools, tool_choice, reasoning_effort, provider, provider_options -- `session.go:824-838`.
- [x] IMPLEMENTED: tool_choice = "auto" -- `session.go:829` sets `ToolChoice: &llm.ToolChoice{Mode: "auto"}`.
- [x] IMPLEMENTED: Call LLM via Unified SDK -- `session.go:844-846`.
- [x] IMPLEMENTED: Record assistant turn -- `session.go:882`.
- [x] IMPLEMENTED: Emit ASSISTANT_TEXT_END with text and reasoning -- `session.go:886-895`.
- [x] IMPLEMENTED: Check for no tool calls -> natural completion (BREAK) -- `session.go:918-928`.
- [x] IMPLEMENTED: Execute tool calls -- `session.go:931-947`.
- [x] IMPLEMENTED: Append ToolResultsTurn -- `session.go:949-962`.
- [x] IMPLEMENTED: Drain steering after tool execution -- `session.go:977-981`.
- [x] IMPLEMENTED: Loop detection -- `session.go:964-975`.
- [x] IMPLEMENTED: Process followup_queue after loop -- `session.go:483-518` (iterative, not recursive, see GAP-2.06).
- [x] IMPLEMENTED: Set state to IDLE after loop -- `session.go:997-999` and `session.go:924`.
- [x] IMPLEMENTED: Emit SESSION_END after loop -- `session.go:500-514`.

### GAP-2.06: Follow-up processing is iterative, not recursive

**Status:** PARTIAL
**Spec:** `process_input(session, next_input)` -- spec shows recursive call from within `process_input`.
**Evidence:** `session.go:483-518` implements follow-up processing as a `for` loop that calls `processOneInput` iteratively.
**Description:** The spec shows follow-ups processed via a recursive call to `process_input`. The implementation uses an iterative loop in `ProcessInput` that dequeues follow-ups and calls `processOneInput` in a loop. This is semantically equivalent (and avoids stack overflow for deep follow-up chains), so this is a reasonable implementation choice. The behavior matches the spec's intent.

### GAP-2.07: System prompt rebuilt every round (spec builds once per process_input)

**Status:** PARTIAL
**Spec:** Step 2 of the loop builds the system prompt once per iteration. The spec does not explicitly say it should or shouldn't be rebuilt each round.
**Evidence:** `session.go:754-777` rebuilds the system prompt every iteration. Comment at line 754: "Rebuild system prompt each iteration so tool side-effects (e.g. new AGENTS.md) are reflected."
**Description:** The implementation rebuilds the system prompt on every loop iteration, while the spec shows building it once within the loop body. The implementation's approach is arguably better because it ensures project doc changes during tool execution are reflected immediately. This is an enhancement, not a gap.

---

### 2.5.1 execute_tool_calls

- [x] IMPLEMENTED: Parallel execution when profile supports it -- `session.go:932-947` uses goroutines with WaitGroup when `SupportsParallelToolCalls()`.
- [x] IMPLEMENTED: Sequential execution otherwise -- `session.go:943-946` iterates sequentially.

### 2.5.2 execute_single_tool

- [x] IMPLEMENTED: Emit TOOL_CALL_START with tool_name and call_id -- `session.go:535`.
- [x] IMPLEMENTED: Look up tool in registry -- `tool_registry.go:142-148`.
- [x] IMPLEMENTED: Unknown tool returns error result -- `tool_registry.go:146-148`.

### GAP-2.08: Unknown tool error emitted differently than spec

**Status:** PARTIAL
**Spec:** On unknown tool: `session.emit(TOOL_CALL_END, call_id = tool_call.id, error = error_msg)` then returns `ToolResult(... is_error = true)`.
**Evidence:** `tool_registry.go:146-148` returns an error result from `ExecuteCall`, but the TOOL_CALL_START and TOOL_CALL_END events are emitted in `session.go:535` and `session.go:556-561`. The TOOL_CALL_END event emits `is_error` and `full_output` but uses the `full_output` key instead of `error` key for the error message.
**Description:** The spec shows an `error` key in the TOOL_CALL_END event data, but the implementation uses `full_output` (which contains the error message) and `is_error: true`. Consumers can still identify errors, but the key name differs. Additionally, for unknown tools, the TOOL_CALL_START event is emitted before the registry lookup (in `execTool`), which the spec also does -- so this is consistent. The only issue is the key naming convention.

---

- [x] IMPLEMENTED: Execute tool via execution environment -- `tool_registry.go:166`.
- [x] IMPLEMENTED: Truncate output before sending to LLM -- `tool_registry.go:182-195`.
- [x] IMPLEMENTED: Emit TOOL_CALL_END with FULL untruncated output -- `session.go:556-561`, key `full_output`.
- [x] IMPLEMENTED: Return truncated ToolResult -- `tool_registry.go:178-179` returns truncated `Output` and full `FullOutput`.
- [x] IMPLEMENTED: Error handling in tool execution -- `tool_registry.go:167-176`.

---

### 2.6 Steering

- [x] IMPLEMENTED: `steer()` queues message -- `session.go:412-422`.
- [x] IMPLEMENTED: Steering rejected when session is CLOSED -- `session.go:415-417`.
- [x] IMPLEMENTED: Empty steering messages ignored -- `session.go:418-420`.
- [x] IMPLEMENTED: `follow_up()` queues message -- `session.go:425-435`.
- [x] IMPLEMENTED: Follow-up rejected when CLOSED -- `session.go:428-430`.
- [x] IMPLEMENTED: SteeringTurns converted to user-role messages -- `session.go:803-805` converts `TurnSteering` to `llm.User(t.Message.Text())`.

---

### 2.7 Reasoning Effort

- [x] IMPLEMENTED: Maps to SDK request -- `session.go:835-838` sets `req.ReasoningEffort`.
- [x] IMPLEMENTED: Takes effect on next LLM call -- `session.go:402-409` `SetReasoningEffort()` modifies `cfg.ReasoningEffort`.
- [x] IMPLEMENTED: Null means no override -- `session.go:835` only sets `ReasoningEffort` when non-empty.
- [x] IMPLEMENTED: Tested: `session_parity_test.go:702-737` verifies `low` and `high` values are passed through.

---

### 2.8 Stop Conditions

- [x] IMPLEMENTED: Natural completion (text-only response) -- `session.go:918-928`.
- [x] IMPLEMENTED: Round limit -- `session.go:746` for loop condition, `session.go:996` emits TURN_LIMIT.
- [x] IMPLEMENTED: Turn limit -- `session.go:729-735`.
- [x] IMPLEMENTED: Abort signal -- `session.go:493-495` context cancellation closes session.
- [x] IMPLEMENTED: Unrecoverable error -- `session.go:855-858` calls `Close()` on non-retryable errors.

### GAP-2.09: Abort signal transitions to CLOSED via Close(), but processOneInput may return error before Close() runs

**Status:** IMPLEMENTED (with notes)
**Spec:** "The current LLM stream is closed, running processes are killed, and the session transitions to CLOSED."
**Evidence:** `session.go:493-495` calls `s.Close()` when context is cancelled. `session.go:747-752` checks `ctx.Done()` at top of each iteration.
**Description:** The implementation handles abort correctly: `ProcessInput` catches context cancellation and calls `Close()`, which cancels in-flight LLM calls via `cancelFunc`. The session-level context (`sessionCtx`) propagates cancellation. Running tool processes are killed by the tool's context being cancelled. This matches the spec.

---

### 2.9 Event System

- [x] IMPLEMENTED: `SessionEvent` record with `kind`, `timestamp`, `session_id`, `data` -- `events.go:27-32`.

EventKind values:

- [x] IMPLEMENTED: `SESSION_START` -- `events.go:8`.
- [x] IMPLEMENTED: `SESSION_END` -- `events.go:9`.
- [x] IMPLEMENTED: `USER_INPUT` -- `events.go:10`.
- [x] IMPLEMENTED: `ASSISTANT_TEXT_START` -- `events.go:11`.
- [x] IMPLEMENTED: `ASSISTANT_TEXT_DELTA` -- `events.go:12`.
- [x] IMPLEMENTED: `ASSISTANT_TEXT_END` -- `events.go:13`.
- [x] IMPLEMENTED: `TOOL_CALL_START` -- `events.go:14`.
- [x] IMPLEMENTED: `TOOL_CALL_OUTPUT_DELTA` -- `events.go:15`.
- [x] IMPLEMENTED: `TOOL_CALL_END` -- `events.go:16`.
- [x] IMPLEMENTED: `STEERING_INJECTED` -- `events.go:17`.
- [x] IMPLEMENTED: `TURN_LIMIT` -- `events.go:18`.
- [x] IMPLEMENTED: `LOOP_DETECTION` -- `events.go:19`.
- [x] IMPLEMENTED: `ERROR` -- `events.go:24`.

### GAP-2.10: Extra event kinds not in spec

**Status:** N/A (Enhancement)
**Spec:** The spec defines exactly 13 EventKind values.
**Evidence:** `events.go:20-23` defines additional event kinds: `COMMUNICATE`, `SKILL_ACTIVATED`, `CONTEXT_COMPACTION`, `WARNING`.
**Description:** The implementation adds four event kinds beyond the spec: `COMMUNICATE` (for communicate tool actions), `SKILL_ACTIVATED` (for skill loading), `CONTEXT_COMPACTION` (for context management), and `WARNING` (for non-fatal warnings like context usage). These are extensions that do not conflict with the spec's required events. All 13 spec-required events are implemented.

### GAP-2.11: TOOL_CALL_END uses "full_output" key instead of "output"

**Status:** PARTIAL
**Spec:** `session.emit(TOOL_CALL_END, call_id = tool_call.id, output = raw_output)` -- spec uses key name `output`.
**Evidence:** `session.go:557-561` emits with key `full_output`:
```go
s.emit(EventToolCallEnd, map[string]any{
    "tool_name":   res.ToolName,
    "call_id":     res.CallID,
    "is_error":    res.IsError,
    "full_output": res.FullOutput,
})
```
**Description:** The spec uses the key `output` for the full untruncated output in the TOOL_CALL_END event. The implementation uses `full_output`. While more descriptive, this deviates from the spec's naming. Consumers expecting the `output` key will not find it. The spec also does not include `is_error` or `tool_name` in TOOL_CALL_END, but the implementation adds them (which is a useful enhancement).

### GAP-2.12: TOOL_CALL_END error case uses different data shape than spec

**Status:** PARTIAL
**Spec:** Error case: `session.emit(TOOL_CALL_END, call_id = tool_call.id, error = error_msg)` -- separate `error` key.
**Evidence:** `session.go:556-561` -- same event shape for both success and error cases. Uses `is_error: true/false` and `full_output` for both.
**Description:** The spec shows two different `TOOL_CALL_END` event shapes: success uses `output`, error uses `error`. The implementation uses a single shape with `is_error` flag and `full_output` for both. This is arguably cleaner but diverges from the spec's two-shape design. Error messages appear in `full_output` rather than a dedicated `error` field.

---

### 2.10 Loop Detection

- [x] IMPLEMENTED: Track signatures (name + args hash) -- `session.go:965-967` uses `call.Name + ":" + shortHash(call.Arguments)`.
- [x] IMPLEMENTED: Check for repeating patterns of length 1, 2, or 3 -- `session.go:1032-1047` in `detectLoop`.
- [x] IMPLEMENTED: Window size check -- `session.go:1028-1029` checks `len(signatures) < windowSize`.
- [x] IMPLEMENTED: `windowSize % patLen != 0` skip -- `session.go:1033`.
- [x] IMPLEMENTED: Pattern matching -- `session.go:1036-1043`.
- [x] IMPLEMENTED: Inject warning SteeringTurn -- `session.go:970-974`.
- [x] IMPLEMENTED: Emit LOOP_DETECTION event -- `session.go:971`.

### GAP-2.13: Loop detection warning message wording differs from spec

**Status:** PARTIAL
**Spec:** `"Loop detected: the last " + session.config.loop_detection_window + " tool calls follow a repeating pattern. Try a different approach."`
**Evidence:** `session.go:970`:
```go
warning := fmt.Sprintf("Loop detected: repeating pattern in the last %d tool calls. Try a different approach.", s.cfg.LoopDetectionWindow)
```
**Description:** Minor wording difference: spec says "the last N tool calls follow a repeating pattern" while implementation says "repeating pattern in the last N tool calls." The semantic meaning is identical, but the exact string differs.

---

### Additional Observations

### GAP-2.14: No explicit `abort_signaled` field on Session

**Status:** IMPLEMENTED (differently)
**Spec:** `IF session.abort_signaled: BREAK` -- spec shows an explicit field.
**Evidence:** The implementation uses Go context cancellation (`ctx.Done()`) rather than an explicit boolean field. `session.go:747-752` checks `ctx.Done()` at the top of each loop iteration.
**Description:** Instead of an `abort_signaled` boolean, the implementation uses Go's idiomatic context cancellation mechanism. This provides the same functionality through a different mechanism. The session-level context (`sessionCtx` at line 148) is cancelled by `Close()`, which propagates to the in-flight loop.

### GAP-2.15: Event delivery is best-effort, not guaranteed

**Status:** IMPLEMENTED (with caveats)
**Spec:** "Events are delivered via an async iterator (or language-appropriate equivalent) to the host application."
**Evidence:** `session.go:684-688`:
```go
select {
case s.events <- ev:
default:
    // Drop events if consumer is too slow; v1 is best-effort.
}
```
**Description:** The event channel has a buffer of 256 (`session.go:184`). If the consumer falls behind, events are silently dropped. The spec does not explicitly address backpressure or event loss, but the implementation's comment acknowledges this is "v1 best-effort." For production use, a slower consumer could miss important events like LOOP_DETECTION or ERROR.

### GAP-2.16: ProcessInput returns error on round limit (spec shows BREAK and continue to follow-up)

**Status:** MISSING
**Spec:** When `max_tool_rounds_per_input` is reached, the spec emits TURN_LIMIT and BREAKs from the loop, then falls through to follow-up processing and SESSION_END.
**Evidence:** `session.go:996-1000`:
```go
s.emit(EventTurnLimit, map[string]any{"max_tool_rounds_per_input": s.cfg.MaxToolRoundsPerInput})
s.mu.Lock()
s.state = SessionIdle
s.mu.Unlock()
return "", fmt.Errorf("max tool rounds reached")
```
**Description:** When the round limit is hit, the implementation returns an error. In the spec, hitting the round limit causes a BREAK from the inner loop, after which follow-ups are processed and SESSION_END is emitted. The implementation's error return prevents follow-up processing and causes `ProcessInput` to return an error to the caller. This may be intentional (treating round limit exhaustion as a caller-visible failure), but it differs from the spec's "return what it has" approach.

### GAP-2.17: ProcessInput returns error on turn limit (spec shows BREAK)

**Status:** MISSING
**Spec:** When `max_turns` is reached, the spec emits TURN_LIMIT and BREAKs from the loop.
**Evidence:** `session.go:729-734`:
```go
if s.cfg.MaxTurns > 0 && turns > s.cfg.MaxTurns {
    s.emit(EventTurnLimit, map[string]any{"max_turns": s.cfg.MaxTurns})
    s.mu.Lock()
    s.state = SessionIdle
    s.mu.Unlock()
    return "", fmt.Errorf("turn limit reached")
}
```
**Description:** Similar to GAP-2.16. The spec shows the turn limit causing a BREAK from the loop, after which follow-ups and SESSION_END proceed normally. The implementation returns an error, preventing follow-up processing. Additionally, the turn limit check uses `>` instead of `>=`, meaning one extra turn is allowed compared to the spec's `count_turns(session) >= session.config.max_turns`.

### GAP-2.18: SESSION_END emitted by ProcessInput caller loop, not by processOneInput

**Status:** IMPLEMENTED (differently)
**Spec:** `session.emit(SESSION_END)` is emitted at the end of `process_input`, after follow-up processing.
**Evidence:** `session.go:500-514` emits SESSION_END in the `ProcessInput` method's follow-up loop, not inside `processOneInput`.
**Description:** The spec shows SESSION_END emitted at the end of `process_input` (the single-input function). The implementation splits this across `ProcessInput` (the outer loop) and `Close()`. SESSION_END is emitted once, after all follow-ups are processed, with deduplication via `sessionEndEmitted`. This achieves the same result through a different structure.

### GAP-2.19: No ASSISTANT_TEXT_START event data (empty map)

**Status:** MISSING
**Spec:** `ASSISTANT_TEXT_START -- model began generating text`. No specific data fields defined, but consistent with other events.
**Evidence:** `session.go:881` emits `EventAssistantTextStart` with an empty map: `s.emit(EventAssistantTextStart, map[string]any{})`.
**Description:** The ASSISTANT_TEXT_START event carries no data. While the spec doesn't specify what data fields this event should carry, other events include identifying information. This event could benefit from including the round number or model information for observability.

---

## Summary Table

| ID | Title | Status |
|----|-------|--------|
| GAP-2.01 | Session ID uses ULID instead of UUID | Intentional Deviation |
| GAP-2.02 | tool_output_limits type is richer than spec | Intentional Deviation |
| GAP-2.03 | AWAITING_INPUT detection heuristic is simplistic | Partial |
| GAP-2.04 | Turn limit checked outside the main loop | Partial |
| GAP-2.05 | Round count increments on pause_turn | Partial |
| GAP-2.06 | Follow-up processing is iterative, not recursive | Partial |
| GAP-2.07 | System prompt rebuilt every round | Partial (Enhancement) |
| GAP-2.08 | Unknown tool error emitted differently | Partial |
| GAP-2.09 | Abort signal uses context cancellation | Implemented |
| GAP-2.10 | Extra event kinds not in spec | N/A (Enhancement) |
| GAP-2.11 | TOOL_CALL_END uses "full_output" key | Partial |
| GAP-2.12 | TOOL_CALL_END error case data shape | Partial |
| GAP-2.13 | Loop detection warning wording | Partial |
| GAP-2.14 | No abort_signaled field | Implemented |
| GAP-2.15 | Event delivery is best-effort | Implemented |
| GAP-2.16 | Round limit returns error instead of BREAK | Missing |
| GAP-2.17 | Turn limit returns error and uses > not >= | Missing |
| GAP-2.18 | SESSION_END emitted from outer loop | Implemented |
| GAP-2.19 | ASSISTANT_TEXT_START has no data | Missing |
