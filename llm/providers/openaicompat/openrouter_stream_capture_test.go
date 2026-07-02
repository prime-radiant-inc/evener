package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"primeradiant.com/serf/llm"
)

// TestReplay_CapturedOpenRouterStream replays a REAL OpenRouter SSE response
// (anthropic/claude-sonnet-4.5 routed via Amazon Bedrock, captured live
// 2026-07-02 with reasoning:{effort:"xhigh"} and a tool present) through the
// adapter and pins that its wire shape — delta.reasoning strings plus
// reasoning_details [{type:"reasoning.text"}] — surfaces as thinking. Guards
// against OpenRouter stream-shape drift breaking reasoning silently.
func TestReplay_CapturedOpenRouterStream(t *testing.T) {
	raw, err := os.ReadFile("testdata/openrouter_anthropic_stream_2026-07-02.sse")
	if err != nil {
		t.Fatalf("read capture fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(raw) //nolint:errcheck
	}))
	defer srv.Close()
	a := NewForInstance(OpenAICompatInstanceParams{Name: "openrouter", BaseURL: srv.URL, APIKey: "k", Quirks: QuirksPreset("openrouter")})
	effort := "high"
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:           "anthropic/claude-sonnet-4.5",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	thinking, text := 0, 0
	var final *llm.Response
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventReasoningDelta:
			thinking += len(ev.ReasoningDelta)
		case llm.StreamEventTextDelta:
			text += len(ev.Delta)
		case llm.StreamEventFinish:
			final = ev.Response
		}
	}
	finalThinking := 0
	if final != nil {
		for _, p := range final.Message.Content {
			if p.Kind == llm.ContentThinking && p.Thinking != nil {
				finalThinking += len(p.Thinking.Text)
			}
		}
	}
	t.Logf("thinking deltas=%d text deltas=%d final thinking=%d", thinking, text, finalThinking)
	if thinking == 0 || finalThinking == 0 {
		t.Fatalf("captured real OpenRouter stream produced no thinking (deltas=%d final=%d)", thinking, finalThinking)
	}
}
