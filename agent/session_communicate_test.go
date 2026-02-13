package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// communicateCall builds a tool call to the communicate tool.
func communicateCall(id, action, message string) llm.ToolCallData {
	return communicateCallArgs(id, map[string]any{"action": action, "message": message})
}

func communicateCallArgs(id string, args map[string]any) llm.ToolCallData {
	raw, _ := json.Marshal(args)
	return llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: raw,
		Type:      "function",
	}
}

// toolCallResponse is defined in tool_web_fetch_test.go (same package).

func TestCommunicate_ToolChoiceAuto_SetOnRequest(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "result", "done")
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

func TestCommunicate_ResultExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "result", "Here is your answer.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
			// If the loop doesn't exit, this step would be reached.
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second LLM call after communicate(result)")
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

func TestCommunicate_ResultStructuredOutputExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCallArgs("c1", map[string]any{
		"action": "result",
		"output": map[string]any{
			"decision": "approve",
			"message":  "Structured final answer.",
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
				t.Fatalf("should not reach second LLM call after communicate(result)")
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
	want := `{"decision":"approve","message":"Structured final answer.","data":{"a":"x","z":1},"artifacts":[]}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("ProcessInput returned %q, want %q", out, want)
	}
	sess.Close()

	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
}

func TestCommunicate_StatusContinuesLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	status := communicateCall("c1", "status", "Working on it...")
	result := communicateCall("c2", "result", "All done.")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(status) },
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
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
	if strings.TrimSpace(out) != "All done." {
		t.Fatalf("out: %q, want %q", out, "All done.")
	}
	sess.Close()

	// Two LLM requests: one for the status round, one for the result round.
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestCommunicate_StatusWithOutputContinuesLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	status := communicateCallArgs("c1", map[string]any{
		"action":  "status",
		"message": "Working on it...",
		"output": map[string]any{
			"decision": "noop",
			"message":  "ignored",
			"data":     map[string]any{},
		},
	})
	result := communicateCall("c2", "result", "All done.")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(status) },
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
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
	if strings.TrimSpace(out) != "All done." {
		t.Fatalf("out: %q, want %q", out, "All done.")
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestCommunicate_BareTextFallback(t *testing.T) {
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

func TestCommunicate_InboxDrainsSteering(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Queue a steering message, then call communicate directly through the registry.
	sess.Steer("change direction: do Y instead")

	res := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c1", "status", "Working..."))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}

	// The tool result should contain the steering message in the inbox.
	if !strings.Contains(res.Output, "change direction: do Y instead") {
		t.Fatalf("expected steering message in inbox, got: %s", res.Output)
	}

	// A second call should have an empty inbox (steering was already drained).
	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c2", "status", "Still working..."))
	if res2.IsError {
		t.Fatalf("communicate error: %s", res2.Output)
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

func TestCommunicate_ResultSchemaRejectsMalformedOutput(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCallArgs("c1", map[string]any{
		"action": "result",
		"output": map[string]any{
			"decision": "approve",
			"message":  "missing data field",
		},
	}))
	if !res.IsError {
		t.Fatalf("expected schema error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "schema validation failed") {
		t.Fatalf("expected schema validation error, got: %s", res.Output)
	}
}

func TestCommunicate_EmitsEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	status := communicateCall("c1", "status", "Progress update")
	result := communicateCall("c2", "result", "Final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(status) },
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

	// Should have exactly 2 COMMUNICATE events: one status, one result.
	var commEvents []SessionEvent
	for _, ev := range events {
		if ev.Kind == EventCommunicate {
			commEvents = append(commEvents, ev)
		}
	}
	if len(commEvents) != 2 {
		t.Fatalf("expected 2 COMMUNICATE events, got %d", len(commEvents))
	}

	// First should be status.
	if action, _ := commEvents[0].DataMap()["action"].(string); action != "status" {
		t.Fatalf("event 0 action: got %q want %q", action, "status")
	}
	if msg, _ := commEvents[0].DataMap()["message"].(string); msg != "Progress update" {
		t.Fatalf("event 0 message: got %q want %q", msg, "Progress update")
	}

	// Second should be result.
	if action, _ := commEvents[1].DataMap()["action"].(string); action != "result" {
		t.Fatalf("event 1 action: got %q want %q", action, "result")
	}
	if msg, _ := commEvents[1].DataMap()["message"].(string); msg != "Final answer" {
		t.Fatalf("event 1 message: got %q want %q", msg, "Final answer")
	}
}
