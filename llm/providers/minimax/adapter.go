// Package minimax registers a "minimax" LLM provider for MiniMax's
// Anthropic-compatible API. It wraps the anthropic adapter pointed at
// MiniMax's endpoint (https://api.minimax.io/anthropic).
package minimax

import (
	"net/http"
	"strings"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
)

const defaultBaseURL = "https://api.minimax.io/anthropic"

const providerName = "minimax"

// adapter is the minimax provider adapter: a forwarder over the Anthropic
// backing adapter (MiniMax exposes an Anthropic-compatible API) that presents
// the "minimax" provider name. ListModels and the completion methods are
// promoted from the embedded backing adapter.
type adapter = providerfwd.Anthropic

// Compile-time assertions that the minimax adapter satisfies the provider
// contract and, via concrete embedding, the optional ModelLister capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single minimax adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs a minimax adapter from explicit parameters.
// Empty BaseURL falls back to the minimax default.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return providerfwd.NewAnthropic(params.Name, providerName, &anthropic.Adapter{
		APIKey:  params.APIKey,
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
	})
}

// newTestAdapter constructs an adapter for testing with a custom base URL and client.
func newTestAdapter(baseURL, apiKey string, client *http.Client) *adapter {
	return providerfwd.NewAnthropic("", providerName, &anthropic.Adapter{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  client,
	})
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := envvars.MinimaxAPIKey.Trimmed()
		if key == "" {
			return nil, false, nil
		}
		base := envvars.MinimaxBaseURL.Trimmed()
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("minimax", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
