// Package openrouter registers an "openrouter" LLM provider for OpenRouter's
// multi-model proxy. It wraps the openaicompat adapter with no quirks, since
// OpenRouter normalizes to standard OpenAI Chat Completions format.
package openrouter

import (
	"context"
	"os"
	"strings"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://openrouter.ai/api/v1" // includes /v1 per OpenAI SDK convention

type adapter struct {
	name  string
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openrouter"
}

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

// ListModels forwards to the inner OpenAI-compatible adapter, which fetches
// /v1/models from OpenRouter's API.
func (a *adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return a.inner.ListModels(ctx)
}

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
	return &adapter{
		name: params.Name,
		inner: openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
			Name:    params.Name,
			BaseURL: base,
			APIKey:  params.APIKey,
			Quirks:  openaicompat.QuirksPreset("openrouter"),
		}),
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL"))
		return NewForInstance(InstanceParams{
			Name:    "openrouter",
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("openrouter", "", func(inst providerconfig.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
		}), nil
	})
}
