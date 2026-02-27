package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// submitResultCall builds a tool call to the submit_result tool.
func submitResultCall(id, message string) llm.ToolCallData {
	return submitResultCallArgs(id, map[string]any{"message": message})
}

func submitResultCallArgs(id string, args map[string]any) llm.ToolCallData {
	raw, _ := json.Marshal(args)
	return llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: raw,
		Type:      "function",
	}
}

func approveCall(id, message string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{"message": message})
	return llm.ToolCallData{ID: id, Name: "approve", Arguments: raw, Type: "function"}
}

func rejectCall(id, feedback string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{"feedback": feedback})
	return llm.ToolCallData{ID: id, Name: "reject", Arguments: raw, Type: "function"}
}

// toolCallResponse is defined in tool_web_fetch_test.go (same package).

func TestSubmitResult_ToolChoiceAuto_SetOnRequest(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := submitResultCall("c1", "done")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatalf("expected at least 1 request")
	}
	for i, req := range reqs {
		if req.ToolChoice == nil {
			t.Fatalf("request %d: ToolChoice is nil, expected auto", i)
		}
		if req.ToolChoice.Mode != "auto" {
			t.Fatalf("request %d: ToolChoice.Mode = %q, want %q", i, req.ToolChoice.Mode, "auto")
		}
	}
}

func TestSubmitResult_ResultExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := submitResultCall("c1", "Here is your answer.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
			// If the loop doesn't exit, this step would be reached.
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second LLM call after submit_result")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "Here is your answer." {
		t.Fatalf("ProcessInput returned %q, want %q", out, "Here is your answer.")
	}
	sess.Close()

	// Only one LLM request should have been made.
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
}

func TestSubmitResult_StructuredOutputExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := submitResultCallArgs("c1", map[string]any{
		"output": map[string]any{
			"message": "Structured final answer.",
			"data": map[string]any{
				"z": 1,
				"a": "x",
			},
		},
	})
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second LLM call after submit_result")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	want := `{"message":"Structured final answer.","data":{"a":"x","z":1},"artifacts":[]}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("ProcessInput returned %q, want %q", out, want)
	}
	sess.Close()

	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
}

func TestSubmitResult_BareTextFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Model returns bare text (no tool calls) — simulating a provider that
	// doesn't honor tool_choice=required.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("bare text response")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "bare text response" {
		t.Fatalf("out: %q, want %q", out, "bare text response")
	}
	sess.Close()
}

func TestSubmitResult_InboxDrainsSteering(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Queue a steering message, then call submit_result directly through the registry.
	sess.Steer("change direction: do Y instead")

	res := sess.reg.ExecuteCall(context.Background(), sess.env, submitResultCall("c1", "Working..."))
	if res.IsError {
		t.Fatalf("submit_result error: %s", res.Output)
	}

	// The tool result should contain the steering message in the inbox.
	if !strings.Contains(res.Output, "change direction: do Y instead") {
		t.Fatalf("expected steering message in inbox, got: %s", res.Output)
	}

	// A second call should have an empty inbox (steering was already drained).
	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, submitResultCall("c2", "Still working..."))
	if res2.IsError {
		t.Fatalf("submit_result error: %s", res2.Output)
	}

	// Parse the JSON to verify inbox is empty.
	var resp2 map[string]any
	if err := json.Unmarshal([]byte(res2.Output), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inbox2, _ := resp2["inbox"].([]any)
	if len(inbox2) != 0 {
		t.Fatalf("expected empty inbox on second call, got: %v", inbox2)
	}
}

func TestSubmitResult_SchemaRejectsMalformedOutput(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, submitResultCallArgs("c1", map[string]any{
		"output": map[string]any{
			"message": "missing data field",
		},
	}))
	if !res.IsError {
		t.Fatalf("expected schema error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "schema validation failed") {
		t.Fatalf("expected schema validation error, got: %s", res.Output)
	}
}

func TestSubmitResult_EmitsEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := submitResultCall("c1", "Final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []SessionEvent
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			events = append(events, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-evDone

	// Should have exactly 1 SUBMIT_RESULT event.
	var srEvents []SessionEvent
	for _, ev := range events {
		if ev.Kind == EventSubmitResult {
			srEvents = append(srEvents, ev)
		}
	}
	if len(srEvents) != 1 {
		t.Fatalf("expected 1 SUBMIT_RESULT event, got %d", len(srEvents))
	}

	if msg, _ := srEvents[0].DataMap()["message"].(string); msg != "Final answer" {
		t.Fatalf("event 0 message: got %q want %q", msg, "Final answer")
	}
}

func TestSubmitResult_MinResultRound_HidesToolBeforeThreshold(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	lateResult := submitResultCall("c3", "verified answer")

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}

	hasSubmitResult := func(req llm.Request) bool {
		for _, td := range req.Tools {
			if td.Name == "communicate" {
				return true
			}
		}
		return false
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: submit_result should NOT be in tool list.
			func(req llm.Request) llm.Response {
				if hasSubmitResult(req) {
					t.Errorf("round 0: submit_result should not be in tool list before MinResultRound")
				}
				return toolCallResponse(shellCall("s0"))
			},
			// Round 1: still before threshold.
			func(req llm.Request) llm.Response {
				if hasSubmitResult(req) {
					t.Errorf("round 1: submit_result should not be in tool list before MinResultRound")
				}
				return toolCallResponse(shellCall("s1"))
			},
			// Round 2: still before threshold (MinResultRound=3 means rounds 0,1,2 hidden).
			func(req llm.Request) llm.Response {
				if hasSubmitResult(req) {
					t.Errorf("round 2: submit_result should not be in tool list before MinResultRound")
				}
				return toolCallResponse(shellCall("s2"))
			},
			// Round 3: at threshold — submit_result should now be available.
			func(req llm.Request) llm.Response {
				if !hasSubmitResult(req) {
					t.Errorf("round 3: submit_result should be in tool list at MinResultRound")
				}
				return toolCallResponse(lateResult)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MinResultRound: 3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "verified answer" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "verified answer")
	}
	sess.Close()

	// All 4 LLM requests should have been made.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4", got)
	}
}

func TestSubmitResult_MinResultRound_ZeroAllowsImmediate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := submitResultCall("c1", "immediate answer")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second step with MinResultRound=0")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MinResultRound: 0, // default, no minimum
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "quick task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "immediate answer" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "immediate answer")
	}
	sess.Close()

	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Phase 2: Reviewer gate tests
// ---------------------------------------------------------------------------

// TestSubmitResult_Depth0_ReviewerToolsInRequest verifies that the approve and
// reject tools are actually present in the API request sent to the reviewer
// subagent. This guards against a regression where custom-registered tools
// were in the registry (for execution) but not in allToolDefinitions() (so the
// model never saw them).
func TestSubmitResult_Depth0_ReviewerToolsInRequest(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: call submit_result
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "done"))
			},
			// Reviewer subagent: check that approve/reject are in the tools list
			func(req llm.Request) llm.Response {
				toolNames := make(map[string]bool)
				for _, td := range req.Tools {
					toolNames[td.Name] = true
				}
				if !toolNames["approve"] {
					t.Errorf("reviewer request missing 'approve' tool; tools: %v", toolNames)
				}
				if !toolNames["reject"] {
					t.Errorf("reviewer request missing 'reject' tool; tools: %v", toolNames)
				}
				if toolNames["communicate"] {
					t.Errorf("reviewer request should NOT have 'communicate' tool; tools: %v", toolNames)
				}
				return toolCallResponse(approveCall("review-1", "looks good"))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0,
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
}

// TestSubmitResult_Depth0_ReviewerPass verifies that at depth 0 (root session),
// calling submit_result spawns a reviewer subagent. When the reviewer returns
// PASS, the tool response should indicate accepted:true and the session should
// exit normally.
func TestSubmitResult_Depth0_ReviewerPass(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Step 1: main agent calls submit_result
	// Step 2: reviewer agent (spawned subagent) calls approve
	// After reviewer approve, main session should exit.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: call submit_result
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "I fixed the bug by patching main.go"))
			},
			// Reviewer subagent: approve the work
			func(req llm.Request) llm.Response {
				return toolCallResponse(approveCall("review-1", "The implementation correctly fixes the bug."))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0, // root session
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "fix the bug in main.go")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// The session should have completed (reviewer accepted the result).
	if out == "" {
		t.Fatal("expected non-empty output from accepted submit_result")
	}

	// At depth 0, the reviewer subagent MUST have been spawned, consuming the
	// second step. Exactly 2 LLM requests: main agent + reviewer.
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2 (main + reviewer)", got)
	}
}

// TestSubmitResult_Depth0_ReviewerFail verifies that when the reviewer returns
// FAIL, the main agent receives feedback and can retry. On second attempt with
// reviewer PASS, the session exits.
func TestSubmitResult_Depth0_ReviewerFail(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: first submit_result attempt
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "I fixed the bug"))
			},
			// Reviewer: rejects the work
			func(req llm.Request) llm.Response {
				return toolCallResponse(rejectCall("review-1", "Tests are still failing. Run pytest to verify."))
			},
			// Main agent: receives rejection feedback, tries again
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-2", "Fixed the test failures too"))
			},
			// Reviewer: approves on second attempt
			func(req llm.Request) llm.Response {
				return toolCallResponse(approveCall("review-2", "All tests pass now."))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0,
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "fix the bug")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// Session should complete after second accepted submit_result.
	if out == "" {
		t.Fatal("expected non-empty output after reviewer accepted second attempt")
	}

	// 4 LLM requests: main submit #1, reviewer FAIL, main submit #2, reviewer PASS.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4 (submit-review-submit-review)", got)
	}
}

// TestSubmitResult_DepthGt0_Passthrough verifies that at depth > 0 (subagent),
// submit_result passes through directly without spawning a reviewer.
func TestSubmitResult_DepthGt0_Passthrough(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "subagent done"))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              1, // subagent depth
		MaxSubagentDepth:   3,
		EnableReviewerGate: true, // should be skipped at depth > 0
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the subtask")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// At depth > 0, submit_result should pass through immediately.
	if strings.TrimSpace(out) != "subagent done" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "subagent done")
	}

	// Only one LLM request should have been made (no reviewer spawned).
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1 (reviewer should not be spawned at depth>0)", got)
	}
}

// TestSubmitResult_ReviewerError_FailOpen verifies that if the reviewer errors
// or crashes, the result is still accepted (fail-open behavior).
func TestSubmitResult_ReviewerError_FailOpen(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: submit_result
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "I completed the task"))
			},
			// Reviewer: returns text only (no submit_result tool call).
			// This simulates the reviewer crashing or not producing a valid verdict.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("I encountered an error and cannot review.")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0,
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "complete the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// Fail-open: result should be accepted even though reviewer errored.
	if out == "" {
		t.Fatal("expected non-empty output (fail-open on reviewer error)")
	}

	// 2 LLM requests: main submit + reviewer (which failed to produce verdict).
	if got := len(f.Requests()); got < 2 {
		t.Fatalf("requests: got %d want >=2 (main + reviewer must be spawned)", got)
	}
}

func TestStripPromptSection(t *testing.T) {
	input := "Preamble text.\n\n## keep_me\n\nKeep this section.\n\n## communicate\n\nYou MUST call communicate when done.\nMore communicate details.\n\n## workflow\n\nWorkflow section.\n"
	got := stripPromptSection(input, "communicate")

	if strings.Contains(got, "communicate") {
		t.Errorf("stripped text still contains communicate:\n%s", got)
	}
	if !strings.Contains(got, "keep_me") {
		t.Errorf("stripped text lost keep_me section")
	}
	if !strings.Contains(got, "workflow") {
		t.Errorf("stripped text lost workflow section")
	}
	if !strings.Contains(got, "Preamble text.") {
		t.Errorf("stripped text lost preamble")
	}
}

func TestStripPromptSection_CaseInsensitive(t *testing.T) {
	input := "## Communicate\n\nContent.\n\n## Other\n\nKept.\n"
	got := stripPromptSection(input, "communicate")

	if strings.Contains(got, "Content.") {
		t.Errorf("case-insensitive strip failed:\n%s", got)
	}
	if !strings.Contains(got, "Kept.") {
		t.Errorf("lost other section")
	}
}

// TestSubmitResult_ReviewerPromptNoSubmitResult verifies the reviewer's system
// prompt does NOT mention submit_result (which would confuse the model since
// the reviewer must use approve/reject instead).
func TestSubmitResult_ReviewerPromptNoSubmitResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: call submit_result
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "done"))
			},
			// Reviewer subagent: check system prompt
			func(req llm.Request) llm.Response {
				// The system prompt is the first message (role=system)
				for _, m := range req.Messages {
					if m.Role == "system" {
						for _, p := range m.Content {
							if p.Kind == llm.ContentText && strings.Contains(p.Text, "MUST call communicate") {
								t.Errorf("reviewer system prompt still tells model to call submit_result:\n%s", p.Text[:min(len(p.Text), 500)])
							}
						}
					}
				}
				return toolCallResponse(approveCall("review-1", "ok"))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0,
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
}

// TestSubmitResult_ReviewerGetsOriginalTask verifies that the reviewer receives
// the original task text in its prompt so it can evaluate the work in context.
func TestSubmitResult_ReviewerGetsOriginalTask(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	originalTask := "Fix the authentication bypass vulnerability in auth.go"

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent: submit_result
			func(req llm.Request) llm.Response {
				return toolCallResponse(submitResultCall("call-1", "Fixed the auth bypass"))
			},
			// Reviewer: check that the original task appears in the request
			func(req llm.Request) llm.Response {
				// Search all messages for the original task text
				found := false
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentText && strings.Contains(p.Text, originalTask) {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					t.Errorf("reviewer prompt does not contain original task %q", originalTask)
				}
				return toolCallResponse(approveCall("review-1", "Vulnerability properly fixed."))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Depth:              0,
		MaxSubagentDepth:   3,
		EnableReviewerGate: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, originalTask)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if out == "" {
		t.Fatal("expected non-empty output")
	}

	// Reviewer must have been spawned (2 LLM requests: main + reviewer).
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2 (main + reviewer)", got)
	}
}
