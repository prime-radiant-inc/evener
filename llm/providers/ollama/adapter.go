// Package ollama registers an "ollama" LLM provider that targets a local or
// remote Ollama server via its OpenAI-compatible Chat Completions endpoint.
//
// Resolution order for the base URL:
//  1. OLLAMA_BASE_URL — used as-is (must include /v1)
//  2. OLLAMA_HOST — Ollama's canonical env var; normalized to a /v1 URL
//  3. http://localhost:11434/v1 — default
//
// OLLAMA_API_KEY is optional and used only for authenticated proxies or
// Ollama Cloud. Local Ollama does not require a key.
package ollama

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "http://localhost:11434/v1"

type adapter struct {
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string { return "ollama" }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

func (a *adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := a.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = "ollama"
	}
	return models, nil
}

// resolveBaseURL implements the documented resolution order.
func resolveBaseURL(baseURLEnv, hostEnv string) string {
	if b := strings.TrimSpace(baseURLEnv); b != "" {
		return strings.TrimRight(b, "/")
	}
	if h := strings.TrimSpace(hostEnv); h != "" {
		return normalizeHost(h)
	}
	return defaultBaseURL
}

// normalizeHost converts an OLLAMA_HOST value (host, host:port, or full URL)
// into a complete base URL ending in /v1.
func normalizeHost(h string) string {
	h = strings.TrimRight(strings.TrimSpace(h), "/")
	if !strings.Contains(h, "://") {
		if !strings.Contains(h, ":") {
			h = h + ":11434"
		}
		h = "http://" + h
	}
	return h + "/v1"
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		base := resolveBaseURL(os.Getenv("OLLAMA_BASE_URL"), os.Getenv("OLLAMA_HOST"))
		key := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
		return &adapter{inner: &openaicompat.Adapter{
			APIKey:  key,
			BaseURL: base,
			Client:  &http.Client{Timeout: 0},
		}}, true, nil
	})
}
