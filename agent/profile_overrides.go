package agent

import (
	"encoding/json"
	"slices"
	"strings"

	"primeradiant.com/serf/llm"
)

// WithCommunicateOutputSchema returns a cloned profile whose `communicate` tool
// has its `output` property schema replaced wholesale by the given schema. The
// top-level `required` list on the tool parameters is also updated to include
// `output`.
//
// Passing nil or an empty map returns p unchanged.
//
// Stacking with WithAllowedDecisions: apply this first, then WithAllowedDecisions.
// WithAllowedDecisions mutates output.properties to inject a `decision` field,
// so the caller-supplied schema must have (or be compatible with) a properties
// map. Applying WithCommunicateOutputSchema twice silently replaces the prior
// schema each time.
func WithCommunicateOutputSchema(p ProviderProfile, outputSchema map[string]any) ProviderProfile {
	if p == nil || len(outputSchema) == 0 {
		return p
	}

	var bp *baseProfile
	switch v := p.(type) {
	case *baseProfile:
		bp = v
	case *anthropicProfile:
		bp = &v.baseProfile
	default:
		return p
	}

	clone := *bp
	defs := append([]llm.ToolDefinition{}, bp.toolDefs...)
	for i := range defs {
		if defs[i].Name == "communicate" {
			defs[i] = replaceCommunicateOutputSchema(defs[i], outputSchema)
		}
	}
	clone.toolDefs = defs

	if ap, ok := p.(*anthropicProfile); ok {
		apClone := *ap
		apClone.baseProfile = clone
		return &apClone
	}
	return &clone
}

// WithAllowedDecisions returns a cloned profile where the `communicate` tool
// schema requires a `decision` field constrained to the given values, and
// `output` is required at the top level.
//
// This is intended for orchestration systems (like Toil) that route workflow
// DAGs based on the agent's decision.
func WithAllowedDecisions(p ProviderProfile, decisions []string) ProviderProfile {
	if p == nil || len(decisions) == 0 {
		return p
	}

	var bp *baseProfile
	switch v := p.(type) {
	case *baseProfile:
		bp = v
	case *anthropicProfile:
		bp = &v.baseProfile
	default:
		return p
	}

	clone := *bp
	defs := append([]llm.ToolDefinition{}, bp.toolDefs...)
	for i := range defs {
		if defs[i].Name == "communicate" {
			defs[i] = addDecisionToSchema(defs[i], decisions)
		}
	}
	clone.toolDefs = defs

	if ap, ok := p.(*anthropicProfile); ok {
		apClone := *ap
		apClone.baseProfile = clone
		return &apClone
	}
	return &clone
}

// WithProviderID returns a cloned profile with its id overridden to name,
// preserving the behavior tag and all other profile state unchanged.
// Passing an empty name returns p unchanged.
func WithProviderID(p ProviderProfile, name string) ProviderProfile {
	name = strings.TrimSpace(name)
	if p == nil || name == "" {
		return p
	}

	var bp *baseProfile
	switch v := p.(type) {
	case *baseProfile:
		bp = v
	case *anthropicProfile:
		bp = &v.baseProfile
	default:
		return p
	}

	clone := *bp
	clone.id = name

	if ap, ok := p.(*anthropicProfile); ok {
		apClone := *ap
		apClone.baseProfile = clone
		return &apClone
	}
	return &clone
}

// WithCheapModel returns a cloned profile whose CheapModel method returns the
// supplied model. Passing an empty model returns p unchanged.
func WithCheapModel(p ProviderProfile, model string) ProviderProfile {
	model = strings.TrimSpace(model)
	if p == nil || model == "" {
		return p
	}

	var bp *baseProfile
	switch v := p.(type) {
	case *baseProfile:
		bp = v
	case *anthropicProfile:
		bp = &v.baseProfile
	default:
		return p
	}

	clone := *bp
	clone.cheapModel = model

	if ap, ok := p.(*anthropicProfile); ok {
		apClone := *ap
		apClone.baseProfile = clone
		return &apClone
	}
	return &clone
}

// replaceCommunicateOutputSchema deep-copies td.Parameters and overwrites
// Parameters.properties.output with a deep copy of outputSchema. It also adds
// "output" to Parameters.required if not already present.
func replaceCommunicateOutputSchema(td llm.ToolDefinition, outputSchema map[string]any) llm.ToolDefinition {
	// Deep-copy Parameters via JSON round-trip to avoid mutating shared map state.
	if td.Parameters != nil {
		b, err := json.Marshal(td.Parameters)
		if err != nil {
			return td
		}
		var params map[string]any
		if err := json.Unmarshal(b, &params); err != nil {
			return td
		}
		td.Parameters = params
	}

	params := td.Parameters
	if params == nil {
		return td
	}
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return td
	}

	// Deep-copy outputSchema so later mutations by the caller do not leak in.
	copied, err := deepCopyJSONMap(outputSchema)
	if err != nil {
		return td
	}
	props["output"] = copied

	// Ensure output is listed as required at the top level.
	topReq := toStringSlice(params["required"])
	if !slices.Contains(topReq, "output") {
		topReq = append(topReq, "output")
	}
	params["required"] = topReq

	return td
}

// deepCopyJSONMap returns a deep copy of m via JSON round-trip. Returns an
// error on unmarshalable values.
func deepCopyJSONMap(m map[string]any) (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func addDecisionToSchema(td llm.ToolDefinition, decisions []string) llm.ToolDefinition {
	// IMPORTANT: deep-copy Parameters via JSON round-trip to avoid mutating
	// shared map state from the base profile. Go maps are reference types,
	// so the shallow struct copy in WithAllowedDecisions does NOT copy the
	// nested maps inside Parameters.
	if td.Parameters != nil {
		b, err := json.Marshal(td.Parameters)
		if err != nil {
			return td
		}
		var params map[string]any
		if err := json.Unmarshal(b, &params); err != nil {
			return td
		}
		td.Parameters = params
	}

	params := td.Parameters
	if params == nil {
		return td
	}
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return td
	}
	outputAny, ok := props["output"]
	if !ok {
		return td
	}
	outputSchema, _ := outputAny.(map[string]any)
	if outputSchema == nil {
		return td
	}
	outProps, _ := outputSchema["properties"].(map[string]any)
	if outProps == nil {
		return td
	}

	// Add decision field with enum constraint.
	outProps["decision"] = map[string]any{
		"type": "string",
		"enum": decisions,
	}

	// Add decision to output.required.
	// Note: after JSON round-trip, []string becomes []any, so handle both.
	outReq := toStringSlice(outputSchema["required"])
	if !slices.Contains(outReq, "decision") {
		outReq = append(outReq, "decision")
	}
	outputSchema["required"] = outReq

	// Update output description (remove "optional").
	outputSchema["description"] = "Structured output."

	// Make output required at the top level.
	topReq := toStringSlice(params["required"])
	if !slices.Contains(topReq, "output") {
		topReq = append(topReq, "output")
	}
	params["required"] = topReq

	return td
}

// toStringSlice converts an any (either []string or []any from JSON unmarshal)
// to []string.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
