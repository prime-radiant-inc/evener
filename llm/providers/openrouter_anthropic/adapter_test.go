package openrouter_anthropic

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
	if a.Name() != "openrouter-anthropic" {
		t.Fatalf("Name() = %q, want openrouter-anthropic", a.Name())
	}
}

func TestAdapter_Complete_HitsCorrectPath(t *testing.T) {
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
  "model": "minimax/minimax-m2.7",
  "content": [{"type":"text","text":"Hello from OpenRouter"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 10, "output_tokens": 5}
}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "test-openrouter-key", srv.Client())

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "minimax/minimax-m2.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "Hello from OpenRouter" {
		t.Fatalf("text: %q", resp.Text())
	}
	// The Anthropic adapter appends /v1/messages to BaseURL.
	if gotPath != "/v1/messages" {
		t.Fatalf("path: %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "test-openrouter-key" {
		t.Fatalf("x-api-key: %q", gotAPIKey)
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	// The Anthropic adapter appends /v1/messages, so the base URL must NOT
	// include /v1 — otherwise the request hits /v1/v1/messages (404).
	if defaultBaseURL != "https://openrouter.ai/api" {
		t.Fatalf("defaultBaseURL: %q, want https://openrouter.ai/api", defaultBaseURL)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "ora", APIKey: "k"})
	if a.Name() != "ora" {
		t.Fatalf("Name() = %q, want ora", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "ora", APIKey: "k"})
	if a.inner.BaseURL != defaultBaseURL {
		t.Fatalf("inner.BaseURL = %q, want %q", a.inner.BaseURL, defaultBaseURL)
	}
}
