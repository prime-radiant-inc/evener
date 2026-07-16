package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestAdaptiveResponsesRequestCarriesCredentialHeaders(t *testing.T) {
	type capturedRequest struct {
		path   string
		header http.Header
	}
	captured := make(chan capturedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- capturedRequest{path: r.URL.Path, header: r.Header.Clone()}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_compat", "model": "gpt-4o",
  "output": [{"type": "message", "content": [{"type": "output_text", "text": "ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(OpenAICompatInstanceParams{
		BaseURL:           srv.URL,
		APIKey:            "provider-key",
		Adaptive:          true,
		Headers:           map[string]string{"X-Visible": "visible"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret", "Authorization": "configured-auth"},
	})
	a.Client = srv.Client()
	if _, err := a.Complete(context.Background(), llm.Request{
		Model:    "gpt-4o",
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := <-captured
	if got.path != "/responses" {
		t.Fatalf("request path = %q, want adaptive Responses route", got.path)
	}
	for name, want := range map[string]string{
		"X-Visible":     "visible",
		"X-Gateway-Key": "secret",
		"Authorization": "Bearer provider-key",
		"Content-Type":  "application/json",
	} {
		if values := got.header.Values(name); len(values) != 1 || values[0] != want {
			t.Errorf("%s values = %q, want exactly [%q]", name, values, want)
		}
	}
}
