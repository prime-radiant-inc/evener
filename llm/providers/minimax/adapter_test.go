package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
)

func TestAdapter_Name(t *testing.T) {
	a := providerfwd.NewAnthropic("", providerName, nil)
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
	// Body must be Anthropic-format: "messages", "model", and "max_tokens" required.
	if gotBody["messages"] == nil {
		t.Fatal("request body missing 'messages' field — not Anthropic format")
	}
	if gotBody["model"] != "MiniMax-M2.7" {
		t.Fatalf("request body model = %v, want MiniMax-M2.7", gotBody["model"])
	}
	if gotBody["max_tokens"] == nil {
		t.Fatal("request body missing 'max_tokens' field — not Anthropic format")
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

	var gotText strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			gotText.WriteString(ev.Delta)
		}
	}
	if gotText.String() != "streamed" {
		t.Fatalf("streamed text: %q", gotText.String())
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

// TestClient_ListModels_Forwards verifies that the minimax adapter exposes
// ListModels (via its anthropic backing) so that llm.Client.ListModels
// reaches the provider's Anthropic-style /v1/models endpoint.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"MiniMax-M2.7","display_name":"MiniMax M2.7"}],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "k", srv.Client())

	if _, ok := llm.ProviderAdapter(a).(llm.ModelLister); !ok {
		t.Fatal("minimax adapter does not implement llm.ModelLister — ListModels not promoted")
	}

	c := llm.NewClient()
	c.Register(a)

	models, err := c.ListModels(context.Background(), a.Name())
	if err != nil {
		t.Fatalf("Client.ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID != "MiniMax-M2.7" {
		t.Fatalf("model ID = %q, want MiniMax-M2.7", models[0].ID)
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
	if a.BaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("backing BaseURL = %q, want %q", a.BaseURL, "https://api.minimax.io/anthropic")
	}
}
