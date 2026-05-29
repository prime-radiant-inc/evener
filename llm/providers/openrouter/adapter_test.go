package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestAdapter_Name(t *testing.T) {
	a := &adapter{inner: &openaicompat.Adapter{}}
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

	a := &adapter{inner: &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

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
	if a.inner.BaseURL != defaultBaseURL {
		t.Fatalf("inner.BaseURL = %q, want %q", a.inner.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_DefaultQuirks(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "oc", APIKey: "k"})
	if !a.inner.Quirks.TranslateMaxToXHigh {
		t.Fatal("expected openrouter quirks (TranslateMaxToXHigh) to be applied")
	}
}
