package chatcompletions

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
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/registry"
)

// modelEntry is one /models row: the core OpenAI shape plus the extensions
// OpenAI-compatible servers publish on it, each absent (and therefore not
// advertised) wherever a server does not state it. OpenRouter's shape
// carries context_window, max_input_tokens, max_completion_tokens and
// max_output_tokens at the row's top level instead of under top_provider;
// the Codex backend's spellings (supported_reasoning_levels,
// default_reasoning_level) travel on other servers' chat rows too; a local
// server may cap output under a limits object and state the reasoning fact
// as a plain boolean.
type modelEntry struct {
	ID                       string                            `json:"id"`
	ContextLength            int                               `json:"context_length"`
	ContextWindow            int                               `json:"context_window"`
	MaxInputTokens           int                               `json:"max_input_tokens"`
	MaxCompletionTokens      int                               `json:"max_completion_tokens"`
	MaxOutputTokens          int                               `json:"max_output_tokens"`
	SupportedParameters      []string                          `json:"supported_parameters"`
	Architecture             requestutil.GatewayArchitecture   `json:"architecture"`
	Capabilities             requestutil.GatewayCapabilities   `json:"capabilities"`
	Reasoning                requestutil.ListedReasoning       `json:"reasoning"`
	SupportedReasoningLevels requestutil.ListedReasoningLevels `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                            `json:"default_reasoning_level"`
	Limits                   requestutil.ListedLimits          `json:"limits"`
	ClientCompat             requestutil.GatewayClientCompat   `json:"client_compat"`
	Pricing                  struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// advertises reports whether the supported_parameters list names any of the
// given parameters, matching them case- and whitespace-insensitively as
// OpenRouter's listing is not normalized.
func advertises(params []string, names ...string) bool {
	for _, p := range params {
		p = strings.TrimSpace(p)
		for _, name := range names {
			if strings.EqualFold(p, name) {
				return true
			}
		}
	}
	return false
}

// row keeps only advertised facts (registry.ApplyLive keeps only those).
func (m modelEntry) row() registry.Model {
	caps := registry.Caps{}
	if w := requestutil.FirstPositiveInt(m.ContextLength, m.ContextWindow); w > 0 {
		caps.ContextWindow = new(w)
	}
	if m.MaxInputTokens > 0 {
		caps.MaxInputTokens = new(m.MaxInputTokens)
	}
	topProviderCap := requestutil.PositivePointerInt(m.TopProvider.MaxCompletionTokens)
	if o := requestutil.FirstPositiveInt(topProviderCap, m.MaxCompletionTokens, m.MaxOutputTokens, m.Limits.MaxOutputTokens); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	if len(m.SupportedParameters) > 0 {
		caps.Tools = new(advertises(m.SupportedParameters, "tools"))
		caps.Reasoning = new(advertises(m.SupportedParameters, "reasoning", "reasoning_effort"))
	}
	requestutil.ApplyListedReasoningFacts(&caps, m.Reasoning, m.SupportedReasoningLevels, m.DefaultReasoningLevel)
	requestutil.ApplyGatewayFacts(&caps, m.Architecture, m.Capabilities, m.ClientCompat)
	if in, ok := perTokenCostToPerMillion(m.Pricing.Prompt); ok {
		if out, ok := perTokenCostToPerMillion(m.Pricing.Completion); ok {
			caps.Cost = &registry.Cost{Input: in, Output: out}
		}
	}
	return registry.Model{ID: m.ID, Caps: caps}
}

// ListModels implements llm.Protocol.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	call := &protocolhttp.Call{Operation: "models.list", EndpointFamily: "openai_models", Method: http.MethodGet, URL: protocolhttp.URL(res, res.Transport.ModelsEndpoint), Req: llm.Request{Model: "*", AdapterTimeout: llm.ModelListingTimeout(ctx)}, Res: res, Client: p.Client}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		// Rows decode independently: a row whose shape the reader cannot
		// read is dropped, so one alien row cannot fail the healthy ones.
		// Only a listing with no usable row at all fails, and it fails
		// with the row's own decode error.
		entries, rowErr := requestutil.DecodeListingRows[modelEntry](payload.Data)
		for _, e := range entries {
			if e.ID != "" {
				rows = append(rows, e.row())
			}
		}
		if rowErr != nil && len(rows) == 0 {
			return nil, fmt.Errorf("models.list: %w", rowErr)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}

// perTokenCostToPerMillion converts OpenRouter's per-token price strings
// to the registry's per-million unit; "0" is a valid free price.
// strconv.ParseFloat accepts "NaN"/"Inf"/"infinity" (case-insensitive)
// without error, and neither is < 0, so a non-finite result must be
// rejected explicitly — json.Marshal fails outright on NaN/Inf, and one
// bad row would otherwise break serialization of the whole listing.
func perTokenCostToPerMillion(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v * 1e6, true
}
