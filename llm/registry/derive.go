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

	// 1. Effort control pass-through.
	controlsWereEmpty := len(c.ReasoningControls) == 0
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

// EffortCapable reports whether a row accepts a reasoning effort (spec
// §8.4): effort ∈ ReasoningControls, which after derivation is every
// reasoning row except one that lists controls without effort. A row with
// no controls and no Reasoning verdict (an unknown model) is capable too,
// so an explicit effort still reaches the wire as it did before the
// registry.
// EffortOffCapable reports whether the row's ladder lists the explicit off
// level, i.e. whether the model can be told to stop reasoning at all (spec
// §8.4). Only such a row gets an off on the wire; on any other the control is
// omitted, because no value would say "off" to it.
func (c Caps) EffortOffCapable() bool {
	return slices.ContainsFunc(c.EffortValues, func(l string) bool {
		return strings.EqualFold(strings.TrimSpace(l), "none")
	})
}

func (c Caps) EffortCapable() bool {
	if slices.Contains(c.ReasoningControls, "effort") {
		return true
	}
	return len(c.ReasoningControls) == 0 && c.Reasoning == nil
}
