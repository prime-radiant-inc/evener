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
// The factory always registers the adapter so explicit selection
// (--provider ollama) works zero-config. The adapter implements
// llm.NonDefaultEligible, which prevents it from becoming the silent
// default provider in environments where the user didn't intend it —
// the original concern that motivated the previous env-gate. Explicit
// addressing by name still works, so `serf --provider ollama` succeeds
// regardless of whether any OLLAMA_* env var is set.
package ollama

import (
	"context"
	"net"
	"net/http"
	"strings"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "http://localhost:11434/v1"

const providerName = "ollama"

// adapter is the ollama provider adapter. It embeds the shared
// openai-compatible forwarder for Name/Complete/Stream (promoted from the
// backing adapter), marks itself NonDefaultEligible, and overrides ListModels
// to stamp the ollama provider name onto returned models.
//
// Complete/Stream do NOT stamp the provider name here: llm.Client already
// stamps the resolved instance name onto responses, stream finish payloads,
// and errors. ListModels is the exception — llm.Client.ListModels does not
// rewrite ModelInfo.Provider, so the adapter must do it.
type adapter struct {
	*providerfwd.OpenAICompat
}

// Compile-time assertions for the provider contract plus the optional
// capabilities ollama participates in.
var (
	_ llm.ProviderAdapter    = (*adapter)(nil)
	_ llm.NonDefaultEligible = (*adapter)(nil)
	_ llm.ModelLister        = (*adapter)(nil)
)

// newAdapter wraps a backing openai-compatible adapter with the ollama
// forwarder under the given instance name (empty name => "ollama").
func newAdapter(instanceName string, backing *openaicompat.Adapter) *adapter {
	return &adapter{OpenAICompat: providerfwd.NewOpenAICompat(instanceName, providerName, backing)}
}

// NonDefaultEligible marks the ollama adapter as ineligible for the
// client's auto-selected default provider. This adapter is always
// registered (so explicit --provider ollama works zero-config), but the
// silent default fallback should never land here.
func (a *adapter) NonDefaultEligible() {}

// ListModels delegates to the backing adapter's /models fetch and rewrites the
// provider stamp on each model to "ollama". The backing openai-compatible
// adapter stamps "openai-compatible"; llm.Client.ListModels does not rewrite
// it, so this override is required for models to surface under the ollama
// provider name.
func (a *adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	models, err := a.OpenAICompat.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = providerName
	}
	return models, nil
}

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
// into a complete base URL ending in /v1. IPv6 hosts are bracketed correctly:
// bare "::1" becomes "[::1]:11434", which a naive strings.Contains(":") check
// would have left as "::1" with the wrong scheme syntax. Values whose path
// already terminates in /v1 are preserved so paths like
// https://proxy.example/ollama/v1 are not double-suffixed.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimRight(h, "/")
	if h == "" {
		return defaultBaseURL
	}
	if strings.Contains(h, "://") {
		// Has scheme — append /v1 if not already present.
		if strings.HasSuffix(h, "/v1") {
			return h
		}
		return h + "/v1"
	}
	// No scheme. Determine whether a port is present and whether the host
	// is a bare IPv6 literal that needs brackets.
	if _, _, err := net.SplitHostPort(h); err != nil {
		switch {
		case strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]"):
			// Bracketed IPv6 with no port.
			h += ":11434"
		case strings.Count(h, ":") >= 2:
			// Bare IPv6 with no port: "::1" or "fe80::1".
			h = "[" + h + "]:11434"
		default:
			// Hostname or IPv4 without a port.
			h += ":11434"
		}
	}
	return "http://" + h + "/v1"
}

// InstanceParams holds the configuration for a single ollama adapter instance.
type InstanceParams struct {
	Name    string
	BaseURL string
	APIKey  string
	// Compat (instance-wide) and Models (per-model) configure wire behavior;
	// ollama has no built-in quirks preset, so these are the only source of
	// overrides. See providercfg.InstanceConfig.
	Compat *providercfg.CompatConfig
	Models map[string]providercfg.ModelConfig
}

// newForInstance constructs an ollama adapter from explicit parameters.
// Empty BaseURL falls back to the ollama default (http://localhost:11434/v1).
func newForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	return newAdapter(params.Name, openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:    params.Name,
		BaseURL: base,
		APIKey:  params.APIKey,
		Compat:  params.Compat,
		Models:  params.Models,
	}))
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		baseEnv := envvars.OllamaBaseURL.Trimmed()
		hostEnv := envvars.OllamaHost.Trimmed()
		keyEnv := envvars.OllamaAPIKey.Trimmed()
		// Always register: ollama implements NonDefaultEligible, so the
		// "silent default provider" concern is handled at the client
		// level. Explicit --provider ollama works zero-config.
		return newAdapter("", &openaicompat.Adapter{
			APIKey:  keyEnv,
			BaseURL: resolveBaseURL(baseEnv, hostEnv),
			Client:  &http.Client{Timeout: 0},
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("ollama", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return newForInstance(InstanceParams{
			Name:    inst.Name,
			BaseURL: inst.BaseURL,
			APIKey:  inst.APIKey,
			Compat:  inst.Compat,
			Models:  inst.Models,
		}), nil
	})
}
