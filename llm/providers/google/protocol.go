package google

import (
	"net/http"
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Protocol is the Google Interactions API implementation (spec §8). Both
// Gemini Developer API and Vertex AI serve POST .../interactions with the model
// ID carried in the JSON body.
type Protocol struct {
	Client *http.Client
	Hasher *llm.ContinuationHasher
}

// DefaultProtocol is the registered google instance; tests set Client on it.
var DefaultProtocol = &Protocol{}

func init() { llm.RegisterProtocol(DefaultProtocol) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolGoogle }

var prunablePaths = []string{
	"generation_config.max_output_tokens",
	"generation_config.seed",
	"generation_config.stop_sequences",
	"generation_config.temperature",
	registry.FieldGoogleThinkingLevel,
	registry.FieldGoogleThinkingSummaries,
	"generation_config.tool_choice",
	"generation_config.top_p",
	"labels",
	"previous_interaction_id",
	"safety_settings",
	"service_tier",
	"store",
}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (out map[string]any, err error) {
	defer func() { err = llm.RewriteErrorProvider(err, res.Instance) }()
	body, _, err := buildBodyWithPolicy(req, res, false, false)
	return body, err
}
