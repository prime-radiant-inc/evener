// Package glm registers a "glm" LLM provider for Zhipu AI's GLM models.
// It wraps the openaicompat adapter with GLM-specific defaults and quirks.
package glm

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4" // GLM uses v4, not v1

type adapter struct {
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string { return "glm" }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		key := strings.TrimSpace(os.Getenv("GLM_API_KEY"))
		if key == "" {
			return nil, false, nil
		}
		base := strings.TrimSpace(os.Getenv("GLM_BASE_URL"))
		if base == "" {
			base = defaultBaseURL
		}
		return &adapter{inner: &openaicompat.Adapter{
			APIKey:  key,
			BaseURL: strings.TrimRight(base, "/"),
			Client:  &http.Client{Timeout: 0},
			Quirks:  openaicompat.QuirksPreset("glm-5"),
		}}, true, nil
	})
}
