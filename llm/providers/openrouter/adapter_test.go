package openrouter

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
	if a.Name() != "openrouter" {
		t.Fatalf("Name() = %q, want openrouter", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "openrouter/auto",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "openrouter/auto",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("text: %q", resp.Text())
	}
}

func TestAdapter_Quirks(t *testing.T) {
	// OpenRouter should have minimal quirks — no restrictions, but does translate max→xhigh.
	quirks := openaicompat.QuirksPreset("openrouter")
	if quirks.LockTemperature || quirks.ToolChoiceAutoOnly || quirks.StripEmptyContent {
		t.Fatal("openrouter should not have restrictive quirks")
	}
	if !quirks.TranslateMaxToXHigh {
		t.Fatal("openrouter should translate max to xhigh")
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "oc", APIKey: "k"})
	if a.Name() != "oc" {
		t.Fatalf("Name() = %q, want oc", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "oc", APIKey: "k"})
	if a.Adapter.BaseURL != defaultBaseURL {
		t.Fatalf("backing BaseURL = %q, want %q", a.Adapter.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_DefaultQuirks(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "oc", APIKey: "k"})
	if !a.Adapter.Quirks.TranslateMaxToXHigh {
		t.Fatal("expected openrouter quirks (TranslateMaxToXHigh) to be applied")
	}
}

// TestClient_ListModels_Forwards verifies that the openrouter adapter exposes
// ListModels (via its openaicompat backing) so that llm.Client.ListModels
// reaches OpenRouter's /models endpoint. This previously relied on a
// hand-forwarded ListModels method; concrete embedding now promotes it.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openrouter/auto"},{"id":"anthropic/claude-3.5"}]}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{BaseURL: srv.URL, Client: srv.Client()})

	if _, ok := llm.ProviderAdapter(a).(llm.ModelLister); !ok {
		t.Fatal("openrouter adapter does not implement llm.ModelLister — ListModels not promoted")
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
}
