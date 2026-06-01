package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

// clientFor builds an llm.Client with a single ollama adapter (empty instance
// name => "ollama") wrapping the given backing openai-compatible adapter. The
// provider stamping under test now lives at the Client boundary, so the
// delegation/stamping scenarios route through this client and address the
// adapter explicitly by name ("ollama").
func clientFor(backing *openaicompat.Adapter) *llm.Client {
	c := llm.NewClient()
	c.Register(newAdapter("", backing))
	return c
}

func TestAdapter_Name(t *testing.T) {
	a := newAdapter("", &openaicompat.Adapter{})
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

	c := clientFor(&openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	resp, err := c.Complete(context.Background(), llm.Request{
		Provider: "ollama",
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

	a := newAdapter("", &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

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

func TestNewForInstance_Name(t *testing.T) {
	a := newForInstance(InstanceParams{Name: "ol"})
	if a.Name() != "ol" {
		t.Fatalf("Name() = %q, want ol", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	a := newForInstance(InstanceParams{Name: "ol"})
	if a.BaseURL != defaultBaseURL {
		t.Fatalf("Adapter.BaseURL = %q, want %q", a.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_CustomBaseURL(t *testing.T) {
	a := newForInstance(InstanceParams{Name: "ol", BaseURL: "http://custom/v1"})
	if a.BaseURL != "http://custom/v1" {
		t.Fatalf("Adapter.BaseURL = %q, want http://custom/v1", a.BaseURL)
	}
}

// TestAdapter_Stream_DelegatesAndStampsProvider verifies Stream() forwards to
// the backing openaicompat adapter, returns parseable text events, and that the
// Client rewrites Response.Provider on the FINISH event to "ollama".
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

	c := clientFor(&openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	stream, err := c.Stream(context.Background(), llm.Request{
		Provider: "ollama",
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
// configured on the backing adapter is sent as a Bearer token (for use with
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

	a := newAdapter("", &openaicompat.Adapter{
		APIKey:  "secret-token",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

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

	a := newAdapter("", &openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

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

// TestAdapter_Stream_CloseIsIdempotent verifies that calling Close()
// multiple times on a Client-wrapped ollama stream does not panic (e.g. by
// double-closing the done channel).
func TestAdapter_Stream_CloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := clientFor(&openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	stream, err := c.Stream(context.Background(), llm.Request{
		Provider: "ollama",
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

// TestAdapter_Complete_RewritesErrorProvider verifies that an HTTP error from
// the backing openai-compatible adapter has its provider stamp rewritten to
// "ollama" before bubbling up through the Client. Without this, an Ollama
// outage / auth failure would surface as an "openai-compatible" error in logs
// and metrics.
func TestAdapter_Complete_RewritesErrorProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	}))
	t.Cleanup(srv.Close)

	c := clientFor(&openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	_, err := c.Complete(context.Background(), llm.Request{
		Provider: "ollama",
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatal("expected an error from 401 response")
	}
	var llmErr llm.Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not a llm.Error: %T %v", err, err)
	}
	if llmErr.Provider() != "ollama" {
		t.Fatalf("err.Provider() = %q, want ollama", llmErr.Provider())
	}
}

// TestAdapter_Stream_RewritesStartupErrorProvider verifies that a non-200
// startup response on the streaming endpoint surfaces as an ollama-stamped
// error. Client.Stream returns the startup failure as a returned error (not a
// stream event) after rewriting the provider.
func TestAdapter_Stream_RewritesStartupErrorProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"oops"}}`))
	}))
	t.Cleanup(srv.Close)

	c := clientFor(&openaicompat.Adapter{
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	_, err := c.Stream(context.Background(), llm.Request{
		Provider: "ollama",
		Model:    "llama3.2",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err == nil {
		t.Fatal("expected a startup error from 500 response")
	}
	var llmErr llm.Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not a llm.Error: %T %v", err, err)
	}
	if llmErr.Provider() != "ollama" {
		t.Fatalf("err.Provider() = %q, want ollama", llmErr.Provider())
	}
}

// TestAdapter_AbortErrorNotRestamped verifies that user-driven
// cancellation errors (AbortError, constructed with no provider) are
// NOT restamped. Cancellation isn't provider-attributed — turning
// "context canceled" into "ollama error: context canceled" would mislead
// callers about who originated the failure.
func TestAdapter_AbortErrorNotRestamped(t *testing.T) {
	abort := llm.NewAbortError("context canceled", nil)
	got := llm.RewriteErrorProvider(abort, "ollama")
	var llmErr llm.Error
	if !errors.As(got, &llmErr) {
		t.Fatalf("rewritten value is not a llm.Error: %T %v", got, got)
	}
	if llmErr.Provider() != "" {
		t.Fatalf("AbortError.Provider() = %q after rewrite, want \"\" (cancellation must stay provider-less)", llmErr.Provider())
	}
}

// TestAdapter_Complete_RewritesNonHTTPErrorProvider verifies that non-HTTP
// typed adapter errors (e.g. UnsupportedToolChoiceError, which the backing
// adapter returns synchronously before any network call) are also stamped with
// "ollama" by the Client. These derive from nonHTTPBaseError, a different type
// than httpBaseError, so the rewrite plumbing must cover both.
func TestAdapter_Complete_RewritesNonHTTPErrorProvider(t *testing.T) {
	// No server needed — the error fires before any HTTP call.
	c := clientFor(&openaicompat.Adapter{
		BaseURL: "http://unreachable.invalid",
		Client:  &http.Client{},
	})

	_, err := c.Complete(context.Background(), llm.Request{
		Provider:   "ollama",
		Model:      "llama3.2",
		Messages:   []llm.Message{llm.User("hi")},
		ToolChoice: &llm.ToolChoice{Mode: "definitely-not-a-mode"},
	})
	if err == nil {
		t.Fatal("expected an error from invalid ToolChoice.Mode")
	}
	var llmErr llm.Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("error is not a llm.Error: %T %v", err, err)
	}
	if llmErr.Provider() != "ollama" {
		t.Fatalf("err.Provider() = %q, want ollama", llmErr.Provider())
	}
}

// TestProviderAlwaysRegistered verifies that the ollama factory registers
// the adapter unconditionally — even with no OLLAMA_* env vars set —
// so explicit `--provider ollama` selection works zero-config.
func TestProviderAlwaysRegistered(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "")
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
		t.Fatalf("ollama not in registered providers (must be available even with no env vars): %v", c.ProviderNames())
	}
}

// TestProviderNotAutoElectedAsDefault verifies that ollama implements
// llm.NonDefaultEligible and is therefore never auto-selected as the
// client's default provider — even when it's the first (or only)
// adapter registered. This preserves the "no silent default" property
// the env-gate previously enforced, while still allowing explicit
// addressing by name.
func TestProviderNotAutoElectedAsDefault(t *testing.T) {
	c := llm.NewClient()

	// Register ollama first. Without NonDefaultEligible it would become
	// the default. The marker should prevent that.
	a := newAdapter("", &openaicompat.Adapter{})
	c.Register(a)

	if got := c.DefaultProvider(); got != "" {
		t.Errorf("DefaultProvider() = %q after registering only ollama, want empty", got)
	}

	// A subsequent default-eligible adapter SHOULD become the default.
	c.Register(&fakeOpenAI{name: "openai"})
	if got := c.DefaultProvider(); got != "openai" {
		t.Errorf("DefaultProvider() = %q after registering openai, want openai", got)
	}
}

// fakeOpenAI is a minimal ProviderAdapter used in
// TestProviderNotAutoElectedAsDefault to confirm a default-eligible
// adapter wins the default slot over ollama.
type fakeOpenAI struct{ name string }

func (f *fakeOpenAI) Name() string { return f.name }
func (f *fakeOpenAI) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}
func (f *fakeOpenAI) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, nil
}
