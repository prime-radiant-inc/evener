// Package responses implements the OpenAI Responses wire protocol
// (registry.ProtocolOpenAIResponses) as an llm.Protocol driven by
// registry.Resolved (spec §8). It is the Responses half of the pre-registry
// openai adapter; the Codex transport's headers and body rules live in
// llm/providers/tokenauth and reach this package only through the
// RequestPreparer hook of protocolhttp.
package responses

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the single registered openai-responses implementation.
// Client is nil for protocolhttp.DefaultClient. Hasher, when set, stamps
// resp.Raw["id_hash"] for the session's continuation bookkeeping (spec
// §7.6); step 3 wires it from the client's state root.
type Protocol struct {
	Client *http.Client
	Hasher *llm.ContinuationHasher
}

func init() { llm.RegisterProtocol(&Protocol{}) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolOpenAIResponses }

var prunablePaths = []string{
	"background", "conversation", "include", "max_output_tokens", "max_tool_calls", "metadata",
	"parallel_tool_calls", "previous_response_id", "prompt_cache_key", "prompt_cache_retention",
	"reasoning.context", "safety_identifier", "service_tier", "store", "temperature", "text.verbosity",
	"top_p", "truncation",
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
