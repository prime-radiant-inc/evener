package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/google"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/kimi_anthropic"
	_ "primeradiant.com/serf/llm/providers/minimax"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openai"
	_ "primeradiant.com/serf/llm/providers/openaicompat"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
)

type capturedProviderRequest struct {
	method string
	path   string
	query  string
	header http.Header
}

func TestProviderCredentialHeadersReachEveryRequestPath(t *testing.T) {
	captured := make(chan capturedProviderRequest, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- capturedProviderRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			header: r.Header.Clone(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"expected credential-header test stop"}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cfg := providercfg.Config{
		Default: "openai-responses",
		Instances: []providercfg.InstanceConfig{
			providerInstance("openai-responses", "openai", providercfg.StyleResponses, srv.URL, "Authorization"),
			providerInstance("anthropic", "anthropic", "", srv.URL, "x-api-key"),
			providerInstance("google", "google", "", srv.URL, "Content-Type"),
			providerInstance("openai-chat", "openai", providercfg.StyleChatCompletions, srv.URL, "Authorization"),
			providerInstance("glm", "glm", "", srv.URL, "Authorization"),
			providerInstance("kimi", "kimi", "", srv.URL, "Authorization"),
			providerInstance("ollama", "ollama", "", srv.URL, "Authorization"),
			providerInstance("openrouter", "openrouter", "", srv.URL, "Authorization"),
			providerInstance("openrouter-anthropic", "openrouter-anthropic", "", srv.URL, "x-api-key"),
			providerInstance("kimi-anthropic", "kimi-anthropic", "", srv.URL, "x-api-key"),
			providerInstance("minimax", "minimax", "", srv.URL, "x-api-key"),
		},
	}
	client, err := llm.NewFromProviders(cfg)
	if err != nil {
		t.Fatalf("NewFromProviders: %v", err)
	}

	complete := func(provider, model string) {
		_, _ = client.Complete(context.Background(), providerRequest(provider, model))
	}
	stream := func(provider, model string) {
		stream, err := client.Stream(context.Background(), providerRequest(provider, model))
		if err != nil || stream == nil {
			return
		}
		for range stream.Events() { //nolint:revive // drain the deterministic fake response
		}
		_ = stream.Close()
	}
	listModels := func(provider string) {
		_, _ = client.ListModels(context.Background(), provider)
	}
	countTokens := func(provider, model string) {
		_, _ = client.CountInputTokens(context.Background(), providerRequest(provider, model))
	}

	tests := []struct {
		name         string
		provider     string
		method       string
		pathSuffix   string
		builtInName  string
		builtInValue string
		queryKey     bool
		call         func()
	}{
		{"openai complete", "openai-responses", http.MethodPost, "/v1/responses", "Authorization", "Bearer provider-key", false, func() { complete("openai-responses", "gpt-5.2") }},
		{"openai stream", "openai-responses", http.MethodPost, "/v1/responses", "Authorization", "Bearer provider-key", false, func() { stream("openai-responses", "gpt-5.2") }},
		{"openai models", "openai-responses", http.MethodGet, "/v1/models", "Authorization", "Bearer provider-key", false, func() { listModels("openai-responses") }},
		{"anthropic complete", "anthropic", http.MethodPost, "/v1/messages", "x-api-key", "provider-key", false, func() { complete("anthropic", "claude-test") }},
		{"anthropic stream", "anthropic", http.MethodPost, "/v1/messages", "x-api-key", "provider-key", false, func() { stream("anthropic", "claude-test") }},
		{"anthropic models", "anthropic", http.MethodGet, "/v1/models", "x-api-key", "provider-key", false, func() { listModels("anthropic") }},
		{"google complete", "google", http.MethodPost, ":generateContent", "Content-Type", "application/json", true, func() { complete("google", "gemini-test") }},
		{"google stream", "google", http.MethodPost, ":streamGenerateContent", "Content-Type", "application/json", true, func() { stream("google", "gemini-test") }},
		{"google models", "google", http.MethodGet, "/v1beta/models", "Content-Type", "configured-built-in", true, func() { listModels("google") }},
		{"google token count", "google", http.MethodPost, ":countTokens", "Content-Type", "application/json", true, func() { countTokens("google", "gemini-test") }},
		{"openai-compatible complete", "openai-chat", http.MethodPost, "/chat/completions", "Authorization", "Bearer provider-key", false, func() { complete("openai-chat", "gpt-4o") }},
		{"openai-compatible stream", "openai-chat", http.MethodPost, "/chat/completions", "Authorization", "Bearer provider-key", false, func() { stream("openai-chat", "gpt-4o") }},
		{"openai-compatible models", "openai-chat", http.MethodGet, "/models", "Authorization", "Bearer provider-key", false, func() { listModels("openai-chat") }},
		{"glm factory", "glm", http.MethodPost, "/chat/completions", "Authorization", "Bearer provider-key", false, func() { complete("glm", "glm-5") }},
		{"kimi token count factory", "kimi", http.MethodPost, "/tokenizers/estimate-token-count", "Authorization", "Bearer provider-key", false, func() { countTokens("kimi", "kimi-for-coding") }},
		{"ollama factory", "ollama", http.MethodPost, "/chat/completions", "Authorization", "Bearer provider-key", false, func() { complete("ollama", "llama3.2") }},
		{"openrouter factory", "openrouter", http.MethodPost, "/chat/completions", "Authorization", "Bearer provider-key", false, func() { complete("openrouter", "openrouter/auto") }},
		{"openrouter anthropic factory", "openrouter-anthropic", http.MethodPost, "/v1/messages", "x-api-key", "provider-key", false, func() { complete("openrouter-anthropic", "anthropic/claude-test") }},
		{"kimi anthropic factory", "kimi-anthropic", http.MethodPost, "/v1/messages", "x-api-key", "provider-key", false, func() { complete("kimi-anthropic", "kimi-for-coding") }},
		{"minimax factory", "minimax", http.MethodPost, "/v1/messages", "x-api-key", "provider-key", false, func() { complete("minimax", "MiniMax-M2.7") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.call()
			var got capturedProviderRequest
			select {
			case got = <-captured:
			default:
				t.Fatal("operation completed without issuing an HTTP request")
			}
			if got.method != tt.method || !strings.HasSuffix(got.path, tt.pathSuffix) {
				t.Errorf("request = %s %s, want %s *%s", got.method, got.path, tt.method, tt.pathSuffix)
			}
			assertOneHeaderValue(t, got.header, "X-Provider", tt.provider)
			assertOneHeaderValue(t, got.header, "X-Gateway-Key", "credential-"+tt.provider)
			assertOneHeaderValue(t, got.header, tt.builtInName, tt.builtInValue)
			if tt.queryKey && !strings.Contains(got.query, "key=provider-key") {
				t.Errorf("query = %q, want provider API key", got.query)
			}
		})
	}
}

func providerInstance(name, typ string, style providercfg.APIStyle, baseURL, builtInHeader string) providercfg.InstanceConfig {
	return providercfg.InstanceConfig{
		Name:     name,
		Type:     providercfg.Type(typ),
		APIStyle: style,
		BaseURL:  baseURL,
		APIKey:   "provider-key",
		Headers:  map[string]string{"X-Provider": name},
		CredentialHeaders: map[string]string{
			"X-Gateway-Key": "credential-" + name,
			builtInHeader:   "configured-built-in",
		},
	}
}

func providerRequest(provider, model string) llm.Request {
	return llm.Request{
		Provider: provider,
		Model:    model,
		Messages: []llm.Message{llm.User("hi")},
	}
}

func assertOneHeaderValue(t *testing.T, header http.Header, name, want string) {
	t.Helper()
	got := header.Values(name)
	if len(got) != 1 || got[0] != want {
		t.Errorf("%s values = %q, want exactly [%q]", name, got, want)
	}
}
