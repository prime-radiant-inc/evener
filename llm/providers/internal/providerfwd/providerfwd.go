// Package providerfwd holds thin forwarder adapters shared by the OpenAI-
// compatible and Anthropic-compatible provider wrappers (glm, kimi,
// openrouter, minimax, openrouter-anthropic).
//
// Each forwarder embeds a backing adapter by its concrete pointer type, which
// promotes the backing type's core method set onto the forwarder. Wrappers
// override Name so they can present their own provider name while inheriting the
// backing completion and model-list behavior. Optional provider-side features
// that are not guaranteed by compatibility APIs must be opted into explicitly.
// (A single generic forwarder is impossible: Go forbids embedding a type
// parameter.)
package providerfwd

import (
	"context"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

// OpenAICompat forwards to an embedded openai-compatible adapter, overriding
// only Name. ListModels and the Complete/Stream methods are promoted from the
// embedded *openaicompat.Adapter.
type OpenAICompat struct {
	*openaicompat.Adapter
	name        string
	defaultName string
}

// NewOpenAICompat wraps backing with the given instance name and provider-type
// default name. Name() returns instanceName when non-empty, otherwise
// defaultName.
func NewOpenAICompat(instanceName, defaultName string, backing *openaicompat.Adapter) *OpenAICompat {
	return &OpenAICompat{Adapter: backing, name: instanceName, defaultName: defaultName}
}

// Name returns the instance name when set, otherwise the provider-type default.
func (f *OpenAICompat) Name() string {
	if f.name != "" {
		return f.name
	}
	return f.defaultName
}

// Anthropic forwards to an embedded Anthropic adapter, overriding only Name.
// ListModels and the Complete/Stream methods are promoted from the embedded
// *anthropic.Adapter.
type Anthropic struct {
	*anthropic.Adapter
	name             string
	defaultName      string
	countInputTokens bool
}

// NewAnthropic wraps backing with the given instance name and provider-type
// default name. Name() returns instanceName when non-empty, otherwise
// defaultName.
func NewAnthropic(instanceName, defaultName string, backing *anthropic.Adapter) *Anthropic {
	return &Anthropic{Adapter: backing, name: instanceName, defaultName: defaultName}
}

// NewAnthropicWithInputTokenCounter wraps backing and opts into forwarding the
// Anthropic count-tokens endpoint. Most Anthropic-compatible proxies do not
// document this endpoint, so the default constructor leaves it disabled.
func NewAnthropicWithInputTokenCounter(instanceName, defaultName string, backing *anthropic.Adapter) *Anthropic {
	return &Anthropic{Adapter: backing, name: instanceName, defaultName: defaultName, countInputTokens: true}
}

// Name returns the instance name when set, otherwise the provider-type default.
func (f *Anthropic) Name() string {
	if f.name != "" {
		return f.name
	}
	return f.defaultName
}

// CountInputTokens is explicit rather than promoted from the backing adapter so
// Anthropic-compatible wrappers do not claim unsupported provider endpoints.
func (f *Anthropic) CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error) {
	if !f.countInputTokens || f.Adapter == nil {
		return llm.InputTokenCount{}, llm.ErrInputTokenCountUnsupported
	}
	out, err := f.Adapter.CountInputTokens(ctx, req)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	out.Provider = f.Name()
	return out, nil
}
