package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// openAIModelListEntry is one /v1/models row from the public OpenAI API.
type openAIModelListEntry struct {
	ID              string `json:"id"`
	OwnedBy         string `json:"owned_by"`
	ContextWindow   int    `json:"context_window"`
	MaxContext      int    `json:"max_context_window"`
	MaxInputTokens  int    `json:"max_input_tokens"`
	InputTokenLimit int    `json:"input_token_limit"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	OutputTokens    int    `json:"output_token_limit"`
}

// row keeps only advertised facts (registry.Model carries no field this
// entry doesn't report).
func (m openAIModelListEntry) row() registry.Model {
	caps := registry.Caps{}
	if w := firstPositiveInt(m.ContextWindow, m.MaxContext); w > 0 {
		caps.ContextWindow = new(w)
	}
	if i := firstPositiveInt(m.MaxInputTokens, m.InputTokenLimit); i > 0 {
		caps.MaxInputTokens = new(i)
	}
	if o := firstPositiveInt(m.MaxOutputTokens, m.OutputTokens); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	return registry.Model{ID: m.ID, Caps: caps}
}

// codexReasoningLevel is one entry of a Codex model row's
// supported_reasoning_levels.
type codexReasoningLevel struct {
	Effort string `json:"effort"`
}

// codexModelListEntry is one /models row from the Codex backend, narrowed
// to the fields row() advertises.
type codexModelListEntry struct {
	Slug                     string                `json:"slug"`
	ID                       string                `json:"id"`
	Model                    string                `json:"model"`
	ContextWindow            int                   `json:"context_window"`
	MaxContextWindow         int                   `json:"max_context_window"`
	MaxInputTokens           int                   `json:"max_input_tokens"`
	InputTokenLimit          int                   `json:"input_token_limit"`
	MaxOutputTokens          int                   `json:"max_output_tokens"`
	OutputTokenLimit         int                   `json:"output_token_limit"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level"`
	InputModalities          []string              `json:"input_modalities"`
}

// id is the row's identifier: the Codex backend reports it under any of
// three keys, in this order of preference.
func (m codexModelListEntry) id() string {
	for _, candidate := range []string{m.Slug, m.ID, m.Model} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// row keeps only advertised facts (registry.Model carries no field this
// entry doesn't report).
func (m codexModelListEntry) row() registry.Model {
	caps := registry.Caps{}
	if w := firstPositiveInt(m.ContextWindow, m.MaxContextWindow); w > 0 {
		caps.ContextWindow = new(w)
	}
	if i := firstPositiveInt(m.MaxInputTokens, m.InputTokenLimit); i > 0 {
		caps.MaxInputTokens = new(i)
	}
	if o := firstPositiveInt(m.MaxOutputTokens, m.OutputTokenLimit); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	if efforts := codexReasoningEfforts(m.SupportedReasoningLevels); len(efforts) > 0 {
		caps.EffortValues = efforts
		caps.Reasoning = new(true)
	}
	// The Codex backend is the only listing that states the effort a model
	// runs at when the request omits one (spec §7.4).
	if d := strings.TrimSpace(m.DefaultReasoningLevel); d != "" && !strings.EqualFold(d, "ultra") {
		caps.DefaultEffort = new(d)
	}
	if len(m.InputModalities) > 0 {
		caps.InputModalities = m.InputModalities
	}
	return registry.Model{ID: m.id(), Caps: caps}
}

// codexReasoningEfforts dedupes and trims a Codex row's reasoning-level
// list into API effort names. Ultra is a Codex client delegation preset,
// not an effort the Responses API accepts.
func codexReasoningEfforts(levels []codexReasoningLevel) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		effort := strings.TrimSpace(level.Effort)
		if effort == "" || strings.EqualFold(effort, "ultra") || seen[effort] {
			continue
		}
		seen[effort] = true
		out = append(out, effort)
	}
	return out
}

// firstPositiveInt returns the first positive argument, or 0 if none is
// positive.
func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// ListModels implements llm.Protocol. The platform API answers with data[]
// and the Codex backend with models[]; both map to registry rows carrying
// only the facts they advertise.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	call := &protocolhttp.Call{Operation: "models.list", EndpointFamily: "openai_models", Method: http.MethodGet, URL: protocolhttp.URL(res, res.Transport.ModelsEndpoint), Req: llm.Request{Model: "*"}, Res: res, Client: p.Client}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Data   []openAIModelListEntry `json:"data"`
			Models []codexModelListEntry  `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, e := range payload.Data {
			if registry.IsChatModelID(e.ID) {
				rows = append(rows, e.row())
			}
		}
		for _, e := range payload.Models {
			if id := e.id(); registry.IsChatModelID(id) {
				rows = append(rows, e.row())
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}
