package glm

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

	a := &adapter{inner: &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Quirks:  openaicompat.QuirksPreset("glm-5"),
	}}

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
