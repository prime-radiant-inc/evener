// Package glm registers a "glm" LLM provider for Zhipu AI's GLM models.
// It wraps the openaicompat adapter with GLM-specific defaults and quirks.
package glm

import (
	"context"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4" // GLM uses v4, not v1

type adapter struct {
	name  string
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "glm"
}

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

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
	return &adapter{
		name: params.Name,
		inner: openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
			Name:    params.Name,
			BaseURL: base,
			APIKey:  params.APIKey,
			Quirks:  openaicompat.QuirksPreset("glm-5"),
		}),
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("GLM_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("GLM_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    "glm",
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
}
