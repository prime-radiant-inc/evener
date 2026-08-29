// Package chatcompletions implements the OpenAI Chat Completions wire
// protocol (registry.ProtocolOpenAIChat) as an llm.Protocol driven entirely
// by registry.Resolved: base URL, headers, auth scheme, and every quirk
// arrive as data (spec §8). It consolidates the two Chat Completions
// builders that existed before the registry (openaicompat and the openai
// adapter's chat fallback).
package chatcompletions

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the single registered openai-chat implementation. Client is
// nil for protocolhttp.DefaultClient; tests inject httptest clients.
type Protocol struct {
	Client *http.Client
}

// DefaultProtocol is the registered openai-chat instance; step 3 sets
// Client on it from the llm client.
var DefaultProtocol = &Protocol{}

func init() { llm.RegisterProtocol(DefaultProtocol) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolOpenAIChat }

// prunablePaths is the protocol's own statement of the optional wire
// fields its builder emits; TestPrunablePathsMatchRegistry proves it
// equals the registry's table (spec §8.2).
var prunablePaths = []string{
	registry.FieldDeveloperRole, "frequency_penalty", "logprobs", registry.FieldMaxTokens, "metadata", "n",
	"parallel_tool_calls", "presence_penalty", "prompt_cache_key", "prompt_cache_retention", "seed",
	"service_tier", "stop", "store", "stream_options", "temperature", "top_p", "user",
}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol for a non-streaming request.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	return buildBody(req, res, false)
}
