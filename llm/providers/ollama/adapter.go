// Package ollama registers an "ollama" LLM provider that targets a local or
// remote Ollama server via its OpenAI-compatible Chat Completions endpoint.
//
// Resolution order for the base URL:
//  1. OLLAMA_BASE_URL — used as-is (must include /v1)
//  2. OLLAMA_HOST — Ollama's canonical env var; normalized to a /v1 URL
//  3. http://localhost:11434/v1 — default
//
// OLLAMA_API_KEY is optional and used only for authenticated proxies or
// Ollama Cloud. Local Ollama does not require a key.
//
// The factory only registers itself when at least one of OLLAMA_BASE_URL,
// OLLAMA_HOST, or OLLAMA_API_KEY is set. This prevents Ollama from silently
// becoming the implicit default provider in environments where it isn't
// actually configured. Users who want zero-config local Ollama can set
// OLLAMA_HOST=localhost (or any of the three vars) as a one-time signal.
package ollama

import (
	"context"
	"net/http"
	"os"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "http://localhost:11434/v1"

const providerName = "ollama"

type adapter struct {
	inner *openaicompat.Adapter
}

func (a *adapter) Name() string { return providerName }

func (a *adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := a.inner.Complete(ctx, req)
	resp.Provider = providerName
	return resp, err
}

func (a *adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	inner, err := a.inner.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return newRewriteStream(inner), nil
}

func (a *adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := a.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = providerName
	}
	return models, nil
}

// rewriteStream wraps an inner llm.Stream and rewrites the Provider field on
// any embedded Response payloads so consumers see "ollama" instead of the
// inner adapter's "openai-compatible" stamp.
type rewriteStream struct {
	inner  llm.Stream
	out    chan llm.StreamEvent
	pumped bool
}

func newRewriteStream(inner llm.Stream) *rewriteStream {
	s := &rewriteStream{inner: inner, out: make(chan llm.StreamEvent, 16)}
	go s.pump()
	return s
}

func (s *rewriteStream) pump() {
	defer close(s.out)
	for ev := range s.inner.Events() {
		if ev.Response != nil {
			ev.Response.Provider = providerName
		}
		s.out <- ev
	}
}

func (s *rewriteStream) Events() <-chan llm.StreamEvent { return s.out }
func (s *rewriteStream) Close() error                   { return s.inner.Close() }

// resolveBaseURL implements the documented resolution order.
func resolveBaseURL(baseURLEnv, hostEnv string) string {
	if b := strings.TrimSpace(baseURLEnv); b != "" {
		return strings.TrimRight(b, "/")
	}
	if h := strings.TrimSpace(hostEnv); h != "" {
		return normalizeHost(h)
	}
	return defaultBaseURL
}

// normalizeHost converts an OLLAMA_HOST value (host, host:port, or full URL)
// into a complete base URL ending in /v1. Values that already terminate in
// /v1 are preserved as-is so paths like https://proxy.example/ollama/v1 are
// not double-suffixed.
func normalizeHost(h string) string {
	h = strings.TrimRight(strings.TrimSpace(h), "/")
	if h == "" {
		return defaultBaseURL
	}
	if !strings.Contains(h, "://") {
		if !strings.Contains(h, ":") {
			h = h + ":11434"
		}
		h = "http://" + h
	}
	if strings.HasSuffix(h, "/v1") {
		return h
	}
	return h + "/v1"
}

func init() {
	llm.RegisterEnvAdapterFactory(func() (llm.ProviderAdapter, bool, error) {
		baseEnv := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL"))
		hostEnv := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
		keyEnv := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
		if baseEnv == "" && hostEnv == "" && keyEnv == "" {
			return nil, false, nil
		}
		return &adapter{inner: &openaicompat.Adapter{
			APIKey:  keyEnv,
			BaseURL: resolveBaseURL(baseEnv, hostEnv),
			Client:  &http.Client{Timeout: 0},
		}}, true, nil
	})
}
