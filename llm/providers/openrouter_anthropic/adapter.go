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
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
)

const defaultBaseURL = "https://openrouter.ai/api"

const providerName = "openrouter-anthropic"

// adapter is the openrouter-anthropic provider adapter: a forwarder over the
// Anthropic backing adapter (pointed at OpenRouter's Anthropic Messages
// endpoint) that presents the "openrouter-anthropic" provider name. ListModels
// and the completion methods are promoted from the embedded backing adapter.
type adapter = providerfwd.Anthropic

// Compile-time assertions that the openrouter-anthropic adapter satisfies the
// provider contract and, via concrete embedding, the optional ModelLister
// capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single openrouter-anthropic adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs an openrouter-anthropic adapter from explicit parameters.
// Empty BaseURL falls back to the openrouter-anthropic default.
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
		key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("openrouter-anthropic", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
