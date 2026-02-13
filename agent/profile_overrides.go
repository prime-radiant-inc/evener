package agent

import (
	"slices"
	"strings"

	"primeradiant.com/serf/llm"
)

// WithCommunicateRequiredDataKeys returns a cloned profile where the `communicate`
// tool schema requires specific `output.data.*` keys for action=result.
//
// This is intended for orchestration systems (like Toil) that can provide the
// required output keys per task/node.
func WithCommunicateRequiredDataKeys(p ProviderProfile, requiredKeys []string) ProviderProfile {
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

	bp, ok := p.(*baseProfile)
	if !ok {
		return p
	}

	clone := *bp
	defs := append([]llm.ToolDefinition{}, bp.toolDefs...)
	for i := range defs {
		if defs[i].Name == "communicate" {
			defs[i] = defCommunicateWithRequiredDataKeys(keys)
		}
	}
	clone.toolDefs = defs
	return &clone
}

func defCommunicateWithRequiredDataKeys(requiredKeys []string) llm.ToolDefinition {
	td := defCommunicate()

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
	// key (any JSON type), since Toil's node.outputs only specifies key presence.
	//
	// NOTE: OpenAI Responses requires every property schema to include a `type`.
	// Using a union type here keeps the schema permissive while satisfying that constraint.
	dataSchema["type"] = "object"
	dataSchema["additionalProperties"] = true
	dataProps := map[string]any{}
	for _, k := range requiredKeys {
		dataProps[k] = map[string]any{
			"type": []string{"object", "array", "string", "number", "boolean"},
		}
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
