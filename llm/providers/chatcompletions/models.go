package chatcompletions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// modelEntry is one /models row: the core OpenAI shape plus OpenRouter's
// extensions, which are absent (and therefore not advertised) elsewhere.
type modelEntry struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"context_length"`
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
	if m.ContextLength > 0 {
		caps.ContextWindow = new(m.ContextLength)
	}
	if m.TopProvider.MaxCompletionTokens != nil && *m.TopProvider.MaxCompletionTokens > 0 {
		caps.MaxOutputTokens = new(*m.TopProvider.MaxCompletionTokens)
	}
	if len(m.SupportedParameters) > 0 {
		caps.Tools = new(advertises(m.SupportedParameters, "tools"))
		caps.Reasoning = new(advertises(m.SupportedParameters, "reasoning", "reasoning_effort"))
	}
	if m.Reasoning.Mandatory != nil || m.Reasoning.DefaultEnabled != nil || len(m.Reasoning.SupportedEfforts) > 0 {
		caps.Reasoning = new(true)
		if m.Reasoning.Mandatory != nil && *m.Reasoning.Mandatory {
			caps.ThinkingAlwaysOn = new(true)
		}
		caps.EffortValues = dedupStrings(m.Reasoning.SupportedEfforts)
	}
	if len(m.Architecture.InputModalities) > 0 {
		caps.InputModalities = dedupStrings(m.Architecture.InputModalities)
	}
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
			Data []modelEntry `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, e := range payload.Data {
			if e.ID != "" {
				rows = append(rows, e.row())
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}

func dedupStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
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
