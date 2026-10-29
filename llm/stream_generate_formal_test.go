package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// A forwarded content payload commits this high-level stream to the attempt:
// another attempt would append to content that the caller has already observed.
func TestStreamGenerate_RetryAfterPublishedContent(t *testing.T) {
	tool := &ToolCallData{ID: "call_7", Name: "lookup", Type: "function", Arguments: []byte(`{"q":1}`)}
	cases := []struct {
		name    string
		prefix  []StreamEvent
		content StreamEvent
	}{
		{"text", []StreamEvent{{Type: StreamEventTextStart, TextID: "txt_7"}}, StreamEvent{Type: StreamEventTextDelta, TextID: "txt_7", Delta: "text-7"}},
		{"reasoning", []StreamEvent{{Type: StreamEventReasoningStart}}, StreamEvent{Type: StreamEventReasoningDelta, ReasoningDelta: "reason-7"}},
		{"tool arguments in delta", []StreamEvent{{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: tool.ID, Name: tool.Name, Type: tool.Type}}}, StreamEvent{Type: StreamEventToolCallDelta, ToolCall: tool}},
		{"complete tool arguments in start", nil, StreamEvent{Type: StreamEventToolCallStart, ToolCall: tool}},
		{"complete tool arguments in end", nil, StreamEvent{Type: StreamEventToolCallEnd, ToolCall: tool}},
		{"object delta", nil, StreamEvent{Type: StreamEventObjectDelta, ObjectDelta: map[string]any{"item": 7}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := append([]StreamEvent{{Type: StreamEventStreamStart}}, tc.prefix...)
			first = append(first, tc.content) // No FINISH: a retryable truncation.
			a := &scriptedStreamAdapter{name: "openai", scripts: []func(context.Context, Request) (Stream, error){
				formalScriptedEvents(first),
				formalScriptedEvents([]StreamEvent{{Type: StreamEventFinish, Response: &Response{Model: "m", Message: Assistant("settled")}}}),
			}}
			client := NewClient()
			client.Register(a)
			prompt := "prompt"
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := StreamGenerate(ctx, GenerateOptions{
				Client: client, Model: "m", Provider: "openai", Prompt: &prompt,
				RetryPolicy: &RetryPolicy{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
				Sleep:       func(context.Context, time.Duration) error { return nil },
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}
			defer result.Close() //nolint:errcheck
			var published []StreamEvent
			var terminalErrors []error
			var finishes int
			for ev := range result.Events() {
				if ev.Type == tc.content.Type {
					published = append(published, ev)
				}
				if ev.Type == StreamEventError {
					terminalErrors = append(terminalErrors, ev.Err)
				}
				if ev.Type == StreamEventFinish {
					finishes++
				}
			}
			response, responseErr := result.Response()
			if len(published) != 1 {
				t.Fatalf("published %s events = %d, want one", tc.content.Type, len(published))
			}
			got := published[0]
			if got.TextID != tc.content.TextID || got.Delta != tc.content.Delta || got.ReasoningDelta != tc.content.ReasoningDelta || !reflect.DeepEqual(got.ToolCall, tc.content.ToolCall) || !reflect.DeepEqual(got.ObjectDelta, tc.content.ObjectDelta) {
				t.Fatalf("published payload = %+v, want %+v", got, tc.content)
			}
			if count := len(a.Requests()); count != 1 {
				t.Errorf("provider requests = %d, want one after published content", count)
			}
			var streamErr *StreamError
			if !errors.As(responseErr, &streamErr) || response != nil {
				t.Errorf("Response() = (%+v, %T), want nil and StreamError", response, responseErr)
			}
			if len(terminalErrors) != 1 || !errors.Is(terminalErrors[0], responseErr) || !errors.As(terminalErrors[0], &streamErr) || finishes != 0 {
				t.Errorf("terminal events: errors=%v, finishes=%d, want one Response() StreamError and no FINISH", terminalErrors, finishes)
			}
		})
	}

	t.Run("control only retries", func(t *testing.T) {
		a := &scriptedStreamAdapter{name: "openai", scripts: []func(context.Context, Request) (Stream, error){
			formalScriptedEvents([]StreamEvent{{Type: StreamEventStreamStart}, {Type: StreamEventTextStart, TextID: "txt_7"}, {Type: StreamEventTextDelta, TextID: "txt_7"}, {Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: "call_7", Name: "lookup"}}}),
			formalScriptedEvents([]StreamEvent{{Type: StreamEventFinish, Response: &Response{Model: "m", Message: Assistant("settled")}}}),
		}}
		client := NewClient()
		client.Register(a)
		prompt := "prompt"
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := StreamGenerate(ctx, GenerateOptions{
			Client: client, Model: "m", Provider: "openai", Prompt: &prompt,
			RetryPolicy: &RetryPolicy{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			Sleep:       func(context.Context, time.Duration) error { return nil },
		})
		if err != nil {
			t.Fatalf("StreamGenerate: %v", err)
		}
		defer result.Close() //nolint:errcheck
		var finishes, errorsSeen int
		for ev := range result.Events() {
			switch ev.Type {
			case StreamEventFinish:
				finishes++
			case StreamEventError:
				errorsSeen++
			}
		}
		response, responseErr := result.Response()
		if responseErr != nil || response == nil || response.Text() != "settled" {
			t.Errorf("Response() = (%+v, %v), want successful second attempt", response, responseErr)
		}
		if count := len(a.Requests()); count != 2 || finishes != 1 || errorsSeen != 0 {
			t.Errorf("requests=%d, FINISH=%d, ERROR=%d, want 2, 1, 0", count, finishes, errorsSeen)
		}
	})
}

// The fake ends at the provider boundary; Client, ChanStream, retry and forwarding are real.
func formalScriptedEvents(events []StreamEvent) func(context.Context, Request) (Stream, error) {
	return func(context.Context, Request) (Stream, error) {
		st := NewChanStream(nil)
		for _, ev := range events {
			st.Send(ev)
		}
		st.CloseSend()
		return st, nil
	}
}

// A failed later model call cannot undo a completed tool round or turn its
// unsuccessful response into a successful final response.
func TestStreamGenerate_PreservesCompletedStepsOnFailure(t *testing.T) {
	for _, failure := range []string{"permanent open", "truncated stream", "cancelled second open"} {
		t.Run(failure, func(t *testing.T) {
			call := ToolCallData{ID: "call_17", Name: "lookup", Type: "function", Arguments: []byte(`{"key":17}`)}
			usage := Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
			first := Response{
				Provider: "openai", Model: "m",
				Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
				Finish:  FinishReason{Reason: FinishReasonToolCalls}, Usage: usage,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			enteredSecondOpen := make(chan struct{})
			permanent := ErrorFromHTTPStatus("openai", 401, "refused", nil, nil)
			second := func(ctx context.Context, req Request) (Stream, error) {
				switch failure {
				case "permanent open":
					return nil, permanent
				case "truncated stream":
					return formalScriptedEvents([]StreamEvent{{Type: StreamEventStreamStart}})(ctx, req)
				default:
					close(enteredSecondOpen)
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}
			a := &scriptedStreamAdapter{name: "openai", scripts: []func(context.Context, Request) (Stream, error){
				formalScriptedEvents([]StreamEvent{
					{Type: StreamEventStreamStart},
					{Type: StreamEventToolCallStart, ToolCall: &ToolCallData{ID: call.ID, Name: call.Name, Type: call.Type}},
					{Type: StreamEventToolCallEnd, ToolCall: &call},
					{Type: StreamEventFinish, FinishReason: &first.Finish, Response: &first, Usage: &usage},
				}),
				second,
			}}
			client := NewClient()
			client.Register(a)
			prompt := "prompt"
			rounds := 1
			executions := 0
			result, err := StreamGenerate(ctx, GenerateOptions{
				Client: client, Model: "m", Provider: "openai", Prompt: &prompt,
				MaxToolRounds: &rounds, RetryPolicy: &RetryPolicy{MaxRetries: 0},
				Tools: []Tool{{
					Definition: ToolDefinition{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "integer"}}}},
					Execute: func(_ context.Context, args any) (any, error) {
						executions++
						return "resolved-17", nil
					},
				}},
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}
			defer result.Close() //nolint:errcheck

			var stepFinishes, finishes int
			var terminalErrors []error
			for ev := range result.Events() {
				switch ev.Type {
				case StreamEventStepFinish:
					stepFinishes++
					if failure == "cancelled second open" {
						<-enteredSecondOpen
						cancel()
					}
				case StreamEventFinish:
					finishes++
				case StreamEventError:
					terminalErrors = append(terminalErrors, ev.Err)
				}
			}
			response, responseErr := result.Response()
			if response != nil || responseErr == nil {
				t.Errorf("Response() = (%+v, %v), want nil and terminal error", response, responseErr)
			}
			if stepFinishes != 1 || finishes != 0 || len(terminalErrors) != 1 {
				t.Errorf("STEP_FINISH=%d, FINISH=%d, ERROR=%d, want 1, 0, 1", stepFinishes, finishes, len(terminalErrors))
			}
			if len(terminalErrors) == 1 && !errors.Is(terminalErrors[0], responseErr) {
				t.Errorf("ERROR event = %v, Response error = %v", terminalErrors[0], responseErr)
			}
			switch failure {
			case "permanent open":
				var providerErr Error
				if !errors.As(responseErr, &providerErr) || providerErr.StatusCode() != 401 || providerErr.Retryable() {
					t.Errorf("Response error = %v, want permanent 401", responseErr)
				}
			case "truncated stream":
				if _, ok := errors.AsType[*StreamError](responseErr); !ok {
					t.Errorf("Response error = %v, want StreamError", responseErr)
				}
			default:
				if !errors.Is(responseErr, context.Canceled) {
					t.Errorf("Response error = %v, want cancellation", responseErr)
				}
			}
			if executions != 1 {
				t.Errorf("tool executions = %d, want 1", executions)
			}
			reqs := a.Requests()
			if len(reqs) != 2 {
				t.Fatalf("provider requests = %d, want 2", len(reqs))
			}
			for i, req := range reqs {
				if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" {
					t.Errorf("request %d tools = %+v, want lookup", i, req.Tools)
				}
			}
			var forwarded bool
			for _, msg := range reqs[1].Messages {
				for _, part := range msg.Content {
					if part.Kind == ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == "call_17" && part.ToolResult.Content == "resolved-17" {
						forwarded = true
					}
				}
			}
			if !forwarded {
				t.Error("second request lost completed lookup result")
			}
			steps := result.Steps()
			if len(steps) != 1 {
				t.Errorf("Steps() = %+v, want one completed tool step", steps)
			} else {
				if len(steps[0].ToolCalls) != 1 || steps[0].ToolCalls[0].ID != "call_17" {
					t.Errorf("completed step calls = %+v, want call_17", steps[0].ToolCalls)
				}
				if len(steps[0].ToolResults) != 1 || steps[0].ToolResults[0].ToolCallID != "call_17" || steps[0].ToolResults[0].Name != "lookup" || steps[0].ToolResults[0].Content != "resolved-17" || steps[0].ToolResults[0].IsError {
					t.Errorf("completed step results = %+v, want successful lookup call_17", steps[0].ToolResults)
				}
				if steps[0].Usage.InputTokens != 10 || steps[0].Usage.OutputTokens != 20 || steps[0].Usage.TotalTokens != 30 {
					t.Errorf("completed step usage = %+v, want 10/20/30", steps[0].Usage)
				}
			}
			got := result.TotalUsage()
			if got.InputTokens != 10 || got.OutputTokens != 20 || got.TotalTokens != 30 {
				t.Errorf("TotalUsage() = %+v, want 10/20/30", got)
			}
		})
	}
}

func TestStreamGenerate_CompletedAccountingAtToolRoundLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxRounds  int
		requests   int
		executions int
		wantUsage  Usage
	}{
		{"zero rounds", 0, 1, 0, Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}},
		{"exhausted after one round", 1, 2, 1, Usage{InputTokens: 20, OutputTokens: 40, TotalTokens: 60}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			makeResponse := func(id string) []StreamEvent {
				call := ToolCallData{ID: id, Name: "lookup", Type: "function", Arguments: []byte(`{}`)}
				resp := Response{
					Provider: "openai", Model: "m",
					Message: Message{Role: RoleAssistant, Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}},
					Finish:  FinishReason{Reason: FinishReasonToolCalls},
					Usage:   Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
				}
				return []StreamEvent{{Type: StreamEventFinish, Response: &resp, FinishReason: &resp.Finish}}
			}
			a := &scriptedStreamAdapter{name: "openai", scripts: []func(context.Context, Request) (Stream, error){
				formalScriptedEvents(makeResponse("call_1")),
				formalScriptedEvents(makeResponse("call_2")),
			}}
			client := NewClient()
			client.Register(a)
			prompt := "prompt"
			executions := 0
			result, err := StreamGenerate(context.Background(), GenerateOptions{
				Client: client, Model: "m", Provider: "openai", Prompt: &prompt,
				MaxToolRounds: &tc.maxRounds,
				Tools: []Tool{{Definition: ToolDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}},
					Execute: func(context.Context, any) (any, error) { executions++; return "ok", nil }}},
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}
			defer result.Close() //nolint:errcheck
			for range result.Events() {
			}
			response, responseErr := result.Response()
			if responseErr != nil || response == nil || response.Finish.Reason != FinishReasonToolCalls {
				t.Errorf("Response() = (%+v, %v), want bounded tool-call completion", response, responseErr)
			}
			if got := len(a.Requests()); got != tc.requests || executions != tc.executions {
				t.Errorf("requests=%d, tool executions=%d, want %d, %d", got, executions, tc.requests, tc.executions)
			}
			steps := result.Steps()
			if len(steps) != tc.requests {
				t.Fatalf("Steps() = %+v, want %d", steps, tc.requests)
			}
			for i, step := range steps {
				if len(step.ToolCalls) != 1 || step.ToolCalls[0].ID != []string{"call_1", "call_2"}[i] {
					t.Errorf("step %d calls = %+v", i, step.ToolCalls)
				}
				wantResults := 0
				if i == 0 && tc.executions == 1 {
					wantResults = 1
				}
				if len(step.ToolResults) != wantResults {
					t.Errorf("step %d results = %+v, want %d", i, step.ToolResults, wantResults)
				}
			}
			got := result.TotalUsage()
			if got.InputTokens != tc.wantUsage.InputTokens || got.OutputTokens != tc.wantUsage.OutputTokens || got.TotalTokens != tc.wantUsage.TotalTokens {
				t.Errorf("TotalUsage() = %+v, want %+v", got, tc.wantUsage)
			}
		})
	}
}
