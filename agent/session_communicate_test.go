package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// communicateCall builds a tool call to the communicate tool.
func communicateCall(id, message string) llm.ToolCallData {
	return communicateCallArgs(id, map[string]any{"message": message})
}

func communicateCallArgs(id string, args map[string]any) llm.ToolCallData {
	normalized := map[string]any{}

	var message string
	if v, ok := args["message"]; ok && v != nil {
		message = strings.TrimSpace(fmt.Sprint(v))
	}

	awaitReply, _ := args["await_reply"].(bool)

	output := map[string]any{
		"message":   "",
		"data":      map[string]any{},
		"artifacts": []string{},
	}
	if rawOutput, ok := args["output"].(map[string]any); ok {
		for k, v := range rawOutput {
			output[k] = v
		}
	}
	if message == "" {
		if outMsg, ok := output["message"].(string); ok {
			message = outMsg
		}
	}

	normalized["message"] = message
	normalized["await_reply"] = awaitReply
	normalized["output"] = output

	raw, _ := json.Marshal(normalized)
	return llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: raw,
		Type:      "function",
	}
}

// toolCallResponse is defined in tool_web_fetch_test.go (same package).

func TestCommunicate_ToolChoiceRequired_SetOnRequest(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "done")
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
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
			t.Fatalf("request %d: ToolChoice is nil, expected required", i)
		}
		if req.ToolChoice.Mode != "required" {
			t.Fatalf("request %d: ToolChoice.Mode = %q, want %q", i, req.ToolChoice.Mode, "required")
		}
	}
}

func TestCommunicate_ResultExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "Here is your answer.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
			// If the loop doesn't exit, this step would be reached.
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second LLM call after communicate")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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

func TestCommunicate_StructuredOutputExitsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCallArgs("c1", map[string]any{
		"await_reply": false,
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
				t.Fatalf("should not reach second LLM call after communicate")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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

func TestCommunicate_BareTextAutoWrappedInInteractive(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Interactive session: bare text from a non-compliant provider should be
	// auto-wrapped as a communicate(message=..., await_reply=true) call rather
	// than steering and retrying.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text response")} },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second request; bare text must be auto-wrapped on first occurrence")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "bare text response" {
		t.Fatalf("out: %q, want %q", out, "bare text response")
	}
	if !sess.Communicated() {
		t.Fatal("expected Communicated() to be true after auto-wrap")
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
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

	res := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c1", "Working..."))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}

	// The tool result should contain the steering message in the inbox.
	if !strings.Contains(res.Output, "change direction: do Y instead") {
		t.Fatalf("expected steering message in inbox, got: %s", res.Output)
	}

	// A second call should have an empty inbox (steering was already drained).
	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c2", "Still working..."))
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

func TestCommunicate_SchemaRejectsMalformedOutput(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	rawArgs, _ := json.Marshal(map[string]any{
		"message":     "missing data field",
		"await_reply": false,
		"output": map[string]any{
			"message": "missing data field",
		},
	})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "communicate",
		Arguments: rawArgs,
		Type:      "function",
	})
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

	result := communicateCall("c1", "Final answer")

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
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-evDone

	// Should have exactly 1 COMMUNICATE event.
	var srEvents []SessionEvent
	for _, ev := range events {
		if ev.Kind == EventCommunicate {
			srEvents = append(srEvents, ev)
		}
	}
	if len(srEvents) != 1 {
		t.Fatalf("expected 1 COMMUNICATE event, got %d", len(srEvents))
	}

	if msg, _ := srEvents[0].DataMap()["message"].(string); msg != "Final answer" {
		t.Fatalf("event 0 message: got %q want %q", msg, "Final answer")
	}
	if awaitReply, _ := srEvents[0].DataMap()["await_reply"].(bool); awaitReply {
		t.Fatalf("event 0 await_reply: got %v want false", awaitReply)
	}
}

func TestCommunicate_AvailableImmediately(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := communicateCall("c1", "immediate answer")
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "quick task", nil)
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
