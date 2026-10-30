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
// publish (lunaroute's shape).
type GatewayClientCompat struct {
	Pi *GatewayPiCompat `json:"pi"`
}

// GatewayPiCompat is one gateway model as the Pi client sees it.
type GatewayPiCompat struct {
	// ThinkingLevelMap maps an evener reasoning level to the wire string the
	// gateway accepts. A null value decodes as the empty string and, like an
	// absent key, is a level the model does not support.
	ThinkingLevelMap map[string]string `json:"thinkingLevelMap"`
	// ThinkingFormat is the reasoning dialect to spell for this model.
	ThinkingFormat string `json:"thinkingFormat"`
	// MaxTokensField is the output-limit spelling this model takes.
	MaxTokensField string `json:"maxTokensField"`
	// SupportsReasoningEffort says whether the model takes an effort
	// parameter at all. Nil means the gateway states nothing.
	SupportsReasoningEffort *bool `json:"supportsReasoningEffort"`
}

// ApplyGatewayFacts folds a row's own per-model facts (lunaroute's shape) into
// caps: the reasoning verdict and effort ladder, then the wire-shaping dialect
// and output-limit spelling the gateway states for that model. Both /models
// readers share it so they cannot disagree about the precedence — the gateway
// knows per model what a listing's provider-wide parameters only infer, and a
// fact it does not state leaves what the caller inferred in place.
func ApplyGatewayFacts(caps *registry.Caps, arch GatewayArchitecture, capsFacts GatewayCapabilities, compat GatewayClientCompat) {
	applyGatewayReasoning(caps, capsFacts, compat)
	if pi := compat.Pi; pi != nil {
		// A value outside the registry's vocabulary is dropped rather than
		// carried: an unknown dialect writes no reasoning control at all, and
		// an unknown output spelling reaches no field, so carrying one would
		// silently switch that behavior off for the row.
		if registry.ValidThinkingFormat(pi.ThinkingFormat) {
			caps.ThinkingFormat = new(pi.ThinkingFormat)
		}
		if registry.ValidMaxTokensField(pi.MaxTokensField) {
			caps.MaxTokensField = new(pi.MaxTokensField)
		}
	}
	caps.InputModalities = gatewayInputModalities(arch, capsFacts)
}

// applyGatewayReasoning is ApplyGatewayFacts' reasoning half.
//
// The two verdicts a gateway can state are checked in precedence order. An
// explicit capabilities verdict on reasoning wins outright: a non-reasoning row
// carries no ladder and no effort control, whatever a map or the effort field
// beside it says. Next, a model the gateway says takes no effort parameter is
// toggle-only — it reasons, but no level ever reaches the wire, so the ladder
// is neither built nor kept. Only then is the map itself consulted, and a
// usable ladder is evidence the model tunes effort: it raises a verdict
// inferred elsewhere (a provider-wide parameter list, the catalog) to true.
//
// A row the gateway marks non-reasoning carries no ladder. Clearing it here,
// rather than merely skipping the write, is what keeps the raw live row
// honest: a listing that also publishes supported_efforts has already set one,
// and the row reaches ApplyLive, the live snapshot, and standalone listings
// before derivation would clear it. A published map with no usable level is the
// gateway saying the same thing, so it too clears any inherited ladder rather
// than letting the replay keep one the gateway marks unsupported.
func applyGatewayReasoning(caps *registry.Caps, capsFacts GatewayCapabilities, compat GatewayClientCompat) {
	// An explicit capabilities verdict is the gateway's own knowledge, and a
	// negative one ends the matter: nothing below may reopen it. A verdict the
	// caller already inferred (a provider-wide parameter list, the catalog) is
	// weaker, and a usable ladder below still raises it.
	if capsFacts.Reasoning != nil {
		caps.Reasoning = capsFacts.Reasoning
		if !*capsFacts.Reasoning {
			caps.EffortValues = nil
			return
		}
	}
	// The gateway's verdict is recorded in both directions. A `true` must clear
	// an inherited rejection as well as a `false` creating one: an alias target
	// or a curated layer can supply either value, and letting a stale rejection
	// survive would make derivation discard a ladder the gateway just advertised.
	if pi := compat.Pi; pi != nil && pi.SupportsReasoningEffort != nil {
		caps.EffortParameterUnsupported = new(!*pi.SupportsReasoningEffort)
	}
	// A model the gateway says takes no effort parameter publishes an empty
	// ladder, so the live layer replaces a curated one instead of inheriting it.
	// The controls themselves are derivation's call: it removes the effort
	// control while preserving a numeric budget, which a wholesale write here
	// would destroy before derivation ever saw it.
	if pi := compat.Pi; pi != nil && pi.SupportsReasoningEffort != nil && !*pi.SupportsReasoningEffort {
		caps.EffortValues = []string{}
		// The map still proves the model reasons, even though its ladder is
		// not published. Without this, a row whose verdict was inferred false
		// from a provider-wide parameter list loses the controls derivation
		// just installed, and request shaping drops reasoning entirely. A
		// published map with no usable level proves nothing, though: it says
		// the model takes no effort, not that it reasons.
		if hasUsableLevel(pi.ThinkingLevelMap) {
			caps.Reasoning = new(true)
		}
		return
	}
	ladder, published := gatewayEffortLadder(compat)
	if len(ladder) > 0 {
		caps.Reasoning = new(true)
	}
	if published {
		caps.EffortValues = ladder
	}
}

// gatewayEffortLadder returns the effort ladder a gateway's per-model
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
func gatewayEffortLadder(compat GatewayClientCompat) (ladder []string, published bool) {
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

// hasUsableLevel reports whether a thinking-level map names at least one level
// evener can rank. It is the evidence that the model reasons, separate from
// whether its ladder is publishable.
func hasUsableLevel(levels map[string]string) bool {
	for _, wire := range levels {
		if llm.ReasoningEffortRank(wire) != 0 {
			return true
		}
	}
	return false
}

// gatewayInputModalities is the input modality list a /models row advertises:
// architecture.input_modalities when that list carries any entry, else text
// plus image for an advertised vision capability, else nil. The protocol
// readers share it so they cannot disagree about the precedence.
func gatewayInputModalities(arch GatewayArchitecture, caps GatewayCapabilities) []string {
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
