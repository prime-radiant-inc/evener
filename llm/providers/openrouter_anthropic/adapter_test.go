package openrouter_anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
)

func TestAdapter_Name(t *testing.T) {
	a := providerfwd.NewAnthropic("", providerName, nil)
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
	if gotBody["model"] != "minimax/minimax-m2.7" {
		t.Fatalf("body model: %q, want minimax/minimax-m2.7", gotBody["model"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("body messages: empty or missing")
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
	if a.BaseURL != defaultBaseURL {
		t.Fatalf("backing BaseURL = %q, want %q", a.BaseURL, defaultBaseURL)
	}
}

// TestClient_ListModels_Forwards verifies that the openrouter-anthropic
// adapter exposes ListModels (via its anthropic backing) so that
// llm.Client.ListModels reaches OpenRouter's Anthropic-style /v1/models
// endpoint. This previously relied on a hand-forwarded ListModels method;
// concrete embedding now promotes it.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"minimax/minimax-m2.7","display_name":"MiniMax M2.7"}],"has_more":false}`))
	}))
	t.Cleanup(srv.Close)

	a := newTestAdapter(srv.URL, "k", srv.Client())

	c := llm.NewClient()
	c.Register(a)

	models, err := c.ListModels(context.Background(), a.Name())
	if err != nil {
		t.Fatalf("Client.ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID != "minimax/minimax-m2.7" {
		t.Fatalf("models[0].ID = %q, want minimax/minimax-m2.7", models[0].ID)
	}
	if models[0].DisplayName != "MiniMax M2.7" {
		t.Fatalf("models[0].DisplayName = %q, want MiniMax M2.7", models[0].DisplayName)
	}
	// Provider is inherited from the backing anthropic adapter.
	if models[0].Provider != "anthropic" {
		t.Fatalf("models[0].Provider = %q, want anthropic", models[0].Provider)
	}
}
