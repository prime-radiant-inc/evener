package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedAdapter struct {
	name  string
	steps []func(req Request) (Response, error)
	i     int
	reqs  []Request
}

func (a *scriptedAdapter) Name() string { return a.name }
func (a *scriptedAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	a.reqs = append(a.reqs, req)
	if a.i >= len(a.steps) {
		return Response{Provider: a.name, Model: req.Model, Message: Assistant("done")}, nil
	}
	fn := a.steps[a.i]
	a.i++
	resp, err := fn(req)
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, err
}
func (a *scriptedAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, errors.New("stream not implemented in scriptedAdapter")
}

type blockingAdapter struct {
	name    string
	started chan struct{}
}

func (a *blockingAdapter) Name() string { return a.name }
func (a *blockingAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = req
	if a.started != nil {
		select {
		case a.started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return Response{}, ctx.Err()
}
func (a *blockingAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	_ = ctx
	_ = req
	return nil, errors.New("stream not implemented in blockingAdapter")
}

func TestGenerate_SimplePrompt(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				return Response{Message: Assistant("Hello")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	res, err := Generate(context.Background(), GenerateOptions{
		Client: c,
		Model:  "m",
		Prompt: &prompt,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Text) != "Hello" {
		t.Fatalf("text: %q", res.Text)
	}
	if got, want := len(res.Steps), 1; got != want {
		t.Fatalf("steps: got %d want %d", got, want)
	}
}

func TestGenerate_MessagesList(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				if got, want := len(req.Messages), 2; got != want {
					return Response{}, fmt.Errorf("messages: got %d want %d (%+v)", got, want, req.Messages)
				}
				if req.Messages[0].Role != RoleUser || strings.TrimSpace(req.Messages[0].Text()) != "hi" {
					return Response{}, fmt.Errorf("msg0: %+v", req.Messages[0])
				}
				if req.Messages[1].Role != RoleAssistant || strings.TrimSpace(req.Messages[1].Text()) != "hello" {
					return Response{}, fmt.Errorf("msg1: %+v", req.Messages[1])
				}
				return Response{Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	res, err := Generate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "m",
		Messages: []Message{User("hi"), Assistant("hello")},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Text) != "ok" {
		t.Fatalf("text: %q", res.Text)
	}
}

func TestGenerate_RejectsPromptAndMessagesTogether(t *testing.T) {
	c := NewClient()
	c.Register(&scriptedAdapter{name: "openai"})
	prompt := "hi"
	_, err := Generate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "m",
		Prompt:   &prompt,
		Messages: []Message{User("u")},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestGenerate_ToolLoop_ExecutesToolsAndContinues(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "add", Arguments: json.RawMessage(`{"a":1,"b":2}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				// Expect tool result in the continuation request.
				found := false
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "call1" {
							found = true
						}
					}
				}
				if !found {
					return Response{}, fmt.Errorf("expected tool result message in continuation request; got %+v", req.Messages)
				}
				return Response{Message: Assistant("Done")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "compute"
	rounds := 1
	res, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{
				Definition: ToolDefinition{
					Name:       "add",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}, "required": []string{"a", "b"}},
				},
				Execute: func(ctx context.Context, args any) (any, error) {
					_ = ctx
					m, _ := args.(map[string]any)
					ai, _ := m["a"].(float64)
					bi, _ := m["b"].(float64)
					return int(ai) + int(bi), nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Text) != "Done" {
		t.Fatalf("text: %q", res.Text)
	}
	if got, want := len(res.Steps), 2; got != want {
		t.Fatalf("steps: got %d want %d", got, want)
	}
	if got, want := len(res.Steps[0].ToolCalls), 1; got != want {
		t.Fatalf("tool calls: got %d want %d", got, want)
	}
	if got, want := len(res.Steps[0].ToolResults), 1; got != want {
		t.Fatalf("tool results: got %d want %d", got, want)
	}
	if res.Steps[0].ToolResults[0].IsError {
		t.Fatalf("unexpected tool error: %+v", res.Steps[0].ToolResults[0])
	}
}

func TestGenerate_PassiveToolCall_ReturnsToolCallsWithoutLooping(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "t1", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "do"
	res, err := Generate(context.Background(), GenerateOptions{
		Client: c,
		Model:  "m",
		Prompt: &prompt,
		Tools: []Tool{
			// Defined but no execute handler => passive tool.
			{Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, want := len(a.reqs), 1; got != want {
		t.Fatalf("adapter calls: got %d want %d", got, want)
	}
	if got, want := len(res.Steps), 1; got != want {
		t.Fatalf("steps: got %d want %d", got, want)
	}
	if got, want := len(res.ToolCalls), 1; got != want {
		t.Fatalf("tool_calls: got %d want %d", got, want)
	}
	if got := len(res.ToolResults); got != 0 {
		t.Fatalf("unexpected tool_results: %+v", res.ToolResults)
	}
}

func TestGenerate_ToolArgsSchemaValidationError_SentAsErrorResult_AndDoesNotExecute(t *testing.T) {
	c := NewClient()

	var execCalls atomic.Int32
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "add", Arguments: json.RawMessage(`{"a":"nope","b":2}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				// The continuation should include an is_error tool result, and the tool should not have executed.
				foundErr := false
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind != ContentToolResult || p.ToolResult == nil {
							continue
						}
						if p.ToolResult.ToolCallID != "call1" {
							continue
						}
						if !p.ToolResult.IsError {
							return Response{}, fmt.Errorf("expected is_error=true tool result; got %+v", p.ToolResult)
						}
						if !strings.Contains(fmt.Sprint(p.ToolResult.Content), "invalid tool arguments") {
							return Response{}, fmt.Errorf("expected validation error content; got %+v", p.ToolResult.Content)
						}
						foundErr = true
					}
				}
				if !foundErr {
					return Response{}, fmt.Errorf("expected tool error result message in continuation request; got %+v", req.Messages)
				}
				if got := execCalls.Load(); got != 0 {
					return Response{}, fmt.Errorf("expected tool not to execute; execCalls=%d", got)
				}
				return Response{Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "compute"
	rounds := 1
	res, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{
				Definition: ToolDefinition{
					Name:       "add",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}, "required": []string{"a", "b"}},
				},
				Execute: func(ctx context.Context, args any) (any, error) {
					_ = ctx
					execCalls.Add(1)
					return 0, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Text) != "ok" {
		t.Fatalf("text: %q", res.Text)
	}
}

func TestGenerate_MaxToolRoundsZero_DisablesAutoExecution(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "add", Arguments: json.RawMessage(`{"a":1,"b":2}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "compute"
	rounds := 0
	res, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{Definition: ToolDefinition{Name: "add"}, Execute: func(ctx context.Context, args any) (any, error) { return nil, nil }},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, want := len(res.Steps), 1; got != want {
		t.Fatalf("steps: got %d want %d", got, want)
	}
	if got, want := len(res.Steps[0].ToolCalls), 1; got != want {
		t.Fatalf("tool calls: got %d want %d", got, want)
	}
	if got, want := len(res.Steps[0].ToolResults), 0; got != want {
		t.Fatalf("tool results: got %d want %d", got, want)
	}
}

func TestGenerate_ParallelToolCalls_ExecuteConcurrently(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call1 := ToolCallData{ID: "c1", Name: "t1", Arguments: json.RawMessage(`{}`), Type: "function"}
				call2 := ToolCallData{ID: "c2", Name: "t2", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &call1},
						{Kind: ContentToolCall, ToolCall: &call2},
					}},
					Finish: FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) { return Response{Message: Assistant("ok")}, nil },
		},
	}
	c.Register(a)

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	exec := func(name string) func(ctx context.Context, args any) (any, error) {
		return func(ctx context.Context, args any) (any, error) {
			_ = args
			select {
			case started <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return name, nil
		}
	}

	prompt := "go"
	rounds := 1
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Generate(ctx, GenerateOptions{
			Client:        c,
			Model:         "m",
			Prompt:        &prompt,
			MaxToolRounds: &rounds,
			Tools: []Tool{
				{Definition: ToolDefinition{Name: "t1"}, Execute: exec("t1")},
				{Definition: ToolDefinition{Name: "t2"}, Execute: exec("t2")},
			},
		})
		done <- err
	}()

	// If tool calls aren't executed concurrently, the first execution blocks and the
	// second never starts, causing ctx to time out and the test to fail.
	<-started
	<-started
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

func TestGenerate_TimeoutPerStep_CancelsLLMCall(t *testing.T) {
	c := NewClient()
	started := make(chan struct{}, 1)
	c.Register(&blockingAdapter{name: "openai", started: started})

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Generate(ctx, GenerateOptions{
			Client:         c,
			Model:          "m",
			Prompt:         &prompt,
			TimeoutPerStep: 50 * time.Millisecond,
		})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for adapter call")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected error")
		}
		var te *RequestTimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("expected RequestTimeoutError, got %T (%v)", err, err)
		}
		if te.Retryable() {
			t.Fatalf("expected non-retryable timeout error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Generate did not time out promptly")
	}
}

func TestGenerate_TimeoutTotal_CancelsOperation(t *testing.T) {
	c := NewClient()
	started := make(chan struct{}, 1)
	c.Register(&blockingAdapter{name: "openai", started: started})

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Generate(ctx, GenerateOptions{
			Client:         c,
			Model:          "m",
			Prompt:         &prompt,
			TimeoutTotal:   50 * time.Millisecond,
			TimeoutPerStep: 0,
		})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for adapter call")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected error")
		}
		var te *RequestTimeoutError
		if !errors.As(err, &te) {
			t.Fatalf("expected RequestTimeoutError, got %T (%v)", err, err)
		}
		if te.Retryable() {
			t.Fatalf("expected non-retryable timeout error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Generate did not time out promptly")
	}
}

func TestGenerate_Cancellation_ReturnsAbortError(t *testing.T) {
	c := NewClient()
	started := make(chan struct{}, 1)
	c.Register(&blockingAdapter{name: "openai", started: started})

	prompt := "hi"
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := Generate(ctx, GenerateOptions{
			Client: c,
			Model:  "m",
			Prompt: &prompt,
		})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for adapter call")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected error")
		}
		var ae *AbortError
		if !errors.As(err, &ae) {
			t.Fatalf("expected AbortError, got %T (%v)", err, err)
		}
		if ae.Retryable() {
			t.Fatalf("expected non-retryable abort error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Generate did not cancel promptly")
	}
}

func TestGenerate_RetriesApplyPerStep_NotWholeOperation(t *testing.T) {
	c := NewClient()
	var toolExec atomic.Int32

	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			// Step 1: model asks for a tool call.
			func(req Request) (Response, error) {
				// No tool results yet.
				for _, m := range req.Messages {
					if m.Role == RoleTool {
						t.Fatalf("unexpected tool results in step-1 request: %+v", req.Messages)
					}
				}
				call := ToolCallData{ID: "call1", Name: "t1", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			// Step 2 attempt 1: transient error.
			func(req Request) (Response, error) {
				// Tool results from step 1 must be present.
				foundToolResult := false
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "call1" {
							foundToolResult = true
						}
					}
				}
				if !foundToolResult {
					t.Fatalf("expected tool result message in step-2 request; msgs=%+v", req.Messages)
				}
				return Response{}, ErrorFromHTTPStatus("openai", 429, "rate limited", map[string]any{"error": "rate limited"}, nil)
			},
			// Step 2 attempt 2: success.
			func(req Request) (Response, error) {
				return Response{Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	rounds := 1
	_, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		RetryPolicy: &RetryPolicy{
			MaxRetries: 1,
			BaseDelay:  1 * time.Millisecond,
			MaxDelay:   1 * time.Millisecond,
			Jitter:     false,
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			_ = ctx
			_ = d
			return nil
		},
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute: func(ctx context.Context, args any) (any, error) {
				_ = ctx
				_ = args
				toolExec.Add(1)
				return "done", nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := toolExec.Load(); got != 1 {
		t.Fatalf("tool execute count: got %d want 1", got)
	}
	if got := len(a.reqs); got != 3 {
		t.Fatalf("adapter calls: got %d want 3", got)
	}
}

func TestGenerate_ToolCallContext_AvailableInHandler(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "t1", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				return Response{Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	var gotCtx ToolCallContext
	var gotOK bool
	prompt := "do"
	rounds := 1
	_, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{
				Definition: ToolDefinition{Name: "t1"},
				Execute: func(ctx context.Context, args any) (any, error) {
					gotCtx, gotOK = ToolCallContextFromCtx(ctx)
					return "done", nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !gotOK {
		t.Fatalf("ToolCallContextFromCtx returned ok=false")
	}
	if gotCtx.ToolCallID != "call1" {
		t.Fatalf("ToolCallID: %q, want %q", gotCtx.ToolCallID, "call1")
	}
	if len(gotCtx.Messages) == 0 {
		t.Fatalf("Messages is empty")
	}
}

func TestGenerate_RepairToolCall_FixesInvalidArgs(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				// Model sends invalid args (a should be integer).
				call := ToolCallData{ID: "call1", Name: "add", Arguments: json.RawMessage(`{"a":"nope","b":2}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				// Should receive the repaired tool result, not an error.
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "call1" {
							if p.ToolResult.IsError {
								return Response{}, fmt.Errorf("expected non-error tool result after repair; got %+v", p.ToolResult)
							}
							return Response{Message: Assistant("ok")}, nil
						}
					}
				}
				return Response{}, fmt.Errorf("expected tool result for call1")
			},
		},
	}
	c.Register(a)

	var repairCalled bool
	prompt := "compute"
	rounds := 1
	_, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		RepairToolCall: func(ctx context.Context, call ToolCallData, validationError error) (json.RawMessage, error) {
			repairCalled = true
			// Fix the args.
			return json.RawMessage(`{"a":1,"b":2}`), nil
		},
		Tools: []Tool{
			{
				Definition: ToolDefinition{
					Name:       "add",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}, "required": []string{"a", "b"}},
				},
				Execute: func(ctx context.Context, args any) (any, error) {
					m, _ := args.(map[string]any)
					ai, _ := m["a"].(float64)
					bi, _ := m["b"].(float64)
					return int(ai) + int(bi), nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !repairCalled {
		t.Fatalf("RepairToolCall was not called")
	}
}

func TestGenerate_RepairToolCall_FailedRepair_SendsErrorToModel(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call1", Name: "add", Arguments: json.RawMessage(`{"a":"nope","b":2}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				// Should receive an error tool result since repair failed.
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.IsError {
							return Response{Message: Assistant("ok")}, nil
						}
					}
				}
				return Response{}, fmt.Errorf("expected error tool result")
			},
		},
	}
	c.Register(a)

	prompt := "compute"
	rounds := 1
	_, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		RepairToolCall: func(ctx context.Context, call ToolCallData, validationError error) (json.RawMessage, error) {
			return nil, fmt.Errorf("cannot repair")
		},
		Tools: []Tool{
			{
				Definition: ToolDefinition{
					Name:       "add",
					Parameters: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}}, "required": []string{"a", "b"}},
				},
				Execute: func(ctx context.Context, args any) (any, error) {
					return 0, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

func TestGenerate_UnknownToolCall_SendsErrorResultToModel(t *testing.T) {
	c := NewClient()
	var sawErrorResult atomic.Bool
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "c1", Name: "missing", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.IsError {
							sawErrorResult.Store(true)
						}
					}
				}
				return Response{Message: Assistant("ok")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "go"
	rounds := 1
	res, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{Definition: ToolDefinition{Name: "t1"}, Execute: func(ctx context.Context, args any) (any, error) { return "x", nil }},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(res.Text) != "ok" {
		t.Fatalf("text: %q", res.Text)
	}
	if !sawErrorResult.Load() {
		t.Fatalf("expected error tool result to be sent to model for unknown tool call")
	}
}

func TestGenerate_StopWhen(t *testing.T) {
	c := NewClient()
	callCount := 0
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "my_tool",
							Arguments: json.RawMessage(`{}`), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "my_tool",
							Arguments: json.RawMessage(`{}`), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "my_tool",
							Arguments: json.RawMessage(`{}`), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{Message: Assistant("done")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "go"
	maxRounds := 10
	result, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &maxRounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "my_tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute:    func(ctx context.Context, args any) (any, error) { return "ok", nil },
		}},
		StopWhen: func(steps []StepResult) bool {
			return len(steps) >= 2 // stop after 2 tool rounds
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (StopWhen should have stopped)", len(result.Steps))
	}
}

func TestGenerate_ToolExecuteError_SentAsIsError(t *testing.T) {
	callCount := 0
	c := NewClient()
	a := &scriptedAdapter{
		name: "mock",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: "call_1", Name: "failing_tool",
							Arguments: json.RawMessage(`{}`), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: "tool_calls"},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				// Verify tool result was sent as is_error
				for _, m := range req.Messages {
					if m.Role == RoleTool {
						for _, p := range m.Content {
							if p.Kind == ContentToolResult && p.ToolResult != nil {
								if !p.ToolResult.IsError {
									t.Error("expected is_error=true in tool result")
								}
								content, _ := p.ToolResult.Content.(string)
								if !strings.Contains(content, "something went wrong") {
									t.Errorf("error content = %q, want to contain 'something went wrong'", content)
								}
							}
						}
					}
				}
				return Response{
					Message: Assistant("recovered"),
					Finish:  FinishReason{Reason: "stop"},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "use the tool"
	maxRounds := 2
	result, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "test",
		Prompt:        &prompt,
		MaxToolRounds: &maxRounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "failing_tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute: func(ctx context.Context, args any) (any, error) {
				return nil, fmt.Errorf("something went wrong")
			},
		}},
	})
	if err != nil {
		t.Fatalf("Generate should NOT error when tool fails: %v", err)
	}
	if result.Text != "recovered" {
		t.Fatalf("text = %q, want %q", result.Text, "recovered")
	}
	if callCount != 2 {
		t.Fatalf("callCount = %d, want 2", callCount)
	}
}

func TestGenerate_MultiStepToolLoop_ThreeRounds(t *testing.T) {
	callCount := 0
	c := NewClient()
	a := &scriptedAdapter{
		name: "mock",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "step_tool",
							Arguments: json.RawMessage(fmt.Sprintf(`{"step":%d}`, callCount)), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: "tool_calls"},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "step_tool",
							Arguments: json.RawMessage(fmt.Sprintf(`{"step":%d}`, callCount)), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: "tool_calls"},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentToolCall, ToolCall: &ToolCallData{
							ID: fmt.Sprintf("call_%d", callCount), Name: "step_tool",
							Arguments: json.RawMessage(fmt.Sprintf(`{"step":%d}`, callCount)), Type: "function",
						}},
					}},
					Finish: FinishReason{Reason: "tool_calls"},
				}, nil
			},
			func(req Request) (Response, error) {
				callCount++
				return Response{
					Message: Assistant("done after 3 rounds"),
					Finish:  FinishReason{Reason: "stop"},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "run steps"
	maxRounds := 5
	result, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "test",
		Prompt:        &prompt,
		MaxToolRounds: &maxRounds,
		Tools: []Tool{{
			Definition: ToolDefinition{
				Name:        "step_tool",
				Description: "a multi-step tool",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"step": map[string]any{"type": "integer"}}},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				m, _ := args.(map[string]any)
				return fmt.Sprintf("completed step %v", m["step"]), nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 4 total LLM calls: 3 tool rounds + 1 final
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(result.Steps))
	}
	// 3 steps should have tool results
	for i := 0; i < 3; i++ {
		if len(result.Steps[i].ToolResults) != 1 {
			t.Fatalf("step %d tool results = %d, want 1", i, len(result.Steps[i].ToolResults))
		}
		if result.Steps[i].ToolResults[0].IsError {
			t.Fatalf("step %d tool result is_error", i)
		}
	}
	// Final step should be text only
	if result.Text != "done after 3 rounds" {
		t.Fatalf("final text = %q", result.Text)
	}
	if callCount != 4 {
		t.Fatalf("callCount = %d, want 4", callCount)
	}
}

func TestGenerate_AdapterTimeout_FlowsToRequest(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				return Response{Message: Assistant("hi")}, nil
			},
		},
	}
	c.Register(a)

	prompt := "hello"
	timeout := AdapterTimeout{
		Connect:    5 * time.Second,
		Request:    60 * time.Second,
		StreamRead: 15 * time.Second,
	}
	_, err := Generate(context.Background(), GenerateOptions{
		Client:         c,
		Model:          "m",
		Prompt:         &prompt,
		AdapterTimeout: &timeout,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(a.reqs) != 1 {
		t.Fatalf("adapter calls: got %d want 1", len(a.reqs))
	}
	got := a.reqs[0].AdapterTimeout
	if got == nil {
		t.Fatal("Request.AdapterTimeout is nil")
	}
	if got.Connect != 5*time.Second {
		t.Fatalf("Connect = %v, want 5s", got.Connect)
	}
	if got.Request != 60*time.Second {
		t.Fatalf("Request = %v, want 60s", got.Request)
	}
	if got.StreamRead != 15*time.Second {
		t.Fatalf("StreamRead = %v, want 15s", got.StreamRead)
	}
}

func TestGenerate_StepResult_ToolCallsHaveParsedArguments(t *testing.T) {
	c := NewClient()
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{
					ID:        "call1",
					Name:      "get_weather",
					Arguments: json.RawMessage(`{"city":"Seattle"}`),
					Type:      "function",
				}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "what's the weather?"
	zero := 0
	res, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &zero,
		Tools: []Tool{
			{Definition: ToolDefinition{Name: "get_weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}},
		},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool_calls: got %d want 1", len(res.ToolCalls))
	}
	tc := res.ToolCalls[0]
	if tc.ParsedArguments == nil {
		t.Fatal("ParsedArguments is nil on result ToolCall")
	}
	if tc.ParsedArguments["city"] != "Seattle" {
		t.Fatalf("ParsedArguments[city] = %v", tc.ParsedArguments["city"])
	}
	// Also check the step's copy.
	if len(res.Steps) != 1 {
		t.Fatalf("steps: got %d want 1", len(res.Steps))
	}
	stc := res.Steps[0].ToolCalls[0]
	if stc.ParsedArguments == nil {
		t.Fatal("step ToolCall ParsedArguments is nil")
	}
	if stc.ParsedArguments["city"] != "Seattle" {
		t.Fatalf("step ParsedArguments[city] = %v", stc.ParsedArguments["city"])
	}
}

func TestGenerate_ToolCallsWithStopFinish_DoesNotExecute(t *testing.T) {
	// Model returns tool call content parts BUT with finish_reason="stop" (not "tool_calls").
	// Per spec section 5.6, tools should NOT be executed.
	c := NewClient()
	executed := false
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{
					Message: Message{Role: RoleAssistant, Content: []ContentPart{
						{Kind: ContentText, Text: "Here is the weather"},
						{Kind: ContentToolCall, ToolCall: &call},
					}},
					Finish: FinishReason{Reason: FinishReasonStop, Raw: "stop"},
					Usage:  Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
				}, nil
			},
		},
	}
	c.Register(a)

	prompt := "weather?"
	rounds := 1
	result, err := Generate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{{
			Definition: ToolDefinition{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				executed = true
				return "sunny", nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Error("tool was executed but finish_reason was 'stop', not 'tool_calls'")
	}
	// The tool calls should still be in the result (for the caller to see).
	if len(result.ToolCalls) == 0 {
		t.Error("expected tool calls in result even though they weren't executed")
	}
}
