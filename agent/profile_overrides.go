package agent

import (
	"encoding/json"
	"slices"
	"strings"

	"primeradiant.com/serf/llm"
)

// WithSubmitResultRequiredDataKeys returns a cloned profile where the `submit_result`
// tool schema requires specific `output.data.*` keys.
//
// This is intended for orchestration systems (like Toil) that can provide the
// required output keys per task/node.
func WithSubmitResultRequiredDataKeys(p ProviderProfile, requiredKeys []string) ProviderProfile {
	if p == nil {
		return p
	}
	keys := make([]string, 0, len(requiredKeys))
	seen := map[string]struct{}{}
	for _, k := range requiredKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
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
			defs[i] = defSubmitResultWithRequiredDataKeys(keys)
		}
	}
	clone.toolDefs = defs

	// Return the same wrapper type as input.
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

func defSubmitResultWithRequiredDataKeys(requiredKeys []string) llm.ToolDefinition {
	td := defSubmitResult()

	// Navigate: parameters.properties.output.properties.data
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
	dataAny, ok := outProps["data"]
	if !ok {
		return td
	}
	dataSchema, _ := dataAny.(map[string]any)
	if dataSchema == nil {
		return td
	}

	// Ensure required keys are present in the schema. Use empty schemas for each
	// key, since Toil's node.outputs only specifies key presence.
	//
	// NOTE: OpenAI function schemas are validated against a strict subset:
	// - property schemas must include a single-string `type`
	// - object schemas must set `additionalProperties: false`
	// Keep this strict enough to be accepted while still allowing required keys
	// to be expressed.
	dataSchema["type"] = "object"
	dataSchema["additionalProperties"] = false
	dataProps := map[string]any{}
	for _, k := range requiredKeys {
		// Heuristic: most orchestrator-required keys are structured lists.
		// Keys ending in "_results" are objects keyed by ID, not arrays.
		propType := "object"
		if strings.HasSuffix(k, "_list") || strings.HasSuffix(k, "_ids") ||
			(strings.HasSuffix(k, "s") && !strings.HasSuffix(k, "_results")) {
			propType = "array"
		}
		var propSchema map[string]any
		switch {
		case k == "components":
			// Special-case used by orchestration workflows: list of component descriptors.
			propSchema = componentsSchema()
		case k == "tasks":
			propSchema = tasksSchema()
		case strings.HasSuffix(k, "_doc") || strings.HasSuffix(k, "_document") || strings.HasSuffix(k, "_markdown"):
			propSchema = map[string]any{"type": "string"}
		case propType == "array":
			// OpenAI requires array schemas to include `items`.
			propSchema = map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			}
		default:
			propSchema = map[string]any{
				"type":                 "object",
				"additionalProperties": false,
			}
		}
		dataProps[k] = propSchema
	}
	dataSchema["properties"] = dataProps

	// Merge with any existing required list (if any).
	required := make([]string, 0, len(requiredKeys))
	if existing, ok := dataSchema["required"].([]string); ok {
		required = append(required, existing...)
	}
	for _, k := range requiredKeys {
		if !slices.Contains(required, k) {
			required = append(required, k)
		}
	}
	dataSchema["required"] = required

	return td
}

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func componentsSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id":              map[string]any{"type": "string"},
				"name":            map[string]any{"type": "string"},
				"spec_slice":      map[string]any{"type": "string"},
				"relevant_stories": stringArraySchema(),
				"interfaces": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"exposes":  stringArraySchema(),
						"consumes": stringArraySchema(),
					},
					"required": []string{"exposes", "consumes"},
				},
				"dependencies": stringArraySchema(),
			},
			"required": []string{"id", "name", "spec_slice", "relevant_stories", "interfaces", "dependencies"},
		},
	}
}

func tasksSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"id":                map[string]any{"type": "string"},
				"name":              map[string]any{"type": "string"},
				"steps":             stringArraySchema(),
				"files":             stringArraySchema(),
				"acceptance_criteria": stringArraySchema(),
				"dependencies":       stringArraySchema(),
			},
			"required": []string{"id", "name", "steps", "files", "acceptance_criteria", "dependencies"},
		},
	}
}
