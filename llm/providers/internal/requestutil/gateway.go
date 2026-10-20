package requestutil

import (
	"slices"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// GatewayArchitecture is the model-facts object an OpenRouter-shaped /models
// listing publishes per row.
type GatewayArchitecture struct {
	InputModalities []string `json:"input_modalities"`
}

// GatewayCapabilities is the capability object some OpenAI-compatible gateways
// publish per /models row (lunaroute's shape).
type GatewayCapabilities struct {
	Vision    *bool `json:"vision"`
	Reasoning *bool `json:"reasoning"`
}

// GatewayClientCompat is the per-model compatibility block some gateways
// publish (lunaroute's shape). Only the Pi client's reasoning map is read.
type GatewayClientCompat struct {
	Pi *GatewayPiCompat `json:"pi"`
}

// GatewayPiCompat is one gateway model as the Pi client sees it.
type GatewayPiCompat struct {
	// ThinkingLevelMap maps an evener reasoning level to the wire string the
	// gateway accepts. A null value decodes as the empty string and, like an
	// absent key, is a level the model does not support.
	ThinkingLevelMap map[string]string `json:"thinkingLevelMap"`
}

// ApplyGatewayReasoning folds a row's own per-model reasoning facts
// (lunaroute's shape) into caps: the capabilities.reasoning verdict, then the
// client_compat effort ladder. Both readers share it so they cannot disagree
// about the precedence — the gateway knows per model what a listing's
// provider-wide parameters only infer, and a fact it does not state leaves what
// the caller inferred in place.
//
// A row the gateway marks non-reasoning states no ladder: the model has no
// effort control, and a stale map beside the verdict must not survive
// derivation's clean-up as a ladder the picker offers.
func ApplyGatewayReasoning(caps *registry.Caps, capsFacts GatewayCapabilities, compat GatewayClientCompat) {
	if capsFacts.Reasoning != nil {
		caps.Reasoning = capsFacts.Reasoning
		if !*capsFacts.Reasoning {
			return
		}
	}
	if ladder := GatewayEffortLadder(compat); len(ladder) > 0 {
		caps.EffortValues = ladder
	}
}

// GatewayEffortLadder returns the effort ladder a gateway's per-model
// client_compat states, ordered as evener displays one: an explicit off level
// first when the model has one, then minimal → max. A null value, or no key at
// all, is a level the model does not support; a value outside evener's
// vocabulary is dropped rather than offered, because the clamp cannot rank it.
// Values are wire spellings deduped in vocabulary order, so two keys sharing a
// value (glm maps medium and high to "high") advertise it once.
//
// Nil means the listing stated no usable map, so the live replay inherits any
// curated ladder instead of erasing it. That includes a map whose every level
// is null: a model with no effort control is stated by reasoning = false,
// which the capabilities block carries.
func GatewayEffortLadder(compat GatewayClientCompat) []string {
	if compat.Pi == nil {
		return nil
	}
	levels := compat.Pi.ThinkingLevelMap
	// off plus the six ranked levels, the most a full map can advertise.
	out := make([]string, 0, 7)
	if levels["off"] == llm.ReasoningEffortNone {
		out = append(out, llm.ReasoningEffortNone)
	}
	for _, level := range llm.ReasoningEffortVocabulary() {
		if wire := levels[level]; llm.ReasoningEffortRank(wire) != 0 {
			out = append(out, wire)
		}
	}
	return DedupStrings(out)
}

// GatewayInputModalities is the input modality list a /models row advertises:
// architecture.input_modalities when that list carries any entry, else text
// plus image for an advertised vision capability, else nil. The protocol
// readers share it so they cannot disagree about the precedence.
func GatewayInputModalities(arch GatewayArchitecture, caps GatewayCapabilities) []string {
	if mods := DedupStrings(arch.InputModalities); len(mods) > 0 {
		return mods
	}
	if caps.Vision != nil && *caps.Vision {
		return []string{"text", "image"}
	}
	return nil
}

// DedupStrings drops empty entries and duplicates from an advertised list, in
// first-seen order, and returns nil when nothing survives. Every live-listing
// modality or effort list goes through it: a raw list a gateway publishes would
// otherwise merge wholesale over a curated one, and an empty one would erase it
// (llm/registry/merge.go).
func DedupStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
