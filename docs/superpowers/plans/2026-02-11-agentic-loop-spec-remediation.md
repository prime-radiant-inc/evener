# Agentic Loop Spec Compliance Remediation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix every actionable gap from the coding-agent-loop-spec compliance audit (docs/audit-logs/COMPREHENSIVE-AUDIT-REPORT.md), excluding INTENTIONAL DEVIATION (4 items) and INFORMATIONAL (12 items). That's 46 items across FAIL (2), MISSING (3), PARTIAL (19), and MINOR (22).

**Architecture:** All changes are in `internal/agent/` and its dependencies. Each task is a self-contained TDD fix: write the failing test, implement the fix, verify, commit. Tasks are ordered by priority and dependency. Some MINOR items are Go idioms that should be documented rather than changed.

**Tech Stack:** Go, `internal/agent/` package, `internal/llm/` types, `os/exec`, `unicode/utf8`, `runtime`

**Audit source:** `docs/audit-logs/COMPREHENSIVE-AUDIT-REPORT.md` and per-section files

---

## Task 1: Fix Graceful Shutdown Ordering (GAP-8.23, GAP-8.24)

**Priority:** P0 (only true FAIL in the audit)

The spec (Appendix B) requires this shutdown ordering:
1. Cancel in-flight LLM stream
2. Send SIGTERM to running child processes, SIGKILL after 2s
3. Flush pending events
4. Emit SESSION_END
5. Close subagent sessions
6. Transition to CLOSED

Current `Close()` in `session.go:443-481` does: state→CLOSED first, cancel LLM, emit SESSION_END, close subagents, env.Cleanup(), close events channel. This is wrong: state transitions to CLOSED before processes are killed and events flushed.

**Files:**
- Modify: `internal/agent/session.go:440-481` (the `Close()` method)
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_Close_ShutdownOrdering(t *testing.T) {
	// Track the order of operations during Close().
	var order []string
	var mu sync.Mutex
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }

	mockEnv := &mockExecutionEnvironment{
		cleanupFn: func() { record("env_cleanup") },
	}
	profile := NewOpenAIProfile("test-model")

	// Create a mock client that blocks on Complete (simulates in-flight LLM call).
	ctrl := newMockLLMCtrl()
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		record("llm_cancelled")
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}

	sess, err := NewSession(ctrl.client, profile, mockEnv, SessionConfig{
		MaxTurns:              10,
		MaxToolRoundsPerInput: 5,
	})
	require.NoError(t, err)

	// Collect events to track SESSION_END timing.
	var sessionEndSeen bool
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventSessionEnd {
				record("session_end_emitted")
				sessionEndSeen = true
			}
		}
		record("events_channel_closed")
	}()

	sess.Close()

	// Wait for event goroutine to finish.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// env_cleanup must happen BEFORE events_channel_closed.
	cleanupIdx := -1
	closedIdx := -1
	endIdx := -1
	for i, s := range order {
		switch s {
		case "env_cleanup":
			cleanupIdx = i
		case "events_channel_closed":
			closedIdx = i
		case "session_end_emitted":
			endIdx = i
		}
	}

	assert.True(t, sessionEndSeen, "SESSION_END must be emitted")
	if cleanupIdx >= 0 && endIdx >= 0 {
		assert.Less(t, endIdx, cleanupIdx, "SESSION_END must be emitted before env.Cleanup()")
	}
	if closedIdx >= 0 && endIdx >= 0 {
		assert.Less(t, endIdx, closedIdx, "SESSION_END must be emitted before events channel closed")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSession_Close_ShutdownOrdering -v -count=1`
Expected: FAIL — ordering wrong

**Step 3: Implement the fix**

Rewrite `Close()` in `session.go`:

```go
func (s *Session) Close() {
	s.mu.Lock()
	if s.state == SessionClosed {
		s.mu.Unlock()
		return
	}

	// Collect subagents under lock.
	subs := make([]*subagent, 0, len(s.subagents))
	for id, sub := range s.subagents {
		subs = append(subs, sub)
		delete(s.subagents, id)
	}
	turns := s.turns
	emitEnd := !s.sessionEndEmitted
	s.sessionEndEmitted = true
	s.mu.Unlock()

	// Step 1: Cancel in-flight LLM calls.
	if s.cancelFunc != nil {
		s.cancelFunc()
	}

	// Step 2: Kill running child processes (via env.Cleanup()).
	s.env.Cleanup()

	// Step 3+4: Emit SESSION_END (events flush through the buffered channel).
	if emitEnd {
		s.emit(EventSessionEnd, map[string]any{
			"reason": "session_closed",
			"state":  string(SessionClosed),
			"turns":  turns,
		})
	}

	// Step 5: Close subagent sessions.
	for _, sub := range subs {
		sub.sess.Close()
	}

	if s.mcpMgr != nil {
		s.mcpMgr.Close()
	}

	// Step 6: Transition to CLOSED and close events channel.
	s.mu.Lock()
	s.state = SessionClosed
	s.mu.Unlock()
	close(s.events)
}
```

Key changes:
- State transitions to CLOSED **last**, not first
- `env.Cleanup()` (kills processes) runs **before** SESSION_END
- `close(s.events)` runs **after** SESSION_END emission

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestSession_Close_ShutdownOrdering -v -count=1`
Expected: PASS

**Step 5: Run full agent test suite to check for regressions**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS. The Close() reordering may affect tests that check state after Close(). Fix any broken tests — the new ordering is correct per spec.

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: reorder Close() to match spec Appendix B shutdown sequence"
```

---

## Task 2: Round and Turn Limit Behavior (GAP-2.16, GAP-2.17, GAP-2.04)

**Priority:** P0 (MISSING items)

Three related issues in the main loop:
1. **GAP-2.16:** Round limit (`max_tool_rounds_per_input`) returns error instead of breaking cleanly. The post-loop code at `session.go:996-1000` returns `fmt.Errorf("max tool rounds reached")`. Spec says: break from loop and proceed to follow-up/SESSION_END processing.
2. **GAP-2.17:** Turn limit (`max_turns`) uses `>` instead of `>=` at `session.go:729`, allowing one extra turn. Also returns error instead of breaking.
3. **GAP-2.04:** Turn limit is checked outside the main for loop (before it), so it doesn't integrate with the loop's natural exit flow.

**Files:**
- Modify: `internal/agent/session.go:729-735` (turn limit) and `996-1000` (round limit)
- Test: `internal/agent/session_test.go` or `session_dod_test.go`

**Step 1: Write the failing tests**

```go
func TestSession_RoundLimit_BreaksCleanlyAndReturnsText(t *testing.T) {
	// Set MaxToolRoundsPerInput=1. After 1 round of tool calls, the session
	// should break and return any assistant text, not an error.
	ctrl := newMockLLMCtrl()
	callCount := 0
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		callCount++
		if callCount == 1 {
			return llm.Response{
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi"}`),
					}},
				}},
				Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
				Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			}, nil
		}
		// Second call after tool result: still returns tool call. This would
		// be round 2, which exceeds the limit of 1.
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Could not complete"},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c2", Name: "shell", Arguments: json.RawMessage(`{"command":"echo more"}`),
				}},
			}},
			Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
			Usage:  llm.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
		}, nil
	}

	sess := newTestSession(t, ctrl, SessionConfig{
		MaxToolRoundsPerInput: 1,
		MaxTurns:              10,
	})

	out, err := sess.ProcessInput(context.Background(), "do something")
	// Should NOT return an error — just break cleanly.
	assert.NoError(t, err, "round limit should break cleanly, not return error")
}

func TestSession_TurnLimit_UsesGreaterThanOrEqual(t *testing.T) {
	// With MaxTurns=2, exactly 2 inputs should succeed, the 3rd should be rejected.
	ctrl := newMockLLMCtrl()
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{
			Message: llm.Assistant("done"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}

	sess := newTestSession(t, ctrl, SessionConfig{MaxTurns: 2})

	// Turn 1
	_, err := sess.ProcessInput(context.Background(), "first")
	require.NoError(t, err)

	// Turn 2
	_, err = sess.ProcessInput(context.Background(), "second")
	require.NoError(t, err)

	// Turn 3 should hit limit (turns >= MaxTurns, i.e. 2 >= 2)
	_, err = sess.ProcessInput(context.Background(), "third")
	assert.Error(t, err, "turn 3 should fail when MaxTurns=2")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run "TestSession_(RoundLimit_Breaks|TurnLimit_Uses)" -v -count=1`
Expected: First test FAIL (error returned instead of nil). Second test may FAIL if turn 3 is allowed.

**Step 3: Implement the fix**

In `session.go`:

A) Fix turn limit check at line 729 — change `>` to `>=`:
```go
if s.cfg.MaxTurns > 0 && turns >= s.cfg.MaxTurns {
```

B) Fix round limit at the post-loop code (line 996-1000) — break and return empty string instead of error:
```go
	// End of for loop
	}

	// Round limit reached: emit event and break cleanly (spec 2.4).
	s.emit(EventTurnLimit, map[string]any{"max_tool_rounds_per_input": s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.state = SessionIdle
	s.mu.Unlock()
	return "", nil
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run "TestSession_(RoundLimit_Breaks|TurnLimit_Uses)" -v -count=1`
Expected: PASS

**Step 5: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS. Check for tests that assert `err != nil` on round/turn limits — those will need updating to match the new behavior.

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: round/turn limits break cleanly instead of returning errors (GAP-2.16/2.17)"
```

---

## Task 3: TOOL_CALL_END Event Data Keys (GAP-2.08, GAP-2.11, GAP-2.12)

**Priority:** P1

The `TOOL_CALL_END` event at `session.go:556-561` uses `full_output` for both success and error cases. The spec says:
- Success: `{"output": "...", "tool_name": "...", "call_id": "..."}`
- Error: `{"error": "...", "tool_name": "...", "call_id": "..."}`
- Unknown tool: `{"error": "unknown tool: ...", ...}`

**Files:**
- Modify: `internal/agent/session.go:556-561`
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_ToolCallEnd_EventDataKeys(t *testing.T) {
	ctrl := newMockLLMCtrl()
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi"}`),
				}},
			}},
			Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
			Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}, nil
	}

	sess := newTestSession(t, ctrl, SessionConfig{MaxToolRoundsPerInput: 1})
	var endEvents []SessionEvent
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventToolCallEnd {
				endEvents = append(endEvents, ev)
			}
		}
	}()

	sess.ProcessInput(context.Background(), "run echo")
	time.Sleep(50 * time.Millisecond)

	require.NotEmpty(t, endEvents)
	ev := endEvents[0]
	// Spec says "output" key, not "full_output".
	_, hasOutput := ev.Data["output"]
	_, hasFullOutput := ev.Data["full_output"]
	assert.True(t, hasOutput, "TOOL_CALL_END should have 'output' key")
	assert.False(t, hasFullOutput, "TOOL_CALL_END should NOT have 'full_output' key")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSession_ToolCallEnd_EventDataKeys -v -count=1`
Expected: FAIL — has `full_output`, not `output`

**Step 3: Implement the fix**

In `session.go` around line 556-561, change:

```go
s.emit(EventToolCallEnd, map[string]any{
	"tool_name":   res.ToolName,
	"call_id":     res.CallID,
	"is_error":    res.IsError,
	"full_output": res.FullOutput,
})
```

To:

```go
data := map[string]any{
	"tool_name": res.ToolName,
	"call_id":   res.CallID,
}
if res.IsError {
	data["error"] = res.FullOutput
} else {
	data["output"] = res.FullOutput
}
s.emit(EventToolCallEnd, data)
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestSession_ToolCallEnd_EventDataKeys -v -count=1`
Expected: PASS

**Step 5: Check for any tests referencing `full_output` or `is_error` in TOOL_CALL_END events and update them.**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: TOOL_CALL_END event uses 'output'/'error' keys per spec (GAP-2.08/2.11/2.12)"
```

---

## Task 4: ASSISTANT_TEXT_START Event Data (GAP-2.19)

**Priority:** P1

`session.go:881` emits `EventAssistantTextStart` with an empty map: `s.emit(EventAssistantTextStart, map[string]any{})`. The spec says this event should carry identifying information.

**Files:**
- Modify: `internal/agent/session.go:881`
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_AssistantTextStart_HasData(t *testing.T) {
	ctrl := newMockLLMCtrl()
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{
			Message: llm.Assistant("hello"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
			Model:   "test-model",
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}

	sess := newTestSession(t, ctrl, defaultTestConfig())
	var startEvent *SessionEvent
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventAssistantTextStart {
				cp := ev
				startEvent = &cp
			}
		}
	}()

	sess.ProcessInput(context.Background(), "hi")
	time.Sleep(50 * time.Millisecond)

	require.NotNil(t, startEvent)
	assert.NotEmpty(t, startEvent.Data, "ASSISTANT_TEXT_START should have non-empty data")
	_, hasModel := startEvent.Data["model"]
	assert.True(t, hasModel, "ASSISTANT_TEXT_START should include model")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSession_AssistantTextStart_HasData -v -count=1`
Expected: FAIL — Data is empty

**Step 3: Implement the fix**

In `session.go`, change:
```go
s.emit(EventAssistantTextStart, map[string]any{})
```

To:
```go
s.emit(EventAssistantTextStart, map[string]any{
	"model": resp.Model,
})
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestSession_AssistantTextStart_HasData -v -count=1`
Expected: PASS

**Step 5: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: ASSISTANT_TEXT_START event includes model info (GAP-2.19)"
```

---

## Task 5: pause_turn Round Counter (GAP-2.05)

**Priority:** P1

At `session.go:898-901`, when the model returns `pause_turn` (web search in progress), the code `continue`s the for loop, which increments the round counter. The spec says only actual tool execution rounds should count.

**Files:**
- Modify: `internal/agent/session.go:898-901`
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_PauseTurn_DoesNotIncrementRoundCounter(t *testing.T) {
	ctrl := newMockLLMCtrl()
	callCount := 0
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		callCount++
		if callCount <= 3 {
			// Return pause_turn 3 times.
			return llm.Response{
				Message: llm.Assistant("searching..."),
				Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn},
				Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
			}, nil
		}
		return llm.Response{
			Message: llm.Assistant("found it"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}

	// MaxToolRoundsPerInput=2 — should still work because pause_turn doesn't count.
	sess := newTestSession(t, ctrl, SessionConfig{
		MaxToolRoundsPerInput: 2,
		MaxTurns:              10,
	})
	go func() { for range sess.Events() {} }()

	out, err := sess.ProcessInput(context.Background(), "search")
	assert.NoError(t, err, "pause_turn should not count toward round limit")
	assert.Equal(t, "found it", out)
	assert.Equal(t, 4, callCount, "should have made 4 LLM calls (3 pause + 1 stop)")
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSession_PauseTurn_DoesNotIncrementRoundCounter -v -count=1`
Expected: FAIL — hits round limit after 2 pause_turns

**Step 3: Implement the fix**

The fix is to not let `pause_turn` consume a round iteration. Change the `for` loop structure so that `pause_turn` doesn't increment the round counter. Before the `continue` at line 900, decrement the round:

```go
if resp.Finish.Reason == llm.FinishReasonPauseTurn {
	round-- // Don't count pause_turn as a tool round.
	continue
}
```

Or better: restructure to only increment on actual tool execution. Change the for loop from `for round := 0; round < max; round++` to a `for` with manual increment that only fires when tools are actually executed.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run TestSession_PauseTurn_DoesNotIncrementRoundCounter -v -count=1`
Expected: PASS

**Step 5: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: pause_turn does not increment round counter (GAP-2.05)"
```

---

## Task 6: Loop Detection Warning Wording (GAP-2.13)

**Priority:** P2

The loop detection warning at `session.go:970` says:
`"Loop detected: repeating pattern in the last %d tool calls. Try a different approach."`

The spec says:
`"Warning: Loop detected — the same tool call pattern has repeated N times. Consider changing approach."`

**Files:**
- Modify: `internal/agent/session.go:970`
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_LoopDetection_WarningWording(t *testing.T) {
	// ... set up session with loop detection enabled and force a repeating pattern ...
	// Assert the warning message matches the spec wording.
	var warningMsg string
	// ... collect EventLoopDetection event, check Data["message"] ...
	assert.Contains(t, warningMsg, "Warning: Loop detected")
	assert.Contains(t, warningMsg, "Consider changing approach")
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — current message uses "Try a different approach"

**Step 3: Implement the fix**

Change the warning string to match spec:
```go
warning := fmt.Sprintf("Warning: Loop detected — the same tool call pattern has repeated %d times. Consider changing approach.", s.cfg.LoopDetectionWindow)
```

Note: Use `%d` for the window size. Check the spec for the exact field — it may say "N times" where N is the number of repetitions, not the window size. Adjust accordingly.

**Step 4: Run test to verify it passes**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: loop detection warning wording matches spec (GAP-2.13)"
```

---

## Task 7: System Prompt Layer Ordering (GAP-6.01)

**Priority:** P1

In `session.go:760-777`, MCP tool descriptions and custom tool descriptions are appended AFTER project docs. The spec's 5-layer ordering puts tool descriptions (layer 3) before project-specific instructions (layer 4).

**Files:**
- Modify: `internal/agent/session.go:754-781`
- Modify: `internal/agent/profile.go` (`BuildSystemPrompt` method)
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing test**

```go
func TestSession_SystemPrompt_ToolDescriptionsBeforeProjectDocs(t *testing.T) {
	// Create a session with MCP tools and project docs.
	// Assert that MCP tool descriptions appear BEFORE project doc content in the system prompt.
	ctrl := newMockLLMCtrl()
	var capturedSysPrompt string
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		capturedSysPrompt = req.SystemPrompt
		return llm.Response{
			Message: llm.Assistant("done"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}

	sess := newTestSession(t, ctrl, defaultTestConfig())
	// Register a custom tool so it appears in the prompt.
	sess.RegisterTool("custom_test_tool", "A custom tool for testing", nil, func(ctx context.Context, args any) (any, error) {
		return nil, nil
	})
	go func() { for range sess.Events() {} }()

	sess.ProcessInput(context.Background(), "test")

	// Check ordering: "Additional tools:" must appear before "BEGIN" project doc marker.
	toolsIdx := strings.Index(capturedSysPrompt, "Additional tools:")
	docsIdx := strings.Index(capturedSysPrompt, "--- BEGIN")
	if toolsIdx >= 0 && docsIdx >= 0 {
		assert.Less(t, toolsIdx, docsIdx, "tool descriptions must appear before project docs")
	}
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — tools appear after project docs

**Step 3: Implement the fix**

Refactor the system prompt assembly in `session.go:processOneInput` to insert MCP and custom tool descriptions into the prompt BEFORE `BuildSystemPrompt` appends project docs. Options:

A) Pass MCP/custom tool descriptions into `BuildSystemPrompt` so it can place them at layer 3.
B) Have `BuildSystemPrompt` return the prompt split at the layer 3/4 boundary, so `processOneInput` can insert between them.

Option A is cleaner. Add a parameter to `BuildSystemPrompt`:

```go
func (p *baseProfile) BuildSystemPrompt(env EnvironmentInfo, docs string, skills []SkillMeta, extraTools string) string {
```

Then in `processOneInput`, build the extra tools string first:
```go
var extraTools strings.Builder
if len(s.mcpTools) > 0 {
	extraTools.WriteString("\nMCP Tools (from external servers):\n")
	for _, td := range s.mcpTools {
		// ...
	}
}
if extra := s.customToolDescriptions(); len(extra) > 0 {
	extraTools.WriteString("\nAdditional tools:\n")
	extraTools.WriteString(extra)
}
sys := s.profile.BuildSystemPrompt(s.envInfo, docs, skillList, extraTools.String())
```

And in `BuildSystemPrompt`, insert `extraTools` between tool definitions (layer 3) and project docs (layer 4).

**Step 4: Run test to verify it passes**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/profile.go internal/agent/session_test.go internal/agent/profile_test.go
git commit -m "fix: MCP/custom tool descriptions placed before project docs in system prompt (GAP-6.01)"
```

---

## Task 8: OSVersion Implementation (GAP-6.02)

**Priority:** P2

`env_local.go:93` returns `runtime.GOOS + "/" + runtime.GOARCH` (e.g., `darwin/arm64`) instead of an actual OS version string (e.g., `Darwin 25.2.0`).

**Files:**
- Modify: `internal/agent/env_local.go:93`
- Test: `internal/agent/env_local_test.go`

**Step 1: Write the failing test**

```go
func TestLocalExecutionEnvironment_OSVersion_ReturnsActualVersion(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	version := env.OSVersion()
	// Should NOT be just "darwin/arm64" or "linux/amd64".
	assert.NotEqual(t, runtime.GOOS+"/"+runtime.GOARCH, version,
		"OSVersion should return actual version, not GOOS/GOARCH")
	// Should contain something that looks like a version number.
	assert.Regexp(t, `\d+\.\d+`, version, "OSVersion should contain a version number")
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — returns `darwin/arm64`

**Step 3: Implement the fix**

```go
func (e *LocalExecutionEnvironment) OSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("uname", "-rs").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "linux":
		out, err := exec.Command("uname", "-rs").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	case "windows":
		out, err := exec.Command("cmd", "/c", "ver").Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH // fallback
}
```

**Step 4: Run test to verify it passes**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/env_local.go internal/agent/env_local_test.go
git commit -m "fix: OSVersion() returns actual OS version string (GAP-6.02)"
```

---

## Task 9: Context Window Sizes (GAP-3.02, GAP-3.08)

**Priority:** P2

- Anthropic `contextWindow` at `profile.go:244` is `200_000`. Models like Claude 4 support 1M tokens with the `prompt-caching` beta header (which the profile already sends).
- Gemini `contextWindow` at `profile.go:280` is `128_000`. Gemini 2.5 models support 1M tokens.

**Files:**
- Modify: `internal/agent/profile.go:244,280`
- Test: `internal/agent/profile_test.go`

**Step 1: Write the failing test**

```go
func TestAnthropicProfile_ContextWindow_Is1M(t *testing.T) {
	p := NewAnthropicProfile("claude-sonnet-4-5-20250929")
	assert.Equal(t, 1_000_000, p.ContextWindowSize())
}

func TestGeminiProfile_ContextWindow_Is1M(t *testing.T) {
	p := NewGeminiProfile("gemini-2.5-flash")
	assert.Equal(t, 1_000_000, p.ContextWindowSize())
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — returns 200000 and 128000

**Step 3: Implement the fix**

In `profile.go`:
- Line 244: change `contextWindow: 200_000` to `contextWindow: 1_000_000`
- Line 280 (Gemini): change `contextWindow: 128_000` to `contextWindow: 1_000_000`

**Step 4: Run tests to verify they pass**

**Step 5: Run full suite — check for tests asserting old values**

**Step 6: Commit**

```bash
git add internal/agent/profile.go internal/agent/profile_test.go
git commit -m "fix: update Anthropic and Gemini context window sizes to 1M (GAP-3.02/3.08)"
```

---

## Task 10: UTF-8 Aware Truncation (GAP-5.01)

**Priority:** P2

`truncateChars` in `tool_registry.go:197-214` uses `len(s)` (byte count) instead of `utf8.RuneCountInString(s)` (character count). This means multi-byte characters (emoji, CJK, etc.) are counted as 2-4 characters instead of 1, and truncation can split a multi-byte character.

**Files:**
- Modify: `internal/agent/tool_registry.go:197-214`
- Test: `internal/agent/tool_registry_test.go`

**Step 1: Write the failing test**

```go
func TestTruncateChars_UTF8Aware(t *testing.T) {
	// 5 emoji characters, each 4 bytes in UTF-8: 20 bytes but 5 runes.
	input := "😀😁😂🤣😃"
	assert.Equal(t, 5, utf8.RuneCountInString(input))
	assert.Equal(t, 20, len(input))

	// Truncate to 3 characters (runes), not 3 bytes.
	result := truncateChars(input, 3, TruncHeadTail)
	// Should contain valid UTF-8 (no broken characters).
	assert.True(t, utf8.ValidString(result), "truncated result must be valid UTF-8")
	// The non-marker portion should contain at most 3 runes of original content.
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — `len(s)` treats 20 bytes as <=3, no truncation needed (wrong) or truncation splits a character

**Step 3: Implement the fix**

Replace `len(s)` with `utf8.RuneCountInString(s)` throughout `truncateChars`. Use `[]rune` conversion for slicing:

```go
func truncateChars(s string, max int, strat TruncationStrategy) string {
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	removed := len(runes) - max
	switch strat {
	case TruncTail:
		marker := fmt.Sprintf("[WARNING: Tool output was truncated. First %d characters were removed. The full output is available in the event stream.]\n\n", removed)
		return marker + string(runes[len(runes)-max:])
	default:
		headCount := max / 2
		tailCount := max - headCount
		marker := fmt.Sprintf("\n\n[WARNING: Tool output was truncated. %d characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]\n\n", removed)
		return string(runes[:headCount]) + marker + string(runes[len(runes)-tailCount:])
	}
}
```

**Step 4: Run test to verify it passes**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/tool_registry.go internal/agent/tool_registry_test.go
git commit -m "fix: truncateChars uses rune count instead of byte count (GAP-5.01)"
```

---

## Task 11: Unicode Punctuation Equivalence in apply_patch (GAP-8.13)

**Priority:** P2

`apply_patch.go:300-323` has whitespace normalization for fuzzy matching but no Unicode punctuation equivalence. Spec Appendix A mentions models may produce typographically different quotes/dashes.

**Files:**
- Modify: `internal/agent/apply_patch.go:300-323`
- Test: `internal/agent/apply_patch_test.go`

**Step 1: Write the failing test**

```go
func TestApplyPatch_FuzzyMatch_UnicodeQuotes(t *testing.T) {
	// File uses straight quotes, patch uses curly quotes.
	original := `fmt.Println("hello world")`
	patch := "--- a/test.go\n+++ b/test.go\n@@ -1,1 +1,1 @@\n-fmt.Println(\u201Chello world\u201D)\n+fmt.Println(\u201Cgoodbye world\u201D)\n"
	result, err := applyPatch(original, patch, "test.go")
	require.NoError(t, err)
	assert.Contains(t, result, "goodbye world")
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — curly quotes don't match straight quotes

**Step 3: Implement the fix**

Add a `normalizeUnicode` function that maps typographic variants to ASCII:

```go
var unicodePunctReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", // left/right single quote
	"\u201C", "\"", "\u201D", "\"", // left/right double quote
	"\u2013", "-", "\u2014", "-", // en-dash, em-dash
	"\u2026", "...", // ellipsis
	"\u00A0", " ", // non-breaking space
)

func normalizeUnicode(s string) string {
	return unicodePunctReplacer.Replace(s)
}
```

In `indexOfLine`, add a third matching attempt after whitespace normalization:
```go
// Third attempt: Unicode punctuation equivalence.
normalizedTarget := normalizeUnicode(target)
for i, line := range lines {
	if normalizeUnicode(line) == normalizedTarget {
		return i
	}
}
```

**Step 4: Run test to verify it passes**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/apply_patch.go internal/agent/apply_patch_test.go
git commit -m "feat: add Unicode punctuation equivalence to apply_patch fuzzy matching (GAP-8.13)"
```

---

## Task 12: ToolMiddleware Interface (GAP-8.05)

**Priority:** P2

The spec defines a `ToolMiddleware` interface for approval/permission gates between VALIDATE and EXECUTE in the tool execution pipeline. Currently there's no hook point.

**Files:**
- Modify: `internal/agent/tool_registry.go` (add interface + hook in ExecuteCall)
- Test: `internal/agent/tool_registry_test.go`

**Step 1: Write the failing test**

```go
func TestToolRegistry_Middleware_CalledBeforeExecution(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("test_tool", llm.ToolDefinition{
		Name:       "test_tool",
		Parameters: map[string]any{"type": "object"},
	}, func(ctx context.Context, args any) (any, error) {
		return "executed", nil
	})

	var middlewareCalled bool
	reg.Use(func(ctx context.Context, name string, args any) error {
		middlewareCalled = true
		return nil
	})

	result := reg.ExecuteCall(context.Background(), "test_tool", json.RawMessage(`{}`))
	assert.True(t, middlewareCalled, "middleware must be called before execution")
	assert.False(t, result.IsError)
}

func TestToolRegistry_Middleware_CanBlockExecution(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("test_tool", llm.ToolDefinition{
		Name:       "test_tool",
		Parameters: map[string]any{"type": "object"},
	}, func(ctx context.Context, args any) (any, error) {
		return "executed", nil
	})

	reg.Use(func(ctx context.Context, name string, args any) error {
		return fmt.Errorf("permission denied: tool blocked by policy")
	})

	result := reg.ExecuteCall(context.Background(), "test_tool", json.RawMessage(`{}`))
	assert.True(t, result.IsError, "middleware rejection should produce error result")
	assert.Contains(t, result.Output, "permission denied")
}
```

**Step 2: Run test to verify it fails**

Expected: FAIL — `Use` method doesn't exist

**Step 3: Implement the fix**

Add to `tool_registry.go`:

```go
// ToolMiddleware is called after argument validation but before tool execution.
// Return a non-nil error to block execution (the error message is returned to the LLM).
type ToolMiddleware func(ctx context.Context, toolName string, args any) error

func (r *ToolRegistry) Use(mw ToolMiddleware) {
	r.middleware = append(r.middleware, mw)
}
```

Add a `middleware []ToolMiddleware` field to `ToolRegistry`.

In `ExecuteCall`, after schema validation (line ~163) and before calling `tool.Exec` (line ~167), run middleware:

```go
for _, mw := range r.middleware {
	if err := mw(ctx, name, parsed); err != nil {
		return ToolExecResult{
			ToolName: name,
			CallID:   callID,
			Output:   err.Error(),
			IsError:  true,
		}
	}
}
```

**Step 4: Run tests to verify they pass**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/tool_registry.go internal/agent/tool_registry_test.go
git commit -m "feat: add ToolMiddleware interface for approval gates (GAP-8.05)"
```

---

## Task 13: PermissionDenied Error Type (GAP-8.16)

**Priority:** P2

The spec defines a `PermissionDenied` error type for when tool execution is rejected by middleware or policy. Currently OS-level permission errors propagate as generic errors.

**Files:**
- Create: `internal/agent/errors.go`
- Test: `internal/agent/errors_test.go`

**Step 1: Write the failing test**

```go
func TestPermissionDeniedError(t *testing.T) {
	err := &PermissionDeniedError{
		Tool:    "shell",
		Message: "command not allowed by policy",
	}
	assert.Equal(t, "permission denied for tool shell: command not allowed by policy", err.Error())
	assert.ErrorIs(t, err, ErrPermissionDenied)
}
```

**Step 2: Implement**

```go
package agent

import "errors"

var ErrPermissionDenied = errors.New("permission denied")

type PermissionDeniedError struct {
	Tool    string
	Message string
}

func (e *PermissionDeniedError) Error() string {
	return "permission denied for tool " + e.Tool + ": " + e.Message
}

func (e *PermissionDeniedError) Is(target error) bool {
	return target == ErrPermissionDenied
}
```

**Step 3: Run tests**

**Step 4: Wire into ToolMiddleware** — update Task 12's middleware rejection to return `*PermissionDeniedError`.

**Step 5: Commit**

```bash
git add internal/agent/errors.go internal/agent/errors_test.go
git commit -m "feat: add PermissionDeniedError type (GAP-8.16)"
```

---

## Task 14: Session Runtime Mutability (GAP-1.02, GAP-1.06)

**Priority:** P2

The spec says `SetModel()`, `SetTimeout()`, and `RegisterTool()` should be available at runtime. Currently only `SetReasoningEffort()` is mutable.

**Files:**
- Modify: `internal/agent/session.go` (add methods)
- Test: `internal/agent/session_test.go`

**Step 1: Write the failing tests**

```go
func TestSession_SetModel_TakesEffectOnNextCall(t *testing.T) {
	ctrl := newMockLLMCtrl()
	var capturedModel string
	ctrl.onComplete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		capturedModel = req.Model
		return llm.Response{
			Message: llm.Assistant("ok"),
			Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		}, nil
	}

	sess := newTestSession(t, ctrl, defaultTestConfig())
	go func() { for range sess.Events() {} }()

	sess.ProcessInput(context.Background(), "first")
	assert.Equal(t, "test-model", capturedModel)

	sess.SetModel("new-model")
	sess.ProcessInput(context.Background(), "second")
	assert.Equal(t, "new-model", capturedModel)
}

func TestSession_SetTimeout_TakesEffectOnNextCall(t *testing.T) {
	sess := newTestSession(t, newMockLLMCtrl(), defaultTestConfig())
	sess.SetTimeout(30000)
	// Verify the timeout was stored.
	assert.Equal(t, 30000, sess.cfg.DefaultCommandTimeoutMS)
}
```

**Step 2: Run tests — compile error because methods don't exist**

**Step 3: Implement**

```go
func (s *Session) SetModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = s.profile.WithModel(model)
}

func (s *Session) SetTimeout(timeoutMS int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DefaultCommandTimeoutMS = timeoutMS
}

func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) {
	s.reg.Register(name, llm.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  params,
	}, fn)
}
```

**Step 4: Run tests**

**Step 5: Run full suite**

**Step 6: Commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "feat: add SetModel, SetTimeout, RegisterTool for runtime mutability (GAP-1.02/1.06)"
```

---

## Task 15: AWAITING_INPUT Detection Improvement (GAP-2.03)

**Priority:** P2

`session.go:921` uses `strings.HasSuffix(trimmed, "?")` which is a crude heuristic. The spec implies a more sophisticated detection. However, this is inherently heuristic work. A practical improvement is to also check for common question patterns.

**Files:**
- Modify: `internal/agent/session.go:918-928`
- Test: `internal/agent/session_test.go`

**Step 1: Write test cases for common question patterns**

```go
func TestSession_AwaitingInput_DetectsQuestionPatterns(t *testing.T) {
	tests := []struct {
		text     string
		expected SessionState
	}{
		{"What file should I edit?", SessionAwaitingInput},
		{"Done.", SessionIdle},
		{"Please provide the API key:", SessionAwaitingInput},
		{"Which approach do you prefer?\n", SessionAwaitingInput},
		{"I need more information.", SessionIdle},
	}
	// ... test each case
}
```

**Step 2: Implement improved detection**

Add colon-ending as another question indicator (common in LLM prompts). The detection function:

```go
func looksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if strings.HasSuffix(trimmed, "?") {
		return true
	}
	if strings.HasSuffix(trimmed, ":") {
		return true
	}
	return false
}
```

**Step 3: Run tests, commit**

```bash
git add internal/agent/session.go internal/agent/session_test.go
git commit -m "fix: improve AWAITING_INPUT detection beyond suffix-? heuristic (GAP-2.03)"
```

---

## Task 16: ToolDefinition Description Validation + write_file Restriction (GAP-3.09, GAP-3.13)

**Priority:** P2

Two related issues:
- GAP-3.09: `ToolDefinition.description` is not validated as non-empty during registration.
- GAP-3.13: OpenAI `write_file` is not restricted to new files only (relies on prompt guidance instead of enforcement).

**Files:**
- Modify: `internal/agent/tool_registry.go` (Register — add description validation)
- Modify: `internal/agent/session.go` (write_file handler — add file-exists check)
- Test: both test files

**Step 1: Write failing tests**

```go
func TestToolRegistry_Register_RejectsEmptyDescription(t *testing.T) {
	reg := NewToolRegistry()
	// Should log a warning or reject registration for empty description.
	reg.Register("bad_tool", llm.ToolDefinition{
		Name:        "bad_tool",
		Description: "",
		Parameters:  map[string]any{"type": "object"},
	}, func(ctx context.Context, args any) (any, error) { return nil, nil })

	// Tool should still be registered (soft validation) but description should be defaulted.
	tool := reg.Get("bad_tool")
	assert.NotNil(t, tool)
}
```

For write_file, the restriction is OpenAI-specific. The OpenAI profile's write_file definition should include guidance that it's for new files only, and the execution should check if the file exists:

```go
func TestWriteFile_OpenAI_RejectsExistingFile(t *testing.T) {
	// Create a temp file.
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	os.WriteFile(existing, []byte("content"), 0644)

	env := NewLocalExecutionEnvironment(dir)
	// Call write_file with the existing file path.
	_, err := env.WriteFile(existing, "new content")
	assert.Error(t, err, "write_file should reject existing files for OpenAI profile")
}
```

Actually, `write_file` restriction depends on the profile context. The ExecutionEnvironment shouldn't enforce this — the tool handler in the session should. This needs more thought. For now, add description validation and document the write_file restriction as prompt-based (which is how codex-rs handles it too).

**Step 2-6: Implement description validation, tests, commit**

```bash
git commit -m "fix: validate ToolDefinition description is non-empty (GAP-3.09)"
```

---

## Task 17: Subagent Fixes (GAP-7.05, GAP-7.06, GAP-7.07)

**Priority:** P2

Three subagent issues:

**GAP-7.05:** When parent env is not `*LocalExecutionEnvironment` and `working_dir` is specified, a brand-new `LocalExecutionEnvironment` is created that doesn't share PID tracking.

**GAP-7.06:** Subagent inherits full `SessionConfig` including MCP config, causing duplicate MCP server connections.

**GAP-7.07:** `close_agent` doesn't wait for the running goroutine. Status may be stale.

**Files:**
- Modify: `internal/agent/subagents.go:55-69` (config copy), `160-183` (close_agent wait)
- Test: `internal/agent/session_dod_test.go` or new test file

**Step 1: Write the failing tests**

```go
func TestCloseAgent_WaitsForGoroutine(t *testing.T) {
	// Spawn a subagent that takes a while to complete.
	// Call close_agent. The returned status should be "completed" or "failed",
	// never "running" (which would indicate we didn't wait).
	// ...
}

func TestSubagent_NoMCPInheritance(t *testing.T) {
	// Create a session with MCP config.
	// Spawn a subagent.
	// Verify the subagent's config has empty MCP config.
	// ...
}
```

**Step 2: Implement fixes**

For GAP-7.07, make `close_agent` wait for the goroutine after calling `sub.sess.Close()`:
```go
func (s *Session) closeAgent(agentID string) (any, error) {
	s.mu.Lock()
	sub := s.subagents[agentID]
	delete(s.subagents, agentID)
	s.mu.Unlock()
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}

	sub.sess.Close()

	// Wait for the goroutine to actually finish.
	select {
	case <-sub.done:
	case <-time.After(5 * time.Second):
		// Timeout waiting for goroutine.
	}

	sub.mu.Lock()
	status := sub.status
	result := sub.result
	turnsUsed := sub.turnsUsed
	sub.mu.Unlock()

	b, _ := json.Marshal(map[string]any{
		"status":     string(status),
		"output":     result,
		"turns_used": turnsUsed,
	})
	return string(b), nil
}
```

For GAP-7.06, clear MCP config for subagents:
```go
subCfg := s.cfg
subCfg.MCPConfigFiles = nil
subCfg.MCPInline = nil
```

For GAP-7.05, return an error if parent env doesn't support `WithWorkingDirectory` instead of creating an isolated env:
```go
if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
	type workDirProvider interface {
		WithWorkingDirectory(string) ExecutionEnvironment
	}
	if wdp, ok := s.env.(workDirProvider); ok {
		subEnv = wdp.WithWorkingDirectory(workingDir)
	} else {
		return "", fmt.Errorf("execution environment does not support working_dir override")
	}
}
```

**Step 3: Run tests, commit**

```bash
git add internal/agent/subagents.go internal/agent/session_dod_test.go
git commit -m "fix: subagent close waits for goroutine, no MCP inheritance, safe working_dir (GAP-7.05/7.06/7.07)"
```

---

## Task 18: Windows Shell Support (GAP-4.07)

**Priority:** P3

`env_local.go` hardcodes `/bin/bash -c` for shell execution. Add Windows support with `cmd.exe /c`.

**Files:**
- Modify: `internal/agent/env_local.go` (shell command construction)
- Test: `internal/agent/env_local_test.go`

**Step 1: Write the test**

```go
func TestExecCommand_ShellSelection(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	// Test that the shell is appropriate for the current platform.
	switch runtime.GOOS {
	case "windows":
		// On Windows, should use cmd.exe.
		result, err := env.ExecCommand(context.Background(), "echo hello", 5000)
		require.NoError(t, err)
		assert.Contains(t, result.Stdout, "hello")
	default:
		// On Unix, should use /bin/bash or /bin/sh.
		result, err := env.ExecCommand(context.Background(), "echo hello", 5000)
		require.NoError(t, err)
		assert.Contains(t, result.Stdout, "hello")
	}
}
```

**Step 2: Implement**

In `env_local.go`, replace the hardcoded `/bin/bash -c`:

```go
func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/c", command)
	}
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", command)
}
```

**Step 3: Tests, commit**

```bash
git add internal/agent/env_local.go internal/agent/env_local_test.go
git commit -m "feat: add Windows shell support via cmd.exe (GAP-4.07)"
```

---

## Task 19: Document Intentional Go Idiom Deviations

**Priority:** P3

Several MINOR items are Go language idioms that should not be changed. Document them as intentional deviations from the spec's pseudocode:

- **GAP-4.01:** `EditFile` method on ExecutionEnvironment — useful extension, not in spec
- **GAP-4.02:** `WriteFile` returns string — Go convention for returning status/path info
- **GAP-4.03:** `Initialize` returns error — Go convention for error reporting
- **GAP-4.04:** `Grep` uses inline params — style choice
- **GAP-4.05:** `ExecCommand` has `context.Context` — idiomatic Go
- **GAP-4.08:** `Platform()` never returns "wasm" — WASM not supported
- **GAP-4.09:** `ExecCommand` returns `(ExecResult, error)` — Go convention
- **GAP-4.10:** No wrapper implementations — the interface supports composition
- **GAP-2.06:** Follow-up processing is iterative — functionally equivalent to recursive
- **GAP-2.07:** System prompt rebuilt every round — enhancement over spec
- **GAP-3.03:** `grep` output_mode is global — cross-profile consistency
- **GAP-3.15:** Gemini grounding via WebSearch flag — adequate implementation
- **GAP-4.06:** Shell tool doesn't expose working_dir/env_vars — security decision
- **GAP-3.06:** Method named `ToolDefinitions()` not `tools()` — Go naming convention

**Files:**
- Modify: `coding-agent-loop-spec.md` (add a "Go Implementation Notes" appendix)

**Step 1: Add an appendix to the spec documenting these deviations**

Add a section at the end of `coding-agent-loop-spec.md`:

```markdown
## Appendix C: Go Implementation Notes

The following deviations from this spec's pseudocode are intentional Go language idioms:

- `ExecutionEnvironment.WriteFile` returns `(string, error)` (Go convention for returning status)
- `ExecutionEnvironment.Initialize` returns `error` (Go convention)
- `ExecutionEnvironment.ExecCommand` takes `context.Context` and returns `(ExecResult, error)` (Go idiom)
- `ExecutionEnvironment.EditFile` is an extension not in this spec (used by Anthropic/Gemini profiles)
- `Grep` uses inline parameters instead of a `GrepOptions` struct (style choice)
- Method is named `ToolDefinitions()` rather than `tools()` (Go naming convention)
- Follow-up processing uses iteration instead of recursion (functionally equivalent, avoids stack growth)
- System prompt is rebuilt every tool round (enhancement: picks up newly-created AGENTS.md files)
- `grep` output_mode is available across all profiles, not just Anthropic (consistency)
- Shell tool does not expose working_dir/env_vars parameters to the model (security decision)
- Gemini grounding is enabled via the WebSearch flag rather than provider_options (adequate)
```

**Step 2: Commit**

```bash
git add coding-agent-loop-spec.md
git commit -m "docs: add Go Implementation Notes appendix for intentional deviations (GAP-2.06/2.07/3.03/3.06/3.15/4.01-4.10)"
```

---

## Task 20: Typed Event Payloads + Subagent Lifecycle Events (GAP-1.05)

**Priority:** P2

Two issues: (A) All events use `map[string]any` for data — untyped, error-prone, no IDE support. (B) No SUBAGENT_START or SUBAGENT_END events are emitted during subagent lifecycle.

**Files:**
- Modify: `internal/agent/events.go` (add typed payload structs, new event kinds)
- Modify: `internal/agent/session.go` (all ~25 `s.emit()` calls)
- Modify: `internal/agent/subagents.go` (emit subagent lifecycle events)
- Test: `internal/agent/events_test.go`, `internal/agent/session_test.go`

**Step 1: Write the failing tests**

```go
func TestSessionEvent_TypedData_ToolCallEnd(t *testing.T) {
	ev := SessionEvent{
		Kind: EventToolCallEnd,
		Data: ToolCallEndData{
			ToolName: "shell",
			CallID:   "c1",
			Output:   "hello",
		},
	}
	// Data should be usable as typed struct.
	data, ok := ev.Data.(ToolCallEndData)
	require.True(t, ok)
	assert.Equal(t, "shell", data.ToolName)
}

func TestSessionEvent_DataMap_BackwardCompat(t *testing.T) {
	ev := SessionEvent{
		Kind: EventToolCallEnd,
		Data: ToolCallEndData{
			ToolName: "shell",
			CallID:   "c1",
			Output:   "hello",
		},
	}
	// DataMap() provides backward-compatible map access.
	m := ev.DataMap()
	assert.Equal(t, "shell", m["tool_name"])
	assert.Equal(t, "c1", m["call_id"])
}

func TestSession_SubagentLifecycleEvents(t *testing.T) {
	// Spawn a subagent, wait for it, verify SUBAGENT_START and SUBAGENT_END events.
	ctrl := newMockLLMCtrl()
	// ... setup ...
	var sawStart, sawEnd bool
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventSubagentStart { sawStart = true }
			if ev.Kind == EventSubagentEnd { sawEnd = true }
		}
	}()
	// ... spawn and wait for subagent ...
	assert.True(t, sawStart, "SUBAGENT_START must be emitted")
	assert.True(t, sawEnd, "SUBAGENT_END must be emitted")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run "TestSessionEvent_Typed|TestSession_SubagentLifecycle" -v -count=1`
Expected: FAIL — typed data structs and subagent events don't exist

**Step 3: Define typed payloads in `events.go`**

Change `SessionEvent.Data` from `map[string]any` to `any`:

```go
const (
	// ... existing event kinds ...
	EventSubagentStart EventKind = "SUBAGENT_START"
	EventSubagentEnd   EventKind = "SUBAGENT_END"
)

type SessionEvent struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	Data      any       `json:"data,omitempty"`
}

// DataMap returns Data as map[string]any for backward compatibility.
// If Data is already a map, returns it directly. If it's a typed struct,
// marshals/unmarshals through JSON.
func (e SessionEvent) DataMap() map[string]any {
	if m, ok := e.Data.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(e.Data)
	if err != nil {
		return nil
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

// Typed event payloads.

type SessionStartData struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Profile   string `json:"profile"`
}

type SessionEndData struct {
	Reason string `json:"reason"`
	State  string `json:"state"`
	Turns  int    `json:"turns"`
}

type UserInputData struct {
	Text string `json:"text"`
}

type AssistantTextStartData struct {
	Model string `json:"model"`
}

type AssistantTextDeltaData struct {
	Delta string `json:"delta"`
}

type AssistantTextEndData struct {
	Text         string    `json:"text"`
	Usage        any       `json:"usage"`
	FinishReason string    `json:"finish_reason"`
	Model        string    `json:"model"`
	Reasoning    string    `json:"reasoning,omitempty"`
}

type ToolCallStartData struct {
	ToolName  string `json:"tool_name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments,omitempty"`
}

type ToolCallOutputDeltaData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Delta    string `json:"delta"`
}

type ToolCallEndData struct {
	ToolName string `json:"tool_name"`
	CallID   string `json:"call_id"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

type SteeringInjectedData struct {
	Text string `json:"text"`
}

type TurnLimitData struct {
	MaxTurns             int `json:"max_turns,omitempty"`
	MaxToolRoundsPerInput int `json:"max_tool_rounds_per_input,omitempty"`
}

type LoopDetectionData struct {
	Message string `json:"message"`
}

type ErrorData struct {
	Error string `json:"error"`
}

type WarningData struct {
	Message string `json:"message"`
}

type CommunicateData struct {
	Text string `json:"text"`
}

type SkillActivatedData struct {
	Name string `json:"name"`
}

type SubagentStartData struct {
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
}

type SubagentEndData struct {
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turns_used"`
}
```

**Step 4: Update all `s.emit()` calls in `session.go`**

There are ~25 emit calls. Change each from `map[string]any{...}` to the corresponding typed struct. For example:

```go
// Before:
s.emit(EventToolCallEnd, map[string]any{
	"tool_name": res.ToolName,
	"call_id":   res.CallID,
	"output":    res.FullOutput,
})

// After:
data := ToolCallEndData{
	ToolName: res.ToolName,
	CallID:   res.CallID,
}
if res.IsError {
	data.Error = res.FullOutput
} else {
	data.Output = res.FullOutput
}
s.emit(EventToolCallEnd, data)
```

Update the `emit` helper signature to accept `any` instead of `map[string]any`.

**Step 5: Add subagent lifecycle events in `subagents.go`**

In `spawnAgent`, after successfully starting the subagent:
```go
s.emit(EventSubagentStart, SubagentStartData{
	AgentID: sub.id,
	Task:    task,
})
```

In `sub.run()` completion (or in `waitAgent`/`closeAgent` when status is terminal):
```go
s.emit(EventSubagentEnd, SubagentEndData{
	AgentID:   sub.id,
	Status:    string(sub.status),
	TurnsUsed: sub.turnsUsed,
})
```

**Step 6: Update all test assertions**

Tests that check `ev.Data["key"]` need updating to either type-assert to the specific struct or use `ev.DataMap()["key"]`. Prefer typed assertions in new tests:
```go
data := ev.Data.(ToolCallEndData)
assert.Equal(t, "shell", data.ToolName)
```

**Step 7: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS

**Step 8: Commit**

```bash
git add internal/agent/events.go internal/agent/session.go internal/agent/subagents.go
git add internal/agent/events_test.go internal/agent/session_test.go internal/agent/session_dod_test.go
git commit -m "feat: typed event payloads and SUBAGENT_START/END events (GAP-1.05)"
```

---

## Task 21: SDK Type Alignment (GAP-1.09)

**Priority:** P2

The audit flagged that `RegisteredTool` and `SessionEvent` don't align with `llm.Tool` and `llm.StreamEvent`. The goal is to make the agent layer's types embed or bridge the SDK types so there's a clear type relationship.

**Current state:**
- `RegisteredTool` has `Definition llm.ToolDefinition` + `Exec func(...)` + `Schema` + `Limit` — it already *contains* `llm.ToolDefinition` but doesn't embed `llm.Tool`
- `SessionEvent` has session-specific event kinds (COMMUNICATE, SKILL_ACTIVATED, etc.) that don't exist in `llm.StreamEvent`

**Approach:**
A) Make `RegisteredTool` embed `llm.Tool` — the `llm.Tool` already has `Definition` + `Execute`, which maps naturally. Add agent-specific fields (Schema, Limit) alongside.
B) Add a `ToStreamEvent()` method on `SessionEvent` that converts to `llm.StreamEvent` where mappings exist (TEXT_DELTA → TextDelta, TOOL_CALL_START → ToolCallStart, etc.), returning nil for agent-only events.

**Files:**
- Modify: `internal/agent/tool_registry.go` (RegisteredTool struct)
- Modify: `internal/agent/events.go` (conversion method)
- Modify: `internal/agent/session.go` (adapt to new RegisteredTool shape)
- Test: `internal/agent/tool_registry_test.go`, `internal/agent/events_test.go`

**Step 1: Write the failing tests**

```go
func TestRegisteredTool_EmbedsLLMTool(t *testing.T) {
	tool := RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        "test",
				Description: "A test tool",
				Parameters:  map[string]any{"type": "object"},
			},
			Execute: func(ctx context.Context, args json.RawMessage) (any, error) {
				return "ok", nil
			},
		},
	}
	// Should be able to access Definition through the embedded Tool.
	assert.Equal(t, "test", tool.Definition.Name)
	// Should be able to call Execute through the embedded Tool.
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestSessionEvent_ToStreamEvent_TextDelta(t *testing.T) {
	ev := SessionEvent{
		Kind: EventAssistantTextDelta,
		Data: AssistantTextDeltaData{Delta: "hello"},
	}
	streamEv := ev.ToStreamEvent()
	require.NotNil(t, streamEv)
	assert.Equal(t, llm.StreamEventTextDelta, streamEv.Type)
	assert.Equal(t, "hello", streamEv.Delta)
}

func TestSessionEvent_ToStreamEvent_AgentOnlyEvent_ReturnsNil(t *testing.T) {
	ev := SessionEvent{
		Kind: EventCommunicate,
		Data: CommunicateData{Text: "result"},
	}
	streamEv := ev.ToStreamEvent()
	assert.Nil(t, streamEv, "agent-only events should not map to StreamEvent")
}
```

**Step 2: Run tests to verify they fail**

**Step 3: Implement RegisteredTool embedding**

Change `RegisteredTool` in `tool_registry.go`:

```go
type RegisteredTool struct {
	llm.Tool // embeds Definition + Execute

	// Agent-specific fields.
	Schema *jsonschema.Schema
	Limit  ToolOutputLimit

	// Exec is the agent-layer executor that takes ExecutionEnvironment.
	// This wraps llm.Tool.Execute with environment context.
	Exec func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error)
}
```

Note: `llm.Tool` has `Execute func(ctx, json.RawMessage) (any, error)` while the agent layer's `Exec` has a different signature (takes `ExecutionEnvironment` and `map[string]any`). The `llm.Tool.Execute` field can be set to a wrapper that calls `Exec` with the session's env. The `Register` method should set up this bridge.

Update `Register()`:
```go
func (r *ToolRegistry) Register(t RegisteredTool) error {
	// ... existing validation ...
	// Bridge: set llm.Tool.Execute to wrap the agent-layer Exec.
	// (llm.Tool.Execute is used by the llm.Generate loop for standalone usage)
	if t.Execute == nil && t.Exec != nil {
		t.Execute = func(ctx context.Context, args json.RawMessage) (any, error) {
			var parsed map[string]any
			if err := json.Unmarshal(args, &parsed); err != nil {
				return nil, err
			}
			return t.Exec(ctx, nil, parsed) // nil env for standalone usage
		}
	}
	// ... rest of registration ...
}
```

**Step 4: Implement `ToStreamEvent()` on `SessionEvent`**

```go
func (e SessionEvent) ToStreamEvent() *llm.StreamEvent {
	switch e.Kind {
	case EventAssistantTextStart:
		return &llm.StreamEvent{Type: llm.StreamEventTextStart}
	case EventAssistantTextDelta:
		if d, ok := e.Data.(AssistantTextDeltaData); ok {
			return &llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: d.Delta}
		}
	case EventAssistantTextEnd:
		return &llm.StreamEvent{Type: llm.StreamEventTextEnd}
	case EventToolCallStart:
		if d, ok := e.Data.(ToolCallStartData); ok {
			return &llm.StreamEvent{
				Type: llm.StreamEventToolCallStart,
				ToolCall: &llm.ToolCallData{ID: d.CallID, Name: d.ToolName},
			}
		}
	case EventToolCallEnd:
		if d, ok := e.Data.(ToolCallEndData); ok {
			return &llm.StreamEvent{
				Type: llm.StreamEventToolCallEnd,
				ToolCall: &llm.ToolCallData{ID: d.CallID, Name: d.ToolName},
			}
		}
	case EventSessionStart:
		return &llm.StreamEvent{Type: llm.StreamEventStreamStart}
	case EventSessionEnd:
		return &llm.StreamEvent{Type: llm.StreamEventFinish}
	}
	return nil // Agent-only events don't map to StreamEvent.
}
```

**Step 5: Update all code that accesses `RegisteredTool.Definition` directly**

Since `llm.Tool` embeds `llm.ToolDefinition` as `Definition`, most accesses like `tool.Definition.Name` should still work. Search for `\.Definition\.` and verify each still compiles.

**Step 6: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS

**Step 7: Commit**

```bash
git add internal/agent/tool_registry.go internal/agent/events.go internal/agent/session.go
git add internal/agent/tool_registry_test.go internal/agent/events_test.go
git commit -m "fix: RegisteredTool embeds llm.Tool, SessionEvent bridges to llm.StreamEvent (GAP-1.09)"
```

---

## Task 22: Public API Package (GAP-1.01)

**Priority:** P2

Everything is under `internal/` so external Go modules can't import serf as a library. The fix is to move the public API surface out of `internal/`.

**Approach:** Move `internal/agent/` to `agent/` (top-level package). This is the simplest approach — it makes the entire agent package importable. The `internal/llm/` package should also be moved to `llm/` since it's the SDK layer.

This is a large but mechanical refactor. Every import path changes from `primeradiant.com/serf/internal/agent` to `primeradiant.com/serf/agent` and from `primeradiant.com/serf/internal/llm` to `primeradiant.com/serf/llm`.

**Files:**
- Move: `internal/agent/` → `agent/`
- Move: `internal/llm/` → `llm/`
- Update: every `*.go` file's import paths
- Update: `cmd/serf/main.go` imports
- Update: all test files

**Step 1: Write a test that verifies the package is importable**

Create `agent/importable_test.go`:
```go
package agent_test

import (
	"testing"

	"primeradiant.com/serf/agent"
)

func TestPackageIsImportable(t *testing.T) {
	// This test passing means the package is no longer under internal/.
	var _ agent.ExecutionEnvironment
	var _ agent.ProviderProfile
	var _ agent.SessionEvent
}
```

**Step 2: Run test — fails because package is still internal**

**Step 3: Move directories**

```bash
# Move agent package
git mv internal/agent agent

# Move llm package
git mv internal/llm llm

# Remove internal/ if empty
rmdir internal/ 2>/dev/null || true
```

**Step 4: Fix all import paths**

Use `sed` or `goimports` to update every import:

```bash
# Update all Go files
find . -name '*.go' -exec sed -i '' \
  -e 's|"primeradiant.com/serf/internal/agent"|"primeradiant.com/serf/agent"|g' \
  -e 's|"primeradiant.com/serf/internal/llm"|"primeradiant.com/serf/llm"|g' \
  -e 's|primeradiant.com/serf/internal/agent|primeradiant.com/serf/agent|g' \
  -e 's|primeradiant.com/serf/internal/llm|primeradiant.com/serf/llm|g' \
  {} +
```

Also update any references in:
- `go.mod` (shouldn't need changes — module path stays the same)
- `.serf/prompts/` files if they reference internal paths
- `CLAUDE.md` / `AGENTS.md` if they mention internal paths
- `coding-agent-loop-spec.md` path references

**Step 5: Fix compilation**

Run: `go build ./...`
Fix any remaining import issues.

**Step 6: Run full test suite**

Run: `go test ./... -short -count=1`
Expected: All PASS

**Step 7: Commit**

```bash
git add -A
git commit -m "refactor: move agent/ and llm/ packages out of internal/ for public API (GAP-1.01)"
```

**Note:** This task should be done LAST (after all other tasks) since it changes every import path and would conflict with parallel work.

---

## Task 23: ProviderProfile ToolRegistry Ownership (GAP-3.01)

**Priority:** P2

The spec says each ProviderProfile should own a ToolRegistry pre-populated with its tools. Currently the Session creates a single ToolRegistry and registers all profile tools into it. The profile has no registry of its own.

**Approach:** Add a `NewToolRegistry()` method to ProviderProfile that returns a pre-populated registry. The Session calls this at construction time instead of manually registering each tool. Custom tools are then registered on top of the profile's registry.

**Files:**
- Modify: `internal/agent/profile.go` (add NewToolRegistry method to ProviderProfile interface + baseProfile)
- Modify: `internal/agent/session.go` (use profile's registry instead of building one)
- Test: `internal/agent/profile_test.go`, `internal/agent/session_test.go`

**Step 1: Write the failing tests**

```go
func TestProviderProfile_NewToolRegistry_ContainsProfileTools(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("test"),
		NewAnthropicProfile("test"),
		NewGeminiProfile("test"),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			reg := p.NewToolRegistry()
			require.NotNil(t, reg)

			// All profile tool definitions should be registered.
			for _, td := range p.ToolDefinitions() {
				// Use canonical name (reverse-map provider names).
				name := td.Name
				if nameMap := p.ToolNameMap(); nameMap != nil {
					for canonical, provider := range nameMap {
						if provider == td.Name {
							name = canonical
							break
						}
					}
				}
				tool := reg.Get(name)
				assert.NotNilf(t, tool, "tool %q should be in registry", name)
			}
		})
	}
}

func TestSession_CustomToolOverridesProfileTool(t *testing.T) {
	// Register a custom tool with the same name as a profile tool.
	// The custom one should win.
	ctrl := newMockLLMCtrl()
	// ... setup ...
	sess := newTestSession(t, ctrl, defaultTestConfig())
	var customCalled bool
	sess.RegisterTool("shell", "Custom shell", nil, func(ctx context.Context, args any) (any, error) {
		customCalled = true
		return "custom", nil
	})
	// ... trigger tool call for "shell" ...
	assert.True(t, customCalled)
}
```

**Step 2: Run tests to verify they fail**

Expected: FAIL — `NewToolRegistry()` method doesn't exist on ProviderProfile

**Step 3: Add `NewToolRegistry()` to ProviderProfile interface**

In `profile.go`:

```go
type ProviderProfile interface {
	// ... existing methods ...
	NewToolRegistry() *ToolRegistry
}
```

Implement on `baseProfile`:

```go
func (p *baseProfile) NewToolRegistry() *ToolRegistry {
	reg := NewToolRegistry()
	for _, td := range p.toolDefs {
		// Register each profile tool with a placeholder executor.
		// The Session will wire up the actual executors.
		reg.Register(RegisteredTool{
			Tool: llm.Tool{
				Definition: td,
			},
		})
	}
	return reg
}
```

The actual tool executors (shell, read_file, etc.) are wired up by the Session, not the profile. The profile's registry just pre-registers the definitions and schemas. The Session's `initTools()` then sets the `Exec` functions.

**Step 4: Update Session to use profile's registry**

In `session.go` `NewSession()`, change:

```go
// Before:
s.reg = NewToolRegistry()
s.registerCoreTools()

// After:
s.reg = s.profile.NewToolRegistry()
s.wireToolExecutors() // sets Exec functions on pre-registered tools
```

Add `wireToolExecutors()` that walks the registry and sets the `Exec` function for each known tool name:

```go
func (s *Session) wireToolExecutors() {
	executors := map[string]func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error){
		"read_file":   s.execReadFile,
		"write_file":  s.execWriteFile,
		"edit_file":   s.execEditFile,
		"shell":       s.execShell,
		"grep":        s.execGrep,
		"glob":        s.execGlob,
		"spawn_agent": s.execSpawnAgent,
		"send_input":  s.execSendInput,
		"wait":        s.execWait,
		"close_agent": s.execCloseAgent,
		"task_list":   s.execTaskList,
		"web_fetch":   s.execWebFetch,
		"communicate": s.execCommunicate,
		"use_skill":   s.execUseSkill,
	}
	for name, exec := range executors {
		if tool := s.reg.Get(name); tool != nil {
			updated := *tool
			updated.Exec = exec
			s.reg.Register(updated)
		}
	}
	// Track core tool names.
	s.coreToolNames = make(map[string]bool)
	for name := range executors {
		s.coreToolNames[name] = true
	}
}
```

**Step 5: Run full suite**

Run: `go test ./internal/agent/ -v -count=1 -short`
Expected: All PASS. This is a significant refactor — existing tests that create profiles and sessions must still work.

**Step 6: Commit**

```bash
git add internal/agent/profile.go internal/agent/session.go
git add internal/agent/profile_test.go internal/agent/session_test.go
git commit -m "refactor: ProviderProfile owns its ToolRegistry, Session wires executors (GAP-3.01)"
```

---

## Summary

| Task | Gaps | Priority | Effort |
|------|------|----------|--------|
| 1. Shutdown ordering | GAP-8.23, 8.24 | P0 | Medium |
| 2. Round/turn limits | GAP-2.16, 2.17, 2.04 | P0 | Medium |
| 3. TOOL_CALL_END keys | GAP-2.08, 2.11, 2.12 | P1 | Small |
| 4. ASSISTANT_TEXT_START data | GAP-2.19 | P1 | Small |
| 5. pause_turn round counter | GAP-2.05 | P1 | Small |
| 6. Loop detection wording | GAP-2.13 | P2 | Small |
| 7. System prompt ordering | GAP-6.01 | P1 | Medium |
| 8. OSVersion | GAP-6.02 | P2 | Small |
| 9. Context window sizes | GAP-3.02, 3.08 | P2 | Small |
| 10. UTF-8 truncation | GAP-5.01 | P2 | Small |
| 11. Unicode punctuation | GAP-8.13 | P2 | Medium |
| 12. ToolMiddleware | GAP-8.05 | P2 | Medium |
| 13. PermissionDenied error | GAP-8.16 | P2 | Small |
| 14. Runtime mutability | GAP-1.02, 1.06 | P2 | Small |
| 15. AWAITING_INPUT | GAP-2.03 | P2 | Small |
| 16. Description validation | GAP-3.09, 3.13 | P2 | Small |
| 17. Subagent fixes | GAP-7.05, 7.06, 7.07 | P2 | Medium |
| 18. Windows shell | GAP-4.07 | P3 | Medium |
| 19. Document Go idioms | 14 MINOR items | P3 | Small |
| 20. Typed events + subagent lifecycle | GAP-1.05 | P2 | Large |
| 21. SDK type alignment | GAP-1.09 | P2 | Large |
| 22. Public API package | GAP-1.01 | P2 | Large |
| 23. Profile ToolRegistry ownership | GAP-3.01 | P2 | Large |

**Total: 23 tasks covering 46 gaps.**

**Execution order notes:**
- Tasks 1-17 are independent, can be done in any order
- Task 20 (typed events) should be done before Task 21 (SDK alignment) since 21 builds on typed event structs
- Task 23 (profile registry) should be done before Task 22 (public API) to stabilize the interface before exposing it
- **Task 22 (public API) should be done LAST** — it changes every import path and would conflict with parallel work on any other task
