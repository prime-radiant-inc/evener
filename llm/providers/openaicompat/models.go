package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
)

// ListModels fetches available models from the /v1/models endpoint.
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	a.setConfiguredHeaders(httpReq)
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, attempt, err := transport.DoWithAPIAttempts(ctx, a.Client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.apiLogProviderInstance(),
			RequestModel:       "*",
			EndpointFamily:     "openai_compatible_models",
			RequestBody:        requestBody,
			CredentialMaterial: a.apiLogCredentialMaterial(wireRequest),
		}
	})
	if err != nil {
		attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutSourceForTransport(ctx, ctx, err), nil, err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		returnedErr := fmt.Errorf("list models: HTTP %d", resp.StatusCode)
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, Err: returnedErr}, llm.APITimeoutNone, nil, nil)
		return nil, returnedErr
	}

	var result struct {
		Data []openaiCompatModelEntry `json:"data"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	if decodeErr != nil {
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, Err: decodeErr}, llm.APITimeoutNone, decodeErr, nil)
		return nil, decodeErr
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode}, llm.APITimeoutNone, nil, nil)

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.modelInfo())
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// openaiCompatModelEntry is the wire shape of a single entry in the /v1/models
// response. The core fields (id, context_length) are shared by every
// OpenAI-compatible provider. The remaining fields are OpenRouter-specific
// extensions that are silently absent on other providers (Kimi, GLM, Ollama,
// stock OpenAI) — their omitempty / pointer/zero-value semantics keep those
// providers working unchanged.
type openaiCompatModelEntry struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`

	// OpenRouter extensions.
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Reasoning struct {
		Mandatory        *bool    `json:"mandatory"`
		DefaultEnabled   *bool    `json:"default_enabled"`
		SupportedEfforts []string `json:"supported_efforts"`
	} `json:"reasoning"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// modelInfo converts a wire entry to the normalized llm.ModelInfo. The
// provider stamp is "openai-compatible" (wrapper adapters like ollama rewrite
// it to their own name). Rich OpenRouter fields are populated when present;
// providers that don't return them leave the corresponding ModelInfo fields at
// their zero values, so catalog enrichment in the profile/hub layer fills the
// gaps.
func (m openaiCompatModelEntry) modelInfo() llm.ModelInfo {
	info := llm.ModelInfo{
		ID:            m.ID,
		Provider:      "openai-compatible",
		DisplayName:   m.ID,
		ContextWindow: m.ContextLength,
	}
	// When the provider returns supported_parameters, capability fields are
	// authoritative — set CapabilitiesAdvertised so callers don't fall back
	// to the catalog and override explicit values (including false).
	if len(m.SupportedParameters) > 0 {
		info.CapabilitiesAdvertised = true
		info.SupportsTools = openRouterSupportsTools(m.SupportedParameters)
		info.SupportsVision = inputModalitiesIncludeVision(m.Architecture.InputModalities)
		// "reasoning" or "reasoning_effort" in supported_parameters indicates
		// reasoning support even when the reasoning object is absent or all
		// its fields are false/opt-in.
		if openRouterSupportsReasoning(m.SupportedParameters) {
			info.SupportsReasoning = true
		}
	}
	// The reasoning object, when present, refines reasoning metadata. Its
	// presence alone (even all-false) means reasoning is supported — the
	// model can do reasoning, it's just opt-in.
	if m.Reasoning.Mandatory != nil || m.Reasoning.DefaultEnabled != nil || len(m.Reasoning.SupportedEfforts) > 0 {
		info.SupportsReasoning = true
		if m.Reasoning.Mandatory != nil && *m.Reasoning.Mandatory {
			info.ThinkingAlwaysOn = true
		}
		if len(m.Reasoning.SupportedEfforts) > 0 {
			info.ReasoningEffortLevels = dedupStrings(m.Reasoning.SupportedEfforts)
		}
	}
	if cost := perTokenCostToPerMillion(m.Pricing.Prompt); cost != nil {
		info.InputCostPerMillion = cost
	}
	if cost := perTokenCostToPerMillion(m.Pricing.Completion); cost != nil {
		info.OutputCostPerMillion = cost
	}
	if m.TopProvider.MaxCompletionTokens != nil && *m.TopProvider.MaxCompletionTokens > 0 {
		maxOut := *m.TopProvider.MaxCompletionTokens
		info.MaxOutputTokens = &maxOut
	}
	return info
}

// openRouterSupportsTools reports whether the model's supported_parameters list
// includes "tools", indicating function-calling capability per OpenRouter's API.
func openRouterSupportsTools(params []string) bool {
	for _, p := range params {
		if strings.EqualFold(strings.TrimSpace(p), "tools") {
			return true
		}
	}
	return false
}

// openRouterSupportsReasoning reports whether the model's supported_parameters
// list includes "reasoning" or "reasoning_effort", indicating the model can
// produce reasoning/thinking output even when the reasoning object is absent.
func openRouterSupportsReasoning(params []string) bool {
	for _, p := range params {
		isReasoning := strings.EqualFold(strings.TrimSpace(p), "reasoning")
		if isReasoning || strings.EqualFold(strings.TrimSpace(p), "reasoning_effort") {
			return true
		}
	}
	return false
}

// inputModalitiesIncludeVision reports whether the modality list includes
// image input.
func inputModalitiesIncludeVision(modalities []string) bool {
	for _, mod := range modalities {
		switch strings.ToLower(strings.TrimSpace(mod)) {
		case "image", "images", "vision":
			return true
		}
	}
	return false
}

// perTokenCostToPerMillion parses a per-token cost string (as returned by
// OpenRouter's pricing fields, e.g. "0.000002") and converts it to
// per-million-token cost. Returns nil for empty or unparseable values. A valid
// "0" or "0.0" returns a pointer to 0.0 — the model is known-free, which is
// distinct from "no pricing data." Non-finite results (NaN, +Inf) are rejected.
func perTokenCostToPerMillion(perToken string) *float64 {
	perToken = strings.TrimSpace(perToken)
	if perToken == "" {
		return nil
	}
	v, err := strconv.ParseFloat(perToken, 64)
	if err != nil {
		return nil
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	perMillion := v * 1_000_000
	return &perMillion
}

// dedupStrings returns a copy of the input with duplicates removed, preserving
// order.
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
