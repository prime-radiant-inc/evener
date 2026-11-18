// Package responses implements the OpenAI Responses wire protocol
// (registry.ProtocolOpenAIResponses) as an llm.Protocol driven by
// registry.Resolved (spec §8). It is the Responses half of the pre-registry
// openai adapter; the Codex transport's headers and body rules live in
// llm/providers/tokenauth and reach this package only through the
// RequestPreparer hook of protocolhttp.
package responses

import (
	"net/http"
	"reflect"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the single registered openai-responses implementation.
// Client is nil for protocolhttp.DefaultClient. Hasher, when set, stamps
// resp.Raw["id_hash"] for the session's continuation bookkeeping (spec
// §7.6); it stands in for the hasher a dispatching client attaches to the
// context, which wins when both are present.
type Protocol struct {
	Client *http.Client
	Hasher *llm.ContinuationHasher
}

// DefaultProtocol is the registered openai-responses instance; it is the
// handle a test sets Client or Hasher on.
var DefaultProtocol = &Protocol{}

func init() { llm.RegisterProtocol(DefaultProtocol) }

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

// EmitToolSchemas implements llm.ToolSchemaDeriver.
//
// The builder reaches its per-tool decision through toolSchema; this maps the
// same list through the same function, so the local estimate prices the request
// the builder emits rather than the one it received. Two differences make that
// matter near the input cap: a typeless closed value set gains an explicit type
// on both the strict and the refused paths, and a parameterless tool gains a
// synthesized schema.
//
// baseline is the list the request's byte count describes, so a nil Parameters
// means the count priced the builder's substitute and not the caller's nil. The
// same toolSchema call answers both, which is the point of extracting it.
//
// prepared is unused: this builder reads a definition's nil Parameters itself
// and does not branch on how the request arrived.
func (*Protocol) EmitToolSchemas(baseline []llm.ToolDefinition, res registry.Resolved, prepared bool) ([]llm.ToolDefinition, bool) {
	if len(baseline) == 0 {
		return nil, false
	}
	strict := registry.BoolValue(res.Caps.StrictTools)
	sent := make([]llm.ToolDefinition, len(baseline))
	copy(sent, baseline)
	changed := false
	for i, t := range baseline {
		params, _ := toolSchema(t, strict)
		if reflect.DeepEqual(params, t.Parameters) {
			continue
		}
		// DeepEqual is the wrong oracle for a byte count. The projection sorts
		// each object's required list, which reorders a schema without changing
		// what it serializes to: a catalog whose tools are already strictly
		// shaped would then be re-measured on every dispatch even though the
		// count it already holds is exact. What matters here is the emitted
		// length, so the schemas are compared for it instead of trusted to be
		// equal, and only a schema whose projection really costs bytes is
		// reported as changed. The comparison is the projection's own answer
		// (SameSerializedLength) rather than two marshals of the catalog, since
		// admission and dispatch both ask this question and the answer for a
		// reordered required list is the same every time.
		if openaichat.SameSerializedLength(t.Parameters, params) {
			continue
		}
		sent[i].Parameters = params
		changed = true
	}
	if !changed {
		return nil, false
	}
	return sent, true
}
