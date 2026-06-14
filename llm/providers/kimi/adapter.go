// Package kimi registers a "kimi" LLM provider for Moonshot AI's Kimi models.
// It wraps the openaicompat adapter with Kimi-specific defaults and quirks.
package kimi

import (
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/kimicoding"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.moonshot.ai/v1" // includes /v1 per OpenAI SDK convention

const providerName = "kimi"

// adapter is the kimi provider adapter: a forwarder over the openai-compatible
// backing adapter that presents the "kimi" provider name. ListModels and the
// completion methods are promoted from the embedded backing adapter.
type adapter = providerfwd.OpenAICompat

// Compile-time assertions that the kimi adapter satisfies the provider contract
// and, via concrete embedding, the optional ModelLister capability.
var (
	_ llm.ProviderAdapter = (*adapter)(nil)
	_ llm.ModelLister     = (*adapter)(nil)
)

// InstanceParams holds the configuration for a single kimi adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewForInstance constructs a kimi adapter from explicit parameters.
// Empty BaseURL falls back to the kimi default. The kimi quirks preset is always applied.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	backing := openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:    params.Name,
		BaseURL: base,
		APIKey:  params.APIKey,
		Quirks:  openaicompat.QuirksPreset("kimi-k2.5"),
	})
	// Kimi For Coding gates its endpoints behind a coding-agent User-Agent
	// allowlist; announce as Claude Code so the coding-plan base URL is accepted.
	backing.DefaultHeaders = map[string]string{"User-Agent": kimicoding.UserAgent}
	return providerfwd.NewOpenAICompat(params.Name, providerName, backing)
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("KIMI_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("kimi", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
