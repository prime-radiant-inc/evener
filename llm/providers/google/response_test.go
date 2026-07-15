package google

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestGeminiStreamSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	sse := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}` + "\n\n" +
		`data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
	s := llm.NewChanStream(cancel)
	a := &Adapter{}
	go a.decodeStream(ctx, cancel, resp, s, llm.Request{Model: "test"}, nil, "http://test")
	var got string
	for ev := range s.Events() {
		if ev.ToolCall != nil {
			got = ev.ToolCall.ID
		}
	}
	if err := identifier.ValidateSyntheticCallID(got); err != nil {
		t.Fatalf("stream synthetic ID %q: %v", got, err)
	}
}

func TestGeminiSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	r := fromGeminiResponse(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{
				"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "x"}},
			}}},
		}},
	}, "test")
	calls := r.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if err := identifier.ValidateSyntheticCallID(calls[0].ID); err != nil {
		t.Fatalf("synthetic ID %q: %v", calls[0].ID, err)
	}
}
