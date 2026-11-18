package llm

import (
	"testing"
)

// TestPreparedToolBytesPriceTheSchemaTheAdapterEmits pins that the estimate
// prices what a built-in adapter actually sends for a parameterless tool.
//
// prepareTools substitutes the open empty schema for a tool that declares no
// parameters, so the definitions a generation carries already include those
// bytes and there is nothing left to add. What must hold is that the number the
// generation stamps is the length of that substituted list: a stamp taken over
// the definitions before the substitution would omit the schema the adapters
// send, and near the input limit that difference admits a request the budget
// would have rejected.
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
	// Every definition carries a schema, which is what the adapters send.
	for _, def := range gs.toolDefs {
		if def.Parameters == nil {
			t.Fatalf("prepared definition %q carries no parameters: the adapters substitute a schema for a "+
				"parameterless tool, so the estimate has to price one", def.Name)
		}
	}

	// The stamp is the length of exactly the list the adapters are handed.
	wantBytes := ToolsJSONBytes(gs.toolDefs)
	if gs.toolDefsJSONBytes != wantBytes {
		t.Fatalf("prepared byte estimate = %d, want %d: the estimate must price the tool list the adapters "+
			"are handed", gs.toolDefsJSONBytes, wantBytes)
	}

	// And that list, measured as the definition alone, is larger than the bare
	// name would be: the schema is what the estimate is here to include.
	bare := 0
	for _, def := range gs.toolDefs {
		bare += len(def.Name)
	}
	if wantBytes <= bare {
		t.Fatalf("prepared byte estimate = %d, want more than the bare names' %d: the substituted schemas are "+
			"the bytes under test", wantBytes, bare)
	}
}

// A request's tool count reaches admission as the price of the list the builder
// emits, not the list the generation advertised.
//
// This is the same property as the adapter test above, one layer out: the number
// the generation stamps has to survive to the budget, because that is the number
// a caller is charged.
func TestAPreparedGenerationPricesTheEmittedList(t *testing.T) {
	gs, err := prepareGeneration(GenerateOptions{
		Model:  "model",
		Prompt: new("hi"),
		Tools:  []Tool{{Definition: ToolDefinition{Name: "probe"}}},
	})
	if err != nil {
		t.Fatalf("prepareGeneration: %v", err)
	}
	req := gs.request(GenerateOptions{Model: "model", Prompt: new("hi")}, nil)
	if len(req.Tools) != 1 {
		t.Fatalf("request carries %d tools, want 1", len(req.Tools))
	}
	if got, want := req.ToolsJSONBytes, ToolsJSONBytes(req.Tools); got != want {
		t.Fatalf("request stamped ToolsJSONBytes = %d, want the list's own %d", got, want)
	}
	// The per-request stamp is the generation's, not a re-measurement that could
	// drift from it.
	if req.ToolsJSONBytes != gs.toolDefsJSONBytes {
		t.Fatalf("request ToolsJSONBytes = %d, want the generation's %d",
			req.ToolsJSONBytes, gs.toolDefsJSONBytes)
	}
}
