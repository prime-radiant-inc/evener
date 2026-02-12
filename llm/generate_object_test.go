package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestGenerateObject_Success(t *testing.T) {
	c := NewClient()
	c.Register(&scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) {
				return Response{Message: Assistant(`{"name":"Alice","age":30}`)}, nil
			},
		},
	})

	prompt := "extract"
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
		},
		"required": []string{"name", "age"},
	}
	res, err := GenerateObject(context.Background(), GenerateObjectOptions{
		GenerateOptions: GenerateOptions{
			Client: c,
			Model:  "m",
			Prompt: &prompt,
		},
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("GenerateObject: %v", err)
	}
	m, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("output type: %T", res.Output)
	}
	if m["name"] != "Alice" {
		t.Fatalf("name: %v", m["name"])
	}
	if _, ok := m["age"].(json.Number); !ok {
		t.Fatalf("age type: %T (%v)", m["age"], m["age"])
	}
}

func TestGenerateObject_ParseFailure_RaisesNoObjectGeneratedError(t *testing.T) {
	c := NewClient()
	c.Register(&scriptedAdapter{
		name: "openai",
		steps: []func(req Request) (Response, error){
			func(req Request) (Response, error) { return Response{Message: Assistant("not json")}, nil },
		},
	})

	prompt := "extract"
	_, err := GenerateObject(context.Background(), GenerateObjectOptions{
		GenerateOptions: GenerateOptions{
			Client: c,
			Model:  "m",
			Prompt: &prompt,
		},
		Schema: map[string]any{"type": "object"},
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var noe *NoObjectGeneratedError
	if !errors.As(err, &noe) {
		t.Fatalf("expected NoObjectGeneratedError, got %T (%v)", err, err)
	}
	if noe.RawText == "" {
		t.Fatalf("expected RawText to be set")
	}
}

func TestStreamGenerateObject_Success(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "text_1"})
					// Stream JSON in chunks
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: `{"na`})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: `me":"Al`})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: `ice","ag`})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: `e":30}`})
					st.Send(StreamEvent{Type: StreamEventTextEnd, TextID: "text_1"})
					resp := Response{Provider: "openai", Model: "m", Message: Assistant(`{"name":"Alice","age":30}`), Finish: FinishReason{Reason: "stop"}}
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

	prompt := "extract"
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
		},
		"required": []string{"name", "age"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerateObject(ctx, GenerateObjectOptions{
		GenerateOptions: GenerateOptions{
			Client:   c,
			Model:    "m",
			Provider: "openai",
			Prompt:   &prompt,
		},
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("StreamGenerateObject: %v", err)
	}
	defer res.Close() //nolint:errcheck

	var objectDeltas int
	for ev := range res.Events() {
		if ev.Type == StreamEventObjectDelta {
			objectDeltas++
		}
	}
	if objectDeltas == 0 {
		t.Fatalf("expected at least one OBJECT_DELTA event")
	}

	resp, err := res.Response()
	if err != nil {
		t.Fatalf("Response: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response")
	}

	output := res.Output()
	if output == nil {
		t.Fatalf("expected non-nil output")
	}
	m, ok := output.(map[string]any)
	if !ok {
		t.Fatalf("output type: %T", output)
	}
	if m["name"] != "Alice" {
		t.Fatalf("name: %v", m["name"])
	}
	if _, ok := m["age"].(json.Number); !ok {
		t.Fatalf("age type: %T (%v)", m["age"], m["age"])
	}
}

func TestStreamGenerateObject_ParseFailure_ReturnsNoObjectGeneratedError(t *testing.T) {
	c := NewClient()
	a := &scriptedStreamAdapter{
		name: "openai",
		scripts: []func(ctx context.Context, req Request) (Stream, error){
			func(ctx context.Context, req Request) (Stream, error) {
				sctx, cancel := context.WithCancel(ctx)
				st := NewChanStream(cancel)
				go func() {
					defer st.CloseSend()
					st.Send(StreamEvent{Type: StreamEventStreamStart})
					st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "text_1"})
					st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "text_1", Delta: "not json at all"})
					st.Send(StreamEvent{Type: StreamEventTextEnd, TextID: "text_1"})
					resp := Response{Provider: "openai", Model: "m", Message: Assistant("not json at all"), Finish: FinishReason{Reason: "stop"}}
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

	prompt := "extract"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := StreamGenerateObject(ctx, GenerateObjectOptions{
		GenerateOptions: GenerateOptions{
			Client:   c,
			Model:    "m",
			Provider: "openai",
			Prompt:   &prompt,
		},
		Schema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("StreamGenerateObject: %v", err)
	}
	defer res.Close() //nolint:errcheck

	// Drain events
	for range res.Events() {
	}

	_, rerr := res.Response()
	if rerr == nil {
		t.Fatalf("expected error from Response()")
	}
	var noe *NoObjectGeneratedError
	if !errors.As(rerr, &noe) {
		t.Fatalf("expected NoObjectGeneratedError, got %T (%v)", rerr, rerr)
	}
	if noe.RawText == "" {
		t.Fatalf("expected RawText to be set")
	}
}
