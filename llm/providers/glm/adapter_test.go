package glm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestAdapter_Name(t *testing.T) {
	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{})
	if a.Name() != "glm" {
		t.Fatalf("Name() = %q, want glm", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "glm-5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Quirks:  openaicompat.QuirksPreset("glm-5"),
	})

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "glm-5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("text: %q", resp.Text())
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

// TestClient_ListModels_Forwards verifies that the glm adapter exposes
// ListModels (via its openaicompat backing) so that llm.Client.ListModels
// reaches the provider's /models endpoint. The glm wrapper does not rewrite
// the model provider stamp, so the backing "openai-compatible" name is
// expected on each ModelInfo.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5"},{"id":"glm-5-air"}]}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{BaseURL: srv.URL, Client: srv.Client()})

	if _, ok := llm.ProviderAdapter(a).(llm.ModelLister); !ok {
		t.Fatal("glm adapter does not implement llm.ModelLister — ListModels not promoted")
	}

	c := llm.NewClient()
	c.Register(a)

	models, err := c.ListModels(context.Background(), a.Name())
	if err != nil {
		t.Fatalf("Client.ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "glm-5" {
		t.Fatalf("models[0].ID = %q, want glm-5", models[0].ID)
	}
	if models[1].ID != "glm-5-air" {
		t.Fatalf("models[1].ID = %q, want glm-5-air", models[1].ID)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "gc", APIKey: "k"})
	if a.Name() != "gc" {
		t.Fatalf("Name() = %q, want gc", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "gc", APIKey: "k"})
	if a.BaseURL != defaultBaseURL {
		t.Fatalf("backing BaseURL = %q, want %q", a.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_DefaultQuirks(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "gc", APIKey: "k"})
	if !a.Quirks.StripEmptyContent {
		t.Fatal("glm quirks: StripEmptyContent should be true")
	}
	if !a.Quirks.ToolChoiceAutoOnly {
		t.Fatal("glm quirks: ToolChoiceAutoOnly should be true")
	}
	if a.Quirks.MaxStopSequences != 1 {
		t.Fatalf("glm quirks: MaxStopSequences = %d, want 1", a.Quirks.MaxStopSequences)
	}
	if !a.Quirks.NoJSONSchema {
		t.Fatal("glm quirks: NoJSONSchema should be true")
	}
}
