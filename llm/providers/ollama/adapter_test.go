package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestAdapter_Name(t *testing.T) {
	a := &adapter{inner: &openaicompat.Adapter{}}
	if a.Name() != "ollama" {
		t.Fatalf("Name() = %q, want ollama", a.Name())
	}
}

func TestAdapter_Complete_DelegatesAndStampsProvider(t *testing.T) {
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
	if resp.Provider != "ollama" {
		t.Fatalf("resp.Provider = %q, want ollama", resp.Provider)
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
		name    string
		baseURL string // OLLAMA_BASE_URL
		host    string // OLLAMA_HOST
		want    string
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
		{
			name: "OLLAMA_HOST already ending in /v1 is preserved",
			host: "http://192.168.1.5:11434/v1",
			want: "http://192.168.1.5:11434/v1",
		},
		{
			name: "OLLAMA_HOST with custom path ending in /v1 is preserved",
			host: "https://proxy.example.com/ollama/v1",
			want: "https://proxy.example.com/ollama/v1",
		},
		{
			name: "OLLAMA_HOST with custom path missing /v1 gets it appended",
			host: "https://proxy.example.com/ollama",
			want: "https://proxy.example.com/ollama/v1",
		},
		{
			name: "OLLAMA_HOST with /v1/ trailing slash preserved (no double v1)",
			host: "http://192.168.1.5:11434/v1/",
			want: "http://192.168.1.5:11434/v1",
		},
		{
			name: "OLLAMA_HOST bare IPv6 unbracketed gets brackets and default port",
			host: "::1",
			want: "http://[::1]:11434/v1",
		},
		{
			name: "OLLAMA_HOST bare IPv6 longer form",
			host: "fe80::1",
			want: "http://[fe80::1]:11434/v1",
		},
		{
			name: "OLLAMA_HOST bracketed IPv6 without port",
			host: "[::1]",
			want: "http://[::1]:11434/v1",
		},
		{
			name: "OLLAMA_HOST bracketed IPv6 with port",
			host: "[::1]:11434",
			want: "http://[::1]:11434/v1",
		},
		{
			name: "OLLAMA_HOST IPv6 in full URL preserved",
			host: "http://[::1]:11434",
			want: "http://[::1]:11434/v1",
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

// TestAdapter_Stream_DelegatesAndStampsProvider verifies Stream() forwards to
// the embedded openaicompat adapter, returns parseable text events, and
// rewrites Response.Provider on the FINISH event to "ollama".
func TestAdapter_Stream_DelegatesAndStampsProvider(t *testing.T) {
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
	var sawFinish bool
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			sawFinish = true
			if ev.Response.Provider != "ollama" {
				t.Errorf("FINISH event Response.Provider = %q, want ollama", ev.Response.Provider)
			}
		}
	}
	if got := text.String(); got != "hi!" {
		t.Fatalf("text deltas = %q, want %q", got, "hi!")
	}
	if !sawFinish {
		t.Fatal("did not observe a FINISH event with a non-nil Response")
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

// TestAdapter_Stream_CloseWithoutDraining verifies that Close() on the
// rewriteStream wrapper returns promptly even when the consumer never
// reads from Events(). Without proper shutdown coordination, the pump
// goroutine would block forever on a full out-channel and Close() would
// hang waiting for it. Regression test for the goroutine leak found in
// review of commit be4e79b.
func TestAdapter_Stream_CloseWithoutDraining(t *testing.T) {
	// Server emits many small deltas to fill the wrapper's out-channel
	// buffer (cap 16). The exact count doesn't matter as long as it
	// exceeds the buffer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		var lines []string
		for i := 0; i < 64; i++ {
			lines = append(lines, `data: {"id":"c1","model":"llama3.2","choices":[{"index":0,"delta":{"content":"x"}}]}`)
		}
		lines = append(lines, `data: [DONE]`, "")
		_, _ = io.WriteString(w, strings.Join(lines, "\n\n"))
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

	// Give the pump time to fill its buffer, then close without draining.
	time.Sleep(50 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- stream.Close() }()
	select {
	case <-closed:
		// expected — Close returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("Close() hung — likely goroutine leak in rewriteStream.pump")
	}

	// After Close returns, the events channel must be drained/closed so a
	// later range over it terminates.
	drained := make(chan struct{})
	go func() {
		for range stream.Events() {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Events() channel did not close after Close()")
	}
}

// TestAdapter_Stream_CloseIsIdempotent verifies that calling Close()
// multiple times does not panic (e.g. by double-closing the done channel).
func TestAdapter_Stream_CloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
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
	// First close — drains naturally.
	for range stream.Events() {
	}
	if err := stream.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Second close must not panic.
	_ = stream.Close()
}

// TestProviderRegistered_WhenEnvSet verifies that init() has registered the
// ollama factory with the global env registry, and that setting any one of
// the OLLAMA_* env vars causes llm.NewFromEnv() to produce an "ollama"
// provider.
func TestProviderRegistered_WhenEnvSet(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://example.invalid:9999/v1")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OLLAMA_API_KEY", "")

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

// TestProviderNotRegistered_WhenNoEnvSet verifies the factory does NOT
// register itself when none of OLLAMA_BASE_URL / OLLAMA_HOST / OLLAMA_API_KEY
// are set. This protects against ollama becoming the implicit default
// provider in environments where it isn't actually configured.
func TestProviderNotRegistered_WhenNoEnvSet(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OLLAMA_API_KEY", "")

	// llm.NewFromEnv() may or may not succeed depending on what other
	// provider env vars are set in the test runner's environment. In
	// either case, ollama must not appear in the registered providers.
	c, err := llm.NewFromEnv()
	if err != nil {
		// "no providers configured" is expected if no other provider env
		// vars are set. That itself proves ollama did not register.
		return
	}
	for _, name := range c.ProviderNames() {
		if name == "ollama" {
			t.Fatalf("ollama is registered without any OLLAMA_* env var: providers=%v", c.ProviderNames())
		}
	}
}
