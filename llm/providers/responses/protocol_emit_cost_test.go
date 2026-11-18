package responses

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// A catalog whose tools are already strictly shaped is the common case on a
// strict-capable route, and the projection still rewrites it: strictifyJSONSchema
// sorts every object's required list. A reorder costs no bytes, so the count the
// request already holds is exact.
//
// The deriver's report is the thing that decides reuse: a change means the
// caller re-measures the whole catalog, and no change means it keeps its count.
// So the assertion is on that report, with the price checked alongside it —
// comparing only prices cannot see the defect, because the wrong answer and the
// right answer are the same number.
func TestAnAlreadyStrictCatalogIsNotRemeasured(t *testing.T) {
	t.Parallel()
	res := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses}
	res.Caps.StrictTools = new(true)

	// The required list is deliberately out of sorted order, and every property
	// is a plain schema the gate can read. That makes this catalog certifiable
	// and already closed: the projection's only effect on it is the reorder.
	defs := []llm.ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"content":   map[string]any{"type": "string"},
				"file_path": map[string]any{"type": "string"},
				"note":      map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content", "note"},
		},
	}}

	// The projection of this catalog differs from it only by the sort, so the
	// two serialize to the same length. That is asserted rather than assumed:
	// if the fixture ever stops being byte-neutral, this test would be asking a
	// different question.
	projected := defs[0].Parameters
	emitted, changed := (&Protocol{}).EmitToolSchemas(defs, res, true)
	if changed {
		t.Fatalf("deriver reported a change for a schema whose projection only reorders its required list; "+
			"the catalog it describes is %v", func() any {
			if emitted == nil {
				return nil
			}
			return emitted[0].Parameters["required"]
		}())
	}
	if emitted != nil {
		t.Fatalf("deriver returned a list for an unchanged catalog, want nil so the caller reuses its count")
	}
	if llm.ToolsJSONBytes([]llm.ToolDefinition{{Name: "write_file", Parameters: projected}}) !=
		llm.ToolsJSONBytes(defs) {
		t.Fatal("fixture is not byte-neutral: the projection would have to be measured, so this test asks nothing")
	}

	// The control: a schema the projection really changes is still reported, so
	// a deriver that always answers "unchanged" cannot pass above.
	changing := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
	if changingBytes := llm.ToolsJSONBytes(changing); changingBytes > 0 {
		control, controlChanged := (&Protocol{}).EmitToolSchemas(changing, res, true)
		if !controlChanged {
			t.Fatalf("control: a closed value set with no type was reported unchanged, so the reuse "+
				"assertion above would pass for a deriver that never answers (fixture %d bytes)", changingBytes)
		}
		if control[0].Parameters == nil {
			t.Fatal("control: emitted schema is nil")
		}
	}
}
