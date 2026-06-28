package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
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

	endTurn := true
	if v, ok := args["end_turn"].(bool); ok {
		endTurn = v
	}

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
	normalized["end_turn"] = endTurn
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
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestCommunicate_StatusMessageContinuesTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	status := communicateCallArgs("c1", map[string]any{
		"message":  "Starting the work.",
		"end_turn": false,
	})
	final := communicateCall("c2", "Finished the work.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(status)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(final)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if strings.TrimSpace(out) != "Finished the work." {
		t.Fatalf("ProcessInput returned %q, want final message", out)
	}
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestCommunicateRejectsLegacyAwaitReply(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	rawArgs, _ := json.Marshal(map[string]any{
		"message":     "legacy",
		"await_reply": false,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "communicate",
		Arguments: rawArgs,
		Type:      "function",
	})
	if !res.IsError {
		t.Fatalf("legacy await_reply call succeeded: %s", res.Output)
	}
}

func TestCommunicate_StructuredOutputExitsLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCallArgs("c1", map[string]any{
		"end_turn": true,
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

func TestCommunicate_FirstTerminalResultWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	first := communicateCallArgs("c1", map[string]any{
		"end_turn": true,
		"output": map[string]any{
			"message": "First final answer.",
			"data": map[string]any{
				"winner": "first",
			},
		},
	})
	second := communicateCallArgs("c2", map[string]any{
		"end_turn": true,
		"output": map[string]any{
			"message": "Second final answer.",
			"data": map[string]any{
				"winner": "second",
			},
		},
	})
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(first, second)
			},
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach second LLM call after terminal communicate")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	want := `{"message":"First final answer.","data":{"winner":"first"},"artifacts":[]}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("ProcessInput returned %q, want %q", out, want)
	}
	sess.Close()

	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests: got %d want 1", got)
	}
}

func TestCommunicate_StatusBatchedWithDelegateDoesNotEndTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	delegateArgs, _ := json.Marshal(map[string]any{
		"task":        "Return CHILD_DONE.",
		"max_wait_ms": 5000,
	})
	delegate := llm.ToolCallData{
		ID:        "delegate_1",
		Name:      "delegate",
		Arguments: delegateArgs,
		Type:      "function",
	}
	status := communicateCallArgs("status_1", map[string]any{
		"message":  "Starting observer delegate and watch flow.",
		"end_turn": false,
	})
	childDone := communicateCall("child_done", "CHILD_DONE")
	parentDone := communicateCall("parent_done", "Parent saw the delegate complete.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(delegate, status)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(childDone)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(parentDone)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "start the observer flow", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if strings.TrimSpace(out) != "Parent saw the delegate complete." {
		t.Fatalf("ProcessInput returned %q, want parent final", out)
	}
	if got := len(f.Requests()); got != 3 {
		t.Fatalf("requests: got %d want 3 (parent, child delegate, parent continuation)", got)
	}
}

func TestCommunicateCapturesRawStructuredOutput(t *testing.T) {
	t.Parallel()
	var captured any
	deps := &toolDeps{
		emit: func(events.EventKind, events.EventData) {},
		abort: func(context.Context) error {
			return nil
		},
		drainSteering: func() []steeringMessage {
			return nil
		},
		prependSteering: func([]steeringMessage) {},
		resultToolName: func() string {
			return "communicate"
		},
		setCommunicateResult:     func(string, string, string) {},
		setCommunicateStructured: func(raw any) { captured = raw },
	}
	reg := tool.NewRegistry()
	registerCommunicateTool(reg, deps)
	rt := reg.Get("communicate")
	if rt == nil {
		t.Fatal("communicate not registered")
	}

	args := map[string]any{
		"message":  "report",
		"end_turn": true,
		"output": map[string]any{
			"summary": "did the thing",
			"files":   []any{"a.go", "b.go"},
		},
	}
	if _, err := rt.Exec(context.Background(), execenv.ExecutionEnvironment(nil), args); err != nil {
		t.Fatalf("exec: %v", err)
	}

	want := map[string]any{"summary": "did the thing", "files": []any{"a.go", "b.go"}}
	if !reflect.DeepEqual(captured, want) {
		got, _ := json.Marshal(captured)
		t.Fatalf("captured structured = %s, want raw output preserved", got)
	}
}

func TestCommunicateCapturesEmptyRawStructuredOutputForCustomSchema(t *testing.T) {
	t.Parallel()
	var captured any
	deps := &toolDeps{
		emit: func(events.EventKind, events.EventData) {},
		abort: func(context.Context) error {
			return nil
		},
		drainSteering: func() []steeringMessage {
			return nil
		},
		prependSteering: func([]steeringMessage) {},
		resultToolName: func() string {
			return "communicate"
		},
		setCommunicateResult:     func(string, string, string) {},
		setCommunicateStructured: func(raw any) { captured = raw },
	}
	reg := tool.NewRegistry()
	def := tool.DefCommunicateNamed("communicate")
	params := tool.CloneSchemaMap(def.Parameters)
	props := params["properties"].(map[string]any)
	props["output"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
	}
	def.Parameters = params
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: def},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("pre-register communicate: %v", err)
	}
	registerCommunicateTool(reg, deps)
	rt := reg.Get("communicate")
	if rt == nil {
		t.Fatal("communicate not registered")
	}

	output := map[string]any{}
	args := map[string]any{
		"message":  "report",
		"end_turn": true,
		"output":   output,
	}
	if _, err := rt.Exec(context.Background(), execenv.ExecutionEnvironment(nil), args); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if !reflect.DeepEqual(captured, output) {
		got, _ := json.Marshal(captured)
		t.Fatalf("captured structured = %s, want empty raw output preserved", got)
	}
}

func TestCommunicate_BareTextFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Model keeps returning bare text (no tool calls) — simulating a provider
	// that doesn't honor tool_choice=required.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text response")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text response")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text response")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare text response")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected bare-text contract error, got output %q", out)
	}
	if out != "" {
		t.Fatalf("out: %q, want empty string on error", out)
	}
	if !strings.Contains(err.Error(), "bare text without calling communicate") {
		t.Fatalf("unexpected error: %v", err)
	}
	sess.Close()
}

func TestCommunicate_InboxDrainsSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	// The tool result should contain the steering message specifically inside the 'inbox' array.
	var resp1 map[string]any
	if err := json.Unmarshal([]byte(res.Output), &resp1); err != nil {
		t.Fatalf("unmarshal first response: %v", err)
	}
	inbox1, ok := resp1["inbox"].([]any)
	if !ok || len(inbox1) == 0 {
		t.Fatalf("expected non-empty inbox array in response, got: %v", resp1)
	}
	found := false
	for _, item := range inbox1 {
		if s, ok := item.(string); ok && strings.Contains(s, "change direction: do Y instead") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected steering message inside inbox array, got inbox: %v", inbox1)
	}

	// A second call should have an empty inbox (steering was already drained).
	res2 := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c2", "Still working..."))
	if res2.IsError {
		t.Fatalf("communicate error: %s", res2.Output)
	}

	// Parse the JSON to verify inbox is present but empty.
	var resp2 map[string]any
	if err := json.Unmarshal(toolResultJSON(res2), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := resp2["inbox"]; !exists {
		t.Fatal("inbox field missing from second response")
	}
	inbox2, _ := resp2["inbox"].([]any)
	if len(inbox2) != 0 {
		t.Fatalf("expected empty inbox on second call, got: %v", inbox2)
	}
}

func TestCommunicate_DrainedImageSteeringRequeuesForPostToolInjection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SteerWithImages("look at this", []ImageAttachment{{
		MediaType: "image/png",
		Data:      []byte("png bytes"),
		Name:      "shot.png",
	}})

	res := sess.reg.ExecuteCall(context.Background(), sess.env, communicateCall("c1", "Working..."))
	if res.IsError {
		t.Fatalf("communicate error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "look at this") {
		t.Fatalf("expected steering text in inbox, got: %s", res.Output)
	}

	drained := sess.drainSteering()
	if len(drained) != 1 {
		t.Fatalf("deferred steering entries=%d, want 1", len(drained))
	}
	msg := steeringMessageToLLM(drained[0])
	var foundImage bool
	for _, part := range msg.Content {
		if part.Kind == llm.ContentImage && part.Image != nil && string(part.Image.Data) == "png bytes" {
			foundImage = true
		}
	}
	if !foundImage {
		t.Fatalf("deferred steering did not preserve image content: %+v", msg.Content)
	}
}

func TestCommunicate_SchemaRejectsMalformedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	rawArgs, _ := json.Marshal(map[string]any{
		"message":  "missing data field",
		"end_turn": true,
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

func TestCommunicate_SchemaRejectsPurposeInsideOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	rawArgs, _ := json.Marshal(map[string]any{
		"message":  "RESULT_LIST_DIR visible-item.txt",
		"end_turn": true,
		"output": map[string]any{
			"message":   "RESULT_LIST_DIR visible-item.txt",
			"data":      map[string]any{},
			"artifacts": []any{},
			"purpose":   "final_result",
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
	if !strings.Contains(res.Output, "additionalProperties") || !strings.Contains(res.Output, "purpose") {
		t.Fatalf("expected output.purpose schema error, got: %s", res.Output)
	}
}

func TestCommunicate_SchemaRejectsTopLevelPurpose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	rawArgs, _ := json.Marshal(map[string]any{
		"message":  "RESULT_LIST_DIR visible-item.txt",
		"end_turn": true,
		"purpose":  "final_result",
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []any{},
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
	if !strings.Contains(res.Output, "additionalProperties") || !strings.Contains(res.Output, "purpose") {
		t.Fatalf("expected top-level purpose schema error, got: %s", res.Output)
	}
}

func TestCommunicate_ModelFacingSchemaOmitsPurpose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var communicate *llm.ToolDefinition
	var readFile *llm.ToolDefinition
	for i := range sess.cachedToolDefs {
		switch sess.cachedToolDefs[i].Name {
		case "communicate":
			communicate = &sess.cachedToolDefs[i]
		case "read_file":
			readFile = &sess.cachedToolDefs[i]
		}
	}
	if communicate == nil {
		t.Fatal("communicate tool not advertised")
	}
	props, _ := communicate.Parameters["properties"].(map[string]any)
	if _, ok := props["purpose"]; ok {
		t.Fatalf("communicate should not advertise purpose: %#v", props["purpose"])
	}
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if _, ok := outProps["purpose"]; ok {
		t.Fatalf("communicate.output should not advertise purpose: %#v", outProps["purpose"])
	}
	if readFile == nil {
		t.Fatal("read_file tool not advertised")
	}
	readProps, _ := readFile.Parameters["properties"].(map[string]any)
	if _, ok := readProps["purpose"]; !ok {
		t.Fatal("read_file should still advertise purpose")
	}
}

func TestCommunicate_ModelFacingSchemaOmitsPurposeForResultAlias(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ResultToolName: "respond",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var respond *llm.ToolDefinition
	for i := range sess.cachedToolDefs {
		if sess.cachedToolDefs[i].Name == "respond" {
			respond = &sess.cachedToolDefs[i]
			break
		}
	}
	if respond == nil {
		t.Fatal("result alias not advertised")
	}
	props, _ := respond.Parameters["properties"].(map[string]any)
	if _, ok := props["purpose"]; ok {
		t.Fatalf("result alias should not advertise purpose: %#v", props["purpose"])
	}
}

func TestCommunicate_EmitsEvent(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			evs = append(evs, ev)
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
	var srEvents []events.SessionEvent
	for _, ev := range evs {
		if ev.Kind == events.EventCommunicate {
			srEvents = append(srEvents, ev)
		}
	}
	if len(srEvents) != 1 {
		t.Fatalf("expected 1 COMMUNICATE event, got %d", len(srEvents))
	}

	d, ok := srEvents[0].Data.(events.CommunicateData)
	if !ok {
		t.Fatalf("event 0 data: got %T want events.CommunicateData", srEvents[0].Data)
	}
	if d.Message != "Final answer" {
		t.Fatalf("event 0 message: got %q want %q", d.Message, "Final answer")
	}
	if !d.EndTurn {
		t.Fatalf("event 0 end_turn: got %v want true", d.EndTurn)
	}
}

func TestCommunicate_AvailableImmediately(t *testing.T) {
	t.Parallel()
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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
