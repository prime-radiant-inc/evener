// Package kimi registers a "kimi" LLM provider for Moonshot AI's Kimi models.
// It wraps the openaicompat adapter with Kimi-specific defaults and quirks.
package kimi

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.moonshot.ai/v1" // includes /v1 per OpenAI SDK convention

type adapter struct {
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string { return "kimi" }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("KIMI_BASE_URL"))
		if base == "" {
			base = defaultBaseURL
		}
		return &adapter{inner: &openaicompat.Adapter{
			APIKey:  key,
			BaseURL: strings.TrimRight(base, "/"),
			Client:  &http.Client{Timeout: 0},
			Quirks:  openaicompat.QuirksPreset("kimi-k2.5"),
		}}, true, nil
	})
}
