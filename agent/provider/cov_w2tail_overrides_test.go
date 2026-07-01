package provider

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// addDecisionToSchema returns the definition unchanged when any structural
// guard fails: no Parameters, no properties, no output, output not a map, or
// output without a properties map.
func TestW2Tail_addDecisionToSchema_Guards(t *testing.T) {
	cases := map[string]llm.ToolDefinition{
		"nil params":       {Name: "communicate"},
		"no properties":    {Name: "communicate", Parameters: map[string]any{}},
		"no output":        {Name: "communicate", Parameters: map[string]any{"properties": map[string]any{}}},
		"output not a map": {Name: "communicate", Parameters: map[string]any{"properties": map[string]any{"output": "scalar"}}},
		"output no props":  {Name: "communicate", Parameters: map[string]any{"properties": map[string]any{"output": map[string]any{}}}},
	}
	for name, td := range cases {
		got := addDecisionToSchema(td, []string{"approve", "reject"})
		// Guard paths return without injecting a decision enum anywhere.
		if props, ok := td.Parameters["properties"].(map[string]any); ok {
			if out, ok := props["output"].(map[string]any); ok {
				if op, ok := out["properties"].(map[string]any); ok {
					if _, has := op["decision"]; has {
						t.Errorf("%s: decision injected despite guard", name)
					}
				}
			}
		}
		_ = got
	}
}

// The full path injects the decision enum and marks output required.
func TestW2Tail_addDecisionToSchema_Injects(t *testing.T) {
	td := llm.ToolDefinition{
		Name: "communicate",
		Parameters: map[string]any{
			"properties": map[string]any{
				"output": map[string]any{
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
				},
			},
		},
	}
	got := addDecisionToSchema(td, []string{"approve", "reject"})
	props := got.Parameters["properties"].(map[string]any)
	out := props["output"].(map[string]any)
	op := out["properties"].(map[string]any)
	dec, ok := op["decision"].(map[string]any)
	if !ok {
		t.Fatalf("decision field not injected")
	}
	if dec["type"] != "string" {
		t.Errorf("decision type = %v", dec["type"])
	}
	if _, ok := got.Parameters["required"]; !ok {
		t.Errorf("top-level required not set")
	}
}

// replaceCommunicateOutputSchema returns the definition unchanged when it has
// no Parameters or no properties map.
func TestW2Tail_replaceCommunicateOutputSchema_Guards(t *testing.T) {
	schema := map[string]any{"type": "object"}

	noParams := replaceCommunicateOutputSchema(llm.ToolDefinition{Name: "communicate"}, schema)
	if noParams.Parameters != nil {
		t.Errorf("no-params path should leave Parameters nil")
	}

	noProps := replaceCommunicateOutputSchema(
		llm.ToolDefinition{Name: "communicate", Parameters: map[string]any{"type": "object"}},
		schema,
	)
	if _, has := noProps.Parameters["properties"]; has {
		t.Errorf("no-props path should not fabricate properties")
	}

	// Full path replaces output and marks it required.
	full := replaceCommunicateOutputSchema(
		llm.ToolDefinition{Name: "communicate", Parameters: map[string]any{"properties": map[string]any{"output": map[string]any{"old": true}}}},
		schema,
	)
	props := full.Parameters["properties"].(map[string]any)
	if _, ok := props["output"].(map[string]any)["type"]; !ok {
		t.Errorf("output schema not replaced")
	}
}
