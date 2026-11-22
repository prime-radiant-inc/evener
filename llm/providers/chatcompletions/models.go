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

// modelEntry is one /models row: the core OpenAI shape plus OpenRouter's
// extensions, which are absent (and therefore not advertised) elsewhere. A
// gateway in the OpenAI-compatible shape publishes context_window,
// max_input_tokens, max_completion_tokens and max_output_tokens at the row's
// top level instead of under top_provider, so those are read too.
type modelEntry struct {
	ID                  string                          `json:"id"`
	ContextLength       int                             `json:"context_length"`
	ContextWindow       int                             `json:"context_window"`
	MaxInputTokens      int                             `json:"max_input_tokens"`
	MaxCompletionTokens int                             `json:"max_completion_tokens"`
	MaxOutputTokens     int                             `json:"max_output_tokens"`
	SupportedParameters []string                        `json:"supported_parameters"`
	SupportedEndpoints  []string                        `json:"supported_endpoints"`
	ModelPickerEnabled  *bool                           `json:"model_picker_enabled"`
	Architecture        requestutil.GatewayArchitecture `json:"architecture"`
	Capabilities        accountCapabilities             `json:"capabilities"`
	Policy              struct {
		State string `json:"state"`
	} `json:"policy"`
	Reasoning struct {
		Mandatory        *bool    `json:"mandatory"`
		DefaultEnabled   *bool    `json:"default_enabled"`
		SupportedEfforts []string `json:"supported_efforts"`
	} `json:"reasoning"`
	ClientCompat requestutil.GatewayClientCompat `json:"client_compat"`
	Pricing      struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens *int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// accountCapabilities is the Copilot subscription roster's capability object
// plus the gateway shape the shared reader already understood. The embedded
// fields keep reading lunaroute's capabilities.vision/tools/reasoning, while
// the named fields read the account's family, limits and supports blocks.
type accountCapabilities struct {
	requestutil.GatewayCapabilities
	Family string `json:"family"`
	Limits struct {
		MaxPromptTokens int `json:"max_prompt_tokens"`
		MaxOutputTokens int `json:"max_output_tokens"`
	} `json:"limits"`
	Supports struct {
		ToolCalls *bool `json:"tool_calls"`
		Vision    *bool `json:"vision"`
		Reasoning *bool `json:"reasoning"`
	} `json:"supports"`
}

// provenImageModel is the only Copilot row whose image path passed the live
// probe. The roster advertises vision more broadly; every other row stays
// text-only until its own live proof, because the GPT image leg failed at
// stream open.
func provenImageModel(id string) bool {
	return strings.EqualFold(strings.TrimSpace(id), "claude-haiku-5.5")
}

// accountRow reports whether the row publishes the subscription roster's
// shape. Those rows carry endpoint routing and are the ones whose advertised
// prices are not billed as live dollar prices.
func (m modelEntry) accountRow() bool {
	return len(m.SupportedEndpoints) > 0 || m.Capabilities.Family != "" || m.Policy.State != ""
}

// dropped reports roster rows the account asks Evener not to offer: a row the
// picker excludes and a policy-disabled row. Absent metadata is neutral.
func (m modelEntry) dropped() bool {
	if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(m.Policy.State), "disabled")
}

// advertisesEndpoint reports whether the roster advertises path for this row.
func (m modelEntry) advertisesEndpoint(path string) bool {
	for _, e := range m.SupportedEndpoints {
		if strings.EqualFold(strings.TrimSpace(e), path) {
			return true
		}
	}
	return false
}

// route picks the wire protocol the account advertises for the row. Claude
// rows prefer the live-proven Messages path, every other row prefers
// Responses when advertised and otherwise Chat, and a row with no endpoint
// metadata keeps the OpenAI-compatible default. A row that lists endpoints
// but none Evener speaks is unusable and is dropped by the caller.
func (m modelEntry) route() (string, bool) {
	family := strings.ToLower(strings.TrimSpace(m.Capabilities.Family))
	if strings.Contains(family, "claude") && m.advertisesEndpoint("/v1/messages") {
		return registry.ProtocolAnthropic, true
	}
	if len(m.SupportedEndpoints) == 0 {
		return registry.ProtocolOpenAIChat, true
	}
	if m.advertisesEndpoint("/responses") {
		return registry.ProtocolOpenAIResponses, true
	}
	if m.advertisesEndpoint("/chat/completions") {
		return registry.ProtocolOpenAIChat, true
	}
	return "", false
}

// surfaceForFamily maps the account's model family to the registry's surface.
func surfaceForFamily(family string) string {
	switch {
	case strings.Contains(strings.ToLower(family), "claude"):
		return registry.SurfaceAnthropic
	case strings.Contains(strings.ToLower(family), "gemini"):
		return registry.SurfaceGoogle
	case strings.Contains(strings.ToLower(family), "gpt"):
		return registry.SurfaceOpenAI
	}
	return ""
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
	if w := requestutil.FirstPositiveInt(m.ContextLength, m.ContextWindow, m.Capabilities.Limits.MaxPromptTokens); w > 0 {
		caps.ContextWindow = new(w)
	}
	if m.MaxInputTokens > 0 {
		caps.MaxInputTokens = new(m.MaxInputTokens)
	}
	if m.Capabilities.Limits.MaxPromptTokens > 0 {
		caps.MaxInputTokens = new(m.Capabilities.Limits.MaxPromptTokens)
	}
	topProviderCap := requestutil.PositivePointerInt(m.TopProvider.MaxCompletionTokens)
	if o := requestutil.FirstPositiveInt(topProviderCap, m.MaxCompletionTokens, m.MaxOutputTokens, m.Capabilities.Limits.MaxOutputTokens); o > 0 {
		caps.MaxOutputTokens = new(o)
	}
	if len(m.SupportedParameters) > 0 {
		caps.Tools = new(advertises(m.SupportedParameters, "tools"))
		caps.Reasoning = new(advertises(m.SupportedParameters, "reasoning", "reasoning_effort"))
	}
	if v := m.Capabilities.Supports.ToolCalls; v != nil {
		caps.Tools = new(*v)
	}
	if v := m.Capabilities.Supports.Reasoning; v != nil {
		caps.Reasoning = new(*v)
	}
	if m.Reasoning.Mandatory != nil || m.Reasoning.DefaultEnabled != nil || len(m.Reasoning.SupportedEfforts) > 0 {
		caps.Reasoning = new(true)
		if m.Reasoning.Mandatory != nil && *m.Reasoning.Mandatory {
			caps.ThinkingAlwaysOn = new(true)
		}
		caps.EffortValues = requestutil.DedupStrings(m.Reasoning.SupportedEfforts)
	}
	requestutil.ApplyGatewayFacts(&caps, m.Architecture, m.Capabilities.GatewayCapabilities, m.ClientCompat)
	// On the subscription's own roster, the live image proof outranks every
	// shared gateway field. The account's supports.vision, the embedded
	// gateway capabilities.vision, and the OpenAI-compatible
	// architecture.input_modalities all advertise vision more broadly than the
	// probe found working, so the modalities are settled only after all of
	// that parsing: nothing but the proven row may accept images there.
	if m.accountRow() {
		wantsImages := false
		for _, modality := range caps.InputModalities {
			if strings.EqualFold(strings.TrimSpace(modality), "image") {
				wantsImages = true
			}
		}
		if v := m.Capabilities.Supports.Vision; v != nil && *v {
			wantsImages = true
		}
		if provenImageModel(m.ID) && wantsImages {
			caps.InputModalities = []string{"text", "image"}
		} else {
			caps.InputModalities = nil
		}
	}
	// An account roster's prices are not the dollars a live request bills, so
	// they are never published as live cost.
	if !m.accountRow() {
		if in, ok := perTokenCostToPerMillion(m.Pricing.Prompt); ok {
			if out, ok := perTokenCostToPerMillion(m.Pricing.Completion); ok {
				caps.Cost = &registry.Cost{Input: in, Output: out}
			}
		}
	}
	model := registry.Model{ID: m.ID, Caps: caps}
	model.Family = strings.TrimSpace(m.Capabilities.Family)
	model.Surface = surfaceForFamily(model.Family)
	if proto, ok := m.route(); ok {
		model.Protocol = proto
		if proto == registry.ProtocolAnthropic {
			model.Transport = &registry.Transport{
				Endpoint:            "/v1/messages",
				StreamEndpoint:      "/v1/messages",
				CountTokensEndpoint: registry.EndpointUnsupported,
			}
		}
	}
	return model
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
			if e.ID != "" && !e.dropped() {
				if _, ok := e.route(); !ok {
					continue
				}
				rows = append(rows, e.row())
			}
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
