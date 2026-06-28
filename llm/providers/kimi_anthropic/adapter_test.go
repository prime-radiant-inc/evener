package kimi_anthropic

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
	"primeradiant.com/serf/llm/providers/kimicoding"
)

func TestAdapter_Name(t *testing.T) {
	a := providerfwd.NewAnthropic("", providerName, nil)
	if a.Name() != "kimi-anthropic" {
		t.Fatalf("Name() = %q, want kimi-anthropic", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToAnthropicInner(t *testing.T) {
	// The kimi-anthropic adapter wraps anthropic pointed at the Kimi coding
	// plan's Anthropic-compatible endpoint. Verify it delegates Complete and
	// the server sees Anthropic-format requests at /v1/messages with x-api-key.
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
  "model": "kimi-for-coding",
  "content": [{"type":"text","text":"Hello from Kimi"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "test-kimi-key", srv.Client())

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "kimi-for-coding",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "Hello from Kimi" {
		t.Fatalf("text: %q", resp.Text())
	}
	// Should hit the Anthropic Messages API path.
	if gotPath != "/v1/messages" {
		t.Fatalf("path: %q, want /v1/messages", gotPath)
	}
	// Should use x-api-key header (Anthropic style).
	if gotAPIKey != "test-kimi-key" {
		t.Fatalf("x-api-key: %q", gotAPIKey)
	}
	// KA-1: verify the request body contains the expected messages and model.
	if gotBody == nil {
		t.Fatal("request body was empty or not valid JSON")
	}
	if gotBody["messages"] == nil {
		t.Fatal("gotBody[\"messages\"] is nil — messages not sent in request body")
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("gotBody[\"messages\"] = %v, want non-empty slice", gotBody["messages"])
	}
	firstMsg, _ := msgs[0].(map[string]any)
	if firstMsg["role"] != "user" {
		t.Fatalf("messages[0].role = %v, want \"user\"", firstMsg["role"])
	}
	if gotBody["model"] != "kimi-for-coding" {
		t.Fatalf("gotBody[\"model\"] = %v, want \"kimi-for-coding\"", gotBody["model"])
	}
}

func TestAdapter_Stream_DelegatesToAnthropicInner(t *testing.T) {
	var gotPath string
	var gotAPIKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"kimi-for-coding","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
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
		Model:    "kimi-for-coding",
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
	// KA-3: verify path and API key for the streaming code path.
	if gotPath != "/v1/messages" {
		t.Fatalf("stream path: %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "test-key" {
		t.Fatalf("stream x-api-key: %q, want \"test-key\"", gotAPIKey)
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://api.kimi.com/coding" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

// TestClient_ListModels_Forwards verifies that the kimi-anthropic adapter
// exposes ListModels (via its anthropic backing) so that llm.Client.ListModels
// reaches the provider's Anthropic-style /v1/models endpoint.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-for-coding","display_name":"K2.7 Code"}],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "k", srv.Client())

	if _, ok := llm.ProviderAdapter(a).(llm.ModelLister); !ok {
		t.Fatal("kimi-anthropic adapter does not implement llm.ModelLister — ListModels not promoted")
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
	if models[0].ID != "kimi-for-coding" {
		t.Fatalf("model ID = %q, want kimi-for-coding", models[0].ID)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kimi", APIKey: "k"})
	if a.Name() != "kimi" {
		t.Fatalf("Name() = %q, want kimi", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kimi", APIKey: "k"})
	if a.BaseURL != defaultBaseURL {
		t.Fatalf("backing BaseURL = %q, want %q", a.BaseURL, defaultBaseURL)
	}
}

func TestAdapter_AnnouncesCodingAgentUserAgent(t *testing.T) {
	var gotUA string
	var gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","model":"kimi-for-coding","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(InstanceParams{Name: "kimi", BaseURL: srv.URL, APIKey: "secret-kimi-key"})
	if _, err := a.Complete(context.Background(), llm.Request{Model: "kimi-for-coding", Messages: []llm.Message{llm.User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotUA != kimicoding.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, kimicoding.UserAgent)
	}
	// KA-2: verify APIKey from InstanceParams reaches the outgoing x-api-key header.
	if gotAPIKey != "secret-kimi-key" {
		t.Fatalf("x-api-key = %q, want \"secret-kimi-key\"", gotAPIKey)
	}
}
