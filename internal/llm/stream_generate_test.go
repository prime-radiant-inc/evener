package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedStreamAdapter struct {
	name string

	mu       sync.Mutex
	scripts  []func(ctx context.Context, req Request) (Stream, error)
	requests []Request
}

func (a *scriptedStreamAdapter) Name() string { return a.name }

func (a *scriptedStreamAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	_ = ctx
	_ = req
	return Response{}, fmt.Errorf("not implemented")
}

func (a *scriptedStreamAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	if len(a.scripts) == 0 {
		a.mu.Unlock()
		return nil, fmt.Errorf("no scripted stream remaining")
	}
	s := a.scripts[0]
	a.scripts = a.scripts[1:]
	a.mu.Unlock()
	return s(ctx, req)
}

func (a *scriptedStreamAdapter) Requests() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Request{}, a.requests...)
}

func TestStreamGenerate_RejectsPromptAndMessagesTogether(t *testing.T) {
	c := NewClient()
	c.Register(&scriptedStreamAdapter{name: "openai"})
	prompt := "hi"
	_, err := StreamGenerate(context.Background(), GenerateOptions{
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

func TestStreamGenerate_TimeoutPerStep_EmitsRequestTimeoutError(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				_ = req
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					<-ctx.Done()
					cancel()
				}()
				_ = sctx
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:         c,
		Model:          "m",
		Provider:       "openai",
		Prompt:         &prompt,
		TimeoutPerStep: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var sawErr error
	for ev := range res.Events() {
		if ev.Type == StreamEventError && ev.Err != nil {
			sawErr = ev.Err
		}
	}
	if sawErr == nil {
		t.Fatalf("expected error event")
	}
	var rte *RequestTimeoutError
	if !errors.As(sawErr, &rte) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", sawErr, sawErr)
	}
	_, rerr := res.Response()
	if !errors.As(rerr, &rte) {
		t.Fatalf("Response error: expected RequestTimeoutError, got %T (%v)", rerr, rerr)
	}
}

func TestStreamGenerate_TimeoutTotal_EmitsRequestTimeoutError(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				_ = req
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					<-ctx.Done()
					cancel()
				}()
				_ = sctx
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:       c,
		Model:        "m",
		Provider:     "openai",
		Prompt:       &prompt,
		TimeoutTotal: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var sawErr error
	for ev := range res.Events() {
		if ev.Type == StreamEventError && ev.Err != nil {
			sawErr = ev.Err
		}
	}
	if sawErr == nil {
		t.Fatalf("expected error event")
	}
	var rte *RequestTimeoutError
	if !errors.As(sawErr, &rte) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", sawErr, sawErr)
	}
	_, rerr := res.Response()
	if !errors.As(rerr, &rte) {
		t.Fatalf("Response error: expected RequestTimeoutError, got %T (%v)", rerr, rerr)
	}
}

func TestStreamGenerate_SimpleStreaming_YieldsDeltasAndFinish(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				_ = req
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "text_1"})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: "Hel"})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: "lo"})
					st.Send(StreamEvent{Type: StreamEventTextEnd, TextID: "text_1"})
					resp := Response{Provider: "openai", Model: "m", Message: Assistant("Hello"), Finish: FinishReason{Reason: "stop"}}
					rp := resp
					st.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &resp.Finish, Response: &rp})
					cancel()
				}()
				_ = sctx
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var deltas []string
	var kinds []StreamEventType
	for ev := range res.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == StreamEventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("deltas: %q", strings.Join(deltas, ""))
	}

	gotResp, err := res.Response()
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if gotResp == nil || strings.TrimSpace(gotResp.Text()) != "Hello" {
		t.Fatalf("final response: %+v", gotResp)
	}

	foundStart := false
	foundFinish := false
	for _, k := range kinds {
		if k == StreamEventStreamStart {
			foundStart = true
		}
		if k == StreamEventFinish {
			foundFinish = true
		}
	}
	if !foundStart || !foundFinish {
		t.Fatalf("expected STREAM_START and FINISH (kinds=%v)", kinds)
	}
}

func TestStreamGenerate_ToolLoop_EmitsStepFinishAndContinues(t *testing.T) {
	c := NewClient()
	call := ToolCallData{ID: "c1", Name: "t1", Arguments: json.RawMessage(`{"x":1}`), Type: "function"}

	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			// Step 1: tool call
			func(ctx context.Context, req Request) (Stream, error) {
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
					st.Send(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{ID: call.ID, Name: call.Name, Arguments: call.Arguments, Type: "function"}})
					st.Send(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &call})
					resp := Response{
						Provider: "openai",
						Model:    "m",
						Message: Message{Role: RoleAssistant, Content: []ContentPart{
							{Kind: ContentToolCall, ToolCall: &call},
						}},
						Finish: FinishReason{Reason: "tool_calls"},
					}
					rp := resp
					st.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &resp.Finish, Response: &rp})
					cancel()
				}()
				_ = sctx

				// No tool results should be present in the initial request.
				for _, m := range req.Messages {
					if m.Role == RoleTool {
						t.Fatalf("unexpected tool result in step-1 request: %+v", m)
					}
				}
				return st, nil
			},
			// Step 2: final text
			func(ctx context.Context, req Request) (Stream, error) {
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "text_1"})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: "ok"})
					st.Send(StreamEvent{Type: StreamEventTextEnd, TextID: "text_1"})
					resp := Response{Provider: "openai", Model: "m", Message: Assistant("ok"), Finish: FinishReason{Reason: "stop"}}
					rp := resp
					st.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &resp.Finish, Response: &rp})
					cancel()
				}()
				_ = sctx

				// Step 2 request must include tool results for all tool calls from step 1.
				foundToolResult := false
				for _, m := range req.Messages {
					if m.Role != RoleTool {
						continue
					}
					for _, p := range m.Content {
						if p.Kind == ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "c1" {
							foundToolResult = true
						}
					}
				}
				if !foundToolResult {
					t.Fatalf("expected tool result message in step-2 request; msgs=%+v", req.Messages)
				}
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	rounds := 1
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:        c,
		Model:         "m",
		Provider:      "openai",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{
			{
				Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
				Execute: func(ctx context.Context, args any) (any, error) {
					_ = ctx
					_ = args
					return "done", nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var kinds []StreamEventType
	for ev := range res.Events() {
		kinds = append(kinds, ev.Type)
	}

	stepFinish := false
	finalFinish := false
	for _, k := range kinds {
		if k == StreamEventStepFinish {
			stepFinish = true
		}
		if k == StreamEventFinish {
			finalFinish = true
		}
	}
	if !stepFinish {
		t.Fatalf("expected STEP_FINISH event (kinds=%v)", kinds)
	}
	if !finalFinish {
		t.Fatalf("expected final FINISH event (kinds=%v)", kinds)
	}

	gotResp, err := res.Response()
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if gotResp == nil || strings.TrimSpace(gotResp.Text()) != "ok" {
		t.Fatalf("final response: %+v", gotResp)
	}

	if got := len(a.Requests()); got != 2 {
		t.Fatalf("adapter stream calls: got %d want 2", got)
	}
}

func TestStreamGenerate_PassiveToolCall_StopsWithoutStepFinish(t *testing.T) {
	c := NewClient()
	call := ToolCallData{ID: "c1", Name: "t1", Arguments: json.RawMessage(`{}`), Type: "function"}

	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
					st.Send(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &call})
					resp := Response{
						Provider: "openai",
						Model:    "m",
						Message: Message{Role: RoleAssistant, Content: []ContentPart{
							{Kind: ContentToolCall, ToolCall: &call},
						}},
						Finish: FinishReason{Reason: "tool_calls"},
					}
					rp := resp
					st.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &resp.Finish, Response: &rp})
					cancel()
				}()
				_ = sctx
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
		Tools: []Tool{
			// Defined but no execute handler => passive tool.
			{Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var kinds []StreamEventType
	for ev := range res.Events() {
		kinds = append(kinds, ev.Type)
	}
	for _, k := range kinds {
		if k == StreamEventStepFinish {
			t.Fatalf("did not expect STEP_FINISH for passive tool calls (kinds=%v)", kinds)
		}
	}
}

func TestStreamGenerate_DoesNotRetryAfterPartialDataDelivered(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				_ = req
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "text_1"})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: "hi"})
					st.Send(StreamEvent{Type: StreamEventError, Err: NewStreamError("openai", "boom")})
					cancel()
				}()
				_ = sctx
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
		RetryPolicy: &RetryPolicy{
			MaxRetries: 2,
			BaseDelay:  1 * time.Millisecond,
			MaxDelay:   1 * time.Millisecond,
			Jitter:     false,
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			_ = ctx
			_ = d
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	var sawError bool
	for ev := range res.Events() {
		if ev.Type == StreamEventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("expected ERROR event")
	}
	if got := len(a.Requests()); got != 1 {
		t.Fatalf("expected exactly 1 stream attempt; got %d", got)
	}
	_, rerr := res.Response()
	if rerr == nil {
		t.Fatalf("expected Response() error")
	}
	var se *StreamError
	if !errors.As(rerr, &se) {
		t.Fatalf("expected StreamError, got %T (%v)", rerr, rerr)
	}
}

func TestStreamGenerate_Cancellation_EmitsAbortError(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				_ = req
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					<-sctx.Done()
				}()
				return st, nil
			},
		},
	}
	c.Register(a)

	prompt := "hi"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close()

	seenStart := false
	seenAbort := false
	for ev := range res.Events() {
		if ev.Type == StreamEventStreamStart {
			seenStart = true
			cancel()
		}
		if ev.Type == StreamEventError {
			var ae *AbortError
			if errors.As(ev.Err, &ae) {
				seenAbort = true
			}
		}
	}
	if !seenStart {
		t.Fatalf("expected STREAM_START event")
	}
	if !seenAbort {
		t.Fatalf("expected ERROR event with AbortError")
	}

	_, rerr := res.Response()
	if rerr == nil {
		t.Fatalf("expected Response() error")
	}
	var ae *AbortError
	if !errors.As(rerr, &ae) {
		t.Fatalf("expected AbortError, got %T (%v)", rerr, rerr)
	}
}
