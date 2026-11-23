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
	SupportedEndpoints  *[]string                       `json:"supported_endpoints"`
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

// dropped reports subscription-roster rows the account asks Evener not to
// offer: a row the picker excludes and a policy-disabled row. Absent metadata
// is neutral. The caller applies this only to the Copilot roster.
func (m modelEntry) dropped() bool {
	if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(m.Policy.State), "disabled")
}

// endpoints returns the advertised endpoint list, or nil when the field is
// absent. Presence matters on the Copilot roster: absent metadata falls back to
// Chat, while an explicitly empty list advertises no supported protocol.
func (m modelEntry) endpoints() []string {
	if m.SupportedEndpoints == nil {
		return nil
	}
	return *m.SupportedEndpoints
}

// advertisesEndpoint reports whether the roster advertises path for this row.
func (m modelEntry) advertisesEndpoint(path string) bool {
	for _, e := range m.endpoints() {
		if strings.EqualFold(strings.TrimSpace(e), path) {
			return true
		}
	}
	return false
}

// route picks the wire protocol the Copilot account advertises for the row.
// Claude rows prefer the live-proven Messages path, every other row prefers
// Responses when advertised and otherwise Chat, and a row with no endpoint
// metadata keeps the OpenAI-compatible default. An explicitly empty endpoint
// list, or a list with no path Evener speaks, is unusable and is dropped by
// the caller.
func (m modelEntry) route() (string, bool) {
	family := strings.ToLower(strings.TrimSpace(m.Capabilities.Family))
	if strings.Contains(family, "claude") && m.advertisesEndpoint("/v1/messages") {
		return registry.ProtocolAnthropic, true
	}
	endpoints := m.endpoints()
	if endpoints == nil {
		return registry.ProtocolOpenAIChat, true
	}
	if len(endpoints) == 0 {
		return "", false
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
// copilot selects the subscription roster's policy: its advertised prices are
// not billed as live dollars and only the live-proven image row may accept
// images.
func (m modelEntry) row(copilot bool) registry.Model {
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
	if copilot {
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
			// An explicit text-only row, never nil: a nil Cap leaves the
			// bundled models.dev capabilities free to restore the vision an
			// unproven row must not offer.
			caps.InputModalities = []string{"text"}
		}
	}
	// An account roster's prices are not the dollars a live request bills, so
	// they are never published as live cost.
	if !copilot {
		if in, ok := perTokenCostToPerMillion(m.Pricing.Prompt); ok {
			if out, ok := perTokenCostToPerMillion(m.Pricing.Completion); ok {
				caps.Cost = &registry.Cost{Input: in, Output: out}
			}
		}
	}
	model := registry.Model{ID: m.ID, Caps: caps}
	model.Family = strings.TrimSpace(m.Capabilities.Family)
	model.Surface = surfaceForFamily(model.Family)
	if copilot {
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
		// The Copilot subscription roster is selected by its transport auth
		// scheme, never by a row's payload shape: an OpenAI-compatible gateway
		// may publish the same endpoint, family, policy, pricing and vision
		// fields and must keep its own behavior.
		copilot := res.Transport.Auth == registry.AuthOAuthGitHubCopilot
		for _, e := range payload.Data {
			if e.ID == "" {
				continue
			}
			if copilot {
				if e.dropped() {
					continue
				}
				if _, ok := e.route(); !ok {
					continue
				}
			}
			rows = append(rows, e.row(copilot))
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
