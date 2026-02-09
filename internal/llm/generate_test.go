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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{
					{Kind: ContentToolCall, ToolCall: &call1},
					{Kind: ContentToolCall, ToolCall: &call2},
				}}}, nil
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
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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

func TestGenerate_UnknownToolCall_SendsErrorResultToModel(t *testing.T) {
	c := NewClient()
	var sawErrorResult atomic.Bool
	a := &scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				call := ToolCallData{ID: "c1", Name: "missing", Arguments: json.RawMessage(`{}`), Type: "function"}
				return Response{Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}}, nil
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
