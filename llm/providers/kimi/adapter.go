// Package kimi registers a "kimi" LLM provider for Moonshot AI's Kimi models.
// It wraps the openaicompat adapter with Kimi-specific defaults and quirks.
package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/internal/transport"
	"primeradiant.com/serf/llm/providers/kimicoding"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

const defaultBaseURL = "https://api.moonshot.ai/v1" // includes /v1 per OpenAI SDK convention

const providerName = "kimi"

// adapter is the kimi provider adapter: a forwarder over the openai-compatible
// backing adapter that presents the "kimi" provider name. ListModels and the
// completion methods are promoted from the embedded backing adapter.
type adapter struct {
	*providerfwd.OpenAICompat
}

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
	// Compat (instance-wide) and Models (per-model) overlay the kimi-k2.5
	// quirks preset; see providercfg.InstanceConfig.
	Compat *providercfg.CompatConfig
	Models map[string]providercfg.ModelConfig
	// Headers are user-configured request headers ([instances.X.headers]).
	Headers           map[string]string
	CredentialHeaders map[string]string
}

// NewForInstance constructs a kimi adapter from explicit parameters.
// Empty BaseURL falls back to the kimi default. The kimi quirks preset is always applied.
//
// Unlike its sibling adapters (glm/minimax/openrouter), which alias an exported
// forwarder type, kimi needs a concrete unexported struct to attach
// CountInputTokens. Returning *adapter keeps the shared construction pattern;
// callers use it through the llm.ProviderAdapter interface it registers under.
//
//nolint:revive // unexported-return: see comment above.
func NewForInstance(params InstanceParams) *adapter {
	base := strings.TrimSpace(params.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	backing := openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:              params.Name,
		BaseURL:           base,
		APIKey:            params.APIKey,
		Quirks:            openaicompat.QuirksPreset("kimi-k2.5"),
		Compat:            params.Compat,
		Models:            params.Models,
		CatalogTag:        "kimi",
		Headers:           params.Headers,
		CredentialHeaders: params.CredentialHeaders,
		// Kimi For Coding gates its endpoints behind a coding-agent User-Agent
		// allowlist; announce as Claude Code so the coding-plan base URL is
		// accepted. A user-configured User-Agent header overrides this, but it
		// survives when the user sets none.
		ProviderHeaders: map[string]string{"User-Agent": kimicoding.UserAgent},
	})
	return &adapter{OpenAICompat: providerfwd.NewOpenAICompat(params.Name, providerName, backing)}
}

func (a *adapter) CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error) {
	if a == nil || a.OpenAICompat == nil || a.Adapter == nil {
		return llm.InputTokenCount{}, llm.ErrInputTokenCountUnsupported
	}
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := a.ChatCompletionsBody(req, false)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	stripKimiTokenCountOutputFields(body)

	b, err := json.Marshal(body)
	if err != nil {
		return llm.InputTokenCount{}, err
	}

	parentCtx := ctx
	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	endpoint := strings.TrimRight(a.BaseURL, "/") + "/tokenizers/estimate-token-count"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	for k, v := range a.DefaultHeaders {
		httpReq.Header.Set(k, v)
	}
	for k, v := range a.CredentialHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	client := llm.ClientWithAdapterTimeout(a.Client, req.AdapterTimeout)
	resp, attempt, err := transport.DoWithAPIAttempts(parentCtx, client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.Name(),
			RequestModel:       req.Model,
			HistoryMode:        req.HistoryMode,
			EndpointFamily:     "kimi_estimate_token_count",
			RequestBody:        requestBody,
			CredentialMaterial: a.apiLogCredentialMaterial(wireRequest),
		}
	})
	if err != nil {
		returnedErr := llm.WrapContextError(providerName, err)
		attempt.Complete(llm.APIAttemptResult{Err: returnedErr}, llm.APITimeoutSourceForTransport(parentCtx, ctx, err), nil, err)
		return llm.InputTokenCount{}, returnedErr
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, readErr := io.ReadAll(resp.Body)
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	decodeErr := dec.Decode(&raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := llm.ProviderFailureMessage("estimate-token-count", rawBytes)
		returnedErr := llm.ErrorFromHTTPStatus(providerName, resp.StatusCode, msg, raw, ra)
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, ResponseBody: rawBytes, Err: returnedErr}, llm.APITimeoutNone, nil, nil)
		return llm.InputTokenCount{}, returnedErr
	}
	observedErr := decodeErr
	if readErr != nil {
		observedErr = readErr
	}

	data, _ := raw["data"].(map[string]any)
	attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, ResponseBody: rawBytes, Err: observedErr}, llm.APITimeoutNone, observedErr, nil)
	return llm.InputTokenCount{
		Tokens:   llm.IntFromAny(data["total_tokens"]),
		Exact:    true,
		Source:   llm.TokenCountSourceProvider,
		Provider: a.Name(),
		Model:    req.Model,
		Raw:      raw,
	}, nil
}

func (a *adapter) apiLogCredentialMaterial(httpReq *http.Request) llm.APILogCredentialMaterial {
	headerNames := []string{"Authorization"}
	values := []string{a.APIKey}
	for name, value := range a.CredentialHeaders {
		headerNames = append(headerNames, name)
		values = append(values, value)
	}
	if httpReq != nil && httpReq.URL != nil && httpReq.URL.User != nil {
		values = append(values, httpReq.URL.User.Username())
		if password, ok := httpReq.URL.User.Password(); ok {
			values = append(values, password)
		}
	}
	return llm.NewAPILogCredentialMaterial(headerNames, nil, values...)
}

func stripKimiTokenCountOutputFields(body map[string]any) {
	for _, key := range []string{"max_tokens", "max_completion_tokens", "temperature", "top_p", "stop", "stream", "stream_options"} {
		delete(body, key)
	}
}

func init() {
	llm.RegisterEnvAdapterFactory(func(_ llm.EnvConfig) (llm.ProviderAdapter, bool, error) {
		key := envvars.KimiAPIKey.Trimmed()
		if key == "" {
			return nil, false, nil
		}
		base := envvars.KimiBaseURL.Trimmed()
		return NewForInstance(InstanceParams{
			Name:    providerName,
			BaseURL: base,
			APIKey:  key,
		}), true, nil
	})
	llm.RegisterInstanceAdapterFactory("kimi", "", func(inst providercfg.InstanceConfig, _ string) (llm.ProviderAdapter, error) {
		return NewForInstance(InstanceParams{
			Name:              inst.Name,
			BaseURL:           inst.BaseURL,
			APIKey:            inst.APIKey,
			Compat:            inst.Compat,
			Models:            inst.Models,
			Headers:           inst.Headers,
			CredentialHeaders: inst.CredentialHeaders,
		}), nil
	})
}
