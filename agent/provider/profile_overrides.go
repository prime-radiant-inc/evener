package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/llm"
)

var (
	profileJSONMarshal   = json.Marshal
	profileJSONUnmarshal = json.Unmarshal
)

// WithCommunicateOutputSchema returns a cloned profile whose `communicate`
// tool has its `output` property schema replaced wholesale by the given
// schema. The top-level `required` list on the tool parameters is also
// updated to include `output`.
//
// The schema is validated against the draft 2020-12 meta-schema first: a
// caller-supplied schema spliced into tool parameters verbatim ships on the
// child's first model call, and every provider that validates tool schemas
// (lunaroute's draft-2020-12 check, issue #6298) then rejects the request
// before the delegate does any work. An invalid schema returns an error
// instead of binding.
//
// A communicate tool definition whose parameters cannot be deep-copied
// through JSON also returns an error instead of leaving the `output` schema
// unspliced: the profile would advertise a result contract the child cannot
// satisfy. A definition with no parameters or no properties map is returned
// unchanged, since there is nowhere to splice the schema.
//
// Passing nil or an empty map returns p unchanged.
//
// Stacking with WithAllowedDecisions: apply this first, then WithAllowedDecisions.
// WithAllowedDecisions mutates output.properties to inject a `decision` field,
// so the caller-supplied schema must have (or be compatible with) a properties
// map. Applying WithCommunicateOutputSchema twice silently replaces the prior
// schema each time.
func WithCommunicateOutputSchema(p *Profile, outputSchema map[string]any) (*Profile, error) {
	if p == nil || len(outputSchema) == 0 {
		return p, nil
	}
	if err := llm.ValidateSchemaDocument(outputSchema); err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}

	clone := *p
	defs := append([]llm.ToolDefinition{}, p.toolDefs...)
	for i := range defs {
		if defs[i].Name == "communicate" {
			bound, err := replaceCommunicateOutputSchema(defs[i], outputSchema)
			if err != nil {
				return nil, fmt.Errorf("bind output schema: %w", err)
			}
			defs[i] = bound
		}
	}
	clone.toolDefs = defs
	return &clone, nil
}

// WithAllowedDecisions returns a cloned profile where the `communicate` tool
// schema requires a `decision` field constrained to the given values, and
// `output` is required at the top level.
//
// This is intended for orchestration systems (like Toil) that route workflow
// DAGs based on the agent's decision.
func WithAllowedDecisions(p *Profile, decisions []string) *Profile {
	if p == nil || len(decisions) == 0 {
		return p
	}

	clone := *p
	defs := append([]llm.ToolDefinition{}, p.toolDefs...)
	for i := range defs {
		if defs[i].Name == "communicate" {
			defs[i] = addDecisionToSchema(defs[i], decisions)
		}
	}
	clone.toolDefs = defs
	return &clone
}

// WithCheapModel returns a cloned profile whose auxiliary side calls use the
// supplied cheap model. The ref is "instance/model" to route side calls to a
// different instance than the main model, or a bare "model" to keep the main
// instance. The split is on the first slash (so the model half may itself
// contain slashes, e.g. "openrouter/anthropic/claude"). Passing an empty ref
// returns p unchanged.
func WithCheapModel(p *Profile, ref string) *Profile {
	ref = strings.TrimSpace(ref)
	if p == nil || ref == "" {
		return p
	}

	clone := *p
	if cheapProvider, model, ok := strings.Cut(ref, "/"); ok && cheapProvider != "" && model != "" {
		clone.cheapProvider = cheapProvider
		clone.cheapModel = model
	} else {
		clone.cheapProvider = ""
		clone.cheapModel = ref
	}
	return &clone
}

// replaceCommunicateOutputSchema deep-copies td.Parameters and overwrites
// Parameters.properties.output with a deep copy of outputSchema. It also adds
// "output" to Parameters.required if not already present.
//
// A tool definition with no parameters or no properties map is returned
// unchanged: there is nowhere to splice the schema, and that absence is the
// profile's own shape rather than a failure to bind.
func replaceCommunicateOutputSchema(td llm.ToolDefinition, outputSchema map[string]any) (llm.ToolDefinition, error) {
	// Deep-copy Parameters via JSON round-trip to avoid mutating shared map state.
	if td.Parameters != nil {
		b, err := profileJSONMarshal(td.Parameters)
		if err != nil {
			return td, fmt.Errorf("marshal tool parameters: %w", err)
		}
		var params map[string]any
		if err := profileJSONUnmarshal(b, &params); err != nil {
			return td, fmt.Errorf("unmarshal tool parameters: %w", err)
		}
		td.Parameters = params
	}

	params := td.Parameters
	if params == nil {
		return td, nil
	}
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return td, nil
	}

	// Deep-copy outputSchema so later mutations by the caller do not leak in.
	copied, err := deepCopyJSONMap(outputSchema)
	if err != nil {
		return td, fmt.Errorf("copy output schema: %w", err)
	}
	props["output"] = copied

	// Ensure output is listed as required at the top level.
	topReq := toStringSlice(params["required"])
	if !slices.Contains(topReq, "output") {
		topReq = append(topReq, "output")
	}
	params["required"] = topReq

	return td, nil
}

// deepCopyJSONMap returns a deep copy of m via JSON round-trip. Returns an
// error on unmarshalable values.
func deepCopyJSONMap(m map[string]any) (map[string]any, error) {
	b, err := profileJSONMarshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := profileJSONUnmarshal(b, &out); err != nil {
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
		b, err := profileJSONMarshal(td.Parameters)
		if err != nil {
			return td
		}
		var params map[string]any
		if err := profileJSONUnmarshal(b, &params); err != nil {
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
