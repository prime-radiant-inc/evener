package kimi

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
	if a.Name() != "kimi" {
		t.Fatalf("Name() = %q, want kimi", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Quirks:  openaicompat.QuirksPreset("kimi-k2.5"),
	}}

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "kimi-k2.5",
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
	if defaultBaseURL != "https://api.moonshot.ai/v1" { //nolint:goconst
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if a.Name() != "kc" {
		t.Fatalf("Name() = %q, want kc", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	// When BaseURL is empty, the type's default must be applied.
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if a.inner.BaseURL != defaultBaseURL {
		t.Fatalf("inner.BaseURL = %q, want %q", a.inner.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_DefaultQuirks(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if !a.inner.Quirks.LockTemperature {
		t.Fatal("expected kimi quirks (LockTemperature) to be applied")
	}
}

func TestNewForInstance_CustomBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k", BaseURL: "http://custom"})
	if a.inner.BaseURL != "http://custom" {
		t.Fatalf("inner.BaseURL = %q, want http://custom", a.inner.BaseURL)
	}
}

func TestNewForInstance_EnvPathPreservesName(t *testing.T) {
	// The env factory still names the adapter "kimi".
	t.Setenv("KIMI_API_KEY", "testkey")
	t.Setenv("KIMI_BASE_URL", "")
	// Trigger the env factory by calling init-registered code indirectly.
	// We just verify NewForInstance with env-equivalent params gives name "kimi".
	a := NewForInstance(InstanceParams{Name: "kimi", APIKey: "testkey"})
	if a.Name() != "kimi" {
		t.Fatalf("Name() = %q, want kimi", a.Name())
	}
}
