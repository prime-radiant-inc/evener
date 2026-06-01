// Package glm registers a "glm" LLM provider for Zhipu AI's GLM models.
// It wraps the openaicompat adapter with GLM-specific defaults and quirks.
package glm

import (
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4" // GLM uses v4, not v1

const providerName = "glm"

// adapter is the glm provider adapter: a forwarder over the openai-compatible
// backing adapter that presents the "glm" provider name. ListModels and the
// completion methods are promoted from the embedded backing adapter.
type adapter = providerfwd.OpenAICompat

// Compile-time assertions that the glm adapter satisfies the provider contract
// and, via concrete embedding, the optional ModelLister capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single glm adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs a glm adapter from explicit parameters.
// Empty BaseURL falls back to the glm default. The glm quirks preset is always applied.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return providerfwd.NewOpenAICompat(params.Name, providerName, openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:    params.Name,
		BaseURL: base,
		APIKey:  params.APIKey,
		Quirks:  openaicompat.QuirksPreset("glm-5"),
	}))
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("GLM_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("GLM_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("glm", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
