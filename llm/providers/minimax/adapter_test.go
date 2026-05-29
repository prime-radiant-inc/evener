package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestAdapter_Name(t *testing.T) {
	a := &adapter{inner: nil}
	if a.Name() != "minimax" {
		t.Fatalf("Name() = %q, want minimax", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToAnthropicInner(t *testing.T) {
	// The minimax adapter wraps anthropic pointed at MiniMax's Anthropic-compatible endpoint.
	// Verify it delegates Complete and the server sees Anthropic-format requests.
	var gotPath string
	var gotAPIKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "MiniMax-M2.7",
  "content": [{"type":"text","text":"Hello from MiniMax"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "test-minimax-key", srv.Client())

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "MiniMax-M2.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "Hello from MiniMax" {
		t.Fatalf("text: %q", resp.Text())
	}
	// Should hit Anthropic Messages API path.
	if gotPath != "/v1/messages" {
		t.Fatalf("path: %q, want /v1/messages", gotPath)
	}
	// Should use x-api-key header (Anthropic style).
	if gotAPIKey != "test-minimax-key" {
		t.Fatalf("x-api-key: %q", gotAPIKey)
	}
}

func TestAdapter_Stream_DelegatesToAnthropicInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"MiniMax-M2.7","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(ev + "\n\n"))
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "test-key", srv.Client())

	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "MiniMax-M2.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var gotText string
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			gotText += ev.Delta
		}
	}
	if gotText != "streamed" {
		t.Fatalf("streamed text: %q", gotText)
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "mm", APIKey: "k"})
	if a.Name() != "mm" {
		t.Fatalf("Name() = %q, want mm", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "mm", APIKey: "k"})
	if a.inner.BaseURL != defaultBaseURL {
		t.Fatalf("inner.BaseURL = %q, want %q", a.inner.BaseURL, defaultBaseURL)
	}
}
