// Package kimi_anthropic registers a "kimi-anthropic" LLM provider for the Kimi
// coding plan (Moonshot AI) via its Anthropic-compatible API. It wraps the
// anthropic adapter pointed at the coding endpoint (https://api.kimi.com/coding).
//
// Both Kimi coding routes require a coding-agent User-Agent allowlist, which
// serf supplies on every request (see kimicoding.UserAgent, sent by both this
// adapter and the OpenAI-compatible "kimi" provider). This Anthropic-route
// adapter is preferred for Claude-Code-style agents because the Anthropic
// Messages endpoint keeps the model's native tool-use and thinking format
// end-to-end. The separate "kimi" provider type points at the same coding plan
// over Moonshot's OpenAI-compatible /chat/completions API.
package kimi_anthropic

import (
	"net/http"
	"strings"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/kimicoding"
)

const defaultBaseURL = "https://api.kimi.com/coding"

const providerName = "kimi-anthropic"

// adapter is the kimi-anthropic provider adapter: a forwarder over the Anthropic
// backing adapter (the Kimi coding plan exposes an Anthropic-compatible API)
// that presents the "kimi-anthropic" provider name. ListModels and the
// completion methods are promoted from the embedded backing adapter.
type adapter = providerfwd.Anthropic

// Compile-time assertions that the kimi-anthropic adapter satisfies the provider
// contract and, via concrete embedding, the optional ModelLister capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single kimi-anthropic adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
	// Headers are user-configured request headers ([instances.X.headers]). A
	// user-set User-Agent overrides the coding-plan default, but the default
	// survives when the user sets none.
	Headers map[string]string
}

// NewForInstance constructs a kimi-anthropic adapter from explicit parameters.
// Empty BaseURL falls back to the Kimi coding-plan default.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return providerfwd.NewAnthropic(params.Name, providerName, &anthropic.Adapter{
		APIKey:  params.APIKey,
		BaseURL: strings.TrimRight(base, "/"),
		Client:  &http.Client{Timeout: 0},
		// Kimi For Coding gates its endpoints behind a coding-agent User-Agent
		// allowlist; user headers override it but do not erase it.
		DefaultHeaders: llm.MergeHeaders(map[string]string{"User-Agent": kimicoding.UserAgent}, params.Headers),
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
		key := envvars.KimiCodingAPIKey.Trimmed()
		if key == "" {
			return nil, false, nil
		}
		base := envvars.KimiCodingBaseURL.Trimmed()
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("kimi-anthropic", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
			Headers: inst.Headers,
		}), nil
	})
}
