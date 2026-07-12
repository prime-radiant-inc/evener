package llm

import (
	"context"
	"errors"
	"testing"
)

func FuzzGenerateObjectResidual(f *testing.F) {
	for i := byte(0); i < 11; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		t.Run("start error", func(t *testing.T) {
			streamGenerateObjectStartMu.Lock()
			old := streamGenerateObjectStart
			streamGenerateObjectStart = func(context.Context, GenerateOptions) (*StreamResult, error) {
				return nil, errors.New("start failed")
			}
			streamGenerateObjectStartMu.Unlock()
			t.Cleanup(func() {
				streamGenerateObjectStartMu.Lock()
				streamGenerateObjectStart = old
				streamGenerateObjectStartMu.Unlock()
			})
			if _, err := StreamGenerateObject(context.Background(), GenerateObjectOptions{
				GenerateOptions: GenerateOptions{Model: "m", Messages: []Message{User("x")}},
				Schema:          map[string]any{"type": "object"},
			}); err == nil {
				t.Fatal("injected start error was lost")
			}
		})
		prompt := "object"
		opts := func(schema map[string]any, adapter ProviderAdapter) GenerateObjectOptions {
			c := NewClient()
			c.Register(adapter)
			return GenerateObjectOptions{GenerateOptions: GenerateOptions{
				Client: c, Provider: adapter.Name(), Model: "m", Prompt: &prompt, Sleep: noSleep,
			}, Schema: schema}
		}
		valid := map[string]any{"type": "object"}

		switch selector % 11 {
		case 0:
			_, _ = GenerateObject(context.Background(), GenerateObjectOptions{})
			_, _ = StreamGenerateObject(context.Background(), GenerateObjectOptions{})
		case 1:
			bad := map[string]any{"type": "not-a-json-schema-type"}
			_, _ = GenerateObject(context.Background(), opts(bad, residualObjectAdapter{name: "obj", text: `{}`}))
			_, _ = StreamGenerateObject(context.Background(), opts(bad, residualObjectAdapter{name: "obj", text: `{}`}))
		case 2:
			a := residualObjectAdapter{name: "obj", err: errors.New("provider failed")}
			_, _ = GenerateObject(context.Background(), opts(valid, a))
			_, _ = StreamGenerateObject(context.Background(), opts(valid, a))
		case 3:
			_, _ = GenerateObject(context.Background(), opts(valid, residualObjectAdapter{name: "obj", text: `[]`}))
		case 4:
			r, err := StreamGenerateObject(context.Background(), opts(valid, residualObjectAdapter{name: "obj", text: `[]`}))
			if err == nil {
				for range r.Events() {
				}
				_, _ = r.Response()
			}
		case 5:
			r, err := StreamGenerateObject(context.Background(), opts(valid, residualObjectAdapter{name: "obj", responseOnly: true, text: `{}`}))
			if err == nil {
				for range r.Events() {
				}
				_ = r.Output()
				_, _ = r.Response()
			}
		case 6:
			var r *StreamObjectResult
			_ = r.Output()
			_, _ = r.Response()
		case 7:
			_ = tryParsePartialJSON(`{"a":"x\\`)
			_ = tryParsePartialJSON(`{"a":[]`)
		case 8:
			for _, raw := range []string{"", "{", `{}`} {
				tc := ToolCallData{Arguments: []byte(raw)}
				_ = tc.Parse()
			}
		case 9:
			_ = ClampReasoningEffort("high", []string{"bogus", "none"})
		case 10:
			r, err := StreamGenerateObject(context.Background(), opts(valid, residualObjectAdapter{name: "obj", err: errors.New("stream failed"), midError: true}))
			if err == nil {
				for range r.Events() {
				}
				_, _ = r.Response()
			}
		}
	})
}

type residualObjectAdapter struct {
	name         string
	text         string
	err          error
	responseOnly bool
	midError     bool
}

func (a residualObjectAdapter) Name() string { return a.name }
func (a residualObjectAdapter) Complete(context.Context, Request) (Response, error) {
	if a.err != nil {
		return Response{}, a.err
	}
	return Response{Message: Assistant(a.text), Finish: FinishReason{Reason: FinishReasonStop}}, nil
}
func (a residualObjectAdapter) Stream(context.Context, Request) (Stream, error) {
	if a.err != nil && !a.midError {
		return nil, a.err
	}
	st := NewChanStream(nil)
	go func() {
		defer st.CloseSend()
		if a.midError {
			st.Send(StreamEvent{Type: StreamEventError, Err: a.err})
			return
		}
		if !a.responseOnly {
			st.Send(StreamEvent{Type: StreamEventTextDelta, Delta: a.text})
		}
		resp := Response{Message: Assistant(a.text), Finish: FinishReason{Reason: FinishReasonStop}}
		st.Send(StreamEvent{Type: StreamEventFinish, Response: &resp, FinishReason: &resp.Finish})
	}()
	return st, nil
}
