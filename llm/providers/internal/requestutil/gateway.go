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
// A usable ladder is itself evidence the model tunes effort, so it raises a
// verdict inferred elsewhere (a provider-wide parameter list, the catalog) to
// true. An explicit capabilities verdict still wins: a gateway saying the model
// does not reason is not overruled by a stale map beside it.
//
// A row the gateway marks non-reasoning carries no ladder. Clearing it here,
// rather than merely skipping the write, is what keeps the raw live row
// honest: a listing that also publishes supported_efforts has already set one,
// and the row reaches ApplyLive, the live snapshot, and standalone listings
// before derivation would clear it. A published map with no usable level is the
// gateway saying the same thing, so it too clears any inherited ladder rather
// than letting the replay keep one the gateway marks unsupported.
func ApplyGatewayReasoning(caps *registry.Caps, capsFacts GatewayCapabilities, compat GatewayClientCompat) {
	ladder, published := GatewayEffortLadder(compat)
	if capsFacts.Reasoning != nil {
		caps.Reasoning = capsFacts.Reasoning
		if !*capsFacts.Reasoning {
			caps.EffortValues = nil
			return
		}
	} else if len(ladder) > 0 {
		caps.Reasoning = new(true)
	}
	if published {
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
// published reports whether the listing stated a map at all. Absent (no
// client_compat block, or a Pi block carrying only its other compat fields)
// leaves the ladder nil so the live replay inherits a curated one. A published
// map whose every level is null is the gateway stating that the model takes no
// effort control: the ladder is empty and the caller must apply it as such, not
// inherit around it.
func GatewayEffortLadder(compat GatewayClientCompat) (ladder []string, published bool) {
	if compat.Pi == nil || compat.Pi.ThinkingLevelMap == nil {
		return nil, false
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
	// Non-nil even when nothing survived: the live layer merges a non-nil
	// slice wholesale, and that is how a published map with no usable level
	// clears an inherited ladder instead of inheriting around it.
	if ladder = DedupStrings(out); ladder == nil {
		ladder = []string{}
	}
	return ladder, true
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
