package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestAdapter_Name(t *testing.T) {
	a := &adapter{inner: &openaicompat.Adapter{}}
	if a.Name() != "ollama" {
		t.Fatalf("Name() = %q, want ollama", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "llama3.2",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("text: %q", resp.Text())
	}
}

func TestAdapter_ListModels_RewritesProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3.2"},{"id":"qwen2.5-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	for _, m := range models {
		if m.Provider != "ollama" {
			t.Errorf("model %q provider = %q, want ollama", m.ID, m.Provider)
		}
	}
}

func TestResolveBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string // OLLAMA_BASE_URL
		host     string // OLLAMA_HOST
		want     string
	}{
		{
			name: "default when nothing set",
			want: defaultBaseURL,
		},
		{
			name:    "OLLAMA_BASE_URL wins",
			baseURL: "http://example.com:9000/v1",
			host:    "ignored:11434",
			want:    "http://example.com:9000/v1",
		},
		{
			name:    "OLLAMA_BASE_URL trailing slash trimmed",
			baseURL: "http://example.com:9000/v1/",
			want:    "http://example.com:9000/v1",
		},
		{
			name: "OLLAMA_HOST host:port",
			host: "192.168.1.5:11434",
			want: "http://192.168.1.5:11434/v1",
		},
		{
			name: "OLLAMA_HOST bare host gets default port",
			host: "ollama.local",
			want: "http://ollama.local:11434/v1",
		},
		{
			name: "OLLAMA_HOST with http scheme",
			host: "http://192.168.1.5:11434",
			want: "http://192.168.1.5:11434/v1",
		},
		{
			name: "OLLAMA_HOST with https scheme",
			host: "https://ollama.example.com",
			want: "https://ollama.example.com/v1",
		},
		{
			name: "OLLAMA_HOST trailing slash",
			host: "http://192.168.1.5:11434/",
			want: "http://192.168.1.5:11434/v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBaseURL(tc.baseURL, tc.host)
			if got != tc.want {
				t.Fatalf("resolveBaseURL(%q, %q) = %q, want %q", tc.baseURL, tc.host, got, tc.want)
			}
		})
	}
}

func TestDefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "http://localhost:11434/v1" {
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

// TestAdapter_Stream_DelegatesToInner verifies Stream() forwards to the
// embedded openaicompat adapter and returns parseable events.
func TestAdapter_Stream_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		body := strings.Join([]string{
			`data: {"id":"c1","model":"llama3.2","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
			`data: {"id":"c1","model":"llama3.2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			"",
		}, "\n\n")
		_, _ = io.WriteString(w, body)
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if got := text.String(); got != "hi!" {
		t.Fatalf("text deltas = %q, want %q", got, "hi!")
	}
}

// TestAdapter_Complete_PropagatesAPIKey verifies that an OLLAMA_API_KEY
// configured on the inner adapter is sent as a Bearer token (for use with
// authenticated proxies / Ollama Cloud).
func TestAdapter_Complete_PropagatesAPIKey(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "llama3.2",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
}`))
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		APIKey:  "secret-token",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

	if _, err := a.Complete(context.Background(), llm.Request{
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if seen != "Bearer secret-token" {
		t.Fatalf("Authorization header = %q, want %q", seen, "Bearer secret-token")
	}
}

// TestAdapter_Complete_NoAuthHeaderWhenKeyEmpty verifies that local Ollama
// (no API key set) does not send an Authorization header — Ollama rejects
// or warns on unexpected auth in some configurations.
func TestAdapter_Complete_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "llama3.2",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
}`))
	}))
	t.Cleanup(srv.Close)

	a := &adapter{inner: &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	}}

	if _, err := a.Complete(context.Background(), llm.Request{
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if seen != "" {
		t.Fatalf("Authorization header = %q, want empty", seen)
	}
}

// TestProviderRegistered verifies init() has registered the ollama factory
// with the global env registry, so llm.NewFromEnv() produces an "ollama"
// provider unconditionally (no API key required for local use).
func TestProviderRegistered(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://example.invalid:9999/v1")

	c, err := llm.NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	found := false
	for _, name := range c.ProviderNames() {
		if name == "ollama" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ollama not in registered providers: %v", c.ProviderNames())
	}
}
