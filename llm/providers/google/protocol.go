package google

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the registry-driven Gemini API implementation (spec §8),
// registered beside the pre-registry Adapter until step 3 deletes it. The
// model always rides in the endpoint path, and the credential in the
// header the transport names (x-goog-api-key, or a Vertex bearer token).
type Protocol struct {
	Client *http.Client
}

// DefaultProtocol is the registered google instance; step 3 sets Client on
// it from the llm client.
var DefaultProtocol = &Protocol{}

func init() { llm.RegisterProtocol(DefaultProtocol) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolGoogle }

var prunablePaths = []string{"cachedContent", "generationConfig.stopSequences", "generationConfig.temperature", "generationConfig.topP", "labels", "safetySettings", "toolConfig"}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	caps := res.Caps
	system, contents, err := toGeminiContents(res.WireID, req.Messages, registry.BoolValue(caps.MultimodalToolResults))
	if err != nil {
		return nil, err
	}
	if caps.Reasoning != nil && !*caps.Reasoning {
		req.ReasoningEffort = nil
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort == "none" {
		req.ReasoningEffort = nil
	}
	options, _ := req.ProviderOptions[registry.ProtocolGoogle].(map[string]any)
	webSearch := req.WebSearch && (caps.WebSearch == nil || *caps.WebSearch)
	return generateContentBody(req, system, contents, webSearch, options)
}
