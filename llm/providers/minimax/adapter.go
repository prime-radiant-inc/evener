// Package minimax registers a "minimax" LLM provider for MiniMax's
// Anthropic-compatible API. It wraps the anthropic adapter pointed at
// MiniMax's endpoint (https://api.minimax.io/anthropic).
package minimax

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
)

const defaultBaseURL = "https://api.minimax.io/anthropic"

type adapter struct {
	inner *anthropic.Adapter
}

func (a *adapter) Name() string { return "minimax" }

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
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("MINIMAX_BASE_URL"))
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
