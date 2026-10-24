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
