package anthropic

import (
	"net/http"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const anthropicVersion = "2023-06-01"

// Protocol is the registry-driven Messages API implementation (spec §8),
// registered beside the pre-registry Adapter until step 3 deletes it.
type Protocol struct {
	Client *http.Client
}

// DefaultProtocol is the registered anthropic instance; step 3 sets Client
// on it from the llm client.
var DefaultProtocol = &Protocol{}

func init() { llm.RegisterProtocol(DefaultProtocol) }

// ID implements llm.Protocol.
func (*Protocol) ID() string { return registry.ProtocolAnthropic }

var prunablePaths = []string{"container", "fallbacks", registry.FieldMaxTokens, "metadata", "service_tier", "stop_sequences", "temperature", "top_p"}

// PrunablePaths implements llm.Protocol.
func (*Protocol) PrunablePaths() []string {
	out := slices.Clone(prunablePaths)
	slices.Sort(out)
	return out
}

// BuildBody implements llm.Protocol.
func (*Protocol) BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error) {
	return buildProtocolBody(req, res)
}

// betaHeader merges the row's anthropic-beta header (the curated [1m] alias
// rows carry one) with the caller's beta_headers, comma-joined without
// duplicates.
func betaHeader(res registry.Resolved, req llm.Request) string {
	var betas []string
	add := func(list string) {
		for b := range strings.SplitSeq(list, ",") {
			if b = strings.TrimSpace(b); b != "" && !slices.Contains(betas, b) {
				betas = append(betas, b)
			}
		}
	}
	add(res.Headers["anthropic-beta"])
	add(betaHeaderFromProviderOptions(req.ProviderOptions))
	return strings.Join(betas, ",")
}
