// Package openrouter registers an "openrouter" LLM provider for OpenRouter's
// multi-model proxy. It wraps the openaicompat adapter with no quirks, since
// OpenRouter normalizes to standard OpenAI Chat Completions format.
package openrouter

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://openrouter.ai/api/v1" // includes /v1 per OpenAI SDK convention

type adapter struct {
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string { return "openrouter" }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		if base == "" {
			base = defaultBaseURL
		}
		return &adapter{inner: &openaicompat.Adapter{
			APIKey:  key,
			BaseURL: strings.TrimRight(base, "/"),
			Client:  &http.Client{Timeout: 0},
			Quirks:  openaicompat.QuirksPreset("openrouter"),
		}}, true, nil
	})
}
