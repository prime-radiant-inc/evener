// Package chatcompletions implements the OpenAI Chat Completions wire
// protocol (registry.ProtocolOpenAIChat) as an llm.Protocol driven entirely
// by registry.Resolved: base URL, headers, auth scheme, and every quirk
// arrive as data (spec §8). It consolidates the two Chat Completions
// builders that existed before the registry (openaicompat and the openai
// adapter's chat fallback).
package chatcompletions

import (
	"context"
	"errors"
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

func init() { llm.RegisterProtocol(&Protocol{}) }

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

// errUnimplemented is returned by the transport methods below, which Task 7
// replaces with the real HTTP call, stream decoder, and model listing. Task
// 6 only builds request bodies, but llm.Protocol's method set — and the
// init() registration TestPrunablePathsMatchRegistry depends on — requires
// all seven methods to exist, so these four are temporary stand-ins.
var errUnimplemented = errors.New("chatcompletions: not implemented until Task 7")

// Complete implements llm.Protocol; Task 7 replaces this stub.
func (*Protocol) Complete(context.Context, llm.Request, registry.Resolved) (llm.Response, error) {
	return llm.Response{}, errUnimplemented
}

// Stream implements llm.Protocol; Task 7 replaces this stub.
func (*Protocol) Stream(context.Context, llm.Request, registry.Resolved) (llm.Stream, error) {
	return nil, errUnimplemented
}

// ListModels implements llm.Protocol; Task 7 replaces this stub.
func (*Protocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return nil, errUnimplemented
}

// CountTokens implements llm.Protocol; Task 7 replaces this stub.
func (*Protocol) CountTokens(context.Context, llm.Request, registry.Resolved) (int, error) {
	return 0, errUnimplemented
}
