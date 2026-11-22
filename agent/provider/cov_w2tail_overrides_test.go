package provider

import (
	"testing"

	"primeradiant.com/evener/llm"
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
		got, applied := addDecisionToSchema(td, []string{"approve", "reject"})
		if applied {
			t.Errorf("%s: returned applied despite guard", name)
		}
		// Guard paths return without injecting a decision enum anywhere.
		if props, ok := got.Parameters["properties"].(map[string]any); ok {
			if out, ok := props["output"].(map[string]any); ok {
				if op, ok := out["properties"].(map[string]any); ok {
					if _, has := op["decision"]; has {
						t.Errorf("%s: decision injected despite guard", name)
					}
				}
			}
		}
	}
}

// The full path injects the decision enum and requires decision inside output,
// while leaving output itself optional at the top level.
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
	got, applied := addDecisionToSchema(td, []string{"approve", "reject"})
	if !applied {
		t.Fatal("injected decision should count as bound")
	}
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
	if !contains(toStringSlice(out["required"]), "decision") {
		t.Errorf("decision not required inside output")
	}
	if contains(toStringSlice(got.Parameters["required"]), "output") {
		t.Errorf("output must stay optional at the top level")
	}
}

// replaceCommunicateOutputSchema returns the definition unchanged when it has
// no Parameters or no properties map.
func TestW2Tail_replaceCommunicateOutputSchema_Guards(t *testing.T) {
	schema := map[string]any{"type": "object"}

	noParams, applied, err := replaceCommunicateOutputSchema(llm.ToolDefinition{Name: "communicate"}, schema)
	if err != nil {
		t.Errorf("no-params path should not error: %v", err)
	}
	if noParams.Parameters != nil || applied {
		t.Errorf("no-params path should leave Parameters nil")
	}

	noProps, applied, err := replaceCommunicateOutputSchema(
		llm.ToolDefinition{Name: "communicate", Parameters: map[string]any{"type": "object"}},
		schema,
	)
	if err != nil {
		t.Errorf("no-props path should not error: %v", err)
	}
	if _, has := noProps.Parameters["properties"]; has || applied {
		t.Errorf("no-props path should not fabricate properties")
	}

	// Full path replaces output and records an explicit binding.
	full, applied, err := replaceCommunicateOutputSchema(
		llm.ToolDefinition{Name: "communicate", Parameters: map[string]any{"properties": map[string]any{"output": map[string]any{"old": true}}}},
		schema,
	)
	if err != nil || !applied {
		t.Fatalf("full path: applied=%v, err=%v, want applied without error", applied, err)
	}
	props := full.Parameters["properties"].(map[string]any)
	if _, ok := props["output"].(map[string]any)["type"]; !ok {
		t.Errorf("output schema not replaced")
	}
}
