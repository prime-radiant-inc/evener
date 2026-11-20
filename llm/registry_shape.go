package llm

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// ErrModelListingUnsupported is returned by Protocol.ListModels when the
// transport has no models endpoint (registry.EndpointUnsupported). Callers
// treat it as "registry-only listing", never as a failure (spec §8.1).
var ErrModelListingUnsupported = errors.New("model listing unsupported")

// Protocol is one wire protocol (spec §8.1): openai-chat, openai-responses,
// anthropic, or google. One instance of each is registered at init; base
// URL, headers, auth, and caps arrive in the Resolved record.
type Protocol interface {
	ID() string
	// PrunablePaths must equal registry.PrunablePaths(ID()); a test asserts it.
	PrunablePaths() []string
	BuildBody(req Request, res registry.Resolved) (map[string]any, error)
	Complete(ctx context.Context, req Request, res registry.Resolved) (Response, error)
	Stream(ctx context.Context, req Request, res registry.Resolved) (Stream, error)
	// ListModels returns ErrModelListingUnsupported when res.Transport.ModelsEndpoint is "-".
	ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error)
	// CountTokens returns ErrInputTokenCountUnsupported when res.Transport.CountTokensEndpoint is "-".
	CountTokens(ctx context.Context, req Request, res registry.Resolved) (int, error)
}

// Authenticator sets the auth headers for one auth scheme from
// res.Credential, res.Transport.AuthHeader, and per-instance token state
// keyed by res.Instance (spec §8.1).
type Authenticator interface {
	Apply(ctx context.Context, req *http.Request, res registry.Resolved) error
}

// RequestPreparer is the optional last pass over the built body and the
// HTTP request; only the Codex transport implements it (spec §9.5).
type RequestPreparer interface {
	PrepareRequest(ctx context.Context, req *http.Request, body map[string]any, r Request, res registry.Resolved) error
	RequiresStreamingComplete() bool
}

// ToolSchemaDeriver is implemented by a wire protocol whose builder changes a
// tool definition's parameter schema before sending it.
//
// The local estimate bills a request by its tool bytes, and a builder that
// states an explicit type or closes a parameterless tool's schema changes those
// bytes. Pricing the definitions as they were received therefore prices a list
// other than the one that dispatches: near the input cap that difference admits
// a request the budget would have rejected, or refuses one that fits. The
// protocol owns the answer because the protocol owns the builder —
// EmitToolSchemas must reach the same per-tool decision the builder reaches, so
// the priced list and the emitted list are one list.
type ToolSchemaDeriver interface {
	// EmitToolSchemas returns baseline as this protocol's builder will send it,
	// reporting whether any definition changed. baseline is the list the
	// builder itself is handed, nil Parameters included: a parameterless tool
	// and an explicitly open one are different inputs, and only the first gains
	// the synthesized schema.
	//
	// An unchanged list returns nil and false, so a caller holding a count over
	// baseline reuses it rather than re-measuring the catalog.
	EmitToolSchemas(baseline []ToolDefinition, res registry.Resolved) ([]ToolDefinition, bool)
}

// ProjectToolSchemas is the loop every EmitToolSchemas implementation runs: it
// maps baseline through project, which returns the schema tool i is sent with
// and reports whether that is the caller's own map.
//
// An untouched tool keeps the definition it arrived as, so the bytes the caller
// measured over baseline still describe it. The first tool that is not leaves a
// clone of the list behind, and the remaining tools write into that clone. A
// list none of which changed returns nil and false, so its caller keeps the
// count it already holds rather than re-measuring the catalog.
func ProjectToolSchemas(baseline []ToolDefinition, project func(t ToolDefinition) (params map[string]any, untouched bool)) ([]ToolDefinition, bool) {
	var sent []ToolDefinition
	for i, t := range baseline {
		params, untouched := project(t)
		if untouched {
			continue
		}
		if sent == nil {
			sent = slices.Clone(baseline)
		}
		sent[i].Parameters = params
	}
	return sent, sent != nil
}

// samplingPaths names the prunable paths that carry the request-level
// temperature, top-p, and stop values on each protocol (spec §8.2). An
// empty stop path means the protocol has no stop parameter.
func samplingPaths(protocol string) (temperature, topP, stop string) {
	switch protocol {
	case registry.ProtocolGoogle:
		return "generationConfig.temperature", "generationConfig.topP", "generationConfig.stopSequences"
	case registry.ProtocolAnthropic:
		return "temperature", "top_p", "stop_sequences"
	case registry.ProtocolOpenAIResponses:
		return "temperature", "top_p", ""
	default:
		return "temperature", "top_p", "stop"
	}
}

// ShapeRequest is the single place request-level shaping happens (spec
// §7.5), in this order: clear reasoning controls when the row has
// Reasoning = false; clamp the effort to EffortValues (an empty ladder leaves
// the effort for the protocol builder, which drops a name it cannot vouch for
// but still sizes budget-shaped thinking from it; no effort is ever added
// here — §7.4's rule in agent is what sets one); apply MaxOutputTokens when
// the request has none; drop
// request-level sampling parameters the row's
// Sampling or Fields say not to send; gate the prompt-cache fields; turn
// store on for a planned Responses continuation. It returns a shaped copy
// and never writes through the caller's pointers. Continuation planning
// shapes the request the same way, when the name resolves, before it builds
// the body or calls an override's planner, so a plan describes the request
// that will be sent.
func ShapeRequest(req Request, res registry.Resolved) Request {
	caps := res.Caps
	send := func(path string) bool {
		if path == "" {
			return false
		}
		v, ok := caps.Fields[path]
		return !ok || v
	}
	if caps.Reasoning != nil && !*caps.Reasoning {
		req.ReasoningEffort = nil
	}
	if req.ReasoningEffort != nil && len(caps.EffortValues) > 0 {
		clamped := ClampReasoningEffort(*req.ReasoningEffort, caps.EffortValues)
		req.ReasoningEffort = &clamped
	}
	if caps.MaxOutputTokens != nil && *caps.MaxOutputTokens > 0 &&
		(req.MaxTokens == nil || *req.MaxTokens > *caps.MaxOutputTokens) {
		v := *caps.MaxOutputTokens
		req.MaxTokens = &v
	}
	tempPath, topPPath, stopPath := samplingPaths(res.Protocol)
	samplingOff := caps.Sampling != nil && !*caps.Sampling
	if samplingOff || !send(tempPath) {
		req.Temperature = nil
	}
	if samplingOff || !send(topPPath) {
		req.TopP = nil
	}
	if !send(stopPath) {
		req.StopSequences = nil
	} else if caps.MaxStopSequences != nil && len(req.StopSequences) > *caps.MaxStopSequences {
		req.StopSequences = append([]string(nil), req.StopSequences[:*caps.MaxStopSequences]...)
	}
	if !caps.Fields["prompt_cache_key"] {
		req.PromptCacheKey = ""
	}
	if !caps.Fields["prompt_cache_retention"] {
		req.PromptCacheRetention = ""
	}
	// A planned Responses continuation needs server-side storage; the
	// continuation store override (spec §7.6) turns store on unless the
	// caller decided it explicitly. continuationAvailable is §7.6's own gate,
	// so a row that cannot carry the anchor is never told to store for one.
	plannedContinuation := continuationAvailable(res) &&
		req.HistoryMode == HistoryModeResponsesDelta && strings.TrimSpace(req.PreviousResponseID) != ""
	if plannedContinuation && req.Store == nil {
		req.Store = new(true)
	}
	// A target receives only tool-result media its row can see; direct client
	// callers go through this shaping too, so the estimate and the wire agree
	// (#4217).
	req = dropUnseenToolResultMedia(req, res)
	return req
}

// dropUnseenToolResultMedia returns req with tool-result media bytes the
// resolved row cannot see removed; the tool result's text stays. It returns
// req unchanged when nothing drops and never mutates the caller's content.
func dropUnseenToolResultMedia(req Request, res registry.Resolved) Request {
	needs := false
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 &&
				!RowSeesToolResultMedia(res, p.ToolResult.ImageMediaType) {
				needs = true
				break
			}
		}
		if needs {
			break
		}
	}
	if !needs {
		return req
	}
	messages := append([]Message(nil), req.Messages...)
	for i := range messages {
		content := append([]ContentPart(nil), messages[i].Content...)
		for j := range content {
			p := &content[j]
			if p.Kind != ContentToolResult || p.ToolResult == nil || len(p.ToolResult.ImageData) == 0 ||
				RowSeesToolResultMedia(res, p.ToolResult.ImageMediaType) {
				continue
			}
			tr := *p.ToolResult
			tr.ImageData, tr.ImageMediaType = nil, ""
			p.ToolResult = &tr
		}
		messages[i].Content = content
	}
	req.Messages = messages
	return req
}
