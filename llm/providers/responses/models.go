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
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/registry"
)

// openAIModelListEntry is one /v1/models row from the public OpenAI API or
// an OpenAI-compatible gateway, which publishes the reasoning and limit
// spellings the public API omits.
type openAIModelListEntry struct {
	ID                       string                            `json:"id"`
	OwnedBy                  string                            `json:"owned_by"`
	ContextWindow            int                               `json:"context_window"`
	MaxContext               int                               `json:"max_context_window"`
	ContextLength            int                               `json:"context_length"`
	MaxInputTokens           int                               `json:"max_input_tokens"`
	InputTokenLimit          int                               `json:"input_token_limit"`
	MaxOutputTokens          int                               `json:"max_output_tokens"`
	OutputTokens             int                               `json:"output_token_limit"`
	Architecture             requestutil.GatewayArchitecture   `json:"architecture"`
	Capabilities             requestutil.GatewayCapabilities   `json:"capabilities"`
	ClientCompat             requestutil.GatewayClientCompat   `json:"client_compat"`
	Reasoning                requestutil.ListedReasoning       `json:"reasoning"`
	SupportedReasoningLevels requestutil.ListedReasoningLevels `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                            `json:"default_reasoning_level"`
	Limits                   requestutil.ListedLimits          `json:"limits"`
}

// row keeps only advertised facts (registry.Model carries no field this
// entry doesn't report).
func (m openAIModelListEntry) row() registry.Model {
	caps := registry.Caps{}
	if w := requestutil.FirstPositiveInt(m.ContextWindow, m.MaxContext, m.ContextLength); w > 0 {
		caps.ContextWindow = new(w)
	}
	if i := requestutil.FirstPositiveInt(m.MaxInputTokens, m.InputTokenLimit); i > 0 {
		caps.MaxInputTokens = new(i)
	}
	if o := requestutil.FirstPositiveInt(m.MaxOutputTokens, m.OutputTokens, m.Limits.MaxOutputTokens); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	// A gateway row's listed reasoning facts fill the capability, ladder and
	// default a bare OpenAI listing never states: without them an
	// uncatalogued gateway model resolves with no ladder and no default.
	requestutil.ApplyListedReasoningFacts(&caps, m.Reasoning, m.SupportedReasoningLevels, m.DefaultReasoningLevel)
	// A gateway's own per-model facts (lunaroute's shape) fill the tool
	// verdict and wire dialect.
	requestutil.ApplyGatewayFacts(&caps, m.Architecture, m.Capabilities, m.ClientCompat)
	return registry.Model{ID: m.ID, Caps: caps}
}

// codexModelListEntry is one /models row from the Codex backend, narrowed
// to the fields row() advertises.
type codexModelListEntry struct {
	Slug                     string                            `json:"slug"`
	ID                       string                            `json:"id"`
	Model                    string                            `json:"model"`
	ContextWindow            int                               `json:"context_window"`
	MaxContextWindow         int                               `json:"max_context_window"`
	MaxInputTokens           int                               `json:"max_input_tokens"`
	InputTokenLimit          int                               `json:"input_token_limit"`
	MaxOutputTokens          int                               `json:"max_output_tokens"`
	OutputTokenLimit         int                               `json:"output_token_limit"`
	SupportedReasoningLevels requestutil.ListedReasoningLevels `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                            `json:"default_reasoning_level"`
	InputModalities          []string                          `json:"input_modalities"`
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
	if w := requestutil.FirstPositiveInt(m.ContextWindow, m.MaxContextWindow); w > 0 {
		caps.ContextWindow = new(w)
	}
	if i := requestutil.FirstPositiveInt(m.MaxInputTokens, m.InputTokenLimit); i > 0 {
		caps.MaxInputTokens = new(i)
	}
	if o := requestutil.FirstPositiveInt(m.MaxOutputTokens, m.OutputTokenLimit); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	// The Codex backend states no reasoning object: its rows carry the
	// ladder and, alone among listings, the effort a model runs at when
	// the request omits one (spec §7.4).
	requestutil.ApplyListedReasoningFacts(&caps, requestutil.ListedReasoning{}, m.SupportedReasoningLevels, m.DefaultReasoningLevel)
	caps.InputModalities = requestutil.DedupStrings(m.InputModalities)
	return registry.Model{ID: m.id(), Caps: caps}
}

// ListModels implements llm.Protocol. The platform API answers with data[]
// and the Codex backend with models[]; both map to registry rows carrying
// only the facts they advertise.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	modelsEndpoint := res.Transport.ModelsEndpoint
	if res.Transport.Auth == registry.AuthOAuthOpenAICodex {
		var err error
		modelsEndpoint, err = requestutil.WithQuery(modelsEndpoint, "client_version", res.Transport.CodexClientVersion)
		if err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
	}
	call := &protocolhttp.Call{Operation: "models.list", EndpointFamily: "openai_models", Method: http.MethodGet, URL: protocolhttp.URL(res, modelsEndpoint), Req: llm.Request{Model: "*", AdapterTimeout: llm.ModelListingTimeout(ctx)}, Res: res, Client: p.Client}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		// Rows decode independently: a row whose shape the reader cannot
		// read is dropped, so one alien row cannot fail the healthy ones.
		// Only a listing with no usable row at all fails, and it fails
		// with the row's own decode error.
		openAIRows, dataErr := requestutil.DecodeListingRows[openAIModelListEntry](payload.Data)
		codexRows, modelsErr := requestutil.DecodeListingRows[codexModelListEntry](payload.Models)
		for _, e := range openAIRows {
			if registry.IsChatModelID(e.ID) {
				rows = append(rows, e.row())
			}
		}
		for _, e := range codexRows {
			if id := e.id(); registry.IsChatModelID(id) {
				rows = append(rows, e.row())
			}
		}
		rowErr := dataErr
		if rowErr == nil {
			rowErr = modelsErr
		}
		if rowErr != nil && len(rows) == 0 {
			return nil, fmt.Errorf("models.list: %w", rowErr)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		return nil, nil
	})
	return rows, err
}
