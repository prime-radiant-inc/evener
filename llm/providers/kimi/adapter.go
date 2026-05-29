// Package kimi registers a "kimi" LLM provider for Moonshot AI's Kimi models.
// It wraps the openaicompat adapter with Kimi-specific defaults and quirks.
package kimi

import (
	"context"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.moonshot.ai/v1" // includes /v1 per OpenAI SDK convention

type adapter struct {
	name  string
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "kimi"
}

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

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
	return &adapter{
		name: params.Name,
		inner: openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
			Name:    params.Name,
			BaseURL: base,
			APIKey:  params.APIKey,
			Quirks:  openaicompat.QuirksPreset("kimi-k2.5"),
		}),
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("KIMI_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    "kimi",
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
}
