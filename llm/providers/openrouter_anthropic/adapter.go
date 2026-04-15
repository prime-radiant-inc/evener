// Package openrouter_anthropic registers an "openrouter-anthropic" LLM
// provider that uses OpenRouter's Anthropic-Messages-compatible endpoint
// (https://openrouter.ai/api/v1/messages) with the standard Anthropic adapter.
//
// This is useful for models like minimax/minimax-m2.7 whose native tool-call
// format is Anthropic-style XML. Routing them through OpenRouter's OpenAI
// Chat Completions endpoint produces corrupt tool_calls.arguments (XML
// fragments leaking into the JSON args string). Routing through OpenRouter's
// Anthropic Messages endpoint instead keeps the model's native format end-to-
// end, which is much more reliable.
//
// Authentication uses OPENROUTER_API_KEY (passed via the x-api-key header,
// which the Anthropic adapter already does). Base URL defaults to
// https://openrouter.ai/api and can be overridden with OPENROUTER_BASE_URL.
package openrouter_anthropic

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
)

const defaultBaseURL = "https://openrouter.ai/api"

type adapter struct {
	inner *anthropic.Adapter
}

func (a *adapter) Name() string { return "openrouter-anthropic" }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

// newTestAdapter constructs an adapter for testing with a custom base URL and client.
func newTestAdapter(baseURL, apiKey string, client *http.Client) *adapter {
	return &adapter{inner: &anthropic.Adapter{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  client,
	}}
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		if base == "" {
			base = defaultBaseURL
		}
		return &adapter{inner: &anthropic.Adapter{
			APIKey:  key,
			BaseURL: strings.TrimRight(base, "/"),
			Client:  &http.Client{Timeout: 0},
		}}, true, nil
	})
}
