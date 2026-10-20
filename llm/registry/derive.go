package registry

import (
	"slices"
	"strings"
)

// deriveInput carries what the §7.4 derivations need beyond the caps.
type deriveInput struct {
	Protocol        string
	Synthesized     bool
	ProviderSurface string
	ProviderFamily  string
	// Aliased marks a row seeded from an alias target. AliasTargetProtocol is
	// the protocol that target resolved under. A same-protocol alias inherits
	// the target's settled ThinkingShape verdict (even when the verdict was
	// "no shape") and never re-derives one. A cross-protocol alias carries no
	// verdict — the shape belongs to the target's protocol — and derives from
	// whatever its own replay leaves. AliasControlsRecorded says the target's
	// own catalog row recorded the effort control (not derivation), so the
	// inherited control counts as evidence even though it arrived tagged
	// "alias".
	Aliased               bool
	AliasTargetProtocol   string
	AliasControlsRecorded bool
}

const provDerived = "derived"

// derive applies the spec §7.4 derivations, in order, to a merged row:
// effort pass-through, the junk-cap guard, the surface fallback, and the
// anthropic thinking shape / always-on / display. Each is applied only
// where the field is still unset (the junk cap excepted) and recorded as
// "derived" in prov.
func derive(c *Caps, m *Model, in deriveInput, prov map[string]string) {
	reasoningOff := c.Reasoning != nil && !*c.Reasoning
	if reasoningOff {
		if c.ReasoningControls != nil || c.EffortValues != nil {
			c.ReasoningControls, c.EffortValues = nil, nil
			prov["ReasoningControls"], prov["EffortValues"] = provDerived, provDerived
		}
	}
	// A row that states the no-effort verdict takes no effort parameter,
	// whichever layer stated it, so it publishes no ladder. The controls are
	// deliberately left alone: they are the evidence the thinking-shape
	// derivation below reads, and rejecting an effort parameter says nothing
	// about supporting budget thinking. EffortCapable answers the request-side
	// question instead.
	if !reasoningOff && BoolValue(c.EffortParameterUnsupported) {
		if len(c.EffortValues) > 0 {
			c.EffortValues = []string{}
			prov["EffortValues"] = provDerived
		}
	}

	// 1. Effort control pass-through. Capture the pre-augmentation state:
	// the google shape check below must not mistake an effort control this
	// pass-through invented for catalog evidence.
	controlsWereEmpty := len(c.ReasoningControls) == 0
	effortWasPresent := slices.Contains(c.ReasoningControls, "effort")
	if !reasoningOff {
		if controlsWereEmpty {
			c.ReasoningControls = []string{"effort"}
			prov["ReasoningControls"] = provDerived
		} else if len(c.EffortValues) > 0 && !slices.Contains(c.ReasoningControls, "effort") {
			c.ReasoningControls = append(append([]string(nil), c.ReasoningControls...), "effort")
			prov["ReasoningControls"] = provDerived
		}
	}

	// 2. Junk output cap from a catalog or live layer.
	if c.MaxOutputTokens != nil && c.ContextWindow != nil && *c.MaxOutputTokens >= *c.ContextWindow {
		src := prov["MaxOutputTokens"]
		if strings.HasPrefix(src, LayerSnapshot) || strings.HasPrefix(src, LayerCache) || strings.HasPrefix(src, LayerLive) {
			c.MaxOutputTokens = nil
			prov["MaxOutputTokens"] = provDerived
		}
	}

	// 3. Surface fallback.
	if m.Surface == "" {
		m.Surface = in.ProviderSurface
		if m.Surface == "" {
			m.Surface = SurfaceGeneric
		}
		prov["Surface"] = provDerived
	}

	// Google's thinking shape mirrors the anthropic rule at its one decision:
	// a cataloged row (not synthesized) that records budget_tokens is
	// budget-shaped (the 2.5 family), and a cataloged row whose catalog
	// itself records effort is level-shaped (3.x and later). An effort control
	// that derivation added for an empty list is not evidence — the catalog
	// recorded no reasoning_options, and a 2.5-era row in that state rejects
	// the level field — so it, a toggle-only row, and a synthesized or
	// live-only row all leave the shape unset rather than guess a spelling
	// the model may reject. An alias inherits its target's vouched shape, so
	// a same-protocol alias never re-derives one from the copied controls; a
	// cross-protocol alias carries no verdict (its target's protocol has no
	// shape) and derives from the inherited catalog controls like any
	// directly named row.
	// A same-protocol alias inherits the target's settled verdict only while
	// its controls remain purely inherited; the alias's own layers restating
	// the controls means the verdict is stale and derivation must run.
	// When the verdict is stale and the shape arrived through the alias (not
	// an explicit override), clear it so derivation can re-derive from the
	// alias's own evidence.
	shapeSettledByAlias := in.Aliased && in.AliasTargetProtocol == ProtocolGoogle && prov["ReasoningControls"] == provAlias
	if in.Aliased && !shapeSettledByAlias && prov["ThinkingShape"] == provAlias {
		c.ThinkingShape = nil
	}
	if in.Protocol == ProtocolGoogle && !reasoningOff && c.ThinkingShape == nil && !in.Synthesized && !shapeSettledByAlias {
		var shape string
		switch {
		case slices.Contains(c.ReasoningControls, "budget_tokens"):
			shape = "budget"
		case slices.Contains(c.ReasoningControls, "effort") && !controlsWereEmpty && effortWasPresent:
			// The effort control must be recorded evidence: one derivation
			// invented for an empty list (here or on the alias's target) or
			// the pass-through above appended for a ladder says nothing about
			// what the model accepts. After the full replay, the provenance
			// tag is final: "alias" means the controls were inherited from
			// the target. Cross-protocol inherited controls are never
			// evidence for the shape — a third-party gateway's
			// OpenAI-compatible shim may record effort for a budget-shaped
			// model (nano-gpt's gemini-2.5-pro), and only google-protocol
			// evidence or an explicit override vouches for the level shape.
			// Any non-alias tag means a real layer on this side restated
			// the control.
			aliasInherited := in.Aliased && prov["ReasoningControls"] == provAlias
			if !aliasInherited {
				shape = "level"
			}
		}
		if shape != "" {
			c.ThinkingShape = &shape
			prov["ThinkingShape"] = provDerived
		}
	}

	if in.Protocol != ProtocolAnthropic || reasoningOff {
		return
	}
	// 4. Thinking shape, keyed on family (never on Surface).
	claude := strings.HasPrefix(m.Family, "claude") || (in.Synthesized && in.ProviderFamily == "claude")
	has := func(ctrl string) bool { return slices.Contains(c.ReasoningControls, ctrl) }
	if c.ThinkingShape == nil {
		var shape string
		switch {
		case has("effort") && claude:
			shape = "adaptive"
		case has("budget_tokens"):
			shape = "budget"
		case len(c.ReasoningControls) == 1 && has("toggle"):
			shape = "budget"
		case controlsWereEmpty && claude:
			shape = "adaptive"
		case controlsWereEmpty:
			shape = "budget"
		}
		if shape != "" {
			c.ThinkingShape = &shape
			prov["ThinkingShape"] = provDerived
		}
	}
	if c.ThinkingShape == nil || *c.ThinkingShape != "adaptive" {
		return
	}
	// 5. Adaptive rows send the thinking object on every request.
	if c.ThinkingAlwaysOn == nil {
		c.ThinkingAlwaysOn = new(true)
		prov["ThinkingAlwaysOn"] = provDerived
	}
	// 6. Effort-only adaptive rows get the summarized display.
	if c.ThinkingDisplay == nil && !has("budget_tokens") {
		display := "summarized"
		c.ThinkingDisplay = &display
		prov["ThinkingDisplay"] = provDerived
	}
}

// EffortOffCapable reports whether the row's ladder lists the explicit off
// level, i.e. whether the model can be told to stop reasoning at all (spec
// §8.4). Only such a row gets an off on the wire; on any other the control is
// omitted, because no value would say "off" to it.
func (c Caps) EffortOffCapable() bool {
	return slices.ContainsFunc(c.EffortValues, func(l string) bool {
		return strings.EqualFold(strings.TrimSpace(l), "none")
	})
}

// EffortCapable reports whether a row accepts a reasoning effort (spec
// §8.4): effort ∈ ReasoningControls, which after derivation is every
// reasoning row except one that lists controls without effort. A row with
// no controls and no Reasoning verdict (an unknown model) is capable too,
// so an explicit effort still reaches request shaping as it did before the
// registry; the protocol builder then writes it only for a level an
// EffortValues ladder vouches for (llm.VouchedEffort).
func (c Caps) EffortCapable() bool {
	// A row that states the no-effort verdict takes no effort parameter,
	// whatever its controls say: the controls are capability evidence that
	// thinking-shape derivation still needs, not a promise the wire accepts a
	// level.
	if BoolValue(c.EffortParameterUnsupported) {
		return false
	}
	if slices.Contains(c.ReasoningControls, "effort") {
		return true
	}
	return len(c.ReasoningControls) == 0 && c.Reasoning == nil
}

// ReasoningEnabled reports whether a row reasons at all, which is a different
// question from whether its wire accepts an effort name: a toggle-only row and
// a row stating EffortParameterUnsupported both reason without taking a level.
func (c Caps) ReasoningEnabled() bool {
	return c.Reasoning == nil || *c.Reasoning
}

// RequestEffortCapable reports whether a caller may put an effort on the
// request for this row. That is broader than EffortCapable: a budget-shaped row
// sizes its numeric budget from the effort even though no effort name reaches
// the wire, so a caller that wants the budget still needs to set one. A row
// that only toggles reasoning does not qualify, because nothing consumes the
// level.
func (c Caps) RequestEffortCapable() bool {
	if !c.ReasoningEnabled() {
		return false
	}
	return c.EffortCapable() || slices.Contains(c.ReasoningControls, "budget_tokens")
}

// AcceptsModality reports whether the row declares an input modality. The
// catalog vocabulary is lowercase ("text", "image", "pdf", ...), matching what
// every layer writes; an unknown row with no InputModalities declares none.
func (c Caps) AcceptsModality(modality string) bool {
	return slices.Contains(c.InputModalities, modality)
}
