// Package openrouter registers an "openrouter" LLM provider for OpenRouter's
// multi-model proxy. It wraps the openaicompat adapter with no quirks, since
// OpenRouter normalizes to standard OpenAI Chat Completions format.
package openrouter

import (
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://openrouter.ai/api/v1" // includes /v1 per OpenAI SDK convention

const providerName = "openrouter"

// adapter is the openrouter provider adapter: a forwarder over the
// openai-compatible backing adapter that presents the "openrouter" provider
// name. ListModels and the completion methods are promoted from the embedded
// backing adapter.
type adapter = providerfwd.OpenAICompat

// Compile-time assertions that the openrouter adapter satisfies the provider
// contract and, via concrete embedding, the optional ModelLister capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single openrouter adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs an openrouter adapter from explicit parameters.
// Empty BaseURL falls back to the openrouter default. The openrouter quirks preset is always applied.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return providerfwd.NewOpenAICompat(params.Name, providerName, openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:    params.Name,
		BaseURL: base,
		APIKey:  params.APIKey,
		Quirks:  openaicompat.QuirksPreset("openrouter"),
	}))
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
	llm.RegisterInstanceAdapterFactory("openrouter", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
