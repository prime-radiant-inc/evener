package llm

import (
	"context"
	"errors"
	"net/http"

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
// Reasoning = false; clamp the effort to EffortValues (an empty ladder
// passes it through; no effort is ever added); apply MaxOutputTokens when
// the request has none; drop request-level sampling parameters the row's
// Sampling or Fields say not to send; gate the prompt-cache fields. It
// returns a shaped copy and never writes through the caller's pointers.
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
	if req.MaxTokens == nil && caps.MaxOutputTokens != nil {
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
	return req
}
