// Package providerfwd holds thin forwarder adapters shared by the OpenAI-
// compatible and Anthropic-compatible provider wrappers (glm, kimi,
// openrouter, minimax, openrouter-anthropic).
//
// Each forwarder embeds a backing adapter by its concrete pointer type, which
// promotes the backing type's full method set — Name, Complete, Stream and the
// optional ListModels — onto the forwarder. Only Name is overridden, so a
// wrapper can present its own provider name while inheriting every backing
// capability automatically. (A single generic forwarder is impossible: Go
// forbids embedding a type parameter.)
package providerfwd

import (
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
	name        string
	defaultName string
}

// NewAnthropic wraps backing with the given instance name and provider-type
// default name. Name() returns instanceName when non-empty, otherwise
// defaultName.
func NewAnthropic(instanceName, defaultName string, backing *anthropic.Adapter) *Anthropic {
	return &Anthropic{Adapter: backing, name: instanceName, defaultName: defaultName}
}

// Name returns the instance name when set, otherwise the provider-type default.
func (f *Anthropic) Name() string {
	if f.name != "" {
		return f.name
	}
	return f.defaultName
}
