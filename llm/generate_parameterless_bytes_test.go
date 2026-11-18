package llm

import (
	"testing"
)

// TestPreparedToolBytesPriceTheSchemaTheAdapterEmits pins that the estimate
// prices what a built-in adapter actually sends for a parameterless tool.
//
// prepareTools keeps the definition's nil Parameters so an adapter can tell "no
// arguments" from "advertises an open object", and every built-in adapter
// substitutes the open empty schema when it builds the wire body. Measuring the
// definitions as they stand therefore omits those bytes, and near the input
// limit that difference admits a request the budget would have rejected.
func TestPreparedToolBytesPriceTheSchemaTheAdapterEmits(t *testing.T) {
	gs, err := prepareGeneration(GenerateOptions{
		Model:  "model",
		Prompt: new("hi"),
		Tools: []Tool{
			{Definition: ToolDefinition{Name: "probe_a"}},
			{Definition: ToolDefinition{Name: "probe_b"}},
		},
	})
	if err != nil {
		t.Fatalf("prepareGeneration: %v", err)
	}
	if len(gs.toolDefs) != 2 {
		t.Fatalf("prepared tools = %d, want 2", len(gs.toolDefs))
	}
	for _, def := range gs.toolDefs {
		if def.Parameters != nil {
			t.Fatalf("prepared definition %q carries parameters; the nil distinction must survive preparation", def.Name)
		}
	}

	// What the built-in adapters put on the wire for the same list.
	emitted := make([]ToolDefinition, len(gs.toolDefs))
	copy(emitted, gs.toolDefs)
	for i := range emitted {
		if emitted[i].Parameters == nil {
			emitted[i].Parameters = defaultToolParameters()
		}
	}
	wantBytes := ToolsJSONBytes(emitted)
	if bare := ToolsJSONBytes(gs.toolDefs); bare == wantBytes {
		t.Fatalf("fixture does not distinguish the two prices: bare=%d emitted=%d", bare, wantBytes)
	}
	if gs.toolDefsJSONBytes != wantBytes {
		t.Fatalf("prepared byte estimate = %d, want %d: the estimate must include the schema the adapters substitute for a nil Parameters", gs.toolDefsJSONBytes, wantBytes)
	}
}
